// Package bulkcsv implements Phase 29 — bulk product CSV import/export.
//
// Import is asynchronous: POST /products/import parses the uploaded CSV into
// typed rows, enqueues a products:import job on the shared Asynq queue, and
// returns the job id. The worker inserts every valid row inside a tenant-scoped
// transaction and writes a JSON per-row report to the task result — a single
// malformed row never aborts the batch. GET /products/import/:jobId polls the
// job through the Asynq Inspector (state + report). Export is synchronous:
// GET /products/export streams the whole tenant catalog as a CSV.
package bulkcsv

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/queue"
)

// Row is one CSV record, already validated for type but not for referential
// integrity. Line is the physical CSV line (1-based, header = 1) so the
// per-row error report can point the merchant at the exact offending line.
type Row struct {
	Line           int    `json:"line"`
	Name           string `json:"name"`
	Slug           string `json:"slug"`
	Description    string `json:"description"`
	PriceCents     int    `json:"price_cents"`
	Currency       string `json:"currency"`
	InventoryCount int    `json:"inventory_count"`
	Status         string `json:"status"`
}

// ImportError reports one failed row.
type ImportError struct {
	Line  int    `json:"line"`
	Error string `json:"error"`
}

// Report is the per-batch import outcome, written to the task result.
type Report struct {
	Total    int           `json:"total"`
	Imported int           `json:"imported"`
	Errors   []ImportError `json:"errors,omitempty"`
}

// Service wires the import/export endpoints. With Redis disabled the Enqueuer
// and Inspector are nil: import returns 503 and job polling 503, while the
// synchronous export still works.
type Service struct {
	pool *pgxpool.Pool
	enq  *queue.Enqueuer
	insp *asynq.Inspector
}

// New builds the Service; pass the queue halves from main.go.
func New(pool *pgxpool.Pool, enq *queue.Enqueuer, insp *asynq.Inspector) *Service {
	return &Service{pool: pool, enq: enq, insp: insp}
}

// --- CSV parsing -----------------------------------------------------------------

// expectedHeader are the accepted CSV columns (order-insensitive).
var expectedHeader = map[string]bool{
	"name": true, "slug": true, "description": true, "price_cents": true,
	"currency": true, "inventory_count": true, "status": true,
}

// ParseCSV decodes a product CSV. A missing "name" column is a hard error
// (the sheet can't mean anything without it); per-row problems (bad numbers,
// unknown status, duplicate generated slugs within the batch) are collected in
// Row.Errors so the caller can either surface them or let the async job report
// them per line.
func ParseCSV(r io.Reader) ([]Row, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	if _, ok := idx["name"]; !ok {
		return nil, errors.New(`CSV must have a "name" column`)
	}
	for col := range idx {
		if _, ok := expectedHeader[col]; !ok {
			return nil, fmt.Errorf(`unknown CSV column %q (expected name, slug, description, price_cents, currency, inventory_count, status)`, col)
		}
	}

	var rows []Row
	for n := 2; ; n++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading row %d: %w", n, err)
		}
		f := func(col string) string {
			i, ok := idx[col]
			if !ok || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		price := 0
		if v := f("price_cents"); v != "" {
			p, perr := strconv.Atoi(v)
			if perr != nil {
				return nil, fmt.Errorf("row %d: price_cents must be an integer", n)
			}
			price = p
		}
		inv := 0
		if v := f("inventory_count"); v != "" {
			k, perr := strconv.Atoi(v)
			if perr != nil {
				return nil, fmt.Errorf("row %d: inventory_count must be an integer", n)
			}
			inv = k
		}
		rows = append(rows, Row{
			Line: n, Name: f("name"), Slug: f("slug"), Description: f("description"),
			PriceCents: price, Currency: f("currency"), InventoryCount: inv,
			Status: f("status"),
		})
	}
	return rows, nil
}

// --- import processing (runs in the worker) --------------------------------------

type importJobPayload struct {
	TenantID string      `json:"tenant_id"`
	Rows     []Row       `json:"rows"`
}

// ProcessImportTask decodes a products:import task and runs the batch. The
// returned Report is written to the task result by the handler so the job
// status endpoint can surface it.
func ProcessImportTask(ctx context.Context, pool *pgxpool.Pool, t *asynq.Task) (*Report, error) {
	var p importJobPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return nil, err
	}
	return ProcessImport(ctx, pool, p.TenantID, p.Rows)
}

// ProcessImport inserts a batch of product rows for a tenant. Every row is
// inserted in its own savepoint: a failed row (duplicate slug, bad status, …
// anything a merchant could recover from) is recorded as an ImportError and the
// rest of the batch still lands. Only catastrophic failures (bogus tenant id,
// a broken connection) abort the whole job and bubble up for an Asynq retry.
func ProcessImport(ctx context.Context, pool *pgxpool.Pool, tenantID string, rows []Row) (*Report, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		return nil, err
	}

	rep := &Report{Total: len(rows)}
	taken := map[string]int{}
	for _, row := range rows {
		emsg := validateRow(&row, taken)
		if emsg != "" {
			rep.Errors = append(rep.Errors, ImportError{Line: row.Line, Error: emsg})
			continue
		}
		if err := insertRow(ctx, tx, &row); err != nil {
			rep.Errors = append(rep.Errors, ImportError{Line: row.Line, Error: err.Error()})
			continue
		}
		rep.Imported++
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return rep, nil
}

// validateRow normalizes a row in place, ensuring a unique slug within the
// batch. Returns a human-readable failure reason or "" when valid.
func validateRow(row *Row, taken map[string]int) string {
	if strings.TrimSpace(row.Name) == "" {
		return "name required"
	}
	status := strings.TrimSpace(row.Status)
	if status == "" {
		status = "draft"
	}
	switch status {
	case "draft", "active", "archived":
	default:
		return fmt.Sprintf("invalid status %q (draft|active|archived)", status)
	}
	row.Status = status
	if strings.TrimSpace(row.Currency) == "" {
		row.Currency = "usd"
	}
	slug := strings.TrimSpace(row.Slug)
	if slug == "" {
		slug = slugify(row.Name)
	}
	if slug == "" {
		return "could not derive a slug from name"
	}
	taken[slug]++
	if taken[slug] > 1 {
		slug = fmt.Sprintf("%s-%d", slug, taken[slug])
	}
	row.Slug = slug
	return ""
}

// insertRow writes one product plus its mandatory default variant (Phase 8
// design rule) inside a savepoint scoped to the batch transaction.
func insertRow(ctx context.Context, tx pgx.Tx, row *Row) error {
	if _, err := tx.Exec(ctx, "SAVEPOINT import_row"); err != nil {
		return err
	}
	defer func() {
		_, _ = tx.Exec(ctx, "RELEASE SAVEPOINT import_row")
	}()

	id := uuid.NewString()
	desc := strings.TrimSpace(row.Description)
	if _, err := tx.Exec(ctx, `
		INSERT INTO products (id, tenant_id, name, slug, description, price_cents,
			currency, inventory_count, status)
		VALUES ($1, current_setting('app.current_tenant')::uuid, $2, $3, NULLIF($4, ''), $5, $6, $7, $8)`,
		id, row.Name, row.Slug, desc, row.PriceCents, strings.ToLower(row.Currency),
		row.InventoryCount, row.Status); err != nil {
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT import_row")
		if isUniqueViolation(err) {
			return errors.New("slug already exists")
		}
		return fmt.Errorf("insert product: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
		VALUES (current_setting('app.current_tenant')::uuid, $1, $2, $3, 'active')`,
		id, row.PriceCents, row.InventoryCount); err != nil {
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT import_row")
		return fmt.Errorf("insert variant: %w", err)
	}
	return nil
}

// --- handlers -------------------------------------------------------------------

// Import handles POST /products/import (admin, multipart file "file").
func (s *Service) Import(c *fiber.Ctx) error {
	fh, err := c.FormFile("file")
	if err != nil {
		return httperr.C(fiber.StatusBadRequest, "multipart field 'file' required")
	}
	f, err := fh.Open()
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer f.Close()

	rows, err := ParseCSV(f)
	if err != nil {
		return httperr.C(fiber.StatusBadRequest, err.Error())
	}
	if len(rows) == 0 {
		return httperr.C(fiber.StatusBadRequest, "CSV has no product rows")
	}
	if s.enq == nil {
		return httperr.C(fiber.StatusServiceUnavailable, "import queue unavailable")
	}

	tid, _ := c.Locals("tenant_id").(string)
	payload, _ := json.Marshal(importJobPayload{TenantID: tid, Rows: rows})
	task := asynq.NewTask(queue.TaskTypeProductImport, payload,
		asynq.Retention(24*time.Hour))
	jobID, err := s.enq.EnqueueResult(c.Context(), task)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"job_id": jobID, "total": len(rows)})
}

// JobStatus handles GET /products/import/:jobId (admin). Maps the Asynq task
// state to pending/processing/done/failed and embeds the per-line report once
// the job completes.
func (s *Service) JobStatus(c *fiber.Ctx) error {
	jobID := c.Params("jobId")
	if s.insp == nil {
		return httperr.C(fiber.StatusServiceUnavailable, "job queue unavailable")
	}
	info, err := s.insp.GetTaskInfo("default", jobID)
	if errors.Is(err, asynq.ErrTaskNotFound) {
		return httperr.C(fiber.StatusNotFound, "job not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}

	body := fiber.Map{"job_id": jobID, "status": taskStateLabel(info.State)}
	if info.State == asynq.TaskStateCompleted && len(info.Result) > 0 {
		var rep Report
		if err := json.Unmarshal(info.Result, &rep); err == nil {
			body["total"] = rep.Total
			body["imported"] = rep.Imported
			body["errors"] = rep.Errors
		}
	}
	return c.JSON(body)
}

// Export handles GET /products/export (admin). Streams the full tenant catalog
// as CSV (all statuses — the merchant exports their whole sheet, not just what
// a shopper sees).
func (s *Service) Export(c *fiber.Ctx) error {
	tx, ok := c.Locals("tx").(pgx.Tx)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	rows, err := tx.Query(ctx, `
		SELECT id, name, slug, description, price_cents, currency, inventory_count, status, created_at
		FROM products
		ORDER BY created_at, id`)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()

	var buf strings.Builder
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{"id", "name", "slug", "description", "price_cents", "currency", "inventory_count", "status", "created_at"}); err != nil {
		return httperr.ErrInternalServerError
	}
	var (
		id, name, slug string
		desc           *string
		price          int
		currency       string
		inventory      int
		status         string
		created        time.Time
	)
	for rows.Next() {
		if err := rows.Scan(&id, &name, &slug, &desc, &price, &currency, &inventory, &status, &created); err != nil {
			return httperr.ErrInternalServerError
		}
		d := ""
		if desc != nil {
			d = *desc
		}
		if err := w.Write([]string{id, name, slug, d, strconv.Itoa(price), currency, strconv.Itoa(inventory), status, created.Format(time.RFC3339)}); err != nil {
			return httperr.ErrInternalServerError
		}
	}
	w.Flush()
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}

	c.Set("Content-Type", "text/csv; charset=utf-8")
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="products-export-%s.csv"`, time.Now().Format("20060102-150405")))
	return c.SendString(buf.String())
}

// --- helpers --------------------------------------------------------------------

func taskStateLabel(st asynq.TaskState) string {
	switch st {
	case asynq.TaskStateActive, asynq.TaskStateAggregating:
		return "processing"
	case asynq.TaskStatePending, asynq.TaskStateScheduled:
		return "pending"
	case asynq.TaskStateCompleted:
		return "done"
	case asynq.TaskStateRetry, asynq.TaskStateArchived:
		return "failed"
	default:
		return "pending"
	}
}

// slugify lowercases a name into a URL-safe slug (two+ spaces collapse).
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}