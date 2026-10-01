// Package notifications implements Phase 12: order & account notifications
// behind a swappable provider interface. The core requirement is that a failed
// send must never fail the checkout/request that produced the event.
package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/events"
)

// Notification is a single outbound message to a customer.
type Notification struct {
	TenantID  string
	Type      string // order_confirmation, order_shipped, order_delivered, customer_welcome, cart_abandoned, back_in_stock
	Recipient string // email or phone
	From      string // envelope/From override, e.g. "Shopkeet Support <support@shopkeet.com>"
	ReplyTo   string // optional; when set, customer replies land here
	OrderID   string // empty for customer_welcome
	Subject   string
	Body      string
}

// Provider sends a notification. Implementations are swappable.
// Name() identifies the provider in logs/telemetry.
type Provider interface {
	Name() string
	Send(ctx context.Context, n Notification) error
}

// LogProvider writes to stdout — used when no real provider is configured.
type LogProvider struct{}

func (LogProvider) Name() string { return "log" }

func (LogProvider) Send(ctx context.Context, n Notification) error {
	blob, _ := json.Marshal(n)
	log.Printf("[notifications:%s] %s", n.Type, string(blob))
	return nil
}

// ResendProvider uses Resend (https://resend.com) to send real emails.
// Env: RESEND_API_KEY, NOTIFICATIONS_FROM_EMAIL.
type ResendProvider struct {
	apiKey string
	from   string
	client *http.Client
}

func NewResendProvider(apiKey, from string) *ResendProvider {
	return &ResendProvider{
		apiKey: apiKey,
		from:   from,
		client: &http.Client{Timeout: 8 * time.Second},
	}
}

func (p *ResendProvider) Name() string { return "resend" }

func (p *ResendProvider) Send(ctx context.Context, n Notification) error {
	if n.Recipient == "" {
		return fmt.Errorf("empty recipient")
	}
	// Resend only sends from a verified domain, so the From must always come
	// from NOTIFICATIONS_FROM_EMAIL (p.from), never from Notification.From —
	// per-store subdomains like <store>.shopkeet.com can't be in the From. The
	// per-store support address still lands in Reply-To, which may be any domain.
	body := map[string]string{
		"from":    p.from,
		"to":      n.Recipient,
		"subject": n.Subject,
		"html":    n.Body,
	}
	if n.ReplyTo != "" {
		body["reply_to"] = n.ReplyTo
	}
	bodyBytes, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.resend.com/emails", bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("resend %d", resp.StatusCode)
	}
	return nil
}

// Service runs the event subscribers and logs delivery attempts.
type Service struct {
	pool    *pgxpool.Pool
	prov    Provider
	baseDmn string // app base domain, e.g. "shopkeet.com"
}

func New(pool *pgxpool.Pool, prov Provider, baseDomain string) *Service {
	if prov == nil {
		prov = LogProvider{}
	}
	return &Service{pool: pool, prov: prov, baseDmn: baseDomain}
}

// Subscribe wires the internal event handlers. Handlers always return nil so
// they never break the emitting code path (checkout, order status change).
func (s *Service) Subscribe(bus *events.Bus) {
	bus.Subscribe("order.created", s.onOrderCreated)
	bus.Subscribe("order.paid", s.onOrderPaid)
	bus.Subscribe("customers.signup", s.onCustomerSignup)
	bus.Subscribe("variant.restocked", s.onVariantRestocked)
}

func (s *Service) onOrderCreated(ctx context.Context, e events.Event) error {
	m, ok := e.Data.(fiber.Map)
	if !ok {
		return nil
	}
	orderID, _ := m["order_id"].(string)
	tenantID, _ := m["tenant_id"].(string)
	if orderID == "" || tenantID == "" {
		return nil
	}
	go func() {
		s.deliverOrder(ctx, tenantID, orderID, "order_confirmation")
	}()
	return nil
}

func (s *Service) onOrderPaid(ctx context.Context, e events.Event) error {
	m, ok := e.Data.(fiber.Map)
	if !ok {
		return nil
	}
	orderID, _ := m["order_id"].(string)
	tenantID, _ := m["tenant_id"].(string)
	if orderID == "" || tenantID == "" {
		return nil
	}
	go func() {
		s.deliverOrder(ctx, tenantID, orderID, "order_delivered")
	}()
	return nil
}

func (s *Service) onCustomerSignup(ctx context.Context, e events.Event) error {
	m, ok := e.Data.(fiber.Map)
	if !ok {
		return nil
	}
	tenantID, _ := m["tenant_id"].(string)
	email, _ := m["email"].(string)
	if tenantID == "" || email == "" {
		return nil
	}
	go func() {
		s.deliverWelcome(ctx, tenantID, email)
	}()
	return nil
}

// onVariantRestocked handles variant.restocked (Phase 19): a merchant PATCH
// moved a variant's inventory 0 -> positive, so every subscriber waiting on it
// gets exactly one email. Runs in a goroutine like the other deliveries so a
// slow provider never slows the PATCH.
func (s *Service) onVariantRestocked(ctx context.Context, e events.Event) error {
	m, ok := e.Data.(fiber.Map)
	if !ok {
		return nil
	}
	variantID, _ := m["variant_id"].(string)
	productID, _ := m["product_id"].(string)
	tenantID, _ := m["tenant_id"].(string)
	if variantID == "" || productID == "" || tenantID == "" {
		return nil
	}
	go func() {
		s.deliverBackInStock(ctx, tenantID, variantID, productID)
	}()
	return nil
}

func (s *Service) deliverOrder(ctx context.Context, tenantID, orderID, typ string) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		log.Printf("[notifications] begin failed: %v", err)
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		log.Printf("[notifications] set tenant failed: %v", err)
		return
	}

	var customerName, currency string
	var customerEmail *string
	var totalCents int
	err = tx.QueryRow(ctx, `
		SELECT customer_name, customer_email, currency, total_cents
		FROM orders WHERE id = $1`, orderID).Scan(&customerName, &customerEmail, &currency, &totalCents)
	if err != nil {
		log.Printf("[notifications] load order %s failed: %v", orderID, err)
		return
	}

	// Guest orders carry a NULL customer_email. Providers are email-only, so a
	// phone number is not a usable recipient — skip rather than send garbage.
	recipient := ""
	if customerEmail != nil {
		recipient = strings.TrimSpace(*customerEmail)
	}
	if recipient == "" {
		log.Printf("[notifications] order %s has no email recipient", orderID)
		return
	}

	var storeName, storeSubdomain string
	_ = tx.QueryRow(ctx, `SELECT name, subdomain FROM tenants WHERE id = $1`, tenantID).Scan(&storeName, &storeSubdomain)
	if storeName == "" {
		storeName = "Shopkeet"
	}

	// Shopify-style: the store's own domain addresses the customer. Order
	// confirmations come from the store host and replies land back on the
	// store's support address (<subdomain>.<base>).
	from := fmt.Sprintf("%s via Shopkeet <no-reply@%s>", storeName, s.storeHost(storeSubdomain))
	replyTo := fmt.Sprintf("support@%s", s.storeHost(storeSubdomain))

	var subject, html string
	switch typ {
	case "order_confirmation":
		subject = fmt.Sprintf("Order confirmation #%s from %s", orderID[:8], storeName)
		html = fmt.Sprintf("<p>Hi %s,</p><p>Thanks for your order <strong>%s</strong> (total: %d %s). We'll let you know when it ships.</p>", customerName, orderID, totalCents, currency)
	case "order_delivered":
		subject = fmt.Sprintf("Your order #%s has been delivered", orderID[:8])
		html = fmt.Sprintf("<p>Hi %s,</p><p>Your order <strong>%s</strong> has been delivered. Enjoy!</p>", customerName, orderID)
	default:
		return
	}

	status := "sent"
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.prov.Send(sendCtx, Notification{
		TenantID:  tenantID,
		Type:      typ,
		Recipient: recipient,
		From:      from,
		ReplyTo:   replyTo,
		OrderID:   orderID,
		Subject:   subject,
		Body:      html,
	}); err != nil {
		status = "failed"
		log.Printf("[notifications] %s send failed for order %s: %v", typ, orderID, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO notification_log (tenant_id, notification_type, recipient, order_id, status)
		VALUES ($1, $2, $3, $4, $5)`, tenantID, typ, recipient, orderID, status); err != nil {
		log.Printf("[notifications] log insert failed: %v", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("[notifications] commit failed: %v", err)
	}
}

func (s *Service) deliverWelcome(ctx context.Context, tenantID, email string) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		log.Printf("[notifications] begin failed: %v", err)
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		log.Printf("[notifications] set tenant failed: %v", err)
		return
	}

	var storeName, storeSubdomain string
	_ = tx.QueryRow(ctx, `SELECT name, subdomain FROM tenants WHERE id = $1`, tenantID).Scan(&storeName, &storeSubdomain)
	if storeName == "" {
		storeName = "Shopkeet"
	}

	// Account emails come from the platform brand but still let the customer
	// reply on the store's own support address.
	from := fmt.Sprintf("%s via Shopkeet <no-reply@%s>", storeName, s.storeHost(storeSubdomain))
	replyTo := fmt.Sprintf("support@%s", s.storeHost(storeSubdomain))

	subject := fmt.Sprintf("Welcome to %s!", storeName)
	html := fmt.Sprintf("<p>Hi,</p><p>Thanks for creating an account at <strong>%s</strong>. You can now track orders and save addresses.</p>", storeName)

	status := "sent"
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.prov.Send(sendCtx, Notification{
		TenantID:  tenantID,
		Type:      "customer_welcome",
		Recipient: email,
		From:      from,
		ReplyTo:   replyTo,
		OrderID:   "",
		Subject:   subject,
		Body:      html,
	}); err != nil {
		status = "failed"
		log.Printf("[notifications] welcome send failed: %v", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO notification_log (tenant_id, notification_type, recipient, order_id, status)
		VALUES ($1, 'customer_welcome', $2, NULL, $3)`, tenantID, email, status); err != nil {
		log.Printf("[notifications] log insert failed: %v", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("[notifications] commit failed: %v", err)
	}
}

// deliverBackInStock emails every subscriber waiting on a restocked variant
// (Phase 19) and stamps notified_at exactly once per subscriber. The claim
// UPDATE ... WHERE notified_at IS NULL is the concurrency guard: if two
// restock events land together (or a restock happens while one is sending),
// only the first tx wins each row, so no subscriber is ever emailed twice.
// A failed provider send still stamps the claim (status 'failed' in the log),
// matching the rest of the codebase — a subscriber is never re-mailed.
func (s *Service) deliverBackInStock(ctx context.Context, tenantID, variantID, productID string) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		log.Printf("[notifications] begin failed: %v", err)
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		log.Printf("[notifications] set tenant failed: %v", err)
		return
	}

	var productName string
	if err := tx.QueryRow(ctx, "SELECT name FROM products WHERE id = $1", productID).
		Scan(&productName); err != nil {
		productName = "An item"
	}
	var storeName, storeSubdomain string
	_ = tx.QueryRow(ctx, `SELECT name, subdomain FROM tenants WHERE id = $1`, tenantID).
		Scan(&storeName, &storeSubdomain)
	if storeName == "" {
		storeName = "Shopkeet"
	}

	type sub struct {
		id    string
		email string
	}
	rows, err := tx.Query(ctx, `
		SELECT id, email FROM back_in_stock_subscriptions
		WHERE variant_id = $1 AND notified_at IS NULL
		ORDER BY created_at, id`, variantID)
	if err != nil {
		log.Printf("[notifications] load back-in-stock subs failed: %v", err)
		return
	}
	var subs []sub
	for rows.Next() {
		var x sub
		if err := rows.Scan(&x.id, &x.email); err != nil {
			rows.Close()
			log.Printf("[notifications] scan back-in-stock sub failed: %v", err)
			return
		}
		subs = append(subs, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("[notifications] back-in-stock subs rows failed: %v", err)
		return
	}
	if len(subs) == 0 {
		return
	}

	from := fmt.Sprintf("%s via Shopkeet <no-reply@%s>", storeName, s.storeHost(storeSubdomain))
	replyTo := fmt.Sprintf("support@%s", s.storeHost(storeSubdomain))
	storeURL := fmt.Sprintf("https://%s", s.storeHost(storeSubdomain))

	for _, x := range subs {
		tag, err := tx.Exec(ctx, `
			UPDATE back_in_stock_subscriptions
			SET notified_at = now()
			WHERE id = $1 AND notified_at IS NULL`, x.id)
		if err != nil {
			log.Printf("[notifications] claim back-in-stock sub %s failed: %v", x.id, err)
			continue
		}
		if tag.RowsAffected() == 0 {
			continue // another tx claimed the row first; never email twice
		}

		subject := fmt.Sprintf("Back in stock at %s", storeName)
		html := fmt.Sprintf(
			"<p>Hi,</p><p><strong>%s</strong> is back in stock at <strong>%s</strong>.</p><p><a href=\"%s\">Shop now</a></p>",
			productName, storeName, storeURL)

		status := "sent"
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := s.prov.Send(sendCtx, Notification{
			TenantID:  tenantID,
			Type:      "back_in_stock",
			Recipient: x.email,
			From:      from,
			ReplyTo:   replyTo,
			Subject:   subject,
			Body:      html,
		}); err != nil {
			status = "failed"
			log.Printf("[notifications] back_in_stock send failed for %s: %v", x.email, err)
		}
		cancel()

		if _, err := tx.Exec(ctx, `
			INSERT INTO notification_log (tenant_id, notification_type, recipient, status)
			VALUES ($1, 'back_in_stock', $2, $3)`, tenantID, x.email, status); err != nil {
			log.Printf("[notifications] log insert failed: %v", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("[notifications] commit failed: %v", err)
	}
}

// SendCartAbandoned is the Phase 17 recovery email (Klaviyo replacement). It is
// invoked by the hourly abandonment sweep; it renders the cart contents, sends
// via the provider, and records a notification_log row. A failed send logs a
// 'failed' row and returns nil — the sweep always stamps recovery_sent_at so a
// cart is never emailed twice, matching the "failed send must never fail the
// work that produced the event" rule.
func (s *Service) SendCartAbandoned(ctx context.Context, tenantID, cartID string) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		log.Printf("[notifications] begin failed: %v", err)
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		log.Printf("[notifications] set tenant failed: %v", err)
		return
	}

	var email *string
	if err := tx.QueryRow(ctx,
		"SELECT customer_email FROM carts WHERE id = $1 AND tenant_id = $2", cartID, tenantID).Scan(&email); err != nil {
		log.Printf("[notifications] load cart %s failed: %v", cartID, err)
		return
	}
	if email == nil || *email == "" {
		return
	}
	emailAddr := *email

	type line struct {
		name     string
		qty      int
		price    int
		currency string
	}
	rows, err := tx.Query(ctx, `
		SELECT p.name, ci.quantity, v.price_cents, p.currency
		FROM cart_items ci
		JOIN products p ON p.id = ci.product_id
		JOIN product_variants v ON v.id = ci.variant_id
		WHERE ci.cart_id = $1 AND ci.tenant_id = $2
		ORDER BY p.name`, cartID, tenantID)
	if err != nil {
		log.Printf("[notifications] load cart items %s failed: %v", cartID, err)
		return
	}
	var lines []line
	total := 0
	currency := "usd"
	for rows.Next() {
		var l line
		if err := rows.Scan(&l.name, &l.qty, &l.price, &l.currency); err != nil {
			rows.Close()
			log.Printf("[notifications] scan cart item failed: %v", err)
			return
		}
		lines = append(lines, l)
		total += l.qty * l.price
		currency = l.currency
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("[notifications] cart items rows failed: %v", err)
		return
	}
	if len(lines) == 0 {
		return
	}

	var storeName, storeSubdomain string
	_ = tx.QueryRow(ctx, `SELECT name, subdomain FROM tenants WHERE id = $1`, tenantID).Scan(&storeName, &storeSubdomain)
	if storeName == "" {
		storeName = "Shopkeet"
	}

	from := fmt.Sprintf("%s via Shopkeet <no-reply@%s>", storeName, s.storeHost(storeSubdomain))
	replyTo := fmt.Sprintf("support@%s", s.storeHost(storeSubdomain))
	cartURL := fmt.Sprintf("https://%s/cart", s.storeHost(storeSubdomain))

	var itemsHTML string
	for _, l := range lines {
		itemsHTML += fmt.Sprintf("<li>%dx %s — %d %s</li>", l.qty, l.name, l.qty*l.price, l.currency)
	}
	subject := fmt.Sprintf("Your cart is waiting at %s", storeName)
	html := fmt.Sprintf(
		"<p>Hi,</p><p>You left a few things in your cart at <strong>%s</strong>. They're saved — ready to finish when you are.</p><ul>%s</ul><p><strong>Total: %d %s</strong></p><p><a href=\"%s\">Return to your cart</a></p>",
		storeName, itemsHTML, total, currency, cartURL)

	status := "sent"
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.prov.Send(sendCtx, Notification{
		TenantID:  tenantID,
		Type:      "cart_abandoned",
		Recipient: emailAddr,
		From:      from,
		ReplyTo:   replyTo,
		Subject:   subject,
		Body:      html,
	}); err != nil {
		status = "failed"
		log.Printf("[notifications] cart_abandoned send failed for cart %s: %v", cartID, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO notification_log (tenant_id, notification_type, recipient, order_id, status)
		VALUES ($1, 'cart_abandoned', $2, NULL, $3)`, tenantID, email, status); err != nil {
		log.Printf("[notifications] log insert failed: %v", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("[notifications] commit failed: %v", err)
	}
}

// storeHost builds the public host for a tenant storefront: <sub>.<base>. The
// notify code uses it for per-store From/Reply-To addresses (like Shopify's
// <store>.myshopify.com mail identities).
func (s *Service) storeHost(subdomain string) string {
	if subdomain == "" {
		if s.baseDmn != "" {
			return s.baseDmn
		}
		return "shopkeet.com"
	}
	if s.baseDmn != "" {
		return subdomain + "." + s.baseDmn
	}
	return subdomain + ".shopkeet.com"
}
