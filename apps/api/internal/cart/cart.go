package cart

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/bundles"
	"github.com/shopkeet/api/internal/discounts"
	"github.com/shopkeet/api/internal/giftcards"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// emailRe is a deliberately permissive email shape for recovery capture — the
// provider does the strict validation at send time.
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// Service implements the guest cart surface. Handlers read/write through the
// request transaction CustomerMW opened (c.Locals("tx")), so RLS scopes every
// query to the resolved tenant; within it, cart lookup is keyed by the opaque
// customer_session.
type Service struct {
	pool     *pgxpool.Pool
	reserver Reserver
}

// New builds a cart Service. reserver is best-effort only (see reserve.go).
func New(pool *pgxpool.Pool, reserver Reserver) *Service {
	return &Service{pool: pool, reserver: reserver}
}

// --- helpers ------------------------------------------------------------------

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func customerSession(c *fiber.Ctx) string {
	s, _ := c.Locals("customer_session").(string)
	return s
}

type itemRow struct {
	id             string
	productID      string
	variantID      string
	name           string
	slug           string
	quantity       int
	priceCents     int
	currency       string
	lineTotalCents int
	bundleID       *string
}

type cartPayload struct {
	id            string
	items         []itemRow
	total         int
	currency      string
	discountCode  string
	discountCents int
	giftCardCode  string
	giftCardCents int
	email         string
}

// loadCart returns the customer's cart with its items (product info joined in)
// and totals, plus the applied discount_code and its predicted discount_cents
// and the applied gift_card_code with a predicted gift_card_cents (both
// best-effort — checkout re-validates authoritatively). Returns (nil, nil)
// when the session has no cart yet.
//
// Every query is scoped by tenant_id explicitly. customer_session is a
// client-supplied string that is only unique per tenant (UNIQUE (tenant_id,
// customer_session)), so a session-only predicate could read another tenant's
// cart whenever the role the API connects as bypasses RLS.
func loadCart(c *fiber.Ctx, tx pgx.Tx, session string) (*cartPayload, error) {
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)
	var cartID, discountCode, giftCardCode, email string
	err := tx.QueryRow(ctx,
		`SELECT id, COALESCE(discount_code, ''), COALESCE(gift_card_code, ''), COALESCE(customer_email, '')
		FROM carts WHERE tenant_id = $1 AND customer_session = $2`,
		tid, session).Scan(&cartID, &discountCode, &giftCardCode, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `
		SELECT ci.id, ci.product_id, ci.variant_id, ci.quantity,
		       p.name, p.slug, v.price_cents, p.currency, ci.bundle_id
		FROM cart_items ci
		JOIN products p ON p.id = ci.product_id
		JOIN product_variants v ON v.id = ci.variant_id
		WHERE ci.cart_id = $1 AND ci.tenant_id = $2
		ORDER BY p.name`, cartID, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cp := &cartPayload{id: cartID}
	for rows.Next() {
		var it itemRow
		if err := rows.Scan(&it.id, &it.productID, &it.variantID, &it.quantity,
			&it.name, &it.slug, &it.priceCents, &it.currency, &it.bundleID); err != nil {
			return nil, err
		}
		it.lineTotalCents = it.quantity * it.priceCents
		cp.items = append(cp.items, it)
		cp.total += it.lineTotalCents
		cp.currency = it.currency
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Phase 25 — repricing pass: bundle rows are charged as a unit (flat
	// bundle_price_cents or the configured discount off the summed components)
	// and plain rows get the best qualifying quantity-break discount. The raw
	// sum above is replaced so the cart advertises exactly what checkout will
	// charge; best-effort, checkout re-derives the same arithmetic authoritatively.
	priced := make([]bundles.CartItem, 0, len(cp.items))
	for _, it := range cp.items {
		priced = append(priced, bundles.CartItem{
			CartItemID: it.id, ProductID: it.productID, VariantID: it.variantID,
			Quantity: it.quantity, PriceCents: it.priceCents, BundleID: it.bundleID,
		})
	}
	perLine, total, err := bundles.PriceCart(ctx, tx, priced)
	if err != nil {
		return nil, err
	}
	cp.total = total
	for i := range cp.items {
		cp.items[i].lineTotalCents = perLine[cp.items[i].id]
	}

	cp.discountCode = discountCode
	cp.giftCardCode = giftCardCode
	cp.email = email
	if discountCode != "" {
		if q, err := discounts.Resolve(ctx, tx, tid, discountCode, cp.total); err == nil {
			cp.discountCents = q.DiscountCents
		}
	}
	if giftCardCode != "" {
		// Best-effort preview: what the card would cover against the goods total
		// (no shipping/tax yet — checkout is authoritative). An invalid card
		// still shows its code with 0 predicted cents, exactly like a bad code.
		if q, err := giftcards.Resolve(ctx, tx, tid, giftCardCode); err == nil {
			avail := cp.total - cp.discountCents
			if avail < 0 {
				avail = 0
			}
			if q.Cents < avail {
				avail = q.Cents
			}
			cp.giftCardCents = avail
		}
	}
	return cp, nil
}

func cartJSON(cp *cartPayload) fiber.Map {
	if cp == nil {
		return fiber.Map{"cart": nil}
	}
	items := make([]fiber.Map, 0, len(cp.items))
	for _, it := range cp.items {
		bid := ""
		if it.bundleID != nil {
			bid = *it.bundleID
		}
		items = append(items, fiber.Map{
			"id":               it.id,
			"product_id":       it.productID,
			"variant_id":       it.variantID,
			"name":             it.name,
			"slug":             it.slug,
			"quantity":         it.quantity,
			"price_cents":      it.priceCents,
			"currency":         it.currency,
			"line_total_cents": it.lineTotalCents,
			"bundle_id":        bid,
		})
	}
	return fiber.Map{"cart": fiber.Map{
		"id":              cp.id,
		"items":           items,
		"total_cents":     cp.total,
		"currency":        cp.currency,
		"discount_code":   cp.discountCode,
		"discount_cents":  cp.discountCents,
		"gift_card_code":  cp.giftCardCode,
		"gift_card_cents": cp.giftCardCents,
		"email":           cp.email,
	}}
}

// --- handlers -------------------------------------------------------------------

// GetCart handles GET /cart (Customer). Returns {"cart": null} when this
// session has no cart yet — a fresh guest always has a valid empty cart.
func (s *Service) GetCart(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	cp, err := loadCart(c, tx, customerSession(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

type addItemRequest struct {
	VariantID string `json:"variant_id"`
	Quantity  int    `json:"quantity"`
}

// AddItem handles POST /cart (Customer). Creates the cart on first use and
// merges quantity for the same variant already in the cart. Only variants of
// active products can be carted. Stock is deliberately NOT gated here —
// checkout arbitrates (docs/07-expansion-build-spec.md Phase 8). Best-effort
// Redis reserve on the way out.
func (s *Service) AddItem(c *fiber.Ctx) error {
	var req addItemRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.VariantID == "" || req.Quantity < 1 || req.Quantity > 999 {
		return httperr.C(fiber.StatusBadRequest, "variant_id and quantity (1-999) required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)
	session := customerSession(c)

	// Variant must exist, belong to an active product, and itself be active
	// (RLS-scoped by the request tenant).
	var productID string
	var active bool
	err := tx.QueryRow(ctx, `
		SELECT v.product_id, (p.status = 'active' AND v.status = 'active')
		FROM product_variants v
		JOIN products p ON p.id = v.product_id
		WHERE v.id = $1`, req.VariantID).Scan(&productID, &active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return httperr.C(fiber.StatusNotFound, "variant not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}

	// One cart per session (UNIQUE (tenant_id, customer_session)); merge lines.
	if _, err := tx.Exec(ctx, `
		INSERT INTO carts (tenant_id, customer_session)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id, customer_session) DO NOTHING`, tid, session); err != nil {
		return httperr.ErrInternalServerError
	}
	var cartID string
	if err := tx.QueryRow(ctx,
		"SELECT id FROM carts WHERE tenant_id = $1 AND customer_session = $2", tid, session).Scan(&cartID); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO cart_items (tenant_id, cart_id, product_id, variant_id, quantity)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (cart_id, variant_id)
		DO UPDATE SET quantity = cart_items.quantity + EXCLUDED.quantity`,
		tid, cartID, productID, req.VariantID, req.Quantity); err != nil {
		return httperr.ErrInternalServerError
	}

	// Fast-path reservation; never fails the request (checkout is authoritative).
	_ = s.reserver.ReserveUnits(ctx, req.VariantID, req.Quantity)

	// Phase 17 — cart activity: an add-to-cart is a sign of intent; refresh the
	// recovery clock so the abandoned-cart sweep starts over for active carts.
	if _, err := tx.Exec(ctx,
		"UPDATE carts SET last_activity_at = now() WHERE tenant_id = $1 AND customer_session = $2",
		tid, session); err != nil {
		return httperr.ErrInternalServerError
	}

	cp, err := loadCart(c, tx, session)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

type quantityRequest struct {
	Quantity int `json:"quantity"`
}

// UpdateItemQuantity handles PATCH /cart/items/:id (Customer).
func (s *Service) UpdateItemQuantity(c *fiber.Ctx) error {
	var req quantityRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.Quantity < 1 || req.Quantity > 999 {
		return httperr.C(fiber.StatusBadRequest, "quantity (1-999) required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	tid, _ := c.Locals("tenant_id").(string)
	tag, err := tx.Exec(c.Context(), `
		UPDATE cart_items ci SET quantity = $1
		FROM carts c
		WHERE ci.id = $2 AND ci.cart_id = c.id AND c.tenant_id = $3 AND c.customer_session = $4`,
		req.Quantity, c.Params("id"), tid, customerSession(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "cart item not found")
	}
	if _, err := tx.Exec(c.Context(),
		"UPDATE carts SET last_activity_at = now() WHERE tenant_id = $1 AND customer_session = $2",
		tid, customerSession(c)); err != nil {
		return httperr.ErrInternalServerError
	}
	cp, err := loadCart(c, tx, customerSession(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

// RemoveItem handles DELETE /cart/items/:id (Customer).
func (s *Service) RemoveItem(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	tid, _ := c.Locals("tenant_id").(string)
	tag, err := tx.Exec(c.Context(), `
		DELETE FROM cart_items ci
		USING carts c
		WHERE ci.id = $1 AND ci.cart_id = c.id AND c.tenant_id = $2 AND c.customer_session = $3`,
		c.Params("id"), tid, customerSession(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "cart item not found")
	}
	if _, err := tx.Exec(c.Context(),
		"UPDATE carts SET last_activity_at = now() WHERE tenant_id = $1 AND customer_session = $2",
		tid, customerSession(c)); err != nil {
		return httperr.ErrInternalServerError
	}
	cp, err := loadCart(c, tx, customerSession(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

type discountRequest struct {
	Code string `json:"code"`
}

// ApplyDiscount handles POST /cart/discount (Customer). Validates the code at
// apply-time (exists, active, in date range, subtotal minimum, usage headroom)
// via discounts.Resolve and stores it on the cart. Checkout re-validates and
// claims the usage inside the order transaction — the apply here is never
// authoritative.
func (s *Service) ApplyDiscount(c *fiber.Ctx) error {
	var req discountRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		return httperr.C(fiber.StatusBadRequest, "code required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)
	session := customerSession(c)

	// The cart may not exist yet (code applied before the first item) — the
	// subtotal is then 0, and any minimum still gates the apply.
	cp, err := loadCart(c, tx, session)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	subtotal := 0
	if cp != nil {
		subtotal = cp.total
	}
	if _, err := discounts.Resolve(ctx, tx, tid, code, subtotal); err != nil {
		return translateDiscountErr(err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO carts (tenant_id, customer_session)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id, customer_session) DO NOTHING`, tid, session); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, `
		UPDATE carts SET discount_code = $1, last_activity_at = now()
		WHERE tenant_id = $2 AND customer_session = $3`,
		code, tid, session); err != nil {
		return httperr.ErrInternalServerError
	}
	cp, err = loadCart(c, tx, session)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

// translateDiscountErr maps a discounts.CodeError to the JSON error shape; any
// unexpected error is a 500.
func translateDiscountErr(err error) error {
	var ce *discounts.CodeError
	if errors.As(err, &ce) {
		return httperr.C(ce.Status, ce.Message)
	}
	return httperr.ErrInternalServerError
}

type giftCardRequest struct {
	Code string `json:"code"`
}

// ApplyGiftCard handles POST /cart/gift-card (Customer, Phase 18). Validates
// the card at apply-time (exists, active, unexpired, still has balance) via
// giftcards.Resolve and stores it on the cart. Checkout re-validates and claims
// the balance inside the order transaction — the apply here is never
// authoritative.
func (s *Service) ApplyGiftCard(c *fiber.Ctx) error {
	var req giftCardRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		return httperr.C(fiber.StatusBadRequest, "code required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)
	session := customerSession(c)

	if _, err := giftcards.Resolve(ctx, tx, tid, code); err != nil {
		return translateGiftCardErr(err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO carts (tenant_id, customer_session)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id, customer_session) DO NOTHING`, tid, session); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, `
		UPDATE carts SET gift_card_code = $1, last_activity_at = now()
		WHERE tenant_id = $2 AND customer_session = $3`,
		code, tid, session); err != nil {
		return httperr.ErrInternalServerError
	}
	cp, err := loadCart(c, tx, session)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

// translateGiftCardErr maps a giftcards.CodeError to the JSON error shape; any
// unexpected error is a 500.
func translateGiftCardErr(err error) error {
	var ce *giftcards.CodeError
	if errors.As(err, &ce) {
		return httperr.C(ce.Status, ce.Message)
	}
	return httperr.ErrInternalServerError
}

type emailRequest struct {
	Email string `json:"email"`
}

// CaptureEmail handles POST /cart/email (Customer, Phase 17). The storefront
// collects an optional email before checkout; it is stored on the cart and
// powers the abandoned-cart recovery job. Idempotent by construction — the
// value is simply upserted, so retries never double-side-effect.
func (s *Service) CaptureEmail(c *fiber.Ctx) error {
	var req emailRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !emailRe.MatchString(email) || len(email) > 320 {
		return httperr.C(fiber.StatusBadRequest, "valid email required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)
	session := customerSession(c)

	if _, err := tx.Exec(ctx, `
		INSERT INTO carts (tenant_id, customer_session, customer_email)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, customer_session) DO NOTHING`, tid, session, email); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, `
		UPDATE carts SET customer_email = $1, last_activity_at = now()
		WHERE tenant_id = $2 AND customer_session = $3`, email, tid, session); err != nil {
		return httperr.ErrInternalServerError
	}
	cp, err := loadCart(c, tx, session)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

// --- Phase 17: abandoned-cart sweep -------------------------------------------

// SweepAbandonedCarts runs the hourly recovery job body. For every tenant it
// finds carts that have sat idle (>1h), carry a captured email, have no
// recovery email yet, and still hold items, then invokes send exactly once per
// qualifying cart and stamps recovery_sent_at so the sweep is idempotent.
// "Converted" carts — a cart whose owner checked out — are never candidates
// structurally: checkout deletes the cart and its items in the order
// transaction (orders.go), so they no longer exist to be swept.
func SweepAbandonedCarts(ctx context.Context, pool *pgxpool.Pool,
	send func(ctx context.Context, tenantID, cartID string) error) (int, error) {

	tenantRows, err := pool.Query(ctx, "SELECT id FROM tenants ORDER BY id")
	if err != nil {
		return 0, err
	}
	var tenantIDs []string
	for tenantRows.Next() {
		var id string
		if err := tenantRows.Scan(&id); err != nil {
			tenantRows.Close()
			return 0, err
		}
		tenantIDs = append(tenantIDs, id)
	}
	tenantRows.Close()
	if err := tenantRows.Err(); err != nil {
		return 0, err
	}

	emailed := 0
	for _, tid := range tenantIDs {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return emailed, err
		}
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			tx.Rollback(ctx)
			return emailed, err
		}
		rows, err := tx.Query(ctx, `
			SELECT id FROM carts
			WHERE tenant_id = $1
			  AND customer_email IS NOT NULL
			  AND recovery_sent_at IS NULL
			  AND last_activity_at < now() - interval '1 hour'
			  AND EXISTS (SELECT 1 FROM cart_items ci WHERE ci.cart_id = carts.id)
			ORDER BY last_activity_at`, tid)
		if err != nil {
			tx.Rollback(ctx)
			return emailed, err
		}
		var candidates []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				tx.Rollback(ctx)
				return emailed, err
			}
			candidates = append(candidates, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			tx.Rollback(ctx)
			return emailed, err
		}

		for _, cartID := range candidates {
			// Send outside the marking update so a slow provider call never
			// holds the transaction open; notify failures don't abort the
			// sweep (matching the checkout rule: a failed send must never
			// fail the work that produced the event).
			if err := send(ctx, tid, cartID); err != nil {
				continue
			}
			emailed++
		}
		for _, cartID := range candidates {
			if _, err := tx.Exec(ctx,
				"UPDATE carts SET recovery_sent_at = now() WHERE id = $1 AND tenant_id = $2", cartID, tid); err != nil {
				tx.Rollback(ctx)
				return emailed, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return emailed, err
		}
	}
	return emailed, nil
}
