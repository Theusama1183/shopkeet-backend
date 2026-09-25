package shipping

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/httperr"
)

type payload struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Countries []string `json:"countries"`
	Regions   []string `json:"regions"`
}

func randSuffix4() string {
	return strings.ToLower("t" + time.Now().Format("150405.000000"))
}

type ratePayload struct {
	ID            string `json:"id"`
	ZoneID        string `json:"zone_id"`
	Name          string `json:"name"`
	RateCents     int    `json:"rate_cents"`
	FreeOverCents *int   `json:"free_over_cents"`
	SortOrder     int    `json:"sort_order"`
}

// TestShippingRLSIsolation is the Phase 9 acceptance criterion: zones and rates
// are tenant-scoped admin CRUD (RLS); the public rates endpoint resolves the
// destination (country + region-restricted states) and exposes free-over;
// checkout-time rate resolution matches zones, waives the cost over the
// threshold, and refuses rates that do not cover the destination; a zone still
// backing a rate cannot be deleted; tenant B never sees tenant A's data.
func TestShippingRLSIsolation(t *testing.T) {
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
	sfx := randSuffix4()

	mkTenant := func(name string) (tid, token string) {
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "ship-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		token, err = auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return
	}
	aID, aTok := mkTenant("alpha")
	bID, bTok := mkTenant("beta")

	svc := New(pool)
	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	RegisterRoutes(v1, pool, secret, svc)

	do := func(method, path, tenant, authz, body string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Tenant-ID", tenant)
		if authz != "" {
			req.Header.Set("Authorization", "Bearer "+authz)
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

	// Tenant A: general PK+IN zone (Standard free-over 5000, Express) and a
	// region-restricted PK zone (Local, states PUNJAB/SINDH). Tenant B: their
	// own zone.
	zoneA := do("POST", "/api/v1/shipping/zones", aID, aTok,
		`{"name":"Pakistan","countries":["PK","IN"],"regions":[]}`, fiber.StatusCreated)
	var zA payload
	json.NewDecoder(zoneA.Body).Decode(&zA)
	if zA.ID == "" || zA.Name != "Pakistan" || len(zA.Countries) != 2 {
		t.Fatalf("unexpected zone payload: %+v", zA)
	}

	zoneA2 := do("POST", "/api/v1/shipping/zones", aID, aTok,
		`{"name":"Pakistan Region","countries":["PK"],"regions":["PUNJAB","SINDH"]}`, fiber.StatusCreated)
	var zA2 payload
	json.NewDecoder(zoneA2.Body).Decode(&zA2)
	if len(zA2.Regions) != 2 || zA2.Regions[0] != "PUNJAB" {
		t.Fatalf("region-restricted zone not persisted: %+v", zA2)
	}

	zoneB := do("POST", "/api/v1/shipping/zones", bID, bTok,
		`{"name":"B Zone","countries":["PK"],"regions":[]}`, fiber.StatusCreated)
	var zB payload
	json.NewDecoder(zoneB.Body).Decode(&zB)

	// Admin CRUD: rename the region zone, then create the three rates.
	patchA2 := do("PATCH", "/api/v1/shipping/zones/"+zA2.ID, aID, aTok,
		`{"name":"Punjab","regions":["PUNJAB"]}`, fiber.StatusOK)
	var zA2u payload
	json.NewDecoder(patchA2.Body).Decode(&zA2u)
	if zA2u.Name != "Punjab" || len(zA2u.Regions) != 1 {
		t.Fatalf("zone patch not applied: %+v", zA2u)
	}

	rateStd := do("POST", "/api/v1/shipping/rates", aID, aTok,
		`{"zone_id":"`+zA.ID+`","name":"Standard","rate_cents":500,"free_over_cents":5000,"sort_order":0}`,
		fiber.StatusCreated)
	var rStd ratePayload
	json.NewDecoder(rateStd.Body).Decode(&rStd)
	if rStd.ZoneID != zA.ID || rStd.RateCents != 500 || rStd.FreeOverCents == nil || *rStd.FreeOverCents != 5000 {
		t.Fatalf("unexpected rate payload: %+v", rStd)
	}
	rateExp := do("POST", "/api/v1/shipping/rates", aID, aTok,
		`{"zone_id":"`+zA.ID+`","name":"Express","rate_cents":1200,"sort_order":0}`,
		fiber.StatusCreated)
	var rExp ratePayload
	json.NewDecoder(rateExp.Body).Decode(&rExp)
	rateLoc := do("POST", "/api/v1/shipping/rates", aID, aTok,
		`{"zone_id":"`+zA2u.ID+`","name":"Local","rate_cents":200,"sort_order":0}`,
		fiber.StatusCreated)
	var rLoc ratePayload
	json.NewDecoder(rateLoc.Body).Decode(&rLoc)
	var rateB ratePayload
	json.NewDecoder(do("POST", "/api/v1/shipping/rates", bID, bTok,
		`{"zone_id":"`+zB.ID+`","name":"B Rate","rate_cents":999,"sort_order":0}`,
		fiber.StatusCreated).Body).Decode(&rateB)

	// PATCH a rate: price bump survives.
	upStd := do("PATCH", "/api/v1/shipping/rates/"+rStd.ID, aID, aTok,
		`{"name":"Standard","rate_cents":550}`, fiber.StatusOK)
	var rStdu ratePayload
	json.NewDecoder(upStd.Body).Decode(&rStdu)
	if rStdu.RateCents != 550 || rStdu.FreeOverCents == nil || *rStdu.FreeOverCents != 5000 {
		t.Fatalf("rate patch not applied: %+v", rStdu)
	}

	// Public listing resolves destination. No state → general zone only.
	var list struct {
		Rates []map[string]any `json:"rates"`
	}
	do("GET", "/api/v1/shipping/rates?country=PK", aID, "", "", fiber.StatusOK)
	json.NewDecoder(do("GET", "/api/v1/shipping/rates?country=PK", aID, "", "", fiber.StatusOK).Body).Decode(&list)
	names := map[string]bool{}
	for _, r := range list.Rates {
		names[r["name"].(string)] = true
	}
	if !names["Standard"] || !names["Express"] || names["Local"] {
		t.Fatalf("no-state listing should be Standard+Express only, got %v", names)
	}
	// State PUNJAB adds the region-restricted Local rate.
	do("GET", "/api/v1/shipping/rates?country=PK&state=KPK", aID, "", "", fiber.StatusOK)
	json.NewDecoder(do("GET", "/api/v1/shipping/rates?country=PK&state=PUNJAB", aID, "", "", fiber.StatusOK).Body).Decode(&list)
	if len(list.Rates) != 3 {
		t.Fatalf("PUNJAB should expose 3 rates, got %d", len(list.Rates))
	}

	// Rate resolution used by checkout. Every resolve runs inside a tenant-scoped
	// transaction (as the request tx would be).
	beginTx := func(tid string) pgx.Tx {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin tx: %v", err)
		}
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		return tx
	}
	// free-over: below threshold pays the rate, at/above pays 0.
	txPaid := beginTx(aID)
	paid, err := ResolveRate(ctx, txPaid, rStd.ID, "PK", "", 4999)
	if err != nil {
		t.Fatalf("resolve paid: %v", err)
	}
	txPaid.Rollback(ctx)
	if paid == nil || paid.CostCents != 550 || paid.Method != "Standard" {
		t.Fatalf("paid resolve should be Standard/550, got %+v", paid)
	}
	if paid.ID != rStd.ID {
		t.Fatalf("resolve should echo the rate id, got %q", paid.ID)
	}
	txFree := beginTx(aID)
	freeQt, err := ResolveRate(ctx, txFree, rStd.ID, "PK", "", 5000)
	if err != nil {
		t.Fatalf("resolve free: %v", err)
	}
	txFree.Rollback(ctx)
	if freeQt == nil || freeQt.CostCents != 0 {
		t.Fatalf("at-threshold resolve should be 0, got %+v", freeQt)
	}

	// Region matching: matching state resolves, non-matching state rejects.
	txLoc := beginTx(aID)
	locPK, err := ResolveRate(ctx, txLoc, rLoc.ID, "PK", "PUNJAB", 100)
	if err != nil {
		t.Fatalf("resolve local: %v", err)
	}
	txLoc.Rollback(ctx)
	if locPK == nil || locPK.CostCents != 200 {
		t.Fatalf("PUNJAB should resolve Local/200, got %+v", locPK)
	}
	txKPK := beginTx(aID)
	locKPK, err := ResolveRate(ctx, txKPK, rLoc.ID, "PK", "KPK", 100)
	if err != nil {
		t.Fatalf("resolve local kpk: %v", err)
	}
	txKPK.Rollback(ctx)
	if locKPK != nil {
		t.Fatalf("KPK should NOT resolve the PUNJAB-only rate, got %+v", locKPK)
	}
	txNoState := beginTx(aID)
	locNoState, err := ResolveRate(ctx, txNoState, rLoc.ID, "PK", "", 100)
	if err != nil {
		t.Fatalf("resolve local nostate: %v", err)
	}
	txNoState.Rollback(ctx)
	if locNoState != nil {
		t.Fatalf("region-restricted rate without state should NOT resolve, got %+v", locNoState)
	}

	// Unknown rate resolves nil; tenant-B's rate is hidden from tenant A by RLS.
	txUnknown := beginTx(aID)
	unknown, err := ResolveRate(ctx, txUnknown, "00000000-0000-0000-0000-000000000000", "PK", "", 100)
	if err != nil {
		t.Fatalf("resolve unknown: %v", err)
	}
	txUnknown.Rollback(ctx)
	if unknown != nil {
		t.Fatalf("unknown rate should resolve nil, got %+v", unknown)
	}
	txCross := beginTx(aID)
	cross, err := ResolveRate(ctx, txCross, rateB.ID, "PK", "", 100)
	if err != nil {
		t.Fatalf("resolve b: %v", err)
	}
	txCross.Rollback(ctx)
	if cross != nil {
		t.Fatalf("tenant-B rate must be invisible to tenant A, got %+v", cross)
	}

	// Tenant B sees only its own zone/rate; public list for B excludes A's.
	json.NewDecoder(do("GET", "/api/v1/shipping/rates?country=PK", bID, "", "", fiber.StatusOK).Body).Decode(&list)
	if len(list.Rates) != 1 || list.Rates[0]["name"] != "B Rate" {
		t.Fatalf("tenant B should see only B Rate, got %+v", list.Rates)
	}

	// tenant B cannot touch tenant A's rate; unknown zone 404s.
	do("PATCH", "/api/v1/shipping/rates/"+rStd.ID, bID, bTok, `{"rate_cents":1}`, fiber.StatusNotFound)
	do("PATCH", "/api/v1/shipping/zones/00000000-0000-0000-0000-000000000000", aID, aTok,
		`{"name":"x"}`, fiber.StatusNotFound)

	// A zone still backing a rate refuses deletion.
	do("DELETE", "/api/v1/shipping/zones/"+zA.ID, aID, aTok, "", fiber.StatusConflict)

	// After removing ALL its rates, the zone deletes cleanly.
	do("DELETE", "/api/v1/shipping/rates/"+rStd.ID, aID, aTok, "", fiber.StatusNoContent)
	do("DELETE", "/api/v1/shipping/rates/"+rExp.ID, aID, aTok, "", fiber.StatusNoContent)
	do("DELETE", "/api/v1/shipping/zones/"+zA.ID, aID, aTok, "", fiber.StatusNoContent)
	// No-state listing is now empty (Local is region-restricted); PUNJAB still
	// resolves to it.
	json.NewDecoder(do("GET", "/api/v1/shipping/rates?country=PK", aID, "", "", fiber.StatusOK).Body).Decode(&list)
	if len(list.Rates) != 0 {
		t.Fatalf("no-state list should be empty after zoneA delete, got %+v", list.Rates)
	}
	json.NewDecoder(do("GET", "/api/v1/shipping/rates?country=PK&state=PUNJAB", aID, "", "", fiber.StatusOK).Body).Decode(&list)
	if len(list.Rates) != 1 || list.Rates[0]["name"] != "Local" {
		t.Fatalf("PUNJAB should resolve only Local, got %+v", list.Rates)
	}

	// Unauthenticated admin calls are rejected.
	do("POST", "/api/v1/shipping/zones", aID, "", `{"name":"x","countries":["PK"]}`, fiber.StatusUnauthorized)
}
