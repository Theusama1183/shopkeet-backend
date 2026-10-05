package notifications

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// AuthMailer sends the account-level emails auth triggers — the signup/login
// OTP and the password-reset link. It satisfies auth's Mailer interface
// structurally: auth must not import notifications (notifications' tests
// already import auth, which would close the loop), so the adapter lives here
// and main wires it in.
//
// These emails are platform emails, not tenant emails: there is no store
// involved, so the chrome carries "Shopkeet" and links to the auth origin
// (where login/OTP/reset pages live) rather than a tenant storefront.
type AuthMailer struct {
	prov       Provider
	authOrigin string // e.g. https://auth.shopkeet.com
	replyTo    string // e.g. support@shopkeet.com
}

// NewAuthMailer builds the adapter. authOrigin is where the auth pages are
// served (the reset link is built from it); replyTo may be empty.
func NewAuthMailer(prov Provider, authOrigin, replyTo string) *AuthMailer {
	return &AuthMailer{prov: prov, authOrigin: authOrigin, replyTo: replyTo}
}

// SendOTP mails a 6-digit verification code to the account's address.
func (m *AuthMailer) SendOTP(ctx context.Context, email, code string) error {
	const subject = "Your Shopkeet verification code"
	html, err := RenderOTPEmail(OTPData{
		TemplateData:  m.commonData(email, subject),
		Code:          code,
		ContactMethod: "email",
	})
	if err != nil {
		return fmt.Errorf("render otp email: %w", err)
	}
	return m.prov.Send(ctx, Notification{
		Type:      "otp_verification",
		Recipient: email,
		ReplyTo:   m.replyTo,
		Subject:   subject,
		Body:      html,
	})
}

// SendPasswordReset mails the single-use reset link. The raw token goes only
// into the URL — the database keeps its hash.
func (m *AuthMailer) SendPasswordReset(ctx context.Context, email, token string) error {
	const subject = "Reset your Shopkeet password"
	resetURL := m.authOrigin + "/reset-password?token=" + url.QueryEscape(token)
	html, err := RenderPasswordReset(PasswordResetData{
		TemplateData: m.commonData(email, subject),
		ResetURL:     resetURL,
	})
	if err != nil {
		return fmt.Errorf("render password reset email: %w", err)
	}
	return m.prov.Send(ctx, Notification{
		Type:      "password_reset",
		Recipient: email,
		ReplyTo:   m.replyTo,
		Subject:   subject,
		Body:      html,
	})
}

// commonData is the platform-email chrome: the header links to the auth origin
// (login page) because these emails belong to the account, not to a store.
func (m *AuthMailer) commonData(recipient, subject string) TemplateData {
	return TemplateData{
		StoreName: "Shopkeet",
		StoreURL:  m.authOrigin,
		ReplyTo:   m.replyTo,
		Recipient: recipient,
		Subject:   subject,
		Year:      time.Now().Year(),
	}
}
