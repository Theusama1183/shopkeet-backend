package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
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

// fakeStore records presign/delete calls instead of touching real R2.
type fakeStore struct {
	presigned []string
	deleted   []string
}

func (f *fakeStore) PresignPutURL(_ context.Context, key, contentType string) (string, error) {
	f.presigned = append(f.presigned, key)
	return "https://fake-r2.example/" + key, nil
}
func (f *fakeStore) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func randSuffix2() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type asset struct {
	id    string
	r2Key string
}

// TestMediaRLSIsolation is the Phase 2 acceptance criterion
// (docs/04-agent-build-spec.md): a tenant's media library is fully isolated
// from other tenants at the list and delete layers, verified over the real
// HTTP handlers (TenantMW + live RLS). The ObjectStore is a fake — bytes/R2
// creds aren't needed because the Go API only signs URLs and records metadata.
func TestMediaRLSIsolation(t *testing.T) {
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

	const secret = "test-secret"
	sfx := randSuffix2()

	mkTenant := func(name string) (tid, token string) {
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "med-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		token, err = auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return tid, token
	}
	_, aToken := mkTenant("alpha")
	_, bToken := mkTenant("beta")

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	store := &fakeStore{}
	svc := New(pool, store, "https://pub.example", time.Minute)
	RegisterRoutes(app.Group("/api/v1"), pool, secret, svc)

	testReq := func(req *http.Request) (*http.Response, error) {
		// -1 disables Fiber's default 1s test timeout; the live-DB handlers
		// easily exceed it over the SSH tunnel roundtrips.
		return app.Test(req, -1)
	}

	doJSON := func(method, path, token, body string, wantStatus int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := testReq(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		// Drain fully so Fiber's test conn doesn't stall on leftover bytes.
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != wantStatus {
			t.Fatalf("%s %s: status %d, want %d (body=%s)", method, path, res.StatusCode, wantStatus, raw)
		}
		res.Body = io.NopCloser(strings.NewReader(string(raw)))
		return res
	}

	upload := func(token, filename, contentType string) string {
		res := doJSON("POST", "/api/v1/media/upload-url", token,
			`{"filename":"`+filename+`","content_type":"`+contentType+`"}`, fiber.StatusOK)
		var b struct {
			UploadURL string `json:"upload_url"`
			R2Key     string `json:"r2_key"`
		}
		raw, _ := io.ReadAll(res.Body)
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatalf("decode upload-url: %v (body=%s)", err, raw)
		}
		if !strings.HasPrefix(b.UploadURL, "https://fake-r2.example/") {
			t.Fatalf("presigned URL not from store: %q (body=%s)", b.UploadURL, raw)
		}
		return b.R2Key
	}
	confirm := func(token, key string, wantStatus int) {
		doJSON("POST", "/api/v1/media", token,
			`{"r2_key":"`+key+`","content_type":"image/jpeg","size_bytes":123,"alt_text":"x"}`,
			wantStatus)
	}
	list := func(token string) []asset {
		res := doJSON("GET", "/api/v1/media", token, "", fiber.StatusOK)
		var b struct {
			Assets []struct {
				ID    string `json:"id"`
				R2Key string `json:"r2_key"`
			} `json:"assets"`
		}
		_ = json.NewDecoder(res.Body).Decode(&b)
		out := make([]asset, 0, len(b.Assets))
		for _, a := range b.Assets {
			out = append(out, asset{id: a.ID, r2Key: a.R2Key})
		}
		return out
	}

	// Each tenant uploads + confirms one asset through the real API.
	aKey := upload(aToken, "a.jpg", "image/jpeg")
	bKey := upload(bToken, "b.png", "image/png")
	confirm(aToken, aKey, fiber.StatusCreated)
	confirm(bToken, bKey, fiber.StatusCreated)

	// Cross-tenant key theft is blocked at the API: a key starting with
	// another tenant's id is refused even before RLS.
	confirm(aToken, bKey, fiber.StatusForbidden)

	// Isolation at list time: each tenant sees exactly its own asset.
	if got := list(aToken); len(got) != 1 || got[0].r2Key != aKey {
		t.Fatalf("tenant A should list exactly its own asset, got %+v", got)
	}
	if got := list(bToken); len(got) != 1 || got[0].r2Key != bKey {
		t.Fatalf("tenant B should list exactly its own asset, got %+v", got)
	}

	// A cannot delete B's asset: the row is invisible under A's RLS session.
	bid := list(bToken)[0].id
	doJSON("DELETE", "/api/v1/media/"+bid, aToken, "", fiber.StatusNotFound)
	if len(store.deleted) != 0 {
		t.Fatalf("R2 delete must not fire for a cross-tenant delete, got %v", store.deleted)
	}

	// A can delete its own asset; the object and the row both go away.
	aid := list(aToken)[0].id
	doJSON("DELETE", "/api/v1/media/"+aid, aToken, "", fiber.StatusOK)
	if len(store.deleted) != 1 || store.deleted[0] != aKey {
		t.Fatalf("R2 should have deleted exactly A's object once, got %v", store.deleted)
	}
	if got := list(aToken); len(got) != 0 {
		t.Fatalf("tenant A list should be empty after delete, got %+v", got)
	}
	if got := list(bToken); len(got) != 1 || got[0].r2Key != bKey {
		t.Fatalf("tenant B must be unaffected by A's delete, got %+v", got)
	}
}
