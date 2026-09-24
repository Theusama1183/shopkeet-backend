package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service is the Phase 6 content surface. Handlers read/write through the
// request transaction opened by PublicTenantMW / TenantMW (c.Locals("tx")), so
// RLS scopes every query to the resolved tenant. layout (posts/templates/
// sections) and placement_rules (sections) store Puck-compatible JSON verbatim.
type Service struct {
	pool *pgxpool.Pool
}

// New builds a content Service.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// --- helpers ------------------------------------------------------------------

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// savepoint runs fn inside a SAVEPOINT so a predicted 4xx/409 (e.g. a unique
// violation) does not poison the whole request transaction (see catalog for the
// "commit unexpectedly resulted in rollback" gotcha).
func savepoint(ctx context.Context, tx pgx.Tx, fn func() error) error {
	if _, err := tx.Exec(ctx, "SAVEPOINT content_op"); err != nil {
		return err
	}
	if err := fn(); err != nil {
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT content_op")
		return err
	}
	_, err := tx.Exec(ctx, "RELEASE SAVEPOINT content_op")
	return err
}

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func tenantID(c *fiber.Ctx) string {
	id, _ := c.Locals("tenant_id").(string)
	return id
}

// validRoute enforces the leading-slash routing convention shared by posts and
// redirects, so storefront middleware matching is unambiguous.
func validRoute(p string) bool {
	return strings.HasPrefix(p, "/") && len(p) > 1
}

func nullableStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// postRow mirrors a posts row.
type postRow struct {
	id        string
	postType  string
	route     string
	title     string
	layout    []byte
	metaTitle *string
	metaDesc  *string
	status    string
	published *time.Time
	updated   time.Time
}

func postJSON(p *postRow) fiber.Map {
	pub := ""
	if p.published != nil {
		pub = p.published.Format(time.RFC3339)
	}
	return fiber.Map{
		"id": p.id, "post_type": p.postType, "route": p.route, "title": p.title,
		"layout":           json.RawMessage(p.layout),
		"meta_title":       strp(p.metaTitle),
		"meta_description": strp(p.metaDesc),
		"status":           p.status,
		"published_at":     pub,
		"updated_at":       p.updated.Format(time.RFC3339),
	}
}

func strp(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func scanPost(row pgx.Row) (*postRow, error) {
	var p postRow
	if err := row.Scan(&p.id, &p.postType, &p.route, &p.title, &p.layout,
		&p.metaTitle, &p.metaDesc, &p.status, &p.published, &p.updated); err != nil {
		return nil, err
	}
	return &p, nil
}

const postCols = `id, post_type, route, title, layout, meta_title, meta_description,
		status, published_at, updated_at`

// SeedDefaults creates the storefront chrome a brand-new tenant needs, inside
// the signup transaction (RLS already scoped by SignupHandler). Required-for-v1
// content per docs/04-agent-build-spec.md Phase 6: the home `page` post, the
// five templates every storefront renders through, and the header/footer
// sections. Registered in main.go via auth.RegisterTenantCreatedHook.
func SeedDefaults(ctx context.Context, tx pgx.Tx, tid string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO posts (id, tenant_id, post_type, route, title, layout, status, published_at)
		VALUES (gen_random_uuid(), $1, 'page', '/', 'Home', '{}'::jsonb, 'published', now())`, tid); err != nil {
		return err
	}
	for _, tt := range []string{"product", "product_archive", "cart", "404", "order_confirmation"} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO templates (id, tenant_id, template_type, scope, layout, status)
			VALUES (gen_random_uuid(), $1, $2, 'default', '{}'::jsonb, 'published')`, tid, tt); err != nil {
			return err
		}
	}
	for _, sc := range []struct{ typ, name string }{{"header", "Header"}, {"footer", "Footer"}} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO sections (id, tenant_id, section_type, name, layout, status)
			VALUES (gen_random_uuid(), $1, $2, $3, '{}'::jsonb, 'published')`, tid, sc.typ, sc.name); err != nil {
			return err
		}
	}
	return nil
}

// --- posts ---------------------------------------------------------------------

type postRequest struct {
	PostType        string          `json:"post_type"`
	Route           string          `json:"route"`
	Title           string          `json:"title"`
	Layout          json.RawMessage `json:"layout"`
	MetaTitle       string          `json:"meta_title"`
	MetaDescription string          `json:"meta_description"`
	Status          string          `json:"status"`
}

func validPostStatus(s string) bool {
	switch s {
	case "", "draft", "published":
		return true
	}
	return false
}

// CreatePost handles POST /posts (admin).
func (s *Service) CreatePost(c *fiber.Ctx) error {
	var req postRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	req.PostType = defaultStr(req.PostType, "page")
	if req.Route == "" || req.Title == "" || !validRoute(req.Route) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "route and title required (route starts with /)"})
	}
	if !validPostStatus(req.Status) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid status"})
	}
	layout := req.Layout
	if len(layout) == 0 {
		layout = json.RawMessage(`{}`)
	}
	status := defaultStr(req.Status, "draft")
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()

	var id string
	err := savepoint(ctx, tx, func() error {
		return tx.QueryRow(ctx, `
			INSERT INTO posts (tenant_id, post_type, route, title, layout,
				meta_title, meta_description, status, published_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,
				CASE WHEN $8 = 'published' THEN now() ELSE NULL END)
			RETURNING id`,
			tenantID(c), req.PostType, req.Route, req.Title, layout,
			nullableStr(req.MetaTitle), nullableStr(req.MetaDescription), status).Scan(&id)
	})
	if isUniqueViolation(err) {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "route already in use"})
	}
	if err != nil {
		return fiber.ErrInternalServerError
	}
	p, err := scanPost(tx.QueryRow(ctx, "SELECT "+postCols+" FROM posts WHERE id = $1", id))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(postJSON(p))
}

func defaultStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// GetPosts handles GET /posts (public). With both post_type and route, returns
// the single published post at that route; with only post_type, lists the
// tenant's published posts of that type. Only published rows are visible.
func (s *Service) GetPosts(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()
	postType := c.Query("post_type")
	route := c.Query("route")
	if postType == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "post_type required"})
	}

	if route != "" {
		p, err := scanPost(tx.QueryRow(ctx,
			"SELECT "+postCols+" FROM posts WHERE post_type = $1 AND route = $2 AND status = 'published'",
			postType, route))
		if errors.Is(err, pgx.ErrNoRows) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "post not found"})
		}
		if err != nil {
			return fiber.ErrInternalServerError
		}
		return c.JSON(postJSON(p))
	}

	rows, err := tx.Query(ctx,
		"SELECT "+postCols+" FROM posts WHERE post_type = $1 AND status = 'published' ORDER BY updated_at DESC",
		postType)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	defer rows.Close()
	var posts []fiber.Map
	for rows.Next() {
		var p postRow
		if err := rows.Scan(&p.id, &p.postType, &p.route, &p.title, &p.layout,
			&p.metaTitle, &p.metaDesc, &p.status, &p.published, &p.updated); err != nil {
			return fiber.ErrInternalServerError
		}
		posts = append(posts, postJSON(&p))
	}
	if err := rows.Err(); err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"posts": posts})
}

// UpdatePost handles PATCH /posts/:id (admin). Changing route creates a working
// redirect from the old path (docs/04-agent-build-spec.md Phase 6 acceptance).
func (s *Service) UpdatePost(c *fiber.Ctx) error {
	var req postRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if req.Route != "" && !validRoute(req.Route) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "route must start with /"})
	}
	if !validPostStatus(req.Status) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid status"})
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()
	id := c.Params("id")

	current, err := scanPost(tx.QueryRow(ctx,
		"SELECT "+postCols+" FROM posts WHERE id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "post not found"})
	}
	if err != nil {
		return fiber.ErrInternalServerError
	}

	var sets []string
	var args []any
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if req.PostType != "" && req.PostType != current.postType {
		set("post_type", req.PostType)
	}
	if req.Title != "" {
		set("title", req.Title)
	}
	if len(req.Layout) > 0 {
		set("layout", req.Layout)
	}
	if req.MetaTitle != "" {
		set("meta_title", nullableStr(req.MetaTitle))
	}
	if req.MetaDescription != "" {
		set("meta_description", nullableStr(req.MetaDescription))
	}
	if req.Status != "" && req.Status != current.status {
		set("status", req.Status)
		if req.Status == "published" && current.published == nil {
			set("published_at", "now()")
		}
	}

	routeChanged := req.Route != "" && req.Route != current.route
	if routeChanged {
		set("route", req.Route)
	}

	if err := savepoint(ctx, tx, func() error {
		_, err := tx.Exec(ctx, fmt.Sprintf("UPDATE posts SET %s WHERE id = $%d",
			strings.Join(sets, ", "), len(args)+1), append(args, id)...)
		return err
	}); err != nil {
		if isUniqueViolation(err) {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "route already in use"})
		}
		return fiber.ErrInternalServerError
	}

	// Auto-redirect: route change rewrites any existing redirect for the old
	// path so rename chains converge on the latest target.
	if routeChanged {
		if _, err := tx.Exec(ctx, `
			INSERT INTO redirects (tenant_id, from_path, to_path)
			VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, from_path) DO UPDATE SET to_path = EXCLUDED.to_path`,
			tenantID(c), current.route, req.Route); err != nil {
			return fiber.ErrInternalServerError
		}
	}

	p, err := scanPost(tx.QueryRow(ctx, "SELECT "+postCols+" FROM posts WHERE id = $1", id))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(postJSON(p))
}

// DeletePost handles DELETE /posts/:id (admin).
func (s *Service) DeletePost(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	tag, err := tx.Exec(c.Context(), "DELETE FROM posts WHERE id = $1", c.Params("id"))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "post not found"})
	}
	return c.JSON(fiber.Map{"deleted": c.Params("id")})
}

// --- templates -----------------------------------------------------------------

type templateRow struct {
	id           string
	templateType string
	scope        string
	layout       []byte
	metaTitle    *string
	metaDesc     *string
	status       string
	updated      time.Time
}

func templateJSON(t *templateRow) fiber.Map {
	return fiber.Map{
		"id": t.id, "template_type": t.templateType, "scope": t.scope,
		"layout":           json.RawMessage(t.layout),
		"meta_title":       strp(t.metaTitle),
		"meta_description": strp(t.metaDesc),
		"status":           t.status,
		"updated_at":       t.updated.Format(time.RFC3339),
	}
}

// GetTemplate handles GET /templates/:template_type (public). Returns the
// tenant's published default template so the storefront can render that route's
// page (every product renders through the single scope=default product template,
// etc. per Phase 6 acceptance).
func (s *Service) GetTemplate(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	var t templateRow
	err := tx.QueryRow(c.Context(), `
		SELECT id, template_type, scope, layout, meta_title, meta_description, status, updated_at
		FROM templates
		WHERE template_type = $1 AND scope = 'default' AND status = 'published'`,
		c.Params("template_type")).
		Scan(&t.id, &t.templateType, &t.scope, &t.layout, &t.metaTitle, &t.metaDesc, &t.status, &t.updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "template not found"})
	}
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(templateJSON(&t))
}

// PutTemplate handles PUT /templates/:template_type (admin). Upserts the
// tenant's default template for that type — this is the endpoint the Puck editor
// saves its JSON to on publish. Create requires layout; updates are partial.
func (s *Service) PutTemplate(c *fiber.Ctx) error {
	var req postRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if !validPostStatus(req.Status) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid status"})
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()
	tt := c.Params("template_type")
	tid := tenantID(c)

	var id string
	err := tx.QueryRow(ctx, `
		SELECT id FROM templates
		WHERE template_type = $1 AND scope = 'default'`, tt).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if len(req.Layout) == 0 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "layout required when creating a template"})
		}
		status := defaultStr(req.Status, "published")
		if err := tx.QueryRow(ctx, `
			INSERT INTO templates (id, tenant_id, template_type, scope, layout,
				meta_title, meta_description, status)
			VALUES (gen_random_uuid(), $1, $2, 'default', $3, $4, $5, $6)
			RETURNING id`,
			tid, tt, req.Layout, nullableStr(req.MetaTitle),
			nullableStr(req.MetaDescription), status).Scan(&id); err != nil {
			return fiber.ErrInternalServerError
		}
	case err != nil:
		return fiber.ErrInternalServerError
	default:
		var sets []string
		var args []any
		set := func(col string, v any) {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
		}
		if len(req.Layout) > 0 {
			set("layout", req.Layout)
		}
		if req.MetaTitle != "" {
			set("meta_title", nullableStr(req.MetaTitle))
		}
		if req.MetaDescription != "" {
			set("meta_description", nullableStr(req.MetaDescription))
		}
		if req.Status != "" {
			set("status", req.Status)
		}
		if len(sets) > 0 {
			if _, err := tx.Exec(ctx, fmt.Sprintf("UPDATE templates SET %s WHERE id = $%d",
				strings.Join(sets, ", "), len(args)+1), append(args, id)...); err != nil {
				return fiber.ErrInternalServerError
			}
		}
	}

	var t templateRow
	if err := tx.QueryRow(ctx, `
		SELECT id, template_type, scope, layout, meta_title, meta_description, status, updated_at
		FROM templates WHERE id = $1`, id).
		Scan(&t.id, &t.templateType, &t.scope, &t.layout, &t.metaTitle, &t.metaDesc, &t.status, &t.updated); err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(templateJSON(&t))
}

// --- sections ------------------------------------------------------------------

type sectionRow struct {
	id          string
	sectionType string
	name        string
	layout      []byte
	rules       []byte
	status      string
	updated     time.Time
}

func sectionJSON(sc *sectionRow) fiber.Map {
	rv := json.RawMessage(sc.rules)
	if len(sc.rules) == 0 {
		rv = json.RawMessage(nil)
	}
	return fiber.Map{
		"id": sc.id, "section_type": sc.sectionType, "name": sc.name,
		"layout": json.RawMessage(sc.layout), "placement_rules": rv,
		"status": sc.status, "updated_at": sc.updated.Format(time.RFC3339),
	}
}

const sectionCols = `id, section_type, name, layout, placement_rules, status, updated_at`

func scanSection(row pgx.Row) (*sectionRow, error) {
	var sc sectionRow
	if err := row.Scan(&sc.id, &sc.sectionType, &sc.name, &sc.layout, &sc.rules, &sc.status, &sc.updated); err != nil {
		return nil, err
	}
	return &sc, nil
}

// GetSections handles GET /sections (public). With section_type=popup it lists
// the tenant's published popups (storefront evaluates placement_rules
// client-side); for header/footer it returns the published chrome row(s).
func (s *Service) GetSections(c *fiber.Ctx) error {
	st := c.Query("section_type")
	if st == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "section_type required"})
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	rows, err := tx.Query(c.Context(),
		"SELECT "+sectionCols+" FROM sections WHERE section_type = $1 AND status = 'published' ORDER BY updated_at DESC", st)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	defer rows.Close()
	var sections []fiber.Map
	for rows.Next() {
		var sc sectionRow
		if err := rows.Scan(&sc.id, &sc.sectionType, &sc.name, &sc.layout, &sc.rules, &sc.status, &sc.updated); err != nil {
			return fiber.ErrInternalServerError
		}
		sections = append(sections, sectionJSON(&sc))
	}
	if err := rows.Err(); err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"sections": sections})
}

type sectionRequest struct {
	SectionType    string          `json:"section_type"`
	Name           string          `json:"name"`
	Layout         json.RawMessage `json:"layout"`
	PlacementRules json.RawMessage `json:"placement_rules"`
	Status         string          `json:"status"`
}

// CreateSection handles POST /sections (admin). Used mainly for popups —
// header/footer are typically one row each (seeded at signup) updated via PATCH.
func (s *Service) CreateSection(c *fiber.Ctx) error {
	var req sectionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if req.SectionType == "" || req.Name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "section_type and name required"})
	}
	if !validPostStatus(req.Status) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid status"})
	}
	layout := req.Layout
	if len(layout) == 0 {
		layout = json.RawMessage(`{}`)
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	var id string
	if err := tx.QueryRow(c.Context(), `
		INSERT INTO sections (id, tenant_id, section_type, name, layout, placement_rules, status)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
		RETURNING id`,
		tenantID(c), req.SectionType, req.Name, layout, ruleOrNil(req.PlacementRules),
		defaultStr(req.Status, "draft")).Scan(&id); err != nil {
		return fiber.ErrInternalServerError
	}
	sc, err := scanSection(tx.QueryRow(c.Context(), "SELECT "+sectionCols+" FROM sections WHERE id = $1", id))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(sectionJSON(sc))
}

func ruleOrNil(r json.RawMessage) any {
	if len(r) == 0 {
		return nil
	}
	return r
}

// UpdateSection handles PATCH /sections/:id (admin).
func (s *Service) UpdateSection(c *fiber.Ctx) error {
	var req sectionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if !validPostStatus(req.Status) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid status"})
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()
	id := c.Params("id")

	if _, err := scanSection(tx.QueryRow(ctx,
		"SELECT "+sectionCols+" FROM sections WHERE id = $1", id)); errors.Is(err, pgx.ErrNoRows) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "section not found"})
	} else if err != nil {
		return fiber.ErrInternalServerError
	}

	var sets []string
	var args []any
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if req.Name != "" {
		set("name", req.Name)
	}
	if len(req.Layout) > 0 {
		set("layout", req.Layout)
	}
	if len(req.PlacementRules) > 0 {
		set("placement_rules", req.PlacementRules)
	}
	if req.Status != "" {
		set("status", req.Status)
	}
	if len(sets) > 0 {
		if _, err := tx.Exec(ctx, fmt.Sprintf("UPDATE sections SET %s WHERE id = $%d",
			strings.Join(sets, ", "), len(args)+1), append(args, id)...); err != nil {
			return fiber.ErrInternalServerError
		}
	}
	sc, err := scanSection(tx.QueryRow(ctx, "SELECT "+sectionCols+" FROM sections WHERE id = $1", id))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(sectionJSON(sc))
}

// DeleteSection handles DELETE /sections/:id (admin).
func (s *Service) DeleteSection(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	tag, err := tx.Exec(c.Context(), "DELETE FROM sections WHERE id = $1", c.Params("id"))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "section not found"})
	}
	return c.JSON(fiber.Map{"deleted": c.Params("id")})
}

// --- redirects -----------------------------------------------------------------

type redirectRow struct {
	id      string
	from    string
	to      string
	created time.Time
}

func redirectJSON(r *redirectRow) fiber.Map {
	return fiber.Map{
		"id": r.id, "from_path": r.from, "to_path": r.to,
		"created_at": r.created.Format(time.RFC3339),
	}
}

// ListRedirects handles GET /redirects (admin).
func (s *Service) ListRedirects(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	rows, err := tx.Query(c.Context(), `
		SELECT id, from_path, to_path, created_at FROM redirects ORDER BY created_at DESC`)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	defer rows.Close()
	var redirects []fiber.Map
	for rows.Next() {
		var r redirectRow
		if err := rows.Scan(&r.id, &r.from, &r.to, &r.created); err != nil {
			return fiber.ErrInternalServerError
		}
		redirects = append(redirects, redirectJSON(&r))
	}
	if err := rows.Err(); err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"redirects": redirects})
}

// CreateRedirect handles POST /redirects (admin). The storefront's route change
// handler usually produces these automatically; this endpoint covers operator
// overrides.
func (s *Service) CreateRedirect(c *fiber.Ctx) error {
	var req struct {
		FromPath string `json:"from_path"`
		ToPath   string `json:"to_path"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if !validRoute(req.FromPath) || !validRoute(req.ToPath) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "from_path and to_path required (start with /)"})
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	var id string
	err := savepoint(c.Context(), tx, func() error {
		return tx.QueryRow(c.Context(), `
			INSERT INTO redirects (id, tenant_id, from_path, to_path)
			VALUES (gen_random_uuid(), $1, $2, $3)
			RETURNING id`, tenantID(c), req.FromPath, req.ToPath).Scan(&id)
	})
	if isUniqueViolation(err) {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "from_path already mapped"})
	}
	if err != nil {
		return fiber.ErrInternalServerError
	}
	var r redirectRow
	if err := tx.QueryRow(c.Context(), `
		SELECT id, from_path, to_path, created_at FROM redirects WHERE id = $1`, id).
		Scan(&r.id, &r.from, &r.to, &r.created); err != nil {
		return fiber.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(redirectJSON(&r))
}

// DeleteRedirect handles DELETE /redirects/:id (admin).
func (s *Service) DeleteRedirect(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	tag, err := tx.Exec(c.Context(), "DELETE FROM redirects WHERE id = $1", c.Params("id"))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "redirect not found"})
	}
	return c.JSON(fiber.Map{"deleted": c.Params("id")})
}

// LookupRedirect handles GET /redirects/lookup?path= (public). Next.js
// middleware calls this before falling through to the 404 template.
func (s *Service) LookupRedirect(c *fiber.Ctx) error {
	path := c.Query("path")
	if path == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "path query param required"})
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	var r redirectRow
	err := tx.QueryRow(c.Context(), `
		SELECT id, from_path, to_path, created_at FROM redirects WHERE from_path = $1`, path).
		Scan(&r.id, &r.from, &r.to, &r.created)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "no redirect"})
	}
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"redirect": redirectJSON(&r)})
}
