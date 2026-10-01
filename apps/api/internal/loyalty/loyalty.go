// Package loyalty implements Phase 20 of the expansion spec: a points program
// (Smile.io/Gameball replacement) and per-customer referral codes built on the
// existing discounts table.
//
// Points: an order that reaches delivered (the existing "order.paid" event from
// Phase 12) credits its linked customer_id with floor(total_cents/100) ×
// loyalty_points_per_currency_unit, written to customers.loyalty_points and the
// append-only loyalty_ledger (reason "order_placed"). Guests earn nothing - the
// ledger is keyed by a customer account. Exactly-once is enforced by partial
// unique indexes on loyalty_ledger(order_id) per reason, so even a re-emitted
// event can never double-credit.
//
// Redemption: POST /loyalty/redeem converts N points into a one-time
// fixed_amount discount code (usage_limit=1) of floor(N / loyalty_redemption_rate) × 100
// cents applied to the customer's current cart, debiting the ledger with a
// negative "redeemed" entry. Code-based and automatic discounts never stack
// (see Phase 21); a redeemed code claims a usage exactly like any admin code.
//
// Referrals: each customer owns one fixed_amount code in the discounts table
// with customer_id pointing back at them. Any shopper (guest or account) can
// apply it at checkout; when the referred order reaches delivered, the referrer
// earns the same points the buyer's order_placed credit would have produced.
// Self-referral (a customer using their own code) does not pay out.
package loyalty

import (
	"context"
	"crypto/rand"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// ReferralDiscountCents is the fixed_amount value of a customer's referral code.
// The Phase 20 spec schema keeps the referral value off the tenant table (only
// loyalty_points_per_currency_unit and loyalty_redemption_rate are specified as
// merchant knobs), so the per-code value is a documented package default.
const ReferralDiscountCents = 500

const (
	reasonOrderPlaced = "order_placed"
	reasonReferral    = "referral"
	reasonRedeemed    = "redeemed"
)

// Service is a stateless handler bundle. Handlers run inside the RLS-scoped
// request transaction opened by CustomerAuthMW (c.Locals("tx")); the order.paid
// subscriber opens its own transaction scoped by set_config, exactly like
// notifications.
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Subscribe wires the order.paid handler. It never returns an error: a loyalty
// bookkeeping failure must not fail the status change that produced the event.
func (s *Service) Subscribe(bus *events.Bus) {
	bus.Subscribe("order.paid", s.onOrderPaid)
}

// --- event subscriber ---------------------------------------------------------

func (s *Service) onOrderPaid(ctx context.Context, e events.Event) error {
	m, ok := e.Data.(fiber.Map)
	if !ok {
		return nil
	}
	orderID, _ := m["order_id"].(string)
	tenantID, _ := m["tenant_id"].(string)
	if orderID == "" || tenantID == "" {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		log.Printf("[loyalty] begin failed: %v", err)
		return nil
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		log.Printf("[loyalty] set tenant failed: %v", err)
		return nil
	}

	var customerID *string
	var totalCents int
	var discountCode string
	err = tx.QueryRow(ctx, `
		SELECT customer_id, total_cents, COALESCE(discount_code, '')
		FROM orders WHERE id = $1`, orderID).
		Scan(&customerID, &totalCents, &discountCode)
	if err != nil {
		log.Printf("[loyalty] load order %s failed: %v", orderID, err)
		return nil
	}

	var perUnit int
	if err := tx.QueryRow(ctx,
		"SELECT loyalty_points_per_currency_unit FROM tenants WHERE id = $1", tenantID).
		Scan(&perUnit); err != nil {
		log.Printf("[loyalty] load tenant config failed: %v", err)
		return nil
	}

	points := 0
	if perUnit > 0 {
		points = (totalCents / 100) * perUnit
	}

	// Buyers with an account earn on their own delivered orders. Retry safety:
	// update the balance only when the ledger row was actually inserted.
	if customerID != nil && *customerID != "" && points > 0 {
		if !s.creditOnce(ctx, tx, tenantID, *customerID, points, reasonOrderPlaced, orderID) {
			return nil
		}
	}

	// Referral payout: the order used a discount whose customer_id is a
	// referrer. Same rate as the buyer would earn; self-referral is skipped.
	if discountCode != "" && points > 0 {
		var referrer *string
		err := tx.QueryRow(ctx, `
			SELECT customer_id FROM discounts
			WHERE tenant_id = $1 AND code = $2 AND customer_id IS NOT NULL`,
			tenantID, discountCode).Scan(&referrer)
		if err == nil && referrer != nil && *referrer != "" {
			if customerID == nil || *referrer != *customerID {
				s.creditOnce(ctx, tx, tenantID, *referrer, points, reasonReferral, orderID)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("[loyalty] commit failed: %v", err)
	}
	return nil
}

// creditOnce inserts a ledger entry guarded by its partial unique index and
// bumps the customer's balance iff the insert actually landed. Reports success.
func (s *Service) creditOnce(ctx context.Context, tx pgx.Tx, tenantID, customerID string,
	points int, reason, orderID string) bool {

	tag, err := tx.Exec(ctx, `
		INSERT INTO loyalty_ledger (tenant_id, customer_id, points, reason, order_id)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
		tenantID, customerID, points, reason, orderID)
	if err != nil {
		log.Printf("[loyalty] ledger insert failed: %v", err)
		return false
	}
	if tag.RowsAffected() == 0 {
		return true
	}
	if _, err := tx.Exec(ctx,
		"UPDATE customers SET loyalty_points = loyalty_points + $1 WHERE id = $2",
		points, customerID); err != nil {
		log.Printf("[loyalty] balance update failed: %v", err)
		return false
	}
	return true
}

// --- handlers -----------------------------------------------------------------

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func tenantID(c *fiber.Ctx) string {
	id, _ := c.Locals("tenant_id").(string)
	return id
}

func customerID(c *fiber.Ctx) string {
	id, _ := c.Locals("customer_id").(string)
	return id
}

// GetLoyalty handles GET /customers/me/loyalty (Customer). Returns the running
// balance and the recent ledger (newest first).
func (s *Service) GetLoyalty(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := tenantID(c)
	cid := customerID(c)

	var balance int
	if err := tx.QueryRow(ctx,
		"SELECT loyalty_points FROM customers WHERE id = $1", cid).Scan(&balance); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httperr.C(fiber.StatusNotFound, "customer not found")
		}
		return httperr.ErrInternalServerError
	}

	rows, err := tx.Query(ctx, `
		SELECT id, points, reason, order_id, created_at
		FROM loyalty_ledger
		WHERE customer_id = $1 AND tenant_id = $2
		ORDER BY created_at DESC, id
		LIMIT 100`, cid, tid)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()

	ledger := []fiber.Map{}
	for rows.Next() {
		var id, reason string
		var points int
		var orderID *string
		var createdAt time.Time
		if err := rows.Scan(&id, &points, &reason, &orderID, &createdAt); err != nil {
			return httperr.ErrInternalServerError
		}
		ledger = append(ledger, fiber.Map{
			"id": id, "points": points, "reason": reason,
			"order_id": strOrNil(orderID), "created_at": createdAt.Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}

	return c.JSON(fiber.Map{"balance": balance, "ledger": ledger})
}

func strOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

type redeemRequest struct {
	Points int `json:"points"`
}

// Redeem handles POST /loyalty/redeem (Customer). Converts points into a
// one-time fixed_amount discount code applied to the customer's current cart.
// Rejects over-balance redemptions (the Phase 20 acceptance criterion) and
// point amounts that don't convert to a whole currency unit of discount.
// Runs inside the customer's RLS-scoped request transaction; idempotency-guarded
// (Phase 14) so a retried request doesn't double-spend points.
func (s *Service) Redeem(c *fiber.Ctx) error {
	var req redeemRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.Points < 1 {
		return httperr.C(fiber.StatusBadRequest, "points required")
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := tenantID(c)
	cid := customerID(c)

	var rate int
	if err := tx.QueryRow(ctx,
		"SELECT loyalty_redemption_rate FROM tenants WHERE id = $1", tid).Scan(&rate); err != nil {
		return httperr.ErrInternalServerError
	}
	if rate <= 0 {
		return httperr.C(fiber.StatusBadRequest, "loyalty redemption is disabled")
	}

	var balance int
	if err := tx.QueryRow(ctx,
		"SELECT loyalty_points FROM customers WHERE id = $1 FOR UPDATE", cid).Scan(&balance); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httperr.C(fiber.StatusNotFound, "customer not found")
		}
		return httperr.ErrInternalServerError
	}
	if balance < req.Points {
		return httperr.C(fiber.StatusBadRequest, "insufficient loyalty points")
	}

	units := req.Points / rate
	if units == 0 {
		return httperr.C(fiber.StatusBadRequest,
			"points must convert to at least one currency unit of discount")
	}
	discountCents := units * 100

	code, err := insertDiscountCode(ctx, tx, tid, discountCents)
	if err != nil {
		return err
	}

	// Remember the deduction regardless (idempotency protects replays).
	tag, err := tx.Exec(ctx, `
		INSERT INTO loyalty_ledger (tenant_id, customer_id, points, reason)
		VALUES ($1, $2, $3, $4)`,
		tid, cid, -req.Points, reasonRedeemed)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx,
		"UPDATE customers SET loyalty_points = loyalty_points - $1 WHERE id = $2",
		req.Points, cid); err != nil {
		return httperr.ErrInternalServerError
	}

	// Apply the code to the current cart, mirroring POST /cart/discount: an
	// absent cart is created so a bare code still sticks.
	session := resolveSession(c)
	if _, err := tx.Exec(ctx, `
		INSERT INTO carts (tenant_id, customer_session)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id, customer_session) DO NOTHING`, tid, session); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, `
		UPDATE carts SET discount_code = $1, last_activity_at = now()
		WHERE tenant_id = $2 AND customer_session = $3`, code, tid, session); err != nil {
		return httperr.ErrInternalServerError
	}

	return c.JSON(fiber.Map{
		"code":           code,
		"discount_cents": discountCents,
		"points":         req.Points,
		"balance":        balance - req.Points,
	})
}

// GetReferral handles GET /customers/me/referral (Customer). Lazily creates the
// customer's unique fixed_amount referral code (one per customer) and returns it
// with its value, so a shopper can share it in an instant.
func (s *Service) GetReferral(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := tenantID(c)
	cid := customerID(c)

	var code string
	err := tx.QueryRow(ctx, `
		SELECT code FROM discounts
		WHERE tenant_id = $1 AND customer_id = $2 AND type = 'fixed_amount'
		ORDER BY created_at
		LIMIT 1`, tid, cid).Scan(&code)
	if err == nil {
		return c.JSON(fiber.Map{"code": code, "value_cents": ReferralDiscountCents})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return httperr.ErrInternalServerError
	}

	code, ierr := insertReferralCode(ctx, tx, tid, cid)
	if ierr != nil {
		return ierr
	}
	return c.JSON(fiber.Map{"code": code, "value_cents": ReferralDiscountCents})
}

// resolveSession mirrors CustomerMW's cart-session resolution: the cookie or
// X-Customer-Session header wins, and a missing session mints one that is echoed
// back so non-cookie callers persist it.
func resolveSession(c *fiber.Ctx) string {
	session := c.Cookies(auth.CustomerSessionCookie)
	if h := c.Get("X-Customer-Session"); h != "" {
		session = h
	}
	if session == "" || strings.TrimSpace(session) == "" {
		session = uuid.NewString()
		c.Cookie(&fiber.Cookie{
			Name: auth.CustomerSessionCookie, Value: session, Path: "/",
			HTTPOnly: true, SameSite: "lax",
		})
	}
	c.Set("X-Customer-Session", session)
	return session
}

// insertDiscountCode creates a one-time fixed_amount discount with a random
// unique code, retrying on the (tenant_id, code) collision. Returns the code.
func insertDiscountCode(ctx context.Context, tx pgx.Tx, tid string, valueCents int) (string, error) {
	return insertCode(ctx, tx, func(code string) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO discounts (tenant_id, code, type, value_cents, usage_limit, status)
			VALUES ($1, $2, 'fixed_amount', $3, 1, 'active')`,
			tid, code, valueCents)
		return err
	})
}

// insertReferralCode creates the customer's referral discount (unlimited usage,
// tracked back to the customer). Returns the code.
func insertReferralCode(ctx context.Context, tx pgx.Tx, tid, cid string) (string, error) {
	return insertCode(ctx, tx, func(code string) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO discounts (tenant_id, code, type, value_cents, status, customer_id)
			VALUES ($1, $2, 'fixed_amount', $3, 'active', $4)`,
			tid, code, ReferralDiscountCents, cid)
		return err
	})
}

func insertCode(ctx context.Context, tx pgx.Tx, fn func(code string) error) (string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		code, err := randomCode()
		if err != nil {
			return "", httperr.ErrInternalServerError
		}
		if err := fn(code); err != nil {
			if isUniqueViolation(err) {
				continue
			}
			return "", httperr.ErrInternalServerError
		}
		return code, nil
	}
	return "", httperr.ErrInternalServerError
}

// randomCode returns 10 uppercase alphanumeric characters (crypto randomness so
// redeem codes are unguessable and referral codes are unique per customer).
func randomCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
