// Package tenants implements Phase 13 store settings: GET/PATCH /tenant/settings
// (Admin) and the checkout tax calculation.
package tenants

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service handles tenant settings. The tenant is resolved from the JWT via
// TenantMW (the request tx already has app.current_tenant set).
type Service struct {
	pool   *pgxpool.Pool
	secret string
}

func New(pool *pgxpool.Pool, secret string) *Service {
	return &Service{pool: pool, secret: secret}
}

func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string) {
	svc := New(pool, secret)
	g := router.Group("/tenant", auth.TenantMW(pool, secret))
	g.Get("/settings", svc.GetSettings)
	g.Patch("/settings", svc.PatchSettings)
	g.Post("/onboarding/complete", svc.CompleteOnboarding)
}

// tenantRow matches the tenants table (Phase 13 + Phase 20 columns appended).
type tenantRow struct {
	id                           string
	name                         string
	subdomain                    string
	logoMediaAssetID             *string
	defaultCurrency              string
	timezone                     string
	supportEmail                 *string
	supportPhone                 *string
	taxRatePercent               int
	loyaltyPointsPerCurrencyUnit int
	loyaltyRedemptionRate        int
	onboardingCompleted          bool
}

const tenantSelect = `
	SELECT id, name, subdomain, logo_media_asset_id, default_currency, timezone,
	       support_email, support_phone, tax_rate_percent,
	       loyalty_points_per_currency_unit, loyalty_redemption_rate,
	       onboarding_completed
	FROM tenants`

func tenantJSON(t *tenantRow) fiber.Map {
	return fiber.Map{
		"id":                               t.id,
		"name":                             t.name,
		"subdomain":                        t.subdomain,
		"logo_media_asset_id":              strp(t.logoMediaAssetID),
		"default_currency":                 t.defaultCurrency,
		"timezone":                         t.timezone,
		"support_email":                    strp(t.supportEmail),
		"support_phone":                    strp(t.supportPhone),
		"tax_rate_percent":                 t.taxRatePercent,
		"loyalty_points_per_currency_unit": t.loyaltyPointsPerCurrencyUnit,
		"loyalty_redemption_rate":          t.loyaltyRedemptionRate,
		"onboarding_completed":             t.onboardingCompleted,
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

// scanTenant reads a tenant row from any queryer (the request tx or a plain
// pool) so GetSettings, PatchSettings and CompleteOnboarding share one scan
// order with tenantSelect.
func scanTenant(q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, ctx context.Context, sql string, args ...any) (*tenantRow, error) {
	var t tenantRow
	err := q.QueryRow(ctx, sql, args...).Scan(&t.id, &t.name, &t.subdomain, &t.logoMediaAssetID,
		&t.defaultCurrency, &t.timezone, &t.supportEmail, &t.supportPhone, &t.taxRatePercent,
		&t.loyaltyPointsPerCurrencyUnit, &t.loyaltyRedemptionRate, &t.onboardingCompleted)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetSettings returns the current tenant's settings.
func (s *Service) GetSettings(c *fiber.Ctx) error {
	tx, ok := c.Locals("tx").(pgx.Tx)
	if !ok {
		return httperr.ErrInternalServerError
	}
	t, err := scanTenant(tx, c.Context(), tenantSelect+" WHERE id = $1", c.Locals("tenant_id"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"settings": tenantJSON(t)})
}

type settingsPatchRequest struct {
	Name                         *string `json:"name"`
	Subdomain                    *string `json:"subdomain"`
	LogoMediaAssetID             *string `json:"logo_media_asset_id"`
	DefaultCurrency              *string `json:"default_currency"`
	Timezone                     *string `json:"timezone"`
	SupportEmail                 *string `json:"support_email"`
	SupportPhone                 *string `json:"support_phone"`
	TaxRatePercent               *int    `json:"tax_rate_percent"`
	LoyaltyPointsPerCurrencyUnit *int    `json:"loyalty_points_per_currency_unit"`
	LoyaltyRedemptionRate        *int    `json:"loyalty_redemption_rate"`
}

// subdomainPattern is the store-link rule shared by signup-era provisional
// slugs and the wizard's chosen link: lowercase letters, digits and interior
// hyphens. Anchored so it cannot match a substring.
const subdomainPattern = `^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`

// validSubdomain reports whether s is a usable store link.
func validSubdomain(s string) bool {
	if len(s) < 3 || len(s) > 63 {
		return false
	}
	if matched, _ := regexp.MatchString(subdomainPattern, s); !matched {
		return false
	}
	return true
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

	// Validate numeric ranges if provided.
	if req.TaxRatePercent != nil && (*req.TaxRatePercent < 0 || *req.TaxRatePercent > 100) {
		return httperr.C(fiber.StatusBadRequest, "tax_rate_percent must be 0-100")
	}
	if req.LoyaltyPointsPerCurrencyUnit != nil && *req.LoyaltyPointsPerCurrencyUnit < 0 {
		return httperr.C(fiber.StatusBadRequest, "loyalty_points_per_currency_unit must be >= 0")
	}
	if req.LoyaltyRedemptionRate != nil && *req.LoyaltyRedemptionRate < 0 {
		return httperr.C(fiber.StatusBadRequest, "loyalty_redemption_rate must be >= 0")
	}

	// The wizard is where a merchant claims their store link, so the value is
	// normalised here rather than trusted: lowercased and trimmed, because the
	// storefront host is case-insensitive and a stray capital would otherwise
	// make {store}.shopkeet.com unreachable.
	if req.Subdomain != nil {
		v := strings.ToLower(strings.TrimSpace(*req.Subdomain))
		if !validSubdomain(v) {
			return httperr.C(fiber.StatusBadRequest,
				"subdomain must be 3-63 characters of lowercase letters, numbers, or hyphens, and cannot start or end with a hyphen")
		}
		req.Subdomain = &v
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
	if req.Subdomain != nil {
		sets = append(sets, "subdomain = $"+strconv.Itoa(arg))
		args = append(args, *req.Subdomain)
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
	if req.LoyaltyPointsPerCurrencyUnit != nil {
		sets = append(sets, "loyalty_points_per_currency_unit = $"+strconv.Itoa(arg))
		args = append(args, *req.LoyaltyPointsPerCurrencyUnit)
		arg++
	}
	if req.LoyaltyRedemptionRate != nil {
		sets = append(sets, "loyalty_redemption_rate = $"+strconv.Itoa(arg))
		args = append(args, *req.LoyaltyRedemptionRate)
		arg++
	}

	if len(sets) == 0 {
		return httperr.C(fiber.StatusBadRequest, "no fields to update")
	}

	_, err := tx.Exec(ctx, "UPDATE tenants SET "+join(sets, ", ")+" WHERE id = $1", args...)
	if err != nil {
		if isUniqueViolation(err) {
			// tenants.subdomain is UNIQUE, so this is a taken store link.
			return httperr.C(fiber.StatusConflict, "that store link is already taken")
		}
		return httperr.ErrInternalServerError
	}

	// Return updated settings.
	t, err := scanTenant(tx, ctx, tenantSelect+" WHERE id = $1", tid)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"settings": tenantJSON(t)})
}

// CompleteOnboarding closes the one-time store wizard: it flips the tenant's
// onboarding flag and hands back a fresh session whose onboarding claim is true,
// so the admin guard stops redirecting to the wizard without forcing a re-login.
// Idempotent — a second call is a no-op that still returns a valid token.
func (s *Service) CompleteOnboarding(c *fiber.Ctx) error {
	tx, ok := c.Locals("tx").(pgx.Tx)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := c.Locals("tenant_id").(string)

	if _, err := tx.Exec(ctx,
		"UPDATE tenants SET onboarding_completed = true WHERE id = $1 AND onboarding_completed = false",
		tid); err != nil {
		return httperr.ErrInternalServerError
	}

	t, err := scanTenant(tx, ctx, tenantSelect+" WHERE id = $1", tid)
	if err != nil {
		return httperr.ErrInternalServerError
	}

	userID, _ := c.Locals("user_id").(string)
	role, _ := c.Locals("role").(string)
	// Re-read the account id off the caller's own token so the refreshed session
	// keeps it — without it the merchant could log in but never switch stores.
	accountID := ""
	if h := c.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if claims, err := auth.Parse(s.secret, strings.TrimPrefix(h, "Bearer ")); err == nil {
			accountID = claims.AccountID
		}
	}
	onboarding := true
	token, err := auth.SignMerchant(s.secret, tid, userID, accountID, role, &onboarding, 24*time.Hour)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{
		"token":                token,
		"settings":             tenantJSON(t),
		"onboarding_completed": true,
		"expires":              time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	})
}

// isUniqueViolation reports whether err is a Postgres 23505 (unique_violation),
// so a taken store link returns 409 instead of a 500.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
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
