package reviews

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/httperr"
)

func randSuffix5() string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 5)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

type reviewPayload struct {
	ID        string `json:"id"`
	ProductID string `json:"product_id"`
	OrderID   string `json:"order_id"`
	Rating    int    `json:"rating"`
	Status    string `json:"status"`
	Verified  bool   `json:"verified"`
	CreatedAt string `json:"created_at"`
}

type publicReviewsPayload struct {
	RatingAverage float64         `json:"rating_average"`
	RatingCount   int             `json:"rating_count"`
	Reviews       []reviewPayload `json:"reviews"`
}

// seedTenantAndProduct creates a tenant plus an active product with one active
// variant. When linked is true it also creates a customer who owns a DELIVERED
// order containing the product (the verified-purchase path).
func seedTenantAndProduct(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name, sfx string, linked bool) (
	tok string, tid, pid, cid, oid, vid string) {
	t.Helper()
	if err := pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		name, "rv-"+name+"-"+sfx).Scan(&tid); err != nil {
		t.Fatalf("seed tenant %s: %v", name, err)
	}
	tok, err := auth.Sign("test-secret", tid, tid[:8], "owner", time.Hour)
	if err != nil {
		t.Fatalf("sign merchant: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
		VALUES ($1, $2, $3, $4, 'usd', $5, 'active') RETURNING id`,
		tid, name, "rv-"+name+"-"+sfx, 995, 10).Scan(&pid); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
		VALUES ($1, $2, $3, $4, 'active') RETURNING id`,
		tid, pid, 995, 10).Scan(&vid); err != nil {
		t.Fatalf("seed variant: %v", err)
	}
	if linked {
		if err := tx.QueryRow(ctx, `
			INSERT INTO customers (tenant_id, email, phone) VALUES ($1, $2, $3) RETURNING id`,
			tid, "rv-buyer-"+sfx+"@example.com", "+1-rv-"+sfx).Scan(&cid); err != nil {
			t.Fatalf("seed customer: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO orders (tenant_id, customer_id, customer_name, customer_phone,
				shipping_address, total_cents, status)
			VALUES ($1, $2, $3, $4, $5, $6, 'delivered') RETURNING id`,
			tid, cid, "Rita Buyer", "+1-rv-"+sfx, "1 Main St", 1195).Scan(&oid); err != nil {
			t.Fatalf("seed delivered order: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items (tenant_id, order_id, product_id, variant_id, quantity, unit_price_cents)
			VALUES ($1, $2, $3, $4, 1, 995)`,
			tid, oid, pid, vid); err != nil {
			t.Fatalf("seed order item: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return
}

func productRating(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tid, pid string) (avg string, count int) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid)
	if err := tx.QueryRow(ctx,
		"SELECT rating_average::text, rating_count FROM products WHERE id = $1", pid).Scan(&avg, &count); err != nil {
		t.Fatalf("read rating: %v", err)
	}
	return
}

func newApp(t *testing.T, pool *pgxpool.Pool) *fiber.App {
	t.Helper()
	svc := New(pool)
	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	RegisterRoutes(v1, pool, "test-secret", svc)
	return app
}

type client struct {
	t   *testing.T
	app *fiber.App
	tid string
}

func (cl *client) do(method, path, bearer, body string, want int) (int, map[string]any) {
	cl.t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Tenant-ID", cl.tid)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := cl.app.Test(req, -1)
	if err != nil {
		cl.t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != want {
		cl.t.Fatalf("%s %s (tenant %s): status %d, want %d (body=%s)",
			method, path, cl.tid, res.StatusCode, want, raw)
	}
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			cl.t.Fatalf("decode %d: %v (%s)", want, err, raw)
		}
	}
	return res.StatusCode, out
}

// TestReviewLifecycleAndVerified is the Phase 16 acceptance: a customer's
// review is auto-linked to their delivered order (verified), stays pending and
// never counts toward the product rating until an admin publishes it, then the
// aggregate reflects it immediately; unverified reviews are allowed but
// deduped; cross-tenant reviews are RLS-invisible.
func TestReviewLifecycleAndVerified(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	sfx := randSuffix5()
	aTok, aTid, p1, c1, o1, _ := seedTenantAndProduct(t, ctx, pool, "alpha", sfx, true)
	_, bTid, _, _, _, _ := seedTenantAndProduct(t, ctx, pool, "beta", sfx, true) // isolation pair
	bTok, err := auth.Sign("test-secret", bTid, bTid[:8], "owner", time.Hour)
	if err != nil {
		t.Fatalf("sign beta: %v", err)
	}
	c1Tok, err := auth.SignCustomer("test-secret", aTid, c1, time.Hour)
	if err != nil {
		t.Fatalf("sign customer: %v", err)
	}

	app := newApp(t, pool)
	cl := &client{t: t, app: app, tid: aTid}

	// --- verified review, pending create has no aggregate effect ---
	_, created := cl.do("POST", "/api/v1/products/"+p1+"/reviews", c1Tok,
		`{"rating":4,"title":"Great","body":"Love it"}`, http.StatusCreated)
	rid := created["id"].(string)
	if created["status"] != "pending" || created["verified"] != true {
		t.Fatalf("review should be pending+verified, got %+v", created)
	}
	if created["order_id"] != o1 {
		t.Fatalf("verified review should link order %s, got %v", o1, created["order_id"])
	}
	if avg, count := productRating(t, ctx, pool, aTid, p1); avg != "0.0" || count != 0 {
		t.Fatalf("pending review must not count yet, got avg=%s count=%d", avg, count)
	}
	pub := cl.doPub("/api/v1/products/" + p1 + "/reviews")
	if pub.RatingCount != 0 || len(pub.Reviews) != 0 {
		t.Fatalf("pending review must not appear publicly, got %+v", pub)
	}

	// --- unverified customer's review (same tenant, no delivered order) ---
	var c2id string
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin c2: %v", err)
	}
	_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", aTid)
	if err := tx.QueryRow(ctx, `
		INSERT INTO customers (tenant_id, email, phone) VALUES ($1, $2, $3) RETURNING id`,
		aTid, "rv-c2-"+sfx+"@example.com", "+1-rv-c2-"+sfx).Scan(&c2id); err != nil {
		t.Fatalf("seed c2: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit c2: %v", err)
	}
	c2Tok, err := auth.SignCustomer("test-secret", aTid, c2id, time.Hour)
	if err != nil {
		t.Fatalf("sign c2: %v", err)
	}
	_, unverified := cl.do("POST", "/api/v1/products/"+p1+"/reviews", c2Tok,
		`{"rating":5,"body":"No purchase, still good"}`, http.StatusCreated)
	if unverified["verified"] != false || unverified["order_id"] != "" {
		t.Fatalf("c2 review should be unverified, got %+v", unverified)
	}
	rid2 := unverified["id"].(string)

	// --- publish both; aggregate recomputes on each publish ---
	cl.do("PATCH", "/api/v1/reviews/"+rid, aTok, `{"status":"published"}`, http.StatusOK)
	if avg, count := productRating(t, ctx, pool, aTid, p1); avg != "4.0" || count != 1 {
		t.Fatalf("after first publish want 4.0/1, got %s/%d", avg, count)
	}
	cl.do("PATCH", "/api/v1/reviews/"+rid2, aTok, `{"status":"published"}`, http.StatusOK)
	if avg, count := productRating(t, ctx, pool, aTid, p1); avg != "4.5" || count != 2 {
		t.Fatalf("after second publish want 4.5/2, got %s/%d", avg, count)
	}
	pub = cl.doPub("/api/v1/products/" + p1 + "/reviews")
	if pub.RatingCount != 2 || len(pub.Reviews) != 2 {
		t.Fatalf("public list should show 2 published, got %+v", pub)
	}

	// --- dedupe + validation + auth guards ---
	cl.do("POST", "/api/v1/products/"+p1+"/reviews", c1Tok,
		`{"rating":3}`, http.StatusConflict)
	cl.do("POST", "/api/v1/products/"+p1+"/reviews", c2Tok,
		`{"rating":3}`, http.StatusConflict) // another unverified review is also a dupe
	cl.do("POST", "/api/v1/products/"+p1+"/reviews", c1Tok,
		`{"rating":0}`, http.StatusBadRequest)
	cl.do("POST", "/api/v1/products/"+p1+"/reviews", c1Tok,
		`{"rating":9}`, http.StatusBadRequest)
	cl.do("POST", "/api/v1/products/"+p1+"/reviews", "garbage-token",
		`{"rating":4}`, http.StatusUnauthorized)
	cl.do("PATCH", "/api/v1/reviews/"+rid, aTok, `{"status":"pending"}`, http.StatusBadRequest)
	// Merchant token must not mint reviews; customer token must not manage reviews.
	cl.do("POST", "/api/v1/products/"+p1+"/reviews", aTok, `{"rating":4}`, http.StatusForbidden)
	cl.do("PATCH", "/api/v1/reviews/"+rid, c1Tok, `{"status":"rejected"}`, http.StatusForbidden)

	// --- cross-tenant isolation: beta's admin cannot see alpha's review ---
	betaCl := &client{t: t, app: app, tid: bTid}
	betaCl.do("PATCH", "/api/v1/reviews/"+rid, bTok, `{"status":"rejected"}`, http.StatusNotFound)
	// alpha's review must remain published after beta's 404 attempt.
	pub = cl.doPub("/api/v1/products/" + p1 + "/reviews")
	if len(pub.Reviews) != 2 {
		t.Fatalf("cross-tenant 404 must not mutate alpha, got %d reviews", len(pub.Reviews))
	}
}

// TestRejectAndDeleteRecompute covers admin reject keeping aggregates unchanged
// and admin delete triggering a recompute back to zero.
func TestRejectAndDeleteRecompute(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	sfx := randSuffix5()
	aTok, aTid, p1, c1, _, _ := seedTenantAndProduct(t, ctx, pool, "alpha", "rej"+sfx, true)
	c1Tok, err := auth.SignCustomer("test-secret", aTid, c1, time.Hour)
	if err != nil {
		t.Fatalf("sign customer: %v", err)
	}
	app := newApp(t, pool)
	cl := &client{t: t, app: app, tid: aTid}

	_, created := cl.do("POST", "/api/v1/products/"+p1+"/reviews", c1Tok,
		`{"rating":5}`, http.StatusCreated)
	rid := created["id"].(string)
	cl.do("PATCH", "/api/v1/reviews/"+rid, aTok, `{"status":"published"}`, http.StatusOK)
	if avg, count := productRating(t, ctx, pool, aTid, p1); avg != "5.0" || count != 1 {
		t.Fatalf("published want 5.0/1, got %s/%d", avg, count)
	}

	// A second review that the merchant rejects — must not move the aggregate.
	var c2 string
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin c2: %v", err)
	}
	_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", aTid)
	if err := tx.QueryRow(ctx, `
		INSERT INTO customers (tenant_id, email, phone) VALUES ($1, $2, $3) RETURNING id`,
		aTid, "rv-rej2-"+sfx+"@example.com", "+1-rv-rej2-"+sfx).Scan(&c2); err != nil {
		t.Fatalf("seed c2: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit c2: %v", err)
	}
	c2Tok, err := auth.SignCustomer("test-secret", aTid, c2, time.Hour)
	if err != nil {
		t.Fatalf("sign c2: %v", err)
	}
	_, rejected := cl.do("POST", "/api/v1/products/"+p1+"/reviews", c2Tok,
		`{"rating":1,"title":"Spam"}`, http.StatusCreated)
	rid2 := rejected["id"].(string)
	cl.do("PATCH", "/api/v1/reviews/"+rid2, aTok, `{"status":"rejected"}`, http.StatusOK)
	if avg, count := productRating(t, ctx, pool, aTid, p1); avg != "5.0" || count != 1 {
		t.Fatalf("reject must not change aggregate, got %s/%d", avg, count)
	}

	// Admin list surfaces the rejected review with its status; public never does.
	_, all := cl.do("GET", "/api/v1/reviews", aTok, "", http.StatusOK)
	if len(all["reviews"].([]any)) != 2 {
		t.Fatalf("admin list should show both statuses, got %+v", all["reviews"])
	}
	_, filtered := cl.do("GET", "/api/v1/reviews?status=rejected", aTok, "", http.StatusOK)
	if len(filtered["reviews"].([]any)) != 1 {
		t.Fatalf("status filter should return 1, got %+v", filtered["reviews"])
	}

	// Deleting the published review recomputes back to zero.
	_, del := cl.do("DELETE", "/api/v1/reviews/"+rid, aTok, "", http.StatusOK)
	if del["deleted"] != rid {
		t.Fatalf("delete should echo id, got %+v", del)
	}
	if avg, count := productRating(t, ctx, pool, aTid, p1); avg != "0.0" || count != 0 {
		t.Fatalf("after delete want 0.0/0, got %s/%d", avg, count)
	}
}

func (cl *client) doPub(path string) publicReviewsPayload {
	cl.t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("X-Tenant-ID", cl.tid)
	res, err := cl.app.Test(req, -1)
	if err != nil {
		cl.t.Fatalf("%s: %v", path, err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		cl.t.Fatalf("%s: status %d (body=%s)", path, res.StatusCode, raw)
	}
	var out publicReviewsPayload
	if err := json.Unmarshal(raw, &out); err != nil {
		cl.t.Fatalf("decode public reviews: %v (%s)", err, raw)
	}
	return out
}
