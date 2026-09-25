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
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/events"
)

// Notification is a single outbound message to a customer.
type Notification struct {
	TenantID  string
	Type      string // order_confirmation, order_shipped, order_delivered, customer_welcome
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
	body := map[string]string{
		"from":    p.from,
		"to":      n.Recipient,
		"subject": n.Subject,
		"html":    n.Body,
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

	var customerName, customerEmail, customerPhone, currency string
	var totalCents int
	err = tx.QueryRow(ctx, `
		SELECT customer_name, customer_email, customer_phone, currency, total_cents
		FROM orders WHERE id = $1`, orderID).Scan(&customerName, &customerEmail, &customerPhone, &currency, &totalCents)
	if err != nil {
		log.Printf("[notifications] load order %s failed: %v", orderID, err)
		return
	}

	recipient := customerEmail
	if recipient == "" {
		recipient = customerPhone
	}
	if recipient == "" {
		log.Printf("[notifications] order %s has no recipient", orderID)
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
