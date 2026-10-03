// Package smartcollections implements Phase 31 — Smart (Rule-Based)
// Collections. A category with is_smart=true owns a set of AND-combined rules
// (price < 2000, status = active, name contains "sale", ...). Membership in
// smart categories is derived, not manual: whenever a product is saved the
// engine re-evaluates every smart category and syncs product_categories so the
// storefront listing path never recomputes at query time.
package smartcollections

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service handles the merchant category surface (create/update/delete) and
// exposes the recompute entrypoints the product save path calls into.
type Service struct {
	pool *pgxpool.Pool
}

// New builds a smartcollections Service.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// rule is one AND-term of a smart category.
type rule struct {
	field    string
	operator string
	value    string
}

// smartCategory is a smart category id plus its full rule set.
type smartCategory struct {
	id    string
	rules []rule
}

// productAttr is the subset of a products row rules can see. description is a
// pointer (the column is nullable).
type productAttr struct {
	id             string
	name           string
	description    *string
	status         string
	priceCents     int
	inventoryCount int
}

// loadSmartCategories fetches every smart category with its rules, ordered so
// a category's rules stay grouped. Queries run on the caller's tx, so RLS
// scopes them to the tenant set on the connection.
func loadSmartCategories(ctx context.Context, tx pgx.Tx) ([]smartCategory, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.category_id, r.field, r.operator, r.value
		FROM smart_collection_rules r
		JOIN categories c ON c.id = r.category_id
		WHERE c.is_smart = true
		ORDER BY r.category_id, r.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cats []smartCategory
	var cur *smartCategory
	for rows.Next() {
		var cid, f, op, v string
		if err := rows.Scan(&cid, &f, &op, &v); err != nil {
			return nil, err
		}
		if cur == nil || cur.id != cid {
			cats = append(cats, smartCategory{id: cid})
			cur = &cats[len(cats)-1]
		}
		cur.rules = append(cur.rules, rule{field: f, operator: op, value: v})
	}
	return cats, rows.Err()
}

// loadProduct fetches one product's rule-relevant attributes.
func loadProduct(ctx context.Context, tx pgx.Tx, productID string) (*productAttr, error) {
	var p productAttr
	err := tx.QueryRow(ctx, `
		SELECT id, name, description, price_cents, status, inventory_count
		FROM products WHERE id = $1`, productID).
		Scan(&p.id, &p.name, &p.description, &p.priceCents, &p.status, &p.inventoryCount)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// loadAllProducts fetches every product's rule-relevant attributes.
func loadAllProducts(ctx context.Context, tx pgx.Tx) ([]productAttr, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, name, description, price_cents, status, inventory_count
		FROM products`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []productAttr
	for rows.Next() {
		var p productAttr
		if err := rows.Scan(&p.id, &p.name, &p.description, &p.priceCents, &p.status, &p.inventoryCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// matches reports whether a product satisfies every rule (AND logic).
func matches(p productAttr, rules []rule) bool {
	for _, r := range rules {
		if !ruleHolds(p, r) {
			return false
		}
	}
	return true
}

// ruleHolds evaluates a single rule against a product. Rules are validated on
// write (numeric fields parse as ints; text fields compare exactly), so a rule
// that fails to apply is simply a non-match rather than an operator error.
func ruleHolds(p productAttr, r rule) bool {
	switch r.field {
	case "price":
		n, err := strconv.Atoi(r.value)
		if err != nil {
			return false
		}
		return compareInt(r.operator, p.priceCents, n)
	case "inventory_count":
		n, err := strconv.Atoi(r.value)
		if err != nil {
			return false
		}
		return compareInt(r.operator, p.inventoryCount, n)
	case "status":
		return r.operator == "eq" && p.status == r.value
	case "name":
		return compareText(r.operator, p.name, r.value)
	case "description":
		d := ""
		if p.description != nil {
			d = *p.description
		}
		return compareText(r.operator, d, r.value)
	}
	return false
}

func compareInt(op string, a, n int) bool {
	switch op {
	case "lt":
		return a < n
	case "gt":
		return a > n
	case "eq":
		return a == n
	}
	return false
}

func compareText(op, field, value string) bool {
	switch op {
	case "eq":
		return field == value
	case "contains":
		return strings.Contains(strings.ToLower(field), strings.ToLower(value))
	}
	return false
}

// RecomputeForProduct re-evaluates a saved product against every smart
// category and syncs its product_categories membership. Called from the
// product save path (catalog + bulk CSV import) inside the request tx. Manual
// (non-smart) category links are left untouched.  No smart categories -> no-op.
func RecomputeForProduct(ctx context.Context, tx pgx.Tx, productID string) error {
	cats, err := loadSmartCategories(ctx, tx)
	if err != nil {
		return err
	}
	if len(cats) == 0 {
		return nil
	}
	p, err := loadProduct(ctx, tx, productID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}

	// Drop every smart membership for this product, then re-insert the ones it
	// still satisfies (signature changed or rule changed) — all inside the
	// caller's transaction.
	if err := clearSmartMemberships(ctx, tx, productID); err != nil {
		return err
	}
	for _, c := range cats {
		if matches(*p, c.rules) {
			if _, err := tx.Exec(ctx, `
				INSERT INTO product_categories (tenant_id, product_id, category_id)
				VALUES (current_setting('app.current_tenant')::uuid, $1, $2)
				ON CONFLICT (product_id, category_id) DO NOTHING`, productID, c.id); err != nil {
				return err
			}
		}
	}
	return nil
}

// RecomputeForCategory re-evaluates every product against one smart category
// (rule edits, is_smart toggles and category creation) and rewrites its
// membership. Row count is small-merchant scale; no need for index tricks.
func RecomputeForCategory(ctx context.Context, tx pgx.Tx, categoryID string) error {
	rows, err := tx.Query(ctx, `
		SELECT r.field, r.operator, r.value
		FROM smart_collection_rules r WHERE r.category_id = $1
		ORDER BY r.created_at`, categoryID)
	if err != nil {
		return err
	}
	var rules []rule
	for rows.Next() {
		var r rule
		if err := rows.Scan(&r.field, &r.operator, &r.value); err != nil {
			rows.Close()
			return err
		}
		rules = append(rules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		"DELETE FROM product_categories WHERE category_id = $1", categoryID); err != nil {
		return err
	}
	products, err := loadAllProducts(ctx, tx)
	if err != nil {
		return err
	}
	for _, p := range products {
		if matches(p, rules) {
			if _, err := tx.Exec(ctx, `
				INSERT INTO product_categories (tenant_id, product_id, category_id)
				VALUES (current_setting('app.current_tenant')::uuid, $1, $2)
				ON CONFLICT (product_id, category_id) DO NOTHING`, p.id, categoryID); err != nil {
				return err
			}
		}
	}
	return nil
}

// clearSmartMemberships removes every smart-category membership (any category
// currently flagged is_smart) for a product.
func clearSmartMemberships(ctx context.Context, tx pgx.Tx, productID string) error {
	_, err := tx.Exec(ctx, `
		DELETE FROM product_categories
		WHERE product_id = $1
		  AND category_id IN (SELECT id FROM categories WHERE is_smart = true)`, productID)
	return err
}
