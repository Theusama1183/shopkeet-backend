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
	templates     *template.Template
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

// LoadTemplates loads all embedded templates
func LoadTemplates() error {
	templatesOnce.Do(func() {
		tmpl := template.New("").Funcs(funcMap)
		files, err := templateFS.ReadDir("templates")
		if err != nil {
			initErr = err
			return
		}
		
		for _, f := range files {
			if filepath.Ext(f.Name()) == ".gohtml" {
				content, err := templateFS.ReadFile("templates/" + f.Name())
				if err != nil {
					initErr = err
					return
				}
				name := strings.TrimSuffix(f.Name(), ".gohtml")
				_, err = tmpl.New(name).Parse(string(content))
				if err != nil {
					initErr = err
					return
				}
			}
		}
		templates = tmpl
	})
	return initErr
}

// RenderEmail renders an email template to HTML string
func RenderEmail(templateName string, data TemplateData) (string, error) {
	if err := LoadTemplates(); err != nil {
		return "", err
	}
	
	var buf bytes.Buffer
	err := templates.ExecuteTemplate(&buf, "base", data)
	if err != nil {
		log.Printf("[notifications] template %s render failed: %v", templateName, err)
		return "", err
	}
	return buf.String(), nil
}

// RenderOTPEmail renders the OTP verification email
func RenderOTPEmail(data OTPData) (string, error) {
	return RenderEmail("otp_email", data.TemplateData)
}

// RenderOTPSMS renders the OTP SMS message
func RenderOTPSMS(data OTPData) (string, error) {
	return RenderEmail("otp_sms", data.TemplateData)
}

// RenderPasswordReset renders the password reset email
func RenderPasswordReset(data PasswordResetData) (string, error) {
	return RenderEmail("password_reset", data.TemplateData)
}

// RenderOrderConfirmation renders the order confirmation email
func RenderOrderConfirmation(data OrderEmailData) (string, error) {
	return RenderEmail("order_confirmation", data.TemplateData)
}

// RenderOrderDelivered renders the order delivered email
func RenderOrderDelivered(data OrderEmailData) (string, error) {
	return RenderEmail("order_delivered", data.TemplateData)
}

// RenderCustomerWelcome renders the customer welcome email
func RenderCustomerWelcome(data WelcomeData) (string, error) {
	return RenderEmail("customer_welcome", data.TemplateData)
}

// RenderBackInStock renders the back in stock email
func RenderBackInStock(data BackInStockData) (string, error) {
	return RenderEmail("back_in_stock", data.TemplateData)
}

// RenderCartAbandoned renders the cart abandoned email
func RenderCartAbandoned(data CartAbandonedData) (string, error) {
	return RenderEmail("cart_abandoned", data.TemplateData)
}

// --- Template-specific data structs ---

type OTPData struct {
	TemplateData
	Code         string
	ContactMethod string // "email" or "sms"
}

type PasswordResetData struct {
	TemplateData
	ResetURL string
}

type OrderEmailData struct {
	TemplateData
	OrderID       string
	CustomerName  string
	TotalCents    int
	Currency      string
	ItemsHTML     string
	OrderURL      string
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