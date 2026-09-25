// Package shipping implements Phase 9 of the expansion spec: admin-managed
// shipping zones (country/region coverage) and rates (per zone), a public
// GET /shipping/rates endpoint the storefront can present at checkout, and the
// checkout integration in internal/orders that snapshots the chosen rate.
package shipping

import (
	"context"
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service implements the shipping surface. Handlers execute inside the
// RLS-scoped request transaction (PublicTenantMW for GET /shipping/rates,
// TenantMW for the admin CRUD), so every query is bound to the resolved tenant.
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// --- helpers ------------------------------------------------------------------

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func tenantID(c *fiber.Ctx) string {
	id, _ := c.Locals("tenant_id").(string)
	return id
}

func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// savepoint contains a failing write (e.g. a FK violation mapped to a 4xx) so
// it cannot poison the whole request transaction (TenantMW commits on success;
// an aborted tx would 500 the final Commit).
func savepoint(ctx context.Context, tx pgx.Tx, fn func() error) error {
	if _, err := tx.Exec(ctx, "SAVEPOINT shipping_op"); err != nil {
		return err
	}
	if err := fn(); err != nil {
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT shipping_op")
		return err
	}
	_, err := tx.Exec(ctx, "RELEASE SAVEPOINT shipping_op")
	return err
}

func contains(haystack []string, needle string) bool {
	if needle == "" {
		return false
	}
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// zoneMatches reports whether a zone covers (country, state): the country must
// be in countries, and either the zone has no region restriction or the state
// is in regions. A region-restricted zone without a state never matches.
func zoneMatches(country, state string, countries, regions []string) bool {
	if !contains(countries, country) {
		return false
	}
	if len(regions) == 0 {
		return true
	}
	return contains(regions, state)
}

// ErrStateRequired is returned by ResolveRate when the destination country is
// covered only by region-restricted zones but no state was supplied. Checkout
// maps this to a 400 asking for shipping_state instead of a silent "no rate",
// so a merchant who set up region-only rates (and no country-wide fallback)
// can't leave customers with literally no shipping option.
var ErrStateRequired = errors.New("shipping_state required")

// --- public rates --------------------------------------------------------------

type zoneRow struct {
	id        string
	name      string
	countries []string
	regions   []string
}

type rateRow struct {
	id            string
	zoneID        string
	name          string
	rateCents     int
	freeOverCents *int
	sortOrder     int
}

const zoneRateSelect = `
	SELECT z.id, z.name, z.countries, z.regions, r.id, r.name, r.rate_cents,
	       r.free_over_cents, r.sort_order
	FROM shipping_zones z
	JOIN shipping_rates r ON r.zone_id = z.id`

// ListRates handles GET /shipping/rates?country=&state= (Public). Returns every
// rate of every zone covering the destination (country, and state when given),
// cheapest-first by sort_order then name — the options a checkout can present.
func (s *Service) ListRates(c *fiber.Ctx) error {
	country := c.Query("country")
	if country == "" {
		return httperr.C(fiber.StatusBadRequest, "country query param required")
	}
	state := c.Query("state")
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	rows, err := tx.Query(c.Context(), zoneRateSelect+" ORDER BY r.sort_order, r.name")
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()
	var rates []fiber.Map
	stateRequired := false
	for rows.Next() {
		var z zoneRow
		var r rateRow
		if err := rows.Scan(&z.id, &z.name, &z.countries, &z.regions,
			&r.id, &r.name, &r.rateCents, &r.freeOverCents, &r.sortOrder); err != nil {
			return httperr.ErrInternalServerError
		}
		// When no state is supplied but the destination country has
		// region-restricted zones, downstream checkout needs the state — surface
		// it so the frontend can collect it instead of the customer hitting a
		// dead end at pay time (see ErrStateRequired).
		if state == "" && contains(z.countries, country) && len(z.regions) > 0 {
			stateRequired = true
		}
		if !zoneMatches(country, state, z.countries, z.regions) {
			continue
		}
		rates = append(rates, fiber.Map{
			"id": r.id, "zone_id": z.id, "zone_name": z.name,
			"name": r.name, "rate_cents": r.rateCents,
			"free_over_cents": intOrNil(r.freeOverCents), "sort_order": r.sortOrder,
		})
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"rates": rates, "state_required": stateRequired})
}

func intOrNil(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

// RateQuote is the resolved shipping line for a checkout: the applied cost and
// the method name snapshot after free-over handling.
type RateQuote struct {
	ID        string
	Method    string
	CostCents int
}

// ResolveRate looks up a rate by id within the caller's RLS scope, verifies its
// zone covers (country, state), and returns the applied cost: 0 when the subtotal
// clears the rate's free_over threshold, else rate_cents. Returns (nil, nil)
// when the rate does not exist or does not cover the destination, so checkout
// can reject with a clean 400.
func ResolveRate(ctx context.Context, tx pgx.Tx, rateID, country, state string, subtotalCents int) (*RateQuote, error) {
	var z zoneRow
	var r rateRow
	err := tx.QueryRow(ctx, zoneRateSelect+" WHERE r.id = $1", rateID).
		Scan(&z.id, &z.name, &z.countries, &z.regions,
			&r.id, &r.name, &r.rateCents, &r.freeOverCents, &r.sortOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Destination country is covered, but only via region-restricted zones and
	// the customer didn't provide a state: ask for it rather than silently
	// resolving to "no rate" (see ErrStateRequired).
	if contains(z.countries, country) && len(z.regions) > 0 && state == "" {
		return nil, ErrStateRequired
	}
	if !zoneMatches(country, state, z.countries, z.regions) {
		return nil, nil
	}
	cost := r.rateCents
	if r.freeOverCents != nil && subtotalCents >= *r.freeOverCents {
		cost = 0
	}
	return &RateQuote{ID: r.id, Method: r.name, CostCents: cost}, nil
}

// --- admin zones ---------------------------------------------------------------

type zoneRequest struct {
	Name      string   `json:"name"`
	Countries []string `json:"countries"`
	Regions   []string `json:"regions"`
}

type ZoneJSON struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Countries []string `json:"countries"`
	Regions   []string `json:"regions"`
}

// CreateZone handles POST /shipping/zones (Admin).
func (s *Service) CreateZone(c *fiber.Ctx) error {
	var req zoneRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.Name == "" {
		return httperr.C(fiber.StatusBadRequest, "name required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	var id string
	if err := tx.QueryRow(c.Context(), `
		INSERT INTO shipping_zones (tenant_id, name, countries, regions)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID(c), req.Name, req.Countries, req.Regions).Scan(&id); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(ZoneJSON{
		ID: id, Name: req.Name, Countries: req.Countries, Regions: req.Regions,
	})
}

// UpdateZone handles PATCH /shipping/zones/:id (Admin). Accepts any subset.
func (s *Service) UpdateZone(c *fiber.Ctx) error {
	var req zoneRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	var current ZoneJSON
	err := tx.QueryRow(ctx, `
		SELECT id, name, countries, regions FROM shipping_zones WHERE id = $1`,
		c.Params("id")).Scan(&current.ID, &current.Name, &current.Countries, &current.Regions)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "zone not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}
	newName, newCountries, newRegions := current.Name, current.Countries, current.Regions
	if req.Name != "" {
		newName = req.Name
	}
	if req.Countries != nil {
		newCountries = req.Countries
	}
	if req.Regions != nil {
		newRegions = req.Regions
	}
	if _, err := tx.Exec(ctx, `
		UPDATE shipping_zones SET name = $1, countries = $2, regions = $3 WHERE id = $4`,
		newName, newCountries, newRegions, c.Params("id")); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(ZoneJSON{ID: current.ID, Name: newName, Countries: newCountries, Regions: newRegions})
}

// DeleteZone handles DELETE /shipping/zones/:id (Admin). Refuses while rates
// reference the zone (409).
func (s *Service) DeleteZone(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	if err := savepoint(ctx, tx, func() error {
		_, err := tx.Exec(ctx, "DELETE FROM shipping_zones WHERE id = $1", c.Params("id"))
		return err
	}); err != nil {
		if isFKViolation(err) {
			return httperr.C(fiber.StatusConflict, "zone has rates; delete them first")
		}
		return httperr.ErrInternalServerError
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// --- admin rates ---------------------------------------------------------------

type rateRequest struct {
	ZoneID        string `json:"zone_id"`
	Name          string `json:"name"`
	RateCents     *int   `json:"rate_cents"`
	FreeOverCents *int   `json:"free_over_cents"`
	SortOrder     *int   `json:"sort_order"`
}

// CreateRate handles POST /shipping/rates (Admin).
func (s *Service) CreateRate(c *fiber.Ctx) error {
	var req rateRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.ZoneID == "" || req.Name == "" || req.RateCents == nil {
		return httperr.C(fiber.StatusBadRequest, "zone_id, name, rate_cents required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	var id string
	err := savepoint(ctx, tx, func() error {
		return tx.QueryRow(ctx, `
			INSERT INTO shipping_rates (tenant_id, zone_id, name, rate_cents, free_over_cents, sort_order)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			tenantID(c), req.ZoneID, req.Name, *req.RateCents, req.FreeOverCents,
			intDefault(req.SortOrder, 0)).Scan(&id)
	})
	if err != nil {
		if isFKViolation(err) {
			return httperr.C(fiber.StatusBadRequest, "unknown zone id")
		}
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id": id, "zone_id": req.ZoneID, "name": req.Name, "rate_cents": *req.RateCents,
		"free_over_cents": intOrNil(req.FreeOverCents),
		"sort_order":      intDefault(req.SortOrder, 0),
	})
}

// UpdateRate handles PATCH /shipping/rates/:id (Admin). Accepts any subset;
// free_over_cents null clears the threshold (the body is decoded as a map so
// "absent" and JSON null are distinguishable).
func (s *Service) UpdateRate(c *fiber.Ctx) error {
	var body map[string]any
	if err := c.BodyParser(&body); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	var current rateRow
	err := tx.QueryRow(ctx, `
		SELECT r.id, r.zone_id, r.name, r.rate_cents, r.free_over_cents, r.sort_order
		FROM shipping_rates r
		WHERE r.id = $1`, c.Params("id")).Scan(
		&current.id, &current.zoneID, &current.name,
		&current.rateCents, &current.freeOverCents, &current.sortOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "rate not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}

	newZoneID := current.zoneID
	newName := current.name
	newRate := current.rateCents
	newFree := current.freeOverCents
	newSort := current.sortOrder
	if v, ok := body["zone_id"].(string); ok && v != "" {
		newZoneID = v
	}
	if v, ok := body["name"].(string); ok && v != "" {
		newName = v
	}
	if v, ok := asInt(body["rate_cents"]); ok {
		newRate = v
	}
	if raw, present := body["free_over_cents"]; present {
		if raw == nil {
			newFree = nil
		} else if v, ok := asInt(raw); ok {
			newFree = &v
		}
	}
	if v, ok := asInt(body["sort_order"]); ok {
		newSort = v
	}

	if err := savepoint(ctx, tx, func() error {
		_, err := tx.Exec(ctx, `
			UPDATE shipping_rates SET zone_id = $1, name = $2, rate_cents = $3,
				free_over_cents = $4, sort_order = $5 WHERE id = $6`,
			newZoneID, newName, newRate, newFree, newSort, c.Params("id"))
		return err
	}); err != nil {
		if isFKViolation(err) {
			return httperr.C(fiber.StatusBadRequest, "unknown zone id")
		}
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{
		"id": current.id, "zone_id": newZoneID, "name": newName, "rate_cents": newRate,
		"free_over_cents": intOrNil(newFree), "sort_order": newSort,
	})
}

// DeleteRate handles DELETE /shipping/rates/:id (Admin).
func (s *Service) DeleteRate(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(c.Context(),
		"DELETE FROM shipping_rates WHERE id = $1", c.Params("id")); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func intDefault(v *int, d int) int {
	if v == nil {
		return d
	}
	return *v
}

func asInt(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}
