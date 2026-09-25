// Package customers implements Phase 11 customer accounts: signup/login for
// storefront shoppers, a customer-scoped JWT distinct from the merchant JWT,
// profile + order history, and saved addresses. All handlers run inside the
// RLS-scoped request transaction opened by the middleware (PublicTenantMW for
// signup/login, CustomerAuthMW for the /me group), exactly like the merchant
// surface.
package customers

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service is a stateless handler bundle (state lives in the request
// transaction). New injects the JWT secret and the event bus for emitting
// customers.signup (welcome email).
type Service struct {
	pool   *pgxpool.Pool
	secret string
	bus    *events.Bus
}

// New returns the Phase 11 customer-accounts service.
func New(pool *pgxpool.Pool, secret string, bus *events.Bus) *Service {
	return &Service{pool: pool, secret: secret, bus: bus}
}

// --- helpers -----------------------------------------------------------------

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	if tx, ok := c.Locals("tx").(pgx.Tx); ok {
		return tx, true
	}
	return nil, false
}

func tenantID(c *fiber.Ctx) string {
	if v, ok := c.Locals("tenant_id").(string); ok {
		return v
	}
	return ""
}

func customerID(c *fiber.Ctx) string {
	if v, ok := c.Locals("customer_id").(string); ok {
		return v
	}
	return ""
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// --- requests -----------------------------------------------------------------

type signupRequest struct {
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type addressRequest struct {
	Label        string `json:"label"`
	AddressLine1 string `json:"address_line1"`
	AddressLine2 string `json:"address_line2"`
	City         string `json:"city"`
	State        string `json:"state"`
	PostalCode   string `json:"postal_code"`
	Country      string `json:"country"`
	IsDefault    *bool  `json:"is_default"`
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// --- signup & login ----------------------------------------------------------

type customerRow struct {
	id       string
	email    *string
	phone    *string
	hasLogin bool
}

const customerSelect = `
	SELECT id, email, phone, password_hash IS NOT NULL
	FROM customers`

func customerJSON(c *customerRow) fiber.Map {
	return fiber.Map{
		"id": c.id, "email": strp(c.email), "phone": strp(c.phone),
	}
}

func strp(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nilIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// Signup handles POST /customers/signup (Public — resolves the storefront
// tenant from X-Tenant-ID). Creates the customer with a bcrypt password_hash
// and returns a customer-scoped JWT.
func (s *Service) Signup(c *fiber.Ctx) error {
	var req signupRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	email := normalizeEmail(req.Email)
	if email == "" {
		return httperr.C(fiber.StatusBadRequest, "email required")
	}
	if len(req.Password) < 8 {
		return httperr.C(fiber.StatusBadRequest, "password must be at least 8 characters")
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return httperr.ErrInternalServerError
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := tenantID(c)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO customers (tenant_id, email, phone, password_hash)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		tid, email, nilIfEmpty(req.Phone), hash).Scan(&id); err != nil {
		if isUniqueViolation(err) {
			return httperr.C(fiber.StatusConflict, "email already registered")
		}
		return httperr.ErrInternalServerError
	}

	if s.bus != nil {
		s.bus.Emit(ctx, events.Event{
			Name: "customers.signup",
			Data: fiber.Map{"tenant_id": tid, "email": email, "customer_id": id},
		})
	}

	return s.respond(c, fiber.StatusCreated, &customerRow{id: id, email: &email, phone: nilIfEmpty(req.Phone)})
}

// Login handles POST /customers/login (Public — X-Tenant-ID). Resolves the
// customer by email inside the tenant's RLS scope and verifies the password.
func (s *Service) Login(c *fiber.Ctx) error {
	var req loginRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	email := normalizeEmail(req.Email)
	if email == "" {
		return httperr.C(fiber.StatusBadRequest, "email required")
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	var id string
	var hash *string
	if err := tx.QueryRow(ctx, `
		SELECT id, password_hash FROM customers WHERE email = $1`, email).
		Scan(&id, &hash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httperr.C(fiber.StatusUnauthorized, "invalid credentials")
		}
		return httperr.ErrInternalServerError
	}
	if hash == nil || !auth.CheckPassword(req.Password, *hash) {
		return httperr.C(fiber.StatusUnauthorized, "invalid credentials")
	}
	row, err := loadCustomer(c, "id = $1", id)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return s.respond(c, fiber.StatusOK, row)
}

// respond mints a customer-scoped JWT (24h, like merchant sessions) and returns
// it alongside the profile.
func (s *Service) respond(c *fiber.Ctx, status int, row *customerRow) error {
	token, err := auth.SignCustomer(s.secret, tenantID(c), row.id, 24*time.Hour)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(status).JSON(fiber.Map{
		"token":    token,
		"customer": customerJSON(row),
		"expires":  time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	})
}

// --- profile & order history --------------------------------------------------

// Me handles GET /customers/me (Customer). Returns the profile plus saved
// addresses.
func (s *Service) Me(c *fiber.Ctx) error {
	row, err := loadCustomer(c, "id = $1", customerID(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if row == nil {
		return httperr.C(fiber.StatusNotFound, "customer not found")
	}
	addr, err := loadAddresses(c, customerID(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	out := customerJSON(row)
	out["addresses"] = addr
	return c.JSON(out)
}

// MyOrders handles GET /customers/me/orders (Customer). Returns the orders
// whose customer_id matches the token — guest orders (customer_id NULL) are not
// visible here.
func (s *Service) MyOrders(c *fiber.Ctx) error {
	cid := customerID(c)
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	rows, err := tx.Query(ctx, `
		SELECT id, status, payment_status, total_cents, currency, discount_cents,
		       shipping_cost_cents, created_at
		FROM orders
		WHERE customer_id = $1
		ORDER BY created_at DESC, id`, cid)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()
	list := make([]fiber.Map, 0)
	for rows.Next() {
		var id, status, paymentStatus, currency string
		var total, discount, shipping int
		var createdAt time.Time
		if err := rows.Scan(&id, &status, &paymentStatus, &total, &currency,
			&discount, &shipping, &createdAt); err != nil {
			return httperr.ErrInternalServerError
		}
		list = append(list, fiber.Map{
			"id": id, "status": status, "payment_status": paymentStatus,
			"total_cents": total, "currency": currency,
			"discount_cents": discount, "shipping_cost_cents": shipping,
			"created_at": createdAt.Format(time.RFC3339),
		})
	}
	return c.JSON(fiber.Map{"orders": list})
}

// --- addresses ----------------------------------------------------------------

type addressRow struct {
	id         string
	label      *string
	line1      string
	line2      *string
	city       string
	state      *string
	postalCode *string
	country    string
	isDefault  bool
}

func loadAddresses(c *fiber.Ctx, cid string) ([]fiber.Map, error) {
	tx, ok := txFrom(c)
	if !ok {
		return nil, fmt.Errorf("no tx")
	}
	rows, err := tx.Query(c.Context(), `
		SELECT id, label, address_line1, address_line2, city, state, postal_code, country, is_default
		FROM customer_addresses
		WHERE customer_id = $1
		ORDER BY is_default DESC, id`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]fiber.Map, 0)
	for rows.Next() {
		var a addressRow
		if err := rows.Scan(&a.id, &a.label, &a.line1, &a.line2, &a.city, &a.state,
			&a.postalCode, &a.country, &a.isDefault); err != nil {
			return nil, err
		}
		out = append(out, fiber.Map{
			"id": a.id, "label": strp(a.label), "address_line1": a.line1,
			"address_line2": strp(a.line2), "city": a.city, "state": strp(a.state),
			"postal_code": strp(a.postalCode), "country": a.country,
			"is_default": a.isDefault,
		})
	}
	return out, rows.Err()
}

// ListAddresses handles GET /customers/me/addresses (Customer).
func (s *Service) ListAddresses(c *fiber.Ctx) error {
	addr, err := loadAddresses(c, customerID(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"addresses": addr})
}

// CreateAddress handles POST /customers/me/addresses (Customer).
func (s *Service) CreateAddress(c *fiber.Ctx) error {
	var req addressRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.AddressLine1 == "" || req.City == "" || req.Country == "" {
		return httperr.C(fiber.StatusBadRequest,
			"address_line1, city, country required")
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	cid := customerID(c)
	isDefault := req.IsDefault != nil && *req.IsDefault
	if isDefault {
		if _, err := tx.Exec(ctx,
			"UPDATE customer_addresses SET is_default = false WHERE customer_id = $1", cid); err != nil {
			return httperr.ErrInternalServerError
		}
	}
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO customer_addresses
			(tenant_id, customer_id, label, address_line1, address_line2, city, state,
			 postal_code, country, is_default)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		tenantID(c), cid, nilIfEmpty(req.Label), req.AddressLine1,
		nilIfEmpty(req.AddressLine2), req.City, nilIfEmpty(req.State),
		nilIfEmpty(req.PostalCode), req.Country, isDefault).Scan(&id); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"address": addressJSON(c, id),
	})
}

// UpdateAddress handles PATCH /customers/me/addresses/:id (Customer). Fields
// not supplied keep their current values.
func (s *Service) UpdateAddress(c *fiber.Ctx) error {
	var req addressRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	cid := customerID(c)
	id := c.Params("id")

	var cur addressRow
	if err := tx.QueryRow(ctx, `
		SELECT id, label, address_line1, address_line2, city, state, postal_code, country, is_default
		FROM customer_addresses WHERE id = $1 AND customer_id = $2`, id, cid).
		Scan(&cur.id, &cur.label, &cur.line1, &cur.line2, &cur.city, &cur.state,
			&cur.postalCode, &cur.country, &cur.isDefault); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httperr.C(fiber.StatusNotFound, "address not found")
		}
		return httperr.ErrInternalServerError
	}

	merged := cur
	if req.IsDefault != nil {
		merged.isDefault = *req.IsDefault
	}
	if req.Label != "" {
		merged.label = &req.Label
	}
	if req.AddressLine1 != "" {
		merged.line1 = req.AddressLine1
	}
	if req.AddressLine2 != "" {
		merged.line2 = &req.AddressLine2
	}
	if req.City != "" {
		merged.city = req.City
	}
	if req.State != "" {
		merged.state = &req.State
	}
	if req.PostalCode != "" {
		merged.postalCode = &req.PostalCode
	}
	if req.Country != "" {
		merged.country = req.Country
	}
	if merged.line1 == "" || merged.city == "" || merged.country == "" {
		return httperr.C(fiber.StatusBadRequest,
			"address_line1, city, country required")
	}
	// A PATCH that flips is_default to true promotes this address over the rest.
	if req.IsDefault != nil && *req.IsDefault {
		if _, err := tx.Exec(ctx,
			"UPDATE customer_addresses SET is_default = false WHERE customer_id = $1", cid); err != nil {
			return httperr.ErrInternalServerError
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE customer_addresses
		SET label = $3, address_line1 = $4, address_line2 = $5, city = $6, state = $7,
		    postal_code = $8, country = $9, is_default = $10
		WHERE id = $1 AND customer_id = $2`,
		id, cid, merged.label, merged.line1, merged.line2, merged.city,
		merged.state, merged.postalCode, merged.country, merged.isDefault); err != nil {
		return httperr.ErrInternalServerError
	}

	return c.JSON(fiber.Map{"address": addressJSON(c, id)})
}

// DeleteAddress handles DELETE /customers/me/addresses/:id (Customer).
func (s *Service) DeleteAddress(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	tag, err := tx.Exec(c.Context(),
		"DELETE FROM customer_addresses WHERE id = $1 AND customer_id = $2",
		c.Params("id"), customerID(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "address not found")
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// loadCustomer hydrates one customer by an arbitrary WHERE clause.
func loadCustomer(c *fiber.Ctx, where string, args ...any) (*customerRow, error) {
	tx, ok := txFrom(c)
	if !ok {
		return nil, fmt.Errorf("no tx")
	}
	var row customerRow
	err := tx.QueryRow(c.Context(), customerSelect+" WHERE "+where, args...).
		Scan(&row.id, &row.email, &row.phone, &row.hasLogin)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// addressJSON reads an address back by id so the response is the authoritative
// row, not a request echo.
func addressJSON(c *fiber.Ctx, aid string) fiber.Map {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.Map{"id": aid}
	}
	var a addressRow
	err := tx.QueryRow(c.Context(), `
		SELECT id, label, address_line1, address_line2, city, state, postal_code, country, is_default
		FROM customer_addresses WHERE id = $1 AND customer_id = $2`, aid, customerID(c)).
		Scan(&a.id, &a.label, &a.line1, &a.line2, &a.city, &a.state,
			&a.postalCode, &a.country, &a.isDefault)
	if err != nil {
		return fiber.Map{"id": aid}
	}
	return fiber.Map{
		"id": a.id, "label": strp(a.label), "address_line1": a.line1,
		"address_line2": strp(a.line2), "city": a.city, "state": strp(a.state),
		"postal_code": strp(a.postalCode), "country": a.country,
		"is_default": a.isDefault,
	}
}
