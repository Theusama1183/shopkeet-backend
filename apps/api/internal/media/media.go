package media

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service wires the media module: handlers read/write RLS-scoped rows through
// the request transaction that TenantMW opened (c.Locals("tx")) and delegate
// object bytes to an ObjectStore.
type Service struct {
	pool       *pgxpool.Pool
	store      ObjectStore
	publicURL  string
	presignTTL time.Duration
}

// New builds the media Service. publicURL is the R2 public base
// (e.g. https://media.example.shopkeet.com — no trailing slash).
func New(pool *pgxpool.Pool, store ObjectStore, publicURL string, presignTTL time.Duration) *Service {
	return &Service{pool: pool, store: store, publicURL: strings.TrimRight(publicURL, "/"), presignTTL: presignTTL}
}

// --- validation ---------------------------------------------------------------

// sanitizeExt turns "image.jpg" -> "jpg", ".png" -> "png", unknown -> "".
func sanitizeExt(filename string) string {
	ext := strings.TrimPrefix(path.Ext(filename), ".")
	if ext == "" || len(ext) > 8 {
		return ""
	}
	// Allow only simple alphanumeric extensions (no path tricks via ext).
	for _, r := range ext {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return ext
}

// --- handlers -----------------------------------------------------------------

// uploadURLRequest is the POST /media/upload-url body.
type uploadURLRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

// UploadURL signs a presigned PUT URL for {tenant_id}/{uuid}.{ext} and returns
// it with the key the client must send back on POST /media.
func (s *Service) UploadURL(c *fiber.Ctx) error {
	var req uploadURLRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.Filename == "" || req.ContentType == "" {
		return httperr.C(fiber.StatusBadRequest, "filename and content_type required")
	}
	tenantID, ok := c.Locals("tenant_id").(string)
	if !ok || tenantID == "" {
		return httperr.Unauthorized("unauthorized", "unauthorized")
	}

	ext := sanitizeExt(req.Filename)
	if ext == "" {
		return httperr.C(fiber.StatusBadRequest, "unsupported filename")
	}
	key := fmt.Sprintf("%s/%s.%s", tenantID, uuid.NewString(), ext)

	uploadURL, err := s.store.PresignPutURL(c.Context(), key, req.ContentType)
	if err != nil {
		return httperr.C(fiber.StatusBadGateway, "could not prepare upload")
	}
	return c.JSON(fiber.Map{
		"upload_url": uploadURL,
		"r2_key":     key,
		"expires_in": int(s.presignTTL.Seconds()),
	})
}

// confirmRequest is the POST /media body — client states the upload happened.
type confirmRequest struct {
	R2Key       string `json:"r2_key"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	AltText     string `json:"alt_text"`
}

// Confirm records a completed upload in media_assets and returns the asset.
func (s *Service) Confirm(c *fiber.Ctx) error {
	var req confirmRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.R2Key == "" || req.ContentType == "" || req.SizeBytes < 0 {
		return httperr.C(fiber.StatusBadRequest, "r2_key, content_type, size_bytes required")
	}
	tenantID, ok := c.Locals("tenant_id").(string)
	if !ok || tenantID == "" {
		return httperr.Unauthorized("unauthorized", "unauthorized")
	}
	// The key's first path segment must be the tenant — a tenant must never be
	// able to register a row pointing at another tenant's object key.
	if !strings.HasPrefix(req.R2Key, tenantID+"/") {
		return httperr.C(fiber.StatusForbidden, "r2_key must belong to your tenant")
	}

	tx, ok := c.Locals("tx").(pgx.Tx)
	if !ok || tx == nil {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	var id, url string
	err := tx.QueryRow(ctx, `
		INSERT INTO media_assets (tenant_id, r2_key, url, content_type, size_bytes, alt_text)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, url`,
		tenantID, req.R2Key, s.publicURL+"/"+req.R2Key, req.ContentType, req.SizeBytes, req.AltText,
	).Scan(&id, &url)
	if err != nil {
		return httperr.C(fiber.StatusInternalServerError, "could not record asset")
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id": id, "r2_key": req.R2Key, "url": url,
		"content_type": req.ContentType, "size_bytes": req.SizeBytes,
		"alt_text": req.AltText,
	})
}

// assetRow mirrors a media_assets row for JSON output + delete lookups.
type assetRow struct {
	id          string
	r2Key       string
	url         string
	contentType string
	sizeBytes   int64
	altText     *string
	createdAt   time.Time
}

func scanAssetRow(row pgx.Row) (*assetRow, error) {
	var a assetRow
	if err := row.Scan(&a.id, &a.r2Key, &a.url, &a.contentType, &a.sizeBytes, &a.altText, &a.createdAt); err != nil {
		return nil, err
	}
	return &a, nil
}

func assetJSON(a *assetRow) fiber.Map {
	alt := ""
	if a.altText != nil {
		alt = *a.altText
	}
	return fiber.Map{
		"id": a.id, "r2_key": a.r2Key, "url": a.url,
		"content_type": a.contentType, "size_bytes": a.sizeBytes,
		"alt_text": alt, "created_at": a.createdAt.Format(time.RFC3339),
	}
}

// List returns the tenant's media library, newest first.
func (s *Service) List(c *fiber.Ctx) error {
	tx, ok := c.Locals("tx").(pgx.Tx)
	if !ok || tx == nil {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tenantID, _ := c.Locals("tenant_id").(string)

	rows, err := tx.Query(ctx, `
		SELECT id, r2_key, url, content_type, size_bytes, alt_text, created_at
		FROM media_assets
		WHERE tenant_id = $1
		ORDER BY created_at DESC, id DESC`, tenantID)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()

	assets := make([]fiber.Map, 0)
	for rows.Next() {
		var a assetRow
		if err := rows.Scan(&a.id, &a.r2Key, &a.url, &a.contentType, &a.sizeBytes, &a.altText, &a.createdAt); err != nil {
			return httperr.ErrInternalServerError
		}
		assets = append(assets, assetJSON(&a))
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"assets": assets})
}

// Delete removes an asset: object from R2 first, then the row. RLS scopes the
// row lookup, so deleting another tenant's asset finds no row.
func (s *Service) Delete(c *fiber.Ctx) error {
	tx, ok := c.Locals("tx").(pgx.Tx)
	if !ok || tx == nil {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	id := c.Params("id")
	tenantID, _ := c.Locals("tenant_id").(string)

	var key string
	err := tx.QueryRow(ctx,
		"SELECT r2_key FROM media_assets WHERE id = $1 AND tenant_id = $2", id, tenantID).
		Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "asset not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}

	// Object first: if R2 succeeds but the row disappears underneath us it's
	// just an orphan row; failing the object delete aborts before row removal.
	if err := s.store.Delete(ctx, key); err != nil {
		return httperr.C(fiber.StatusBadGateway, "could not delete object")
	}
	if _, err := tx.Exec(ctx, "DELETE FROM media_assets WHERE id = $1", id); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"deleted": id})
}
