// Package search implements the admin command-palette record search — one
// merchant endpoint that queries products, orders and customers the way the
// palette groups them. Every table carries a GENERATED `search_vector`
// tsvector column (products: migration 0006; orders/customers: 0036) matched
// with the same plainto_tsquery('english', …) the catalog list uses, so the
// three lookups share one readable WHERE clause behind a GIN index each.
//
// Admin-only: the handler runs on the TenantMW request transaction, which is
// scoped to the caller's tenant by Row-Level Security, and every statement
// still predicates `tenant_id = $n` explicitly (known-gotchas: RLS is a net,
// never the scope). Results are newest-first, capped per group.
package search

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// Search limits — the palette shows a handful of matches per record group, so
// perGroup stays small and q is bounded like every other admin input.
const (
	maxQueryLen = 100
	perGroup    = 8
)

// Service groups the search handler behind one constructed value, following
// the other admin packages (analytics, giftcards, …).
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// productResult is a palette hit on the products group.
type productResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type orderResult struct {
	ID           string `json:"id"`
	CustomerName string `json:"customer_name"`
	Status       string `json:"status"`
	TotalCents   int64  `json:"total_cents"`
	Currency     string `json:"currency"`
}

type customerResult struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Phone string `json:"phone"`
}

// Search handles GET /search?q=…. Groups are always present in the response
// (each is a JSON array, empty when nothing matches). An empty or over-long q
// is a 400; a valid query never 404s.
func (s *Service) Search(c *fiber.Ctx) error {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		return httperr.BadRequest("missing_q", "q query parameter is required")
	}
	if len(q) > maxQueryLen {
		return httperr.BadRequest("q_too_long", "q must be 100 characters or fewer")
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	tid := tenantID(c)
	ctx := c.Context()

	products, err := s.searchProducts(ctx, tx, tid, q)
	if err != nil {
		return err
	}
	orders, err := s.searchOrders(ctx, tx, tid, q)
	if err != nil {
		return err
	}
	customers, err := s.searchCustomers(ctx, tx, tid, q)
	if err != nil {
		return err
	}

	return c.JSON(fiber.Map{
		"products":  products,
		"orders":    orders,
		"customers": customers,
	})
}

func (s *Service) searchProducts(ctx context.Context, tx pgx.Tx, tid, q string) ([]productResult, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, name, status
		   FROM products
		  WHERE tenant_id = $1
		    AND search_vector @@ plainto_tsquery('english', $2)
		  ORDER BY created_at DESC, id
		  LIMIT $3`, tid, q, perGroup)
	if err != nil {
		return nil, httperr.ErrInternalServerError
	}
	defer rows.Close()

	out := make([]productResult, 0, perGroup)
	for rows.Next() {
		var r productResult
		if err := rows.Scan(&r.ID, &r.Name, &r.Status); err != nil {
			return nil, httperr.ErrInternalServerError
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, httperr.ErrInternalServerError
	}
	return out, nil
}

func (s *Service) searchOrders(ctx context.Context, tx pgx.Tx, tid, q string) ([]orderResult, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, customer_name, status, total_cents, currency
		   FROM orders
		  WHERE tenant_id = $1
		    AND search_vector @@ plainto_tsquery('english', $2)
		  ORDER BY created_at DESC, id
		  LIMIT $3`, tid, q, perGroup)
	if err != nil {
		return nil, httperr.ErrInternalServerError
	}
	defer rows.Close()

	out := make([]orderResult, 0, perGroup)
	for rows.Next() {
		var r orderResult
		if err := rows.Scan(&r.ID, &r.CustomerName, &r.Status, &r.TotalCents, &r.Currency); err != nil {
			return nil, httperr.ErrInternalServerError
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, httperr.ErrInternalServerError
	}
	return out, nil
}

func (s *Service) searchCustomers(ctx context.Context, tx pgx.Tx, tid, q string) ([]customerResult, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, COALESCE(email, ''), COALESCE(phone, '')
		   FROM customers
		  WHERE tenant_id = $1
		    AND search_vector @@ plainto_tsquery('english', $2)
		  ORDER BY created_at DESC, id
		  LIMIT $3`, tid, q, perGroup)
	if err != nil {
		return nil, httperr.ErrInternalServerError
	}
	defer rows.Close()

	out := make([]customerResult, 0, perGroup)
	for rows.Next() {
		var r customerResult
		if err := rows.Scan(&r.ID, &r.Email, &r.Phone); err != nil {
			return nil, httperr.ErrInternalServerError
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, httperr.ErrInternalServerError
	}
	return out, nil
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