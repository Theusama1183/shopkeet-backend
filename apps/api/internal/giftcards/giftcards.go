// Package giftcards implements Phase 18 of the expansion spec: merchant-issued
// store credit. An admin issues a gift card (code + initial balance + optional
// expiry); a customer applies it to a cart (POST /cart/gift-card) and checkout
// (internal/orders) re-validates the card and atomically claims exactly what
// the order owes against it — locked via FOR UPDATE inside the order
// transaction, so two concurrent checkouts spending the last dollar of a card
// can never double-spend it. The applied code + amount are snapshotted onto the
// order; the unused balance stays on the card. Cards are scoped by RLS exactly
// like every other tenant table.
package giftcards

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service implements the admin gift-cards surface. Handlers execute inside the
// request transaction opened by TenantMW, and every query also carries an
// explicit `tenant_id` predicate so isolation never depends on the RLS GUC (or on
// the database role the API connects as).
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// --- shared row + validation ----------------------------------------------------

type giftCardRow struct {
	id        string
	code      string
	initial   int
	balance   int
	status    string
	expiresAt *time.Time
	createdAt time.Time
}

const giftCardSelect = `
	SELECT id, code, initial_balance_cents, balance_cents, status, expires_at, created_at
	FROM gift_cards`

// Quote is a resolved gift card for a cart or checkout. Cents carries the
// card's remaining spendable balance after Resolve, or the amount actually
// applied to this order after Claim (min of balance and what was due).
type Quote struct {
	Code  string
	Cents int
}

// CodeError is a client-facing validation failure (wrong code, inactive,
// expired, exhausted). HTTP-facing callers translate it to a 4xx JSON error;
// anything else is a 500.
type CodeError struct {
	Status  int
	Message string
}

func (e *CodeError) Error() string { return e.Message }

func invalid(status int, msg string) error {
	return &CodeError{Status: status, Message: msg}
}

func (g giftCardRow) validate() error {
	if g.status != "active" {
		return invalid(fiber.StatusBadRequest, "gift card is not active")
	}
	if g.expiresAt != nil && !time.Now().UTC().Before(*g.expiresAt) {
		return invalid(fiber.StatusBadRequest, "gift card has expired")
	}
	if g.balance <= 0 {
		return invalid(fiber.StatusBadRequest, "gift card has no remaining balance")
	}
	return nil
}

func scanGiftCard(row pgx.Row) (giftCardRow, error) {
	var g giftCardRow
	err := row.Scan(&g.id, &g.code, &g.initial, &g.balance, &g.status, &g.expiresAt, &g.createdAt)
	return g, err
}

// giftCardByCode scopes a gift-card lookup by tenant explicitly. Every gift-card
// query carries `tenant_id = $1` rather than leaning on the RLS GUC: the
// isolation must hold by construction, not by which database role the API
// happens to connect as. (Production's DATABASE_URL uses a superuser, which
// bypasses FORCE RLS entirely — a GUC-only predicate would then match other
// tenants' cards. Same lesson as the Phase 17 cart-recovery fix.)
func giftCardByCode(ctx context.Context, tx pgx.Tx, tenantID, code, lock string) (pgx.Row, error) {
	q := giftCardSelect + " WHERE tenant_id = $1 AND code = $2"
	if lock != "" {
		q += " " + lock
	}
	return tx.QueryRow(ctx, q, tenantID, code), nil
}

// Resolve validates a code without writing, scoped to tenantID. Used by the cart
// apply/read path where checkout remains authoritative.
func Resolve(ctx context.Context, tx pgx.Tx, tenantID, code string) (*Quote, error) {
	row, err := giftCardByCode(ctx, tx, tenantID, code, "")
	if err != nil {
		return nil, err
	}
	g, err := scanGiftCard(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid(fiber.StatusNotFound, "gift card not found")
	}
	if err != nil {
		return nil, err
	}
	if err := g.validate(); err != nil {
		return nil, err
	}
	return &Quote{Code: g.code, Cents: g.balance}, nil
}

// Claim re-validates a code and, if valid, atomically draws from its balance
// inside the caller's (checkout) transaction, scoped to tenantID. The FOR UPDATE
// lock serializes concurrent checkouts so the last dollars of a card are spent
// exactly once. Claims at most amountDue cents; a fully-covered order leaves the
// remainder on the card (total never goes below zero because applied = min(balance, amountDue)).
func Claim(ctx context.Context, tx pgx.Tx, tenantID, code string, amountDue int) (*Quote, error) {
	row, err := giftCardByCode(ctx, tx, tenantID, code, "FOR UPDATE")
	if err != nil {
		return nil, err
	}
	g, err := scanGiftCard(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid(fiber.StatusNotFound, "gift card not found")
	}
	if err != nil {
		return nil, err
	}
	if err := g.validate(); err != nil {
		return nil, err
	}
	applied := g.balance
	if amountDue < applied {
		applied = amountDue
	}
	if applied > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE gift_cards SET balance_cents = balance_cents - $1
			WHERE id = $2 AND tenant_id = $3`, applied, g.id, tenantID); err != nil {
			return nil, err
		}
	}
	return &Quote{Code: g.code, Cents: applied}, nil
}

// --- helpers --------------------------------------------------------------------

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func tenantID(c *fiber.Ctx) string {
	id, _ := c.Locals("tenant_id").(string)
	return id
}

func toJSON(g giftCardRow) fiber.Map {
	return fiber.Map{
		"id":                   g.id,
		"code":                 g.code,
		"initial_balance_cents": g.initial,
		"balance_cents":         g.balance,
		"status":                g.status,
		"expires_at":            timeOrNil(g.expiresAt),
		"created_at":            g.createdAt.Format(time.RFC3339),
	}
}

func timeOrNil(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.Format(time.RFC3339)
}

// generateCode produces GC-XXXXXXXX (8 random uppercase hex chars). Codes are
// unique per tenant (UNIQUE (tenant_id, code)); a generated collision simply
// fails the insert and the merchant retries.
func generateCode() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "GC-" + strings.ToUpper(hex.EncodeToString(b)), nil
}

func translate(err error) error {
	var ce *CodeError
	if errors.As(err, &ce) {
		return httperr.C(ce.Status, ce.Message)
	}
	return httperr.ErrInternalServerError
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// --- admin handlers --------------------------------------------------------------

// ListGiftCards handles GET /gift-cards (Admin). Newest first. Scoped to the
// caller's tenant explicitly.
func (s *Service) ListGiftCards(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	rows, err := tx.Query(c.Context(),
		giftCardSelect+" WHERE tenant_id = $1 ORDER BY created_at DESC, id", tenantID(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()
	var out []fiber.Map
	for rows.Next() {
		g, err := scanGiftCard(rows)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		out = append(out, toJSON(g))
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	if out == nil {
		out = []fiber.Map{}
	}
	return c.JSON(fiber.Map{"gift_cards": out})
}

type createRequest struct {
	Code        string `json:"code"`
	AmountCents int    `json:"amount_cents"`
	ExpiresAt   string `json:"expires_at"`
}

// CreateGiftCard handles POST /gift-cards (Admin). Issues a card with the given
// balance; an empty code is auto-generated. Expiry is optional.
func (s *Service) CreateGiftCard(c *fiber.Ctx) error {
	var req createRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.AmountCents <= 0 {
		return httperr.C(fiber.StatusBadRequest, "amount_cents required and > 0")
	}
	var expiresAt *time.Time
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			return httperr.C(fiber.StatusBadRequest, "expires_at must be RFC3339")
		}
		expiresAt = &t
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		generated, err := generateCode()
		if err != nil {
			return httperr.ErrInternalServerError
		}
		code = generated
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	g, err := scanGiftCard(tx.QueryRow(c.Context(), `
		INSERT INTO gift_cards (tenant_id, code, initial_balance_cents, balance_cents, expires_at)
		VALUES ($1, $2, $3, $3, $4)
		RETURNING id, code, initial_balance_cents, balance_cents, status, expires_at, created_at`,
		tenantID(c), code, req.AmountCents, expiresAt))
	if err != nil {
		if isUniqueViolation(err) {
			return httperr.C(fiber.StatusConflict, "a gift card with this code already exists")
		}
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(toJSON(g))
}