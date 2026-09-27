package reviews

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/cache"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// Package reviews implements Phase 16 — Product Reviews (Judge.me
// replacement). A review lives per tenant+product and is created by a signed-in
// customer (CustomerAuthMW sets c.Locals("customer_id")). When that customer
// has a delivered order containing the product, the review is auto-linked to
// the order (order_id present => verified purchase). Ratings aggregate on the
// products table and are recomputed the moment a review is published/unpublished
// or deleted; a pending create never touches the aggregate, per the acceptance
// criterion.

// Service serves the review surface on /api/v1.
type Service struct {
	pool  *pgxpool.Pool
	cache cache.Cache
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// SetCache wires the shared Redis cache so publish/unpublish/delete drop any
// cached product detail the aggregates are embedded in (nil is safe and keeps
// tests cache-free).
func (s *Service) SetCache(c cache.Cache) { s.cache = c }

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok
}

func tenantID(c *fiber.Ctx) string {
	if tid, _ := c.Locals("tenant_id").(string); tid != "" {
		return tid
	}
	return ""
}

func strp(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullableStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nonNil returns an empty slice for a nil input so uuid[] columns stay '{}'
// instead of encoding NULL and tripping NOT NULL.
func nonNil(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}

// reviewRow is one product_reviews row. photos is the uuid[] flattened to text[]
// at scan time so pgx can decode it as a native []string.
type reviewRow struct {
	id        string
	productID string
	orderID   *string
	rating    int
	title     *string
	body      *string
	photos    []string
	status    string
	createdAt time.Time
	verified  bool
}

func reviewJSON(r *reviewRow) fiber.Map {
	return fiber.Map{
		"id":                    r.id,
		"product_id":            r.productID,
		"order_id":              strp(r.orderID),
		"rating":                r.rating,
		"title":                 strp(r.title),
		"body":                  strp(r.body),
		"photo_media_asset_ids": r.photos,
		"status":                r.status,
		"verified":              r.orderID != nil,
		"created_at":            r.createdAt.Format(time.RFC3339),
	}
}

// reviewSelect builds the shared column list. photo media ids are unnested and
// re-aggregated as text[] so the driver decodes them into a pure []string.
const reviewSelect = `
	SELECT r.id, r.product_id, r.order_id, r.rating, r.title, r.body,
	       COALESCE(ARRAY(SELECT unnest(r.photo_media_asset_ids)::text), '{}'),
	       r.status, r.created_at
	FROM product_reviews r`

func scanReview(row interface{ Scan(dest ...any) error }) (*reviewRow, error) {
	var r reviewRow
	err := row.Scan(&r.id, &r.productID, &r.orderID, &r.rating, &r.title, &r.body,
		&r.photos, &r.status, &r.createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.verified = r.orderID != nil
	return &r, nil
}

func (s *Service) loadReview(ctx context.Context, tx pgx.Tx, id string) (*reviewRow, error) {
	return scanReview(tx.QueryRow(ctx, reviewSelect+" WHERE r.id = $1", id))
}

// recomputeRatings refreshes products.rating_count / rating_average from the
// published reviews for a product. The average is stored as NUMERIC(2,1);
// when no review is published both default back to 0.
func recomputeRatings(ctx context.Context, tx pgx.Tx, productID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE products SET
		  rating_count = (SELECT COUNT(*)::int FROM product_reviews
				WHERE product_id = $1 AND status = 'published'),
		  rating_average = COALESCE((SELECT AVG(rating)::numeric(2,1) FROM product_reviews
				WHERE product_id = $1 AND status = 'published'), 0)
		WHERE id = $1`, productID)
	return err
}

// --- handlers ---------------------------------------------------------------

// createReviewRequest is the POST /products/:id/reviews body. rating is
// required (1–5); title/body/photos are optional.
type createReviewRequest struct {
	Rating int      `json:"rating"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Photos []string `json:"photo_media_asset_ids"`
}

// CreateReview handles POST /products/:id/reviews (Customer). The review is
// created status='pending' (never counted yet). If the customer has a
// delivered order containing this product, order_id is attached => verified.
func (s *Service) CreateReview(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := tenantID(c)
	customerID, _ := c.Locals("customer_id").(string)
	if customerID == "" {
		return httperr.C(fiber.StatusForbidden, "customer identity required")
	}
	productID := c.Params("id")

	var req createReviewRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.Rating < 1 || req.Rating > 5 {
		return httperr.C(fiber.StatusBadRequest, "rating between 1 and 5 required")
	}

	var exists int
	if err := tx.QueryRow(ctx,
		"SELECT 1 FROM customers WHERE id = $1", customerID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httperr.C(fiber.StatusNotFound, "customer not found")
		}
		return httperr.ErrInternalServerError
	}
	if err := tx.QueryRow(ctx,
		"SELECT 1 FROM products WHERE id = $1", productID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httperr.C(fiber.StatusNotFound, "product not found")
		}
		return httperr.ErrInternalServerError
	}

	// Verified purchase: newest delivered order containing this product.
	var orderID *string
	if err := tx.QueryRow(ctx, `
		SELECT o.id
		FROM orders o
		JOIN order_items oi ON oi.order_id = o.id
		WHERE o.customer_id = $1 AND oi.product_id = $2 AND o.status = 'delivered'
		ORDER BY o.created_at DESC, o.id
		LIMIT 1`, customerID, productID).Scan(&orderID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return httperr.ErrInternalServerError
		}
	}

	// One review per purchase (or one anonymous-per-product review).
	var dup string
	var derr error
	if orderID != nil {
		derr = tx.QueryRow(ctx, `
			SELECT id FROM product_reviews
			WHERE product_id = $1 AND order_id = $2 AND customer_id = $3`,
			productID, *orderID, customerID).Scan(&dup)
	} else {
		derr = tx.QueryRow(ctx, `
			SELECT id FROM product_reviews
			WHERE product_id = $1 AND customer_id = $2 AND order_id IS NULL`,
			productID, customerID).Scan(&dup)
	}
	if derr == nil {
		return httperr.C(fiber.StatusConflict,
			"you have already reviewed this purchase")
	}
	if !errors.Is(derr, pgx.ErrNoRows) {
		return httperr.ErrInternalServerError
	}

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO product_reviews
		  (tenant_id, product_id, customer_id, order_id, rating, title, body, photo_media_asset_ids, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::uuid[], 'pending')
		RETURNING id`,
		tid, productID, customerID, orderID, req.Rating,
		nullableStr(req.Title), nullableStr(req.Body), nonNil(req.Photos)).Scan(&id); err != nil {
		return httperr.ErrInternalServerError
	}

	r, err := s.loadReview(ctx, tx, id)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if r == nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(reviewJSON(r))
}

// ListReviews handles GET /products/:id/reviews (Public) — published reviews
// only, newest first, plus the product's live rating aggregates.
func (s *Service) ListReviews(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	productID := c.Params("id")

	var exists int
	err := tx.QueryRow(ctx,
		"SELECT 1 FROM products WHERE id = $1 AND status = 'active'", productID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}

	rows, err := tx.Query(ctx, reviewSelect+`
		WHERE r.product_id = $1 AND r.status = 'published'
		ORDER BY r.created_at DESC, r.id`, productID)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()
	var reviews []fiber.Map
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		reviews = append(reviews, reviewJSON(r))
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	rows.Close()

	var avg float64
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT rating_average::float8, rating_count
		FROM products WHERE id = $1`, productID).Scan(&avg, &count); err != nil {
		return httperr.ErrInternalServerError
	}
	if reviews == nil {
		reviews = []fiber.Map{}
	}

	return c.JSON(fiber.Map{
		"rating_average": avg,
		"rating_count":   count,
		"reviews":        reviews,
	})
}

// updateReviewStatusRequest is the PATCH /reviews/:id body.
type updateReviewStatusRequest struct {
	Status string `json:"status"`
}

func validReviewStatus(s string) bool {
	return s == "published" || s == "rejected"
}

// UpdateReviewStatus handles PATCH /reviews/:id (Admin) — publish or reject.
// Any accepted change recomputes the product's rating aggregates and drops the
// cached product detail so storefront reads reflect it immediately.
func (s *Service) UpdateReviewStatus(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	var req updateReviewStatusRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if !validReviewStatus(req.Status) {
		return httperr.C(fiber.StatusBadRequest, "status must be published or rejected")
	}

	r, err := s.loadReview(ctx, tx, c.Params("id"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if r == nil {
		return httperr.C(fiber.StatusNotFound, "review not found")
	}
	if r.status != req.Status {
		if _, err := tx.Exec(ctx, `
			UPDATE product_reviews SET status = $1 WHERE id = $2`,
			req.Status, r.id); err != nil {
			return httperr.ErrInternalServerError
		}
		if err := recomputeRatings(ctx, tx, r.productID); err != nil {
			return httperr.ErrInternalServerError
		}
		cache.InvalidateProduct(ctx, s.cache, tenantID(c), r.productID)
	}

	updated, err := s.loadReview(ctx, tx, r.id)
	if err != nil || updated == nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(reviewJSON(updated))
}

// DeleteReview handles DELETE /reviews/:id (Admin). The product aggregate is
// recomputed so a deleted published review never leaves a stale average.
func (s *Service) DeleteReview(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	r, err := s.loadReview(ctx, tx, c.Params("id"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if r == nil {
		return httperr.C(fiber.StatusNotFound, "review not found")
	}
	if _, err := tx.Exec(ctx, "DELETE FROM product_reviews WHERE id = $1", r.id); err != nil {
		return httperr.ErrInternalServerError
	}
	if err := recomputeRatings(ctx, tx, r.productID); err != nil {
		return httperr.ErrInternalServerError
	}
	cache.InvalidateProduct(ctx, s.cache, tenantID(c), r.productID)
	return c.JSON(fiber.Map{"deleted": r.id})
}

// ListAllReviews handles GET /reviews (Admin) — every status for admin review
// (approve/reject flow needs to see pending rows), newest first, optional
// ?status= filter.
func (s *Service) ListAllReviews(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	where := "TRUE"
	var args []any
	if st := c.Query("status"); st != "" {
		if !validReviewStatus(st) && st != "pending" {
			return httperr.C(fiber.StatusBadRequest, "invalid status filter")
		}
		args = append(args, st)
		where = fmt.Sprintf("r.status = $%d", len(args))
	}
	rows, err := tx.Query(ctx, reviewSelect+`
		WHERE `+where+`
		ORDER BY r.created_at DESC, r.id`, args...)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()
	var reviews []fiber.Map
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		reviews = append(reviews, reviewJSON(r))
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	if reviews == nil {
		reviews = []fiber.Map{}
	}
	return c.JSON(fiber.Map{"reviews": reviews})
}
