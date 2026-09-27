package orders

import (
	"context"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/cache"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// --- returns (Phase 15) -------------------------------------------------------

type returnItemRow struct {
	id          string
	orderItemID string
	variantID   string
	productID   string
	quantity    int
}

type returnRow struct {
	id        string
	orderID   string
	reason    string
	status    string
	restock   bool
	createdAt time.Time
	items     []returnItemRow
}

// returnTransitions is the admin status chain: requesting is always the birth
// status; rejected can end a return before goods arrive; only received
// restocks. refunded is the terminal state after received.
var returnTransitions = map[string][]string{
	"requested": {"approved", "rejected"},
	"approved":  {"received", "rejected"},
	"received":  {"refunded"},
}

func loadReturn(c *fiber.Ctx, tx pgx.Tx, id string) (*returnRow, error) {
	ctx := c.Context()
	var r returnRow
	if err := tx.QueryRow(ctx, `
		SELECT id, order_id, reason, status, restock, created_at
		FROM returns WHERE id = $1`, id).
		Scan(&r.id, &r.orderID, &r.reason, &r.status, &r.restock, &r.createdAt); errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT ri.id, ri.order_item_id, ri.quantity, oi.variant_id, oi.product_id
		FROM return_items ri
		JOIN order_items oi ON oi.id = ri.order_item_id
		WHERE ri.return_id = $1
		ORDER BY ri.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it returnItemRow
		if err := rows.Scan(&it.id, &it.orderItemID, &it.quantity, &it.variantID, &it.productID); err != nil {
			return nil, err
		}
		r.items = append(r.items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &r, nil
}

func returnJSON(r *returnRow) fiber.Map {
	items := make([]fiber.Map, 0, len(r.items))
	for _, it := range r.items {
		items = append(items, fiber.Map{
			"id": it.id, "order_item_id": it.orderItemID,
			"variant_id": it.variantID, "product_id": it.productID,
			"quantity": it.quantity,
		})
	}
	return fiber.Map{
		"id": r.id, "order_id": r.orderID, "reason": r.reason,
		"status": r.status, "restock": r.restock,
		"created_at": r.createdAt.Format(time.RFC3339), "items": items,
	}
}

type returnLineRequest struct {
	OrderItemID string `json:"order_item_id"`
	Quantity    int    `json:"quantity"`
}

type createReturnRequest struct {
	Reason  string              `json:"reason"`
	Restock *bool               `json:"restock"`
	Items   []returnLineRequest `json:"items"`
}

// CreateReturn handles POST /orders/:id/returns (Customer or Admin). Under
// MerchantOrCustomerMW the actor is either a merchant (bearer token) or a
// customer; a customer is verified against the order's phone[/email] the same
// way GET /orders/:id is — a wrong phone on a random order id sees nothing.
// The return is born status='requested'; only an admin can move it forward.
func (s *Service) CreateReturn(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)

	order, err := loadOrder(c, tx, "id = $1", c.Params("id"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if order == nil {
		return httperr.C(fiber.StatusNotFound, "order not found")
	}
	if actor, _ := c.Locals("actor").(string); actor != "merchant" {
		phone := c.Query("phone")
		if phone == "" {
			return httperr.C(fiber.StatusBadRequest, "phone query param required")
		}
		if email := c.Query("email"); email != "" {
			if order.customerPhone != phone || strp(order.customerEmail) != email {
				return httperr.C(fiber.StatusNotFound, "order not found")
			}
		} else if order.customerPhone != phone {
			return httperr.C(fiber.StatusNotFound, "order not found")
		}
	}

	var req createReturnRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if len(req.Items) == 0 {
		return httperr.C(fiber.StatusBadRequest, "items required")
	}
	restock := true
	if req.Restock != nil {
		restock = *req.Restock
	}

	byID := map[string]orderItemRow{}
	for _, it := range order.items {
		byID[it.id] = it
	}
	type pendingLine struct {
		orderItemID string
		quantity    int
	}
	var pending []pendingLine
	for _, ln := range req.Items {
		if ln.OrderItemID == "" || ln.Quantity <= 0 {
			return httperr.C(fiber.StatusBadRequest,
				"order_item_id and quantity > 0 required per line")
		}
		oi, ok := byID[ln.OrderItemID]
		if !ok {
			return httperr.C(fiber.StatusBadRequest, "order_item_id not in this order")
		}
		var already int
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(ri.quantity), 0)
			FROM return_items ri
			JOIN returns r ON r.id = ri.return_id
			WHERE r.order_id = $1 AND ri.order_item_id = $2`,
			order.id, ln.OrderItemID).Scan(&already); err != nil {
			return httperr.ErrInternalServerError
		}
		if ln.Quantity > oi.quantity-already {
			return httperr.C(fiber.StatusBadRequest, "returning more than ordered")
		}
		pending = append(pending, pendingLine{orderItemID: ln.OrderItemID, quantity: ln.Quantity})
	}

	var returnID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO returns (tenant_id, order_id, reason, status, restock)
		VALUES ($1, $2, $3, 'requested', $4) RETURNING id`,
		tid, order.id, req.Reason, restock).Scan(&returnID); err != nil {
		return httperr.ErrInternalServerError
	}
	for _, pl := range pending {
		if _, err := tx.Exec(ctx, `
			INSERT INTO return_items (tenant_id, return_id, order_item_id, quantity)
			VALUES ($1, $2, $3, $4)`,
			tid, returnID, pl.orderItemID, pl.quantity); err != nil {
			return httperr.ErrInternalServerError
		}
	}

	auth.AfterCommit(c, func() {
		s.bus.Emit(context.Background(), events.Event{
			Name: "return.created",
			Data: fiber.Map{"return_id": returnID, "order_id": order.id, "tenant_id": tid},
		})
	})

	r, err := loadReturn(c, tx, returnID)
	if err != nil || r == nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(returnJSON(r))
}

// restockReturnLines returns received goods to stock: each return item
// increments its variant's inventory (the correct variant, never the product
// aggregate blanket), refreshes the products aggregates and drops the Redis
// product detail cache so the storefront repopulates.
func (s *Service) restockReturnLines(c *fiber.Ctx, tx pgx.Tx, tid string, items []returnItemRow) error {
	ctx := c.Context()
	for _, it := range items {
		if _, err := tx.Exec(ctx,
			"UPDATE product_variants SET inventory_count = inventory_count + $1 WHERE id = $2",
			it.quantity, it.variantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE products p SET
				price_cents = COALESCE((SELECT MIN(price_cents) FROM product_variants v
					WHERE v.product_id = p.id AND v.status = 'active'), 0),
				inventory_count = COALESCE((SELECT SUM(inventory_count) FROM product_variants v
					WHERE v.product_id = p.id AND v.status = 'active'), 0)
			WHERE p.id = $1`, it.productID); err != nil {
			return err
		}
		cache.InvalidateProduct(ctx, s.cache, tid, it.productID)
	}
	return nil
}

type updateReturnStatusRequest struct {
	Status string `json:"status"`
}

// UpdateReturnStatus handles PATCH /returns/:id/status (Admin). Walks the
// return chain requested → approved → received → refunded (rejected can end
// requested/approved). Marking a return received with restock=true returns the
// returned quantities to the correct variants — the Phase 15 acceptance
// criterion.
func (s *Service) UpdateReturnStatus(c *fiber.Ctx) error {
	var req updateReturnStatusRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)
	id := c.Params("id")

	var current string
	var restock bool
	if err := tx.QueryRow(ctx,
		"SELECT status, restock FROM returns WHERE id = $1 FOR UPDATE", id).
		Scan(&current, &restock); errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "return not found")
	} else if err != nil {
		return httperr.ErrInternalServerError
	}

	target := req.Status
	allowed := returnTransitions[current]
	found := false
	for _, a := range allowed {
		if a == target {
			found = true
			break
		}
	}
	if !found {
		return httperr.C(fiber.StatusBadRequest, "invalid status transition")
	}

	if _, err := tx.Exec(ctx,
		"UPDATE returns SET status = $1, updated_at = now() WHERE id = $2", target, id); err != nil {
		return httperr.ErrInternalServerError
	}

	if target == "received" {
		r, err := loadReturn(c, tx, id)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		if restock {
			if err := s.restockReturnLines(c, tx, tid, r.items); err != nil {
				return httperr.ErrInternalServerError
			}
		}
		auth.AfterCommit(c, func() {
			s.bus.Emit(context.Background(), events.Event{
				Name: "return.restocked",
				Data: fiber.Map{"return_id": id, "order_id": r.orderID, "tenant_id": tid},
			})
		})
	}

	r, err := loadReturn(c, tx, id)
	if err != nil || r == nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(returnJSON(r))
}

// ListReturns handles GET /returns (Admin). Returns the tenant's return
// requests, newest first, each with its items and the product/variant they
// reference.
func (s *Service) ListReturns(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	rows, err := tx.Query(ctx, `
		SELECT id, order_id, reason, status, restock, created_at
		FROM returns ORDER BY created_at DESC, id`)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	var found []returnRow
	for rows.Next() {
		var r returnRow
		if err := rows.Scan(&r.id, &r.orderID, &r.reason, &r.status, &r.restock, &r.createdAt); err != nil {
			rows.Close()
			return httperr.ErrInternalServerError
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	rows.Close()

	// Hydrate all items in one pass (pgx refuses a second query on an open
	// cursor); RLS scopes both tables to the resolved tenant.
	itemsByReturn := map[string][]returnItemRow{}
	rows, err = tx.Query(ctx, `
		SELECT ri.return_id, ri.id, ri.order_item_id, ri.quantity, oi.variant_id, oi.product_id
		FROM return_items ri
		JOIN order_items oi ON oi.id = ri.order_item_id
		ORDER BY ri.return_id, ri.id`)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	for rows.Next() {
		var it returnItemRow
		var rid string
		if err := rows.Scan(&rid, &it.id, &it.orderItemID, &it.quantity, &it.variantID, &it.productID); err != nil {
			rows.Close()
			return httperr.ErrInternalServerError
		}
		itemsByReturn[rid] = append(itemsByReturn[rid], it)
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	rows.Close()

	out := make([]fiber.Map, 0, len(found))
	for i := range found {
		found[i].items = itemsByReturn[found[i].id]
		out = append(out, returnJSON(&found[i]))
	}
	return c.JSON(fiber.Map{"returns": out})
}
