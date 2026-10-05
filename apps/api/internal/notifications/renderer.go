package notifications

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

var (
	// templateSets holds one independent template SET per email template: base
	// + that template's content. Every content file defines {{define "content"}}
	// for base to call, and Go associates a define with the set it is parsed
	// into — so parsing all files into one shared set (the natural first
	// attempt) makes them overwrite each other and every email would render
	// whichever "content" was parsed last. One set per template is what keeps
	// the OTP email from wearing the password-reset body.
	templateSets  map[string]*template.Template
	templatesOnce sync.Once
	initErr       error
)

// TemplateData holds common data for all email templates
type TemplateData struct {
	// Store info
	StoreName      string
	StoreSubdomain string
	StoreURL       string
	ReplyTo        string

	// Recipient
	Recipient string

	// Common
	Subject string
	Year    int

	// Email-specific
	ItemsHTML string
}

// Template function map
var funcMap = template.FuncMap{
	"formatCurrency": formatCurrency,
	"lower":          strings.ToLower,
}

// LoadTemplates loads all embedded templates, one set per email template.
func LoadTemplates() error {
	templatesOnce.Do(func() {
		baseBytes, err := templateFS.ReadFile("templates/base.gohtml")
		if err != nil {
			initErr = err
			return
		}
		files, err := templateFS.ReadDir("templates")
		if err != nil {
			initErr = err
			return
		}

		templateSets = make(map[string]*template.Template, len(files))
		for _, f := range files {
			if filepath.Ext(f.Name()) != ".gohtml" {
				continue
			}
			name := strings.TrimSuffix(f.Name(), ".gohtml")
			if name == "base" {
				continue
			}
			content, err := templateFS.ReadFile("templates/" + f.Name())
			if err != nil {
				initErr = err
				return
			}
			set := template.New("").Funcs(funcMap)
			if _, err := set.New("base").Parse(string(baseBytes)); err != nil {
				initErr = err
				return
			}
			if _, err := set.New(name).Parse(string(content)); err != nil {
				initErr = err
				return
			}
			templateSets[name] = set
		}
	})
	return initErr
}

// RenderEmail renders the named email template (base chrome + its content) to
// an HTML string. data is any struct exposing the fields the template reads —
// the embedded TemplateData plus the template's own extras (Code, ResetURL, …).
func RenderEmail(templateName string, data any) (string, error) {
	return render(templateName, "base", data)
}

// RenderTemplate renders a template as itself, with no base chrome — for
// plain-text bodies like the SMS code, which are not HTML documents.
func RenderTemplate(templateName string, data any) (string, error) {
	return render(templateName, templateName, data)
}

// render resolves templateName in the per-template set and executes entry
// ("base" for emails, the template's own name for plain text).
func render(templateName, entry string, data any) (string, error) {
	if err := LoadTemplates(); err != nil {
		return "", err
	}
	set := templateSets[templateName]
	if set == nil {
		err := fmt.Errorf("unknown email template %q", templateName)
		log.Printf("[notifications] template %s render failed: %v", templateName, err)
		return "", err
	}

	var buf bytes.Buffer
	err := set.ExecuteTemplate(&buf, entry, data)
	if err != nil {
		log.Printf("[notifications] template %s render failed: %v", templateName, err)
		return "", err
	}
	return buf.String(), nil
}

// RenderOTPEmail renders the OTP verification email
func RenderOTPEmail(data OTPData) (string, error) {
	return RenderEmail("otp_email", data)
}

// RenderOTPSMS renders the OTP SMS message (plain text — no HTML chrome)
func RenderOTPSMS(data OTPData) (string, error) {
	return RenderTemplate("otp_sms", data)
}

// RenderPasswordReset renders the password reset email
func RenderPasswordReset(data PasswordResetData) (string, error) {
	return RenderEmail("password_reset", data)
}

// RenderOrderConfirmation renders the order confirmation email
func RenderOrderConfirmation(data OrderEmailData) (string, error) {
	return RenderEmail("order_confirmation", data)
}

// RenderOrderDelivered renders the order delivered email
func RenderOrderDelivered(data OrderEmailData) (string, error) {
	return RenderEmail("order_delivered", data)
}

// RenderCustomerWelcome renders the customer welcome email
func RenderCustomerWelcome(data WelcomeData) (string, error) {
	return RenderEmail("customer_welcome", data)
}

// RenderBackInStock renders the back in stock email
func RenderBackInStock(data BackInStockData) (string, error) {
	return RenderEmail("back_in_stock", data)
}

// RenderCartAbandoned renders the cart abandoned email
func RenderCartAbandoned(data CartAbandonedData) (string, error) {
	return RenderEmail("cart_abandoned", data)
}

// --- Template-specific data structs ---

type OTPData struct {
	TemplateData
	Code          string
	ContactMethod string // "email" or "sms"
}

type PasswordResetData struct {
	TemplateData
	ResetURL string
}

type OrderEmailData struct {
	TemplateData
	OrderID        string
	CustomerName   string
	TotalCents     int
	Currency       string
	ItemsHTML      string
	OrderURL       string
	TotalFormatted string
}

type WelcomeData struct {
	TemplateData
	StoreURL string
}

type BackInStockData struct {
	TemplateData
	ProductName string
	StoreURL    string
}

type CartAbandonedData struct {
	TemplateData
	ItemsHTML      string
	TotalFormatted string
	CartURL        string
}

// formatCurrency formats cents to currency string (e.g., 1999 -> "19.99 USD")
func formatCurrency(cents int, currency string) string {
	// Simple formatting - real implementation would use a proper currency library
	dollars := float64(cents) / 100
	return fmt.Sprintf("%.2f %s", dollars, strings.ToUpper(currency))
}
