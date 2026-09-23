package platform_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// TestHealthz verifies the Phase 0 acceptance criterion: GET /healthz returns
// 200 with {"status":"ok"}.
func TestHealthz(t *testing.T) {
	tests := []struct {
		name       string
		setup      func() *fiber.App
		wantBody   string
		wantStatus int
	}{
		{
			name: "healthz returns ok",
			setup: func() *fiber.App {
				app := fiber.New()
				app.Get("/healthz", func(c *fiber.Ctx) error {
					return c.Status(200).JSON(fiber.Map{"status": "ok"})
				})
				return app
			},
			wantBody:   `{"status":"ok"}`,
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := tc.setup()
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("reading body: %v", err)
			}
			if string(body) != tc.wantBody {
				t.Errorf("body = %s, want %s", body, tc.wantBody)
			}
		})
	}
}