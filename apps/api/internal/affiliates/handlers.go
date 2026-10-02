package affiliates

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// --- public -----------------------------------------------------------------

type applyRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Apply handles POST /affiliates/apply (Public, X-Tenant-ID storefront scope).
// Creates a pending application; the merchant sets commission_percent and
// approves it via PATCH /affiliates/:id/status. The affiliate can only log in
// once approved. A duplicate email for the same store is rejected (409).
func (s *Service) Apply(c *fiber.Ctx) error {
	var req applyRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Name == "" {
		return httperr.C(fiber.StatusBadRequest, "name required")
	}
	if req.Email == "" {
		return httperr.C(fiber.StatusBadRequest, "email required")
	}
	if len(req.Password) < 8 {
		return httperr.C(fiber.StatusBadRequest, "password must be at least 8 characters")
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := tenantID(c)

	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM affiliates WHERE tenant_id = $1 AND email = $2)`,
		tid, req.Email).Scan(&exists); err != nil {
		return httperr.ErrInternalServerError
	}
	if exists {
		return httperr.C(fiber.StatusConflict, "an application already exists for this email")
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return httperr.ErrInternalServerError
	}

	// Mint a tracking code unique per tenant, retrying on the code-constraint
	// collision. The UNIQUE(tenant_id, email) backstop still 409s a same-email
	// race reported via the email constraint name.
	var affiliateID string
	for attempt := 0; attempt < 5; attempt++ {
		code, err := randomCode()
		if err != nil {
			return httperr.ErrInternalServerError
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO affiliates (tenant_id, name, email, password_hash, code, commission_percent, status)
			VALUES ($1, $2, $3, $4, $5, 10, 'pending') RETURNING id`,
			tid, req.Name, req.Email, hash, code).Scan(&affiliateID)
		if err != nil {
			if isUniqueViolation(err) {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.ConstraintName == "affiliates_tenant_id_email_key" {
					return httperr.C(fiber.StatusConflict, "an application already exists for this email")
				}
				continue // code collision — try a fresh code
			}
			return httperr.ErrInternalServerError
		}
		break
	}
	if affiliateID == "" {
		return httperr.ErrInternalServerError
	}

	var code string
	if err := tx.QueryRow(ctx,
		"SELECT code FROM affiliates WHERE id = $1", affiliateID).Scan(&code); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id": affiliateID, "name": req.Name, "email": req.Email,
		"code": code, "status": "pending",
	})
}

type loginRequest struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	Subdomain string `json:"subdomain"`
}

// Login handles POST /affiliates/login (Public). Mirrors merchant login: the
// store is resolved by subdomain (tenants has no RLS), then credentials are
// verified inside the tenant's RLS scope. Only an approved affiliate can sign
// in — pending/rejected/suspended accounts get a scoped 403 so a partner
// knows to wait for (or that they've lost) approval.
func (s *Service) Login(c *fiber.Ctx) error {
	var req loginRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Subdomain = strings.TrimSpace(req.Subdomain)

	ctx := c.Context()
	var tenantID string
	if err := s.pool.QueryRow(ctx,
		`SELECT id FROM tenants WHERE subdomain = $1`, req.Subdomain).Scan(&tenantID); err != nil {
		return httperr.C(fiber.StatusUnauthorized, "invalid credentials")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		return httperr.ErrInternalServerError
	}

	var id, status, hash string
	err = tx.QueryRow(ctx, `
		SELECT id, status, password_hash FROM affiliates
		WHERE tenant_id = $1 AND email = $2`, tenantID, req.Email).
		Scan(&id, &status, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusUnauthorized, "invalid credentials")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if hash == "" || !auth.CheckPassword(req.Password, hash) {
		return httperr.C(fiber.StatusUnauthorized, "invalid credentials")
	}
	switch status {
	case "approved":
		// good
	case "pending":
		return httperr.C(fiber.StatusForbidden, "application pending approval")
	case "rejected":
		return httperr.C(fiber.StatusForbidden, "application rejected")
	default: // suspended
		return httperr.C(fiber.StatusForbidden, "account suspended")
	}

	token, err := auth.SignAffiliate(s.secret, tenantID, id, 24*time.Hour)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{
		"token":     token,
		"affiliate": fiber.Map{"id": id, "email": req.Email, "status": status},
		"expires":   time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	})
}

// --- admin ------------------------------------------------------------------

// ListAffiliates handles GET /affiliates (Admin). Returns every affiliate for
// the tenant, pending applications first, newest first within each status.
func (s *Service) ListAffiliates(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	rows, err := tx.Query(c.Context(), `
		SELECT id, name, email, code, commission_percent, status, created_at
		FROM affiliates
		ORDER BY (status = 'pending') DESC, created_at`)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()
	list := make([]fiber.Map, 0)
	for rows.Next() {
		var id, name, email, code, status string
		var percent int
		var createdAt time.Time
		if err := rows.Scan(&id, &name, &email, &code, &percent, &status, &createdAt); err != nil {
			return httperr.ErrInternalServerError
		}
		list = append(list, affiliateJSON(id, name, email, code, percent, status, createdAt))
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"affiliates": list})
}

type updateAffiliateStatusRequest struct {
	Status            string `json:"status"`
	CommissionPercent *int   `json:"commission_percent"` // optional — preserved when absent
}

// UpdateStatus handles PATCH /affiliates/:id/status (Admin). Approve, reject,
// suspend (or re-open to pending). commission_percent is optional and applied
// only when provided, mirroring how tracking fields ride the order status
// endpoint — a merchant typically sets the percent when approving.
func (s *Service) UpdateStatus(c *fiber.Ctx) error {
	var req updateAffiliateStatusRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	switch req.Status {
	case "approved", "rejected", "suspended", "pending":
	case "":
		return httperr.C(fiber.StatusBadRequest, "status required")
	default:
		return httperr.C(fiber.StatusBadRequest, "invalid status")
	}
	if req.CommissionPercent != nil {
		if *req.CommissionPercent < 0 || *req.CommissionPercent > 100 {
			return httperr.C(fiber.StatusBadRequest, "commission_percent must be between 0 and 100")
		}
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	sets := "status = $1"
	args := []any{req.Status}
	if req.CommissionPercent != nil {
		args = append(args, *req.CommissionPercent)
		sets += ", commission_percent = $2"
	}
	args = append(args, c.Params("id"))
	tag, err := tx.Exec(ctx,
		fmt.Sprintf("UPDATE affiliates SET %s WHERE id = $%d", sets, len(args)), args...)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "affiliate not found")
	}

	var id, name, email, code, status string
	var percent int
	var createdAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT id, name, email, code, commission_percent, status, created_at
		FROM affiliates WHERE id = $1`, c.Params("id")).Scan(
		&id, &name, &email, &code, &percent, &status, &createdAt); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(affiliateJSON(id, name, email, code, percent, status, createdAt))
}

type updatePayoutStatusRequest struct {
	Status string `json:"status"` // only 'paid'
}

// UpdatePayoutStatus handles PATCH /affiliate-payouts/:id (Admin). Marking a
// payout paid confirms the merchant transferred the money and flips that
// affiliate's still-approved commissions to paid. Returns 404 if the payout
// isn't this tenant's.
func (s *Service) UpdatePayoutStatus(c *fiber.Ctx) error {
	var req updatePayoutStatusRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.Status != "paid" {
		return httperr.C(fiber.StatusBadRequest, "invalid status")
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := tenantID(c)

	var affiliateID, currentStatus string
	err := tx.QueryRow(ctx,
		`SELECT affiliate_id, status FROM affiliate_payouts WHERE id = $1`, c.Params("id")).
		Scan(&affiliateID, &currentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "payout not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if currentStatus == "paid" {
		return c.JSON(fiber.Map{"id": c.Params("id"), "status": "paid"})
	}

	if _, err := tx.Exec(ctx, `
		UPDATE affiliate_payouts SET status = 'paid', paid_at = now() WHERE id = $1`,
		c.Params("id")); err != nil {
		return httperr.ErrInternalServerError
	}
	// Mark the commissions this payout covers as paid, scoped by affiliate so any
	// commission approved since the request is covered too.
	if _, err := tx.Exec(ctx, `
		UPDATE affiliate_commissions SET status = 'paid'
		WHERE tenant_id = $1 AND affiliate_id = $2 AND status = 'approved'`,
		tid, affiliateID); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"id": c.Params("id"), "status": "paid"})
}

// --- affiliate (self-service) ------------------------------------------------

// Dashboard handles GET /affiliates/me/dashboard (Affiliate). Partner profile
// plus commission totals: pending (in flight), approved (payable now), paid
// (settled), and the sum of payouts still awaiting the merchant.
func (s *Service) Dashboard(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := tenantID(c)
	aid := affiliateID(c)

	var id, name, email, code, status string
	var percent int
	var createdAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT id, name, email, code, commission_percent, status, created_at
		FROM affiliates WHERE id = $1`, aid).Scan(
		&id, &name, &email, &code, &percent, &status, &createdAt); err != nil {
		return httperr.ErrInternalServerError
	}

	var pendingCents, approvedCents, paidCents int
	var orders int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(commission_cents) FILTER (WHERE status = 'pending'), 0),
		       COALESCE(SUM(commission_cents) FILTER (WHERE status = 'approved'), 0),
		       COALESCE(SUM(commission_cents) FILTER (WHERE status = 'paid'), 0)
		FROM affiliate_commissions WHERE tenant_id = $1 AND affiliate_id = $2`,
		tid, aid).Scan(&orders, &pendingCents, &approvedCents, &paidCents); err != nil {
		return httperr.ErrInternalServerError
	}

	var outstandingPayoutCents int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_cents), 0) FROM affiliate_payouts
		WHERE tenant_id = $1 AND affiliate_id = $2 AND status = 'requested'`,
		tid, aid).Scan(&outstandingPayoutCents); err != nil {
		return httperr.ErrInternalServerError
	}

	return c.JSON(fiber.Map{
		"id": id, "name": name, "email": email, "code": code,
		"commission_percent": percent, "status": status, "created_at": createdAt,
		"totals": fiber.Map{
			"orders":                   orders,
			"pending_cents":            pendingCents,
			"payout_available_cents":   approvedCents,
			"paid_cents":               paidCents,
			"outstanding_payout_cents": outstandingPayoutCents,
		},
	})
}

// Commissions handles GET /affiliates/me/commissions (Affiliate). Lists each
// commission with its order id and status, newest first.
func (s *Service) Commissions(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := tenantID(c)
	aid := affiliateID(c)

	rows, err := tx.Query(ctx, `
		SELECT id, order_id, commission_cents, status, created_at
		FROM affiliate_commissions
		WHERE tenant_id = $1 AND affiliate_id = $2
		ORDER BY created_at DESC`, tid, aid)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()
	list := make([]fiber.Map, 0)
	for rows.Next() {
		var id, orderID, status string
		var cents int
		var createdAt time.Time
		if err := rows.Scan(&id, &orderID, &cents, &status, &createdAt); err != nil {
			return httperr.ErrInternalServerError
		}
		list = append(list, fiber.Map{
			"id": id, "order_id": orderID, "commission_cents": cents,
			"status": status, "created_at": createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"commissions": list})
}

// PayoutRequest handles POST /affiliates/me/payout-request (Affiliate). Books a
// payout for everything currently approved. A second request while a payout is
// already requested is rejected so the affiliate can't pile up duplicate
// payouts for the same commissions.
func (s *Service) PayoutRequest(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := tenantID(c)
	aid := affiliateID(c)

	var already int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM affiliate_payouts
		WHERE tenant_id = $1 AND affiliate_id = $2 AND status = 'requested'`,
		tid, aid).Scan(&already); err != nil {
		return httperr.ErrInternalServerError
	}
	if already > 0 {
		return httperr.C(fiber.StatusConflict,
			"a payout is already requested; wait for it to be paid first")
	}

	var amountCents int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(commission_cents), 0) FROM affiliate_commissions
		WHERE tenant_id = $1 AND affiliate_id = $2 AND status = 'approved'`,
		tid, aid).Scan(&amountCents); err != nil {
		return httperr.ErrInternalServerError
	}
	if amountCents <= 0 {
		return httperr.C(fiber.StatusBadRequest, "no approved commissions to request")
	}

	var payoutID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO affiliate_payouts (tenant_id, affiliate_id, amount_cents, status)
		VALUES ($1, $2, $3, 'requested') RETURNING id`,
		tid, aid, amountCents).Scan(&payoutID); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id": payoutID, "amount_cents": amountCents, "status": "requested",
	})
}

// --- shared ----------------------------------------------------------------

func affiliateJSON(id, name, email, code string, percent int, status string, createdAt time.Time) fiber.Map {
	return fiber.Map{
		"id": id, "name": name, "email": email, "code": code,
		"commission_percent": percent, "status": status, "created_at": createdAt,
	}
}