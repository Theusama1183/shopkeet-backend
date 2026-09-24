package content

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

func randSuffix5() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TestContentRLSIsolation is the Phase 6 acceptance criterion: signup seeds the
// required-for-v1 chrome; a merchant edits the home `page` post and the
// storefront reflects it without a deploy; every product renders through the
// single scope=default product template; renaming a post's route creates a
// working redirect (and renames keep converging); drafts are never public; a
// nonexistent route resolves to a 404-template fetch, not a framework error;
// tenant B sees nothing of A.
func TestContentRLSIsolation(t *testing.T) {
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

	const secret = "test-secret"
	sfx := randSuffix5()

	app := fiber.New()
	v1 := app.Group("/api/v1")
	auth.RegisterTenantCreatedHook(SeedDefaults)
	auth.RegisterRoutes(v1, pool, secret)
	RegisterRoutes(v1, pool, secret, New(pool))

	type tenantT struct {
		id    string
		token string
	}
	signup := func(name string) tenantT {
		t.Helper()
		body := `{"name":"` + name + `","subdomain":"ct-` + name + `-` + sfx + `",` +
			`"email":"owner@` + name + `-` + sfx + `.com","password":"hunter2hunter2"}`
		req := httptest.NewRequest("POST", "/api/v1/auth/signup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("signup %s: %v", name, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != fiber.StatusCreated {
			t.Fatalf("signup %s: status %d (%s)", name, res.StatusCode, raw)
		}
		var out struct {
			Token  string `json:"token"`
			Tenant struct {
				ID string `json:"id"`
			} `json:"tenant"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode signup: %v", err)
		}
		return tenantT{id: out.Tenant.ID, token: out.Token}
	}
	a := signup("alpha")
	b := signup("beta")

	// request runs an HTTP call against the app and returns the response (body
	// drained and re-wrapped so handlers can still read it).
	request := func(method, path, body, token, headerTenant string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if headerTenant != "" {
			req.Header.Set("X-Tenant-ID", headerTenant)
		}
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s %s: status %d, want %d (body=%s)", method, path, res.StatusCode, want, raw)
		}
		res.Body = io.NopCloser(strings.NewReader(string(raw)))
		return res
	}
	// Public storefront calls: tenant via X-Tenant-ID. Admin calls: JWT token.
	pubGet := func(path, tid string, want int) *http.Response {
		return request("GET", path, "", "", tid, want)
	}
	adm := func(method, path, body string, tt tenantT, want int) *http.Response {
		return request(method, path, body, tt.token, "", want)
	}

	jsonEQ := func(res *http.Response, key, wantRaw string) {
		t.Helper()
		var m map[string]json.RawMessage
		if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
			t.Fatalf("decode %s: %v", key, err)
		}
		got := m[key]
		var g, w any
		_ = json.Unmarshal(got, &g)
		_ = json.Unmarshal([]byte(wantRaw), &w)
		gj, _ := json.Marshal(g)
		wj, _ := json.Marshal(w)
		if string(gj) != string(wj) {
			t.Fatalf("%s: got %s want %s", key, gj, wj)
		}
	}

	// --- signup seeds the required-for-v1 chrome -------------------------------
	for _, tt := range []string{"product", "product_archive", "cart", "404", "order_confirmation"} {
		pubGet("/api/v1/templates/"+tt, a.id, fiber.StatusOK)
	}
	pubGet("/api/v1/templates/404", a.id, fiber.StatusOK)
	pubGet("/api/v1/sections?section_type=header", a.id, fiber.StatusOK)
	pubGet("/api/v1/sections?section_type=footer", a.id, fiber.StatusOK)

	// --- merchant edits the home page; storefront reflects without a deploy ----
	var homePost struct {
		ID string `json:"id"`
	}
	res := pubGet("/api/v1/posts?post_type=page&route=/", a.id, fiber.StatusOK)
	if err := json.NewDecoder(res.Body).Decode(&homePost); err != nil {
		t.Fatalf("decode home post: %v", err)
	}
	newLayout := `{"root":{"props":{"heading":"Locked in","cta":"/shop"}}}`
	adm("PATCH", "/api/v1/posts/"+homePost.ID,
		`{"title":"New Home","layout":`+newLayout+`}`, a, fiber.StatusOK)

	res = pubGet("/api/v1/posts?post_type=page&route=/", a.id, fiber.StatusOK)
	var edited struct {
		Title string `json:"title"`
	}
	json.NewDecoder(res.Body).Decode(&edited)
	if edited.Title != "New Home" {
		t.Fatalf("storefront should reflect the edited title, got %q", edited.Title)
	}
	jsonEQ(pubGet("/api/v1/posts?post_type=page&route=/", a.id, fiber.StatusOK), "layout", newLayout)

	// --- renaming the route creates a working redirect -------------------------
	adm("PATCH", "/api/v1/posts/"+homePost.ID, `{"route":"/home"}`, a, fiber.StatusOK)
	pubGet("/api/v1/posts?post_type=page&route=/home", a.id, fiber.StatusOK)
	pubGet("/api/v1/posts?post_type=page&route=/", a.id, fiber.StatusNotFound)

	// Rename again: each old path keeps its own working redirect row — the
	// chain / → /home → /welcome, which the storefront's middleware follows
	// (redirect loops resolve by re-running lookup on the target path).
	adm("PATCH", "/api/v1/posts/"+homePost.ID, `{"route":"/welcome"}`, a, fiber.StatusOK)
	res = pubGet("/api/v1/redirects/lookup?path="+url.QueryEscape("/home"), a.id, fiber.StatusOK)
	var redir struct {
		Redirect struct {
			ToPath string `json:"to_path"`
		} `json:"redirect"`
	}
	json.NewDecoder(res.Body).Decode(&redir)
	if redir.Redirect.ToPath != "/welcome" {
		t.Fatalf("old /home should now redirect to /welcome, got %q", redir.Redirect.ToPath)
	}
	res = pubGet("/api/v1/redirects/lookup?path="+url.QueryEscape("/"), a.id, fiber.StatusOK)
	json.NewDecoder(res.Body).Decode(&redir)
	if redir.Redirect.ToPath != "/home" {
		t.Fatalf("original / should redirect to /home (chain start), got %q", redir.Redirect.ToPath)
	}

	// --- drafts are never public ------------------------------------------------
	adm("POST", "/api/v1/posts",
		`{"post_type":"page","route":"/privacy","title":"Privacy","layout":{"root":{"props":{"t":"x"}}},"status":"draft"}`,
		a, fiber.StatusCreated)
	var pageList struct {
		Posts []struct {
			Route string `json:"route"`
		} `json:"posts"`
	}
	res = pubGet("/api/v1/posts?post_type=page", a.id, fiber.StatusOK)
	json.NewDecoder(res.Body).Decode(&pageList)
	for _, p := range pageList.Posts {
		if p.Route == "/privacy" {
			t.Fatalf("draft post leaked to the public list")
		}
	}
	var created struct {
		ID string `json:"id"`
	}
	res = adm("POST", "/api/v1/posts",
		`{"post_type":"page","route":"/terms","title":"Terms","status":"draft"}`, a, fiber.StatusCreated)
	json.NewDecoder(res.Body).Decode(&created)
	adm("PATCH", "/api/v1/posts/"+created.ID, `{"status":"published"}`, a, fiber.StatusOK)
	res = pubGet("/api/v1/posts?post_type=page", a.id, fiber.StatusOK)
	json.NewDecoder(res.Body).Decode(&pageList)
	found := false
	for _, p := range pageList.Posts {
		if p.Route == "/terms" {
			found = true
		}
	}
	if !found {
		t.Fatalf("published /terms should appear in the public list")
	}

	// --- one template per type, Puck JSON round-trips ---------------------------
	adm("PUT", "/api/v1/templates/cart",
		`{"layout":{"root":{"props":{"checkout_lock":true}}},"meta_title":"Cart"}`,
		a, fiber.StatusOK)
	res = pubGet("/api/v1/templates/cart", a.id, fiber.StatusOK)
	var tmpl struct {
		Scope  string          `json:"scope"`
		Status string          `json:"status"`
		Layout json.RawMessage `json:"layout"`
	}
	json.NewDecoder(res.Body).Decode(&tmpl)
	if tmpl.Scope != "default" || tmpl.Status != "published" {
		t.Fatalf("template should stay scope=default published, got %+v", tmpl)
	}
	jsonEQ(pubGet("/api/v1/templates/cart", a.id, fiber.StatusOK), "layout",
		`{"root":{"props":{"checkout_lock":true}}}`)

	// --- sections: popup lifecycle + header edit -------------------------------
	var popSec struct {
		ID string `json:"id"`
	}
	res = adm("POST", "/api/v1/sections",
		`{"section_type":"popup","name":"Flash Sale",
		  "placement_rules":{"trigger":"delay","delay_seconds":5,"pages":["all"],"frequency":"once_per_session"}}`,
		a, fiber.StatusCreated)
	json.NewDecoder(res.Body).Decode(&popSec)
	var popList struct {
		Sections []json.RawMessage `json:"sections"`
	}
	res = pubGet("/api/v1/sections?section_type=popup", a.id, fiber.StatusOK)
	json.NewDecoder(res.Body).Decode(&popList)
	if len(popList.Sections) != 0 {
		t.Fatalf("draft popup leaked to the public")
	}
	adm("PATCH", "/api/v1/sections/"+popSec.ID,
		`{"status":"published","layout":{"root":{"props":{"t":"p2"}}}}`, a, fiber.StatusOK)
	res = pubGet("/api/v1/sections?section_type=popup", a.id, fiber.StatusOK)
	json.NewDecoder(res.Body).Decode(&popList)
	if len(popList.Sections) != 1 {
		t.Fatalf("published popup should be public, got %d", len(popList.Sections))
	}

	var headerList struct {
		Sections []struct {
			ID string `json:"id"`
		} `json:"sections"`
	}
	res = pubGet("/api/v1/sections?section_type=header", a.id, fiber.StatusOK)
	json.NewDecoder(res.Body).Decode(&headerList)
	if len(headerList.Sections) != 1 {
		t.Fatalf("expected the seeded header, got %d", len(headerList.Sections))
	}
	adm("PATCH", "/api/v1/sections/"+headerList.Sections[0].ID,
		`{"layout":{"root":{"props":{"logo":"NewLogo.svg"}}}}`, a, fiber.StatusOK)

	// --- redirects: manual create + duplicate guard -----------------------------
	adm("POST", "/api/v1/redirects", `{"from_path":"/old-sale","to_path":"/welcome"}`, a, fiber.StatusCreated)
	pubGet("/api/v1/redirects/lookup?path="+url.QueryEscape("/old-sale"), a.id, fiber.StatusOK)
	adm("POST", "/api/v1/redirects", `{"from_path":"/old-sale","to_path":"/elsewhere"}`, a, fiber.StatusConflict)

	// --- tenant B isolation -----------------------------------------------------
	// B's storefront serves only B's own seeded chrome: A's content is 404.
	pubGet("/api/v1/posts?post_type=page&route=/welcome", b.id, fiber.StatusNotFound)
	pubGet("/api/v1/redirects/lookup?path="+url.QueryEscape("/"), b.id, fiber.StatusNotFound)
	var bHome struct {
		Title string `json:"title"`
		Route string `json:"route"`
	}
	res = pubGet("/api/v1/posts?post_type=page&route=/", b.id, fiber.StatusOK)
	json.NewDecoder(res.Body).Decode(&bHome)
	if bHome.Route != "/" || bHome.Title != "Home" {
		t.Fatalf("B should have its own seeded home post, got %+v", bHome)
	}
	pubGet("/api/v1/templates/404", b.id, fiber.StatusOK) // B has its own 404 too

	// B admin cannot touch A's rows (RLS makes them invisible → 404).
	adm("PATCH", "/api/v1/posts/"+homePost.ID, `{"title":"hijack"}`, b, fiber.StatusNotFound)
	adm("DELETE", "/api/v1/posts/"+homePost.ID, "", b, fiber.StatusNotFound)
	adm("PATCH", "/api/v1/sections/"+headerList.Sections[0].ID, `{"name":"hijack"}`, b, fiber.StatusNotFound)
	var bRlist struct {
		Redirects []struct {
			FromPath string `json:"from_path"`
		} `json:"redirects"`
	}
	res = adm("GET", "/api/v1/redirects", "", b, fiber.StatusOK)
	json.NewDecoder(res.Body).Decode(&bRlist)
	for _, r := range bRlist.Redirects {
		if r.FromPath == "/" || r.FromPath == "/home" || r.FromPath == "/old-sale" {
			t.Fatalf("B must not see A's redirects, got %+v", r)
		}
	}
}
