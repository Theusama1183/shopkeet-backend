// Package feeds implements Phase 30 — per-tenant product feeds for shopping
// channels. Two machine-readable representations of the ACTIVE catalog:
//
//	GET /feeds/google-shopping.xml  — Google Merchant Center (GMC) XML feed
//	GET /feeds/meta-catalog.csv     — Meta Commerce catalog CSV
//
// Both are public, tenant-scoped by X-Tenant-ID (PublicTenantMW), and only ever
// contain active products. Availability maps from inventory_count, price is
// rendered from price_cents + currency, and every product links back to its
// storefront URL built from the tenant's custom domain or subdomain.
package feeds

import (
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service generates the feeds from the tenant-scoped request transaction.
type Service struct {
	pool    *pgxpool.Pool
	appBase string // e.g. "shopbase.local" — subdomains append in front
}

// New builds the feed Service. appBase is the shared storefront domain
// (cfg.AppBaseDomain); a tenant with a custom domain overrides it.
func New(pool *pgxpool.Pool, appBase string) *Service {
	return &Service{pool: pool, appBase: strings.TrimSuffix(appBase, ".")}
}

// feedProduct is one active catalog row the feeds render.
type feedProduct struct {
	id          string
	name        string
	slug        string
	description string
	priceCents  int
	currency    string
	inStock     bool
	imageURL    string
}

// --- Google Merchant Center XML --------------------------------------------------

// GoogleShoppingXML handles GET /feeds/google-shopping.xml.
func (s *Service) GoogleShoppingXML(c *fiber.Ctx) error {
	tx, ok := c.Locals("tx").(pgx.Tx)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)

	base, name, err := s.storefront(ctx, tx, tid)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	prods, err := s.activeProducts(ctx, tx)
	if err != nil {
		return httperr.ErrInternalServerError
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<rss version="2.0" xmlns:g="http://base.google.com/ns/1.0">` + "\n")
	b.WriteString("<channel>\n")
	xmlField(&b, "title", name)
	xmlField(&b, "link", base)
	xmlField(&b, "description", name+" product feed")
	for _, p := range prods {
		b.WriteString("<item>\n")
		xmlField(&b, "g:id", p.id)
		xmlField(&b, "g:title", p.name)
		if p.description != "" {
			xmlField(&b, "g:description", p.description)
		}
		xmlField(&b, "g:link", s.productURL(base, p.slug))
		if p.imageURL != "" {
			xmlField(&b, "g:image_link", p.imageURL)
		}
		if p.inStock {
			xmlField(&b, "g:availability", "in stock")
		} else {
			xmlField(&b, "g:availability", "out of stock")
		}
		xmlField(&b, "g:price", priceFor(p.priceCents, p.currency))
		xmlField(&b, "g:condition", "new")
		b.WriteString("</item>\n")
	}
	b.WriteString("</channel>\n")
	b.WriteString("</rss>\n")

	c.Set("Content-Type", "application/xml; charset=utf-8")
	return c.SendString(b.String())
}

func xmlField(b *strings.Builder, tag, value string) {
	b.WriteString("<" + tag + ">")
	if strings.ContainsAny(value, "<>&\"'") {
		var esc strings.Builder
		xmlEscape(&esc, value)
		b.WriteString(esc.String())
	} else {
		b.WriteString(value)
	}
	b.WriteString("</" + tag + ">\n")
}

// xmlEscape escapes the five XML text entities.
func xmlEscape(b *strings.Builder, s string) {
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
}

// --- Meta Commerce CSV -----------------------------------------------------------

// MetaCatalogCSV handles GET /feeds/meta-catalog.csv. Column naming follows
// Meta Commerce: availability uses "in stock"/"out of stock", price is the
// underscore form "12.99_USD".
func (s *Service) MetaCatalogCSV(c *fiber.Ctx) error {
	tx, ok := c.Locals("tx").(pgx.Tx)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)

	base, _, err := s.storefront(ctx, tx, tid)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	prods, err := s.activeProducts(ctx, tx)
	if err != nil {
		return httperr.ErrInternalServerError
	}

	var b strings.Builder
	w := csv.NewWriter(&b)
	if err := w.Write([]string{"id", "title", "description", "availability", "condition", "price", "link", "image_link"}); err != nil {
		return httperr.ErrInternalServerError
	}
	for _, p := range prods {
		avail := "out of stock"
		if p.inStock {
			avail = "in stock"
		}
		price := strconv.FormatFloat(float64(p.priceCents)/100, 'f', 2, 64) + "_" + strings.ToUpper(p.currency)
		if err := w.Write([]string{
			p.id, p.name, p.description, avail, "new", price,
			s.productURL(base, p.slug), p.imageURL,
		}); err != nil {
			return httperr.ErrInternalServerError
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return httperr.ErrInternalServerError
	}

	c.Set("Content-Type", "text/csv; charset=utf-8")
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="meta-catalog-%s.csv"`, time.Now().Format("20060102")))
	return c.SendString(b.String())
}

// --- shared queries --------------------------------------------------------------

// activeProducts loads every active product with its first gallery image.
func (s *Service) activeProducts(ctx context.Context, tx pgx.Tx) ([]feedProduct, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (p.id)
		       p.id, p.name, p.slug, COALESCE(p.description, ''), p.price_cents,
		       p.currency, p.inventory_count > 0, m.url
		FROM products p
		LEFT JOIN product_images pi ON pi.product_id = p.id
		LEFT JOIN media_assets m ON m.id = pi.media_asset_id
		WHERE p.status = 'active'
		ORDER BY p.id, pi.sort_order, pi.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []feedProduct
	for rows.Next() {
		var p feedProduct
		var url *string
		if err := rows.Scan(&p.id, &p.name, &p.slug, &p.description, &p.priceCents,
			&p.currency, &p.inStock, &url); err != nil {
			return nil, err
		}
		if url != nil {
			p.imageURL = *url
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// storefront resolves the public base URL (scheme-less custom_domain wins,
// else <subdomain>.<appbase>) and the tenant display name.
func (s *Service) storefront(ctx context.Context, tx pgx.Tx, tid string) (base, name string, err error) {
	var subdomain, custom string
	if err = tx.QueryRow(ctx,
		"SELECT name, subdomain, COALESCE(custom_domain, '') FROM tenants WHERE id = $1", tid).
		Scan(&name, &subdomain, &custom); err != nil {
		return "", "", err
	}
	host := strings.TrimSpace(custom)
	if host == "" {
		host = strings.TrimSuffix(subdomain, ".") + "." + s.appBase
	}
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	return "https://" + strings.TrimSuffix(host, "/"), name, nil
}

func (s *Service) productURL(base, slug string) string {
	return base + "/products/" + slug
}

func priceFor(cents int, currency string) string {
	return strconv.FormatFloat(float64(cents)/100, 'f', 2, 64) + " " + strings.ToUpper(currency)
}