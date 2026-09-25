// Package tenants implements Phase 13 store settings: GET/PATCH /tenant/settings
// (Admin) and the checkout tax calculation.
package tenants

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service handles tenant settings. The tenant is resolved from the JWT via
// TenantMW (the request tx already has app.current_tenant set).
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string) {
	svc := New(pool)
	g := router.Group("/tenant", auth.TenantMW(pool, secret))
	g.Get("/settings", svc.GetSettings)
	g.Patch("/settings", svc.PatchSettings)
}

// tenantRow matches the tenants table (Phase 13 columns appended).
type tenantRow struct {
	id               string
	name             string
	subdomain        string
	logoMediaAssetID *string
	defaultCurrency  string
	timezone         string
	supportEmail     *string
	supportPhone     *string
	taxRatePercent   int
}

const tenantSelect = `
	SELECT id, name, subdomain, logo_media_asset_id, default_currency, timezone,
	       support_email, support_phone, tax_rate_percent
	FROM tenants`

func tenantJSON(t *tenantRow) fiber.Map {
	return fiber.Map{
		"id":                  t.id,
		"name":                t.name,
		"subdomain":           t.subdomain,
		"logo_media_asset_id": strp(t.logoMediaAssetID),
		"default_currency":    t.defaultCurrency,
		"timezone":            t.timezone,
		"support_email":       strp(t.supportEmail),
		"support_phone":       strp(t.supportPhone),
		"tax_rate_percent":    t.taxRatePercent,
	}
}

func strp(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// GetSettings returns the current tenant's settings.
func (s *Service) GetSettings(c *fiber.Ctx) error {
	tx, ok := c.Locals("tx").(pgx.Tx)
	if !ok {
		return httperr.ErrInternalServerError
	}

	var t tenantRow
	err := tx.QueryRow(c.Context(), tenantSelect+" WHERE id = $1",
		c.Locals("tenant_id")).Scan(&t.id, &t.name, &t.subdomain, &t.logoMediaAssetID,
		&t.defaultCurrency, &t.timezone, &t.supportEmail, &t.supportPhone, &t.taxRatePercent)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"settings": tenantJSON(&t)})
}

type settingsPatchRequest struct {
	Name             *string `json:"name"`
	LogoMediaAssetID *string `json:"logo_media_asset_id"`
	DefaultCurrency  *string `json:"default_currency"`
	Timezone         *string `json:"timezone"`
	SupportEmail     *string `json:"support_email"`
	SupportPhone     *string `json:"support_phone"`
	TaxRatePercent   *int    `json:"tax_rate_percent"`
}

// PatchSettings updates the current tenant's settings (partial merge).
func (s *Service) PatchSettings(c *fiber.Ctx) error {
	var req settingsPatchRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}

	tx, ok := c.Locals("tx").(pgx.Tx)
	if !ok {
		return httperr.ErrInternalServerError
	}

	ctx := c.Context()
	tid := c.Locals("tenant_id").(string)

	// Validate tax_rate_percent range if provided.
	if req.TaxRatePercent != nil && (*req.TaxRatePercent < 0 || *req.TaxRatePercent > 100) {
		return httperr.C(fiber.StatusBadRequest, "tax_rate_percent must be 0-100")
	}

	// Build dynamic update (only provided fields).
	sets := []string{}
	args := []any{tid}
	arg := 2

	if req.Name != nil {
		sets = append(sets, "name = $"+strconv.Itoa(arg))
		args = append(args, *req.Name)
		arg++
	}
	if req.LogoMediaAssetID != nil {
		sets = append(sets, "logo_media_asset_id = $"+strconv.Itoa(arg))
		if *req.LogoMediaAssetID == "" {
			args = append(args, nil)
		} else {
			args = append(args, *req.LogoMediaAssetID)
		}
		arg++
	}
	if req.DefaultCurrency != nil {
		sets = append(sets, "default_currency = $"+strconv.Itoa(arg))
		args = append(args, *req.DefaultCurrency)
		arg++
	}
	if req.Timezone != nil {
		sets = append(sets, "timezone = $"+strconv.Itoa(arg))
		args = append(args, *req.Timezone)
		arg++
	}
	if req.SupportEmail != nil {
		sets = append(sets, "support_email = $"+strconv.Itoa(arg))
		args = append(args, nilIfEmpty(*req.SupportEmail))
		arg++
	}
	if req.SupportPhone != nil {
		sets = append(sets, "support_phone = $"+strconv.Itoa(arg))
		args = append(args, nilIfEmpty(*req.SupportPhone))
		arg++
	}
	if req.TaxRatePercent != nil {
		sets = append(sets, "tax_rate_percent = $"+strconv.Itoa(arg))
		args = append(args, *req.TaxRatePercent)
		arg++
	}

	if len(sets) == 0 {
		return httperr.C(fiber.StatusBadRequest, "no fields to update")
	}

	_, err := tx.Exec(ctx, "UPDATE tenants SET "+join(sets, ", ")+" WHERE id = $1", args...)
	if err != nil {
		return httperr.ErrInternalServerError
	}

	// Return updated settings.
	var t tenantRow
	err = tx.QueryRow(ctx, tenantSelect+" WHERE id = $1", tid).Scan(
		&t.id, &t.name, &t.subdomain, &t.logoMediaAssetID,
		&t.defaultCurrency, &t.timezone, &t.supportEmail, &t.supportPhone, &t.taxRatePercent)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"settings": tenantJSON(&t)})
}

func join(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	result := ss[0]
	for i := 1; i < len(ss); i++ {
		result += sep + ss[i]
	}
	return result
}
