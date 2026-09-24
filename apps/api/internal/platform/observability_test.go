package platform_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/observe"
)

// TestErrorShape is the Phase 7 acceptance criterion for the error contract:
// every error crossing the boundary — a handler *E, a framework *fiber.Error
// (e.g. BodyParser), or a panic recovered by middleware — renders as the single
// documented shape {"error":{"code":"...","message":"..."}} with HTTP code
// intact and never a raw stack trace.
func TestErrorShape(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	app.Use(recover.New(recover.Config{EnableStackTrace: false}))

	app.Post("/malformed", func(c *fiber.Ctx) error {
		var in struct {
			Qty int `json:"qty"`
		}
		if err := c.BodyParser(&in); err != nil {
			return httperr.C(fiber.StatusBadRequest, "invalid body")
		}
		return nil
	})
	app.Get("/notfound", func(c *fiber.Ctx) error {
		return httperr.NotFound("product_not_found", "product not found")
	})
	app.Get("/panic", func(c *fiber.Ctx) error {
		panic("boom")
	})

	check := func(name, path, method, body string, wantStatus int, wantCode string) {
		t.Run(name, func(t *testing.T) {
			var reader io.Reader
			if body != "" {
				reader = strings.NewReader(body)
			}
			req := httptest.NewRequest(method, path, reader)
			if method == http.MethodPost || method == http.MethodPut {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, wantStatus)
			}
			raw, _ := io.ReadAll(resp.Body)
			var got struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("body is not the standard error shape: %s", raw)
			}
			if got.Error.Code != wantCode {
				t.Errorf("error.code = %q, want %q (body=%s)", got.Error.Code, wantCode, raw)
			}
			if got.Error.Message == "" {
				t.Errorf("error.message unexpectedly empty (body=%s)", raw)
			}
		})
	}

	check("malformed body -> 400 invalid_request", "/malformed", http.MethodPost, `{"qty":`, http.StatusBadRequest, "invalid_request")
	check("handler *E -> 404 with code", "/notfound", http.MethodGet, "", http.StatusNotFound, "product_not_found")
	check("panic -> 500 internal_error", "/panic", http.MethodGet, "", http.StatusInternalServerError, "internal_error")
}

// TestMetricsExposition is the Phase 7 acceptance criterion for observability:
// /metrics (token-gated, mirroring main.go) serves Prometheus text containing
// the HTTP request counter and latency histogram plus the DB pool gauges, and
// refuses requests without the token.
func TestMetricsExposition(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set; skipping integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	const token = "test-metrics-token"
	metrics := observe.NewMetrics(pool)
	m := func(c *fiber.Ctx) error {
		if c.Get("Authorization") != "Bearer "+token {
			return httperr.Unauthorized("unauthorized", "invalid or missing bearer token")
		}
		return metrics.Handler()(c)
	}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	app.Use(observe.RequestID)
	app.Use(metrics.Middleware)
	app.Get("/metrics", m)
	app.Get("/work", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"ok": true}) })

	// fire a few requests through the counter
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/work", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("work request: %v", err)
		}
		resp.Body.Close()
	}

	// unauthorized without token
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("metrics request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("metrics without token: status %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("metrics request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("metrics content-type = %q, want text/plain", ct)
	}
	text := string(raw)
	for _, want := range []string{
		"shopkeet_http_requests_total",
		"shopkeet_http_request_duration_seconds",
		"shopkeet_db_pool_max",
		`route="/work",status="200"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}
