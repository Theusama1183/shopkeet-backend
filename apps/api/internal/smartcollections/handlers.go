// handlers.go — merchant category surface for Phase 31. Categories start as
// ordinary ones (Phase 3); a category becomes a smart collection by creating/
// updating it with is_smart=true and a rules array. Smart memberships are
// recomputed immediately so GET /categories and product listing reflect the
// new rules without a merchant-side "publish" step.
package smartcollections

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/shopkeet/api/internal/platform/httperr"
)

type ruleInput struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

var numericFields = map[string]bool{"price": true, "inventory_count": true}
var textFields = map[string]bool{"status": true, "name": true, "description": true}

// validateRules checks each rule's field/operator/value combination and
// returns a normalized copy, or a message describing the first bad rule.
func validateRules(in []ruleInput) ([]ruleInput, string) {
	var out []ruleInput
	for _, r := range in {
		f := strings.TrimSpace(r.Field)
		op := strings.TrimSpace(r.Operator)
		v := strings.TrimSpace(r.Value)
		if !numericFields[f] && !textFields[f] {
			return nil, "invalid field (price|inventory_count|status|name|description)"
		}
		switch op {
		case "lt", "gt", "eq", "contains":
		default:
			return nil, "invalid operator (lt|gt|eq|contains)"
		}
		if numericFields[f] {
			if op == "contains" {
				return nil, "contains not allowed on numeric fields"
			}
			if _, err := strconv.Atoi(v); err != nil {
				return nil, "rule value must be an integer for " + f
			}
		}
		if f == "status" && op == "contains" {
			return nil, "contains not allowed on status"
		}
		if f == "status" {
			switch v {
			case "draft", "active", "archived":
			default:
				return nil, "status rule value must be draft|active|archived"
			}
		}
		out = append(out, ruleInput{Field: f, Operator: op, Value: v})
	}
	return out, ""
}

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func tenantID(c *fiber.Ctx) string {
	id, _ := c.Locals("tenant_id").(string)
	return id
}

// slugify derives a category slug from its name (mirrors the bulk-CSV rules).
func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "category"
	}
	return slug
}

func categoryResponse(cid, name, slug string, isSmart bool, rules []ruleInput) fiber.Map {
	rs := make([]fiber.Map, 0, len(rules))
	for _, r := range rules {
		rs = append(rs, fiber.Map{"field": r.Field, "operator": r.Operator, "value": r.Value})
	}
	return fiber.Map{
		"id": cid, "name": name, "slug": slug, "is_smart": isSmart, "rules": rs,
	}
}

// CreateCategory handles POST /categories (admin). Always creates the category
// ({name} required, slug auto-derived when omitted); with is_smart=true the
// rules ride along and membership is computed immediately.
func (s *Service) CreateCategory(c *fiber.Ctx) error {
	var req struct {
		Name    string      `json:"name"`
		Slug    string      `json:"slug"`
		IsSmart bool        `json:"is_smart"`
		Rules   []ruleInput `json:"rules"`
	}
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return httperr.C(fiber.StatusBadRequest, "name required")
	}
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		slug = slugify(req.Name)
	}

	rules := req.Rules
	if req.IsSmart {
		norm, msg := validateRules(rules)
		if msg != "" {
			return httperr.C(fiber.StatusBadRequest, msg)
		}
		rules = norm
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := tenantID(c)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO categories (tenant_id, name, slug, is_smart)
		VALUES ($1, $2, $3, $4) RETURNING id`, tid, req.Name, slug, req.IsSmart).Scan(&id); err != nil {
		if isUniqueViolation(err) {
			return httperr.C(fiber.StatusConflict, "slug taken")
		}
		return httperr.ErrInternalServerError
	}
	if err := s.setRules(ctx, tx, tid, id, rules); err != nil {
		return httperr.ErrInternalServerError
	}
	if req.IsSmart {
		if err := RecomputeForCategory(ctx, tx, id); err != nil {
			return httperr.ErrInternalServerError
		}
	}
	return c.Status(fiber.StatusCreated).JSON(categoryResponse(id, req.Name, slug, req.IsSmart, rules))
}

// UpdateCategory handles PATCH /categories/:id (admin). Renames, changes the
// slug, toggles is_smart, and replaces the rules — after which smart members
// are recomputed so existing products rejoin/leave immediately.
func (s *Service) UpdateCategory(c *fiber.Ctx) error {
	var req struct {
		Name    *string      `json:"name"`
		Slug    *string      `json:"slug"`
		IsSmart *bool        `json:"is_smart"`
		Rules   *[]ruleInput `json:"rules"`
	}
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid := tenantID(c)
	id := c.Params("id")

	var cur struct {
		name    string
		slug    string
		isSmart bool
	}
	err := tx.QueryRow(ctx,
		"SELECT name, slug, is_smart FROM categories WHERE id = $1", id).
		Scan(&cur.name, &cur.slug, &cur.isSmart)
	if err != nil {
		if err == pgx.ErrNoRows {
			return httperr.C(fiber.StatusNotFound, "category not found")
		}
		return httperr.ErrInternalServerError
	}

	name, slug := cur.name, cur.slug
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" {
			return httperr.C(fiber.StatusBadRequest, "name required")
		}
		name = n
	}
	if req.Slug != nil {
		slug = strings.TrimSpace(*req.Slug)
	}

	var sets []string
	var args []any
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, col+" = $"+strconv.Itoa(len(args)))
	}
	set("name", name)
	set("slug", slug)

	wantSmart := cur.isSmart
	if req.IsSmart != nil {
		wantSmart = *req.IsSmart
	}
	if wantSmart != cur.isSmart {
		set("is_smart", wantSmart)
	}

	var rules []ruleInput
	if req.Rules != nil {
		norm, msg := validateRules(*req.Rules)
		if msg != "" {
			return httperr.C(fiber.StatusBadRequest, msg)
		}
		rules = norm
	}

	args = append(args, id)
	if _, err := tx.Exec(ctx,
		"UPDATE categories SET "+strings.Join(sets, ", ")+" WHERE id = $"+strconv.Itoa(len(args)), args...); err != nil {
		if isUniqueViolation(err) {
			return httperr.C(fiber.StatusConflict, "slug taken")
		}
		return httperr.ErrInternalServerError
	}

	// Rules replace wholesale whenever provided. Smart membership is rebuilt:
	// an explicit rules/rules-removal, an is_smart on-switch, or (for safety)
	// any change to a category that is currently smart.
	if req.Rules != nil {
		if err := s.setRules(ctx, tx, tid, id, rules); err != nil {
			return httperr.ErrInternalServerError
		}
	}
	if wantSmart && (req.Rules != nil || (!cur.isSmart && wantSmart)) {
		if err := RecomputeForCategory(ctx, tx, id); err != nil {
			return httperr.ErrInternalServerError
		}
	}
	if !wantSmart && cur.isSmart {
		if _, err := tx.Exec(ctx,
			"DELETE FROM smart_collection_rules WHERE category_id = $1", id); err != nil {
			return httperr.ErrInternalServerError
		}
	}

	rs := rules
	if req.Rules == nil {
		rs, _ = s.loadRuleInputs(ctx, tx, id)
	}
	return c.JSON(categoryResponse(id, name, slug, wantSmart, rs))
}

// DeleteCategory handles DELETE /categories/:id (admin). Removes memberships,
// rules and the category itself inside the request tx.
func (s *Service) DeleteCategory(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	id := c.Params("id")

	if _, err := tx.Exec(ctx,
		"DELETE FROM product_categories WHERE category_id = $1", id); err != nil {
		return httperr.ErrInternalServerError
	}
	ct, err := tx.Exec(ctx, "DELETE FROM categories WHERE id = $1", id)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if ct.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "category not found")
	}
	return c.JSON(fiber.Map{"deleted": true})
}

// setRules replaces a category's rule set wholesale.
func (s *Service) setRules(ctx context.Context, tx pgx.Tx, tid, categoryID string, rules []ruleInput) error {
	if _, err := tx.Exec(ctx,
		"DELETE FROM smart_collection_rules WHERE category_id = $1", categoryID); err != nil {
		return err
	}
	for _, r := range rules {
		if _, err := tx.Exec(ctx, `
			INSERT INTO smart_collection_rules (tenant_id, category_id, field, operator, value)
			VALUES ($1, $2, $3, $4, $5)`, tid, categoryID, r.Field, r.Operator, r.Value); err != nil {
			return err
		}
	}
	return nil
}

// loadRuleInputs fetches a category's current rules (for PATCH responses that
// didn't touch rules).
func (s *Service) loadRuleInputs(ctx context.Context, tx pgx.Tx, categoryID string) ([]ruleInput, error) {
	rows, err := tx.Query(ctx, `
		SELECT field, operator, value FROM smart_collection_rules
		WHERE category_id = $1 ORDER BY created_at`, categoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ruleInput
	for rows.Next() {
		var r ruleInput
		if err := rows.Scan(&r.Field, &r.Operator, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
