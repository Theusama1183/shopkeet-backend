// Package affiliates implements Phase 27 — the affiliate program. Single-level
// only by design (no MLM tiers): a merchant approves an affiliate and sets their
// commission_percent; a checkout that arrives via ?ref=CODE snapshots the code
// onto the order and books a pending commission at commission_percent of the
// order's discounted goods subtotal (shipping/tax excluded). The commission
// flips to approved only when the order reaches delivered (the existing
// order.paid event), so cancelled/returned orders never pay out. Payouts are
// merchant-initiated and manual.
//
// Affiliates hold their own JWT scope ("affiliate", signed via auth.SignAffiliate)
// and can never reach merchant or customer endpoints — parseMerchant and
// CustomerAuthMW both refuse that scope.
package affiliates

import (
	"context"
	"crypto/rand"
	"errors"
	"log"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/events"
)

// Service is a stateless handler bundle. Handlers run inside the RLS-scoped
// request transaction opened by TenantMW (admin) or AffiliateAuthMW (partner);
// the order.paid subscriber opens its own transaction scoped by set_config,
// exactly like loyalty/notifications. secret is the JWT signing key — affiliate
// login mints the same-scope tokens as merchant/customer login.
type Service struct {
	pool   *pgxpool.Pool
	secret string
}

func New(pool *pgxpool.Pool, secret string) *Service {
	return &Service{pool: pool, secret: secret}
}

// Subscribe wires the order.paid handler that approves a referred order's
// commission on delivery. It never returns an error: failing to approve a
// commission must not fail the status change that produced the event.
func (s *Service) Subscribe(bus *events.Bus) {
	bus.Subscribe("order.paid", s.onOrderPaid)
}

// RecordCheckout is called from orders.Checkout inside the checkout's request
// transaction. It validates the tracking code (tenant-scoped, status approved),
// snapshots the canonical code onto the order, and books a pending commission at
// commission_percent of baseCents (the discounted goods subtotal). An unknown /
// pending / rejected / suspended code is silently ignored — an affiliate link
// must never break checkout. Returns an error only for real failures.
func RecordCheckout(ctx context.Context, tx pgx.Tx, tid, orderID, code string, baseCents int) error {
	var affiliateID string
	var percent int
	var status string
	err := tx.QueryRow(ctx, `
		SELECT id, commission_percent, status FROM affiliates
		WHERE tenant_id = $1 AND code = $2`, tid, code).
		Scan(&affiliateID, &percent, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "approved" {
		return nil
	}

	// Partner the order with the affiliate's canonical code (case/variant of the
	// code as the partner registered it) and book the pending commission. The
	// UNIQUE(affiliate_id, order_id) guard makes retries a no-op.
	if _, err := tx.Exec(ctx, `
		UPDATE orders SET affiliate_code = $1 WHERE id = $2`, code, orderID); err != nil {
		return err
	}
	commission := baseCents * percent / 100
	if _, err := tx.Exec(ctx, `
		INSERT INTO affiliate_commissions (tenant_id, affiliate_id, order_id, commission_cents, status)
		VALUES ($1, $2, $3, $4, 'pending') ON CONFLICT (affiliate_id, order_id) DO NOTHING`,
		tid, affiliateID, orderID, commission); err != nil {
		return err
	}
	return nil
}

// onOrderPaid approves any still-pending commission for the delivered order.
// Idempotent by construction: re-emitting order.paid is a no-op once approved.
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
		log.Printf("[affiliates] begin failed: %v", err)
		return nil
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		log.Printf("[affiliates] set tenant failed: %v", err)
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE affiliate_commissions SET status = 'approved'
		WHERE order_id = $1 AND status = 'pending'`, orderID); err != nil {
		log.Printf("[affiliates] approve commission for order %s failed: %v", orderID, err)
		return nil
	}
	if err := tx.Commit(ctx); err != nil {
		log.Printf("[affiliates] commit failed: %v", err)
	}
	return nil
}

// --- helpers ----------------------------------------------------------------

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func tenantID(c *fiber.Ctx) string {
	id, _ := c.Locals("tenant_id").(string)
	return id
}

func affiliateID(c *fiber.Ctx) string {
	id, _ := c.Locals("affiliate_id").(string)
	return id
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// randomCode returns 8 uppercase alphanumeric chars (crypto randomness so
// tracking codes aren't guessable and are unique per tenant).
func randomCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}