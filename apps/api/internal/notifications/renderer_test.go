package notifications

import (
	"strings"
	"testing"
)

// TestRenderEachTemplateUsesItsOwnContent locks in the per-template set
// loading. Every content file defines {{define "content"}} for base to call,
// and Go associates a define with the set it is parsed into — parsing all
// files into one shared set made them overwrite each other, so every email
// rendered whichever "content" parsed last (and any render whose data lacked
// that template's fields failed outright). Each template must render its OWN
// body inside the shared chrome, and an unknown name must fail loudly.
func TestRenderEachTemplateUsesItsOwnContent(t *testing.T) {
	base := TemplateData{
		StoreName:      "Acme",
		StoreSubdomain: "acme",
		StoreURL:       "https://acme.shopkeet.com",
		ReplyTo:        "support@shopkeet.com",
		Recipient:      "buyer@example.com",
		Subject:        "A subject",
		Year:           2026,
	}

	otp, err := RenderOTPEmail(OTPData{TemplateData: base, Code: "839201", ContactMethod: "email"})
	if err != nil {
		t.Fatalf("render otp: %v", err)
	}
	if !strings.Contains(otp, "839201") {
		t.Fatalf("otp email missing its code: %s", otp)
	}
	if strings.Contains(otp, "Reset your password") {
		t.Fatalf("otp email rendered another template's content")
	}

	reset, err := RenderPasswordReset(PasswordResetData{
		TemplateData: base,
		ResetURL:     "https://auth.shopkeet.com/reset-password?token=tok-abc",
	})
	if err != nil {
		t.Fatalf("render password reset: %v", err)
	}
	if !strings.Contains(reset, "tok-abc") {
		t.Fatalf("reset email missing its link: %s", reset)
	}
	if strings.Contains(reset, "839201") {
		t.Fatalf("reset email rendered the otp content")
	}

	sms, err := RenderOTPSMS(OTPData{TemplateData: base, Code: "111222", ContactMethod: "sms"})
	if err != nil {
		t.Fatalf("render otp sms: %v", err)
	}
	if !strings.Contains(sms, "111222") {
		t.Fatalf("otp sms missing its code: %s", sms)
	}

	orderData := OrderEmailData{
		TemplateData:   base,
		OrderID:        "order-1a2b3c4d",
		CustomerName:   "Sam",
		TotalCents:     2599,
		Currency:       "usd",
		ItemsHTML:      "<li>1x Widget</li>",
		OrderURL:       "https://acme.shopkeet.com/orders/order-1a2b3c4d",
		TotalFormatted: "25.99 USD",
	}
	confirmed, err := RenderOrderConfirmation(orderData)
	if err != nil {
		t.Fatalf("render order confirmation: %v", err)
	}
	if !strings.Contains(confirmed, "order-1a2b3c4d") || !strings.Contains(confirmed, "1x Widget") {
		t.Fatalf("order confirmation missing order data: %s", confirmed)
	}
	delivered, err := RenderOrderDelivered(orderData)
	if err != nil {
		t.Fatalf("render order delivered: %v", err)
	}
	if !strings.Contains(delivered, "order-1a2b3c4d") || !strings.Contains(delivered, "delivered") {
		t.Fatalf("order delivered missing order data: %s", delivered)
	}

	welcome, err := RenderCustomerWelcome(WelcomeData{TemplateData: base, StoreURL: base.StoreURL})
	if err != nil {
		t.Fatalf("render welcome: %v", err)
	}
	if !strings.Contains(welcome, "Welcome to Acme!") {
		t.Fatalf("welcome missing heading: %s", welcome)
	}

	back, err := RenderBackInStock(BackInStockData{TemplateData: base, ProductName: "Widget Pro", StoreURL: base.StoreURL})
	if err != nil {
		t.Fatalf("render back in stock: %v", err)
	}
	if !strings.Contains(back, "Widget Pro") {
		t.Fatalf("back-in-stock missing product: %s", back)
	}

	abandoned, err := RenderCartAbandoned(CartAbandonedData{
		TemplateData:   base,
		ItemsHTML:      "<li>2x Gadget</li>",
		TotalFormatted: "40.00 USD",
		CartURL:        "https://acme.shopkeet.com/cart",
	})
	if err != nil {
		t.Fatalf("render cart abandoned: %v", err)
	}
	if !strings.Contains(abandoned, "2x Gadget") || !strings.Contains(abandoned, "40.00 USD") {
		t.Fatalf("cart abandoned missing cart data: %s", abandoned)
	}

	// Every HTML render wears the shared chrome, and no two templates collide.
	// (otp_sms is plain text by design — checked above, no chrome.)
	renders := map[string]string{}
	for name, body := range map[string]string{
		"otp": otp, "reset": reset, "confirmed": confirmed,
		"delivered": delivered, "welcome": welcome, "back": back, "abandoned": abandoned,
	} {
		if !strings.Contains(body, "<!DOCTYPE html>") || !strings.Contains(body, "A subject") {
			t.Fatalf("%s missing base chrome: %s", name, body)
		}
		for other, otherBody := range renders {
			if otherBody == body {
				t.Fatalf("%s and %s rendered identical bodies", other, name)
			}
		}
		renders[name] = body
	}
	if strings.Contains(sms, "<!DOCTYPE html>") {
		t.Fatalf("otp sms must stay plain text, got HTML")
	}
}

func TestRenderUnknownTemplateFails(t *testing.T) {
	if _, err := RenderEmail("no-such-template", TemplateData{Year: 2026}); err == nil {
		t.Fatalf("unknown template must error, not render something else")
	}
}
