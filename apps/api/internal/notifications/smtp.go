package notifications

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"sync/atomic"
	"time"
)

// SMTPConfig configures a standard SMTP / STARTTLS / SMTPS client.
type SMTPConfig struct {
	Host       string
	Port       string // default :587 -> starttls, :465 -> implicit TLS
	Username   string
	Password   string
	From       string // from address used when Notification.From is empty
	TLSMode    string // "auto" (default), "starttls", "smtps", "none"
	SkipVerify bool   // allow self-signed certs (dev mailpit behind TLS)
}

// SMTPProvider sends via any SMTP server (Mailpit, Postfix, SES, SendGrid,
// Resend SMTP, ...). Uses net/smtp only — no external dependency.
type SMTPProvider struct {
	cfg SMTPConfig
}

func NewSMTPProvider(cfg SMTPConfig) *SMTPProvider {
	if cfg.Port == "" {
		cfg.Port = "587"
	}
	if cfg.TLSMode == "" {
		cfg.TLSMode = "auto"
	}
	return &SMTPProvider{cfg: cfg}
}

func (p *SMTPProvider) Name() string { return "smtp" }

func (p *SMTPProvider) Send(ctx context.Context, n Notification) error {
	if n.Recipient == "" {
		return fmt.Errorf("empty recipient")
	}
	from := n.From
	if from == "" {
		from = p.cfg.From
	}
	if from == "" {
		return fmt.Errorf("empty from address")
	}
	fromAddr := extractAddr(from)
	if fromAddr == "" {
		return fmt.Errorf("invalid from address %q", from)
	}

	msg := buildMIME(from, n.Recipient, n.Subject, n.Body, n.ReplyTo)

	addr := net.JoinHostPort(p.cfg.Host, p.cfg.Port)
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	defer conn.Close()

	if ctxTimeout, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(ctxTimeout)
	} else {
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	}

	client, err := smtp.NewClient(conn, p.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp hello: %w", err)
	}
	defer client.Close()

	if err := p.setupEncryption(client); err != nil {
		return err
	}

	if p.cfg.Username != "" {
		auth := smtp.PlainAuth("", p.cfg.Username, p.cfg.Password, p.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(fromAddr); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := client.Rcpt(n.Recipient); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data close: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

func (p *SMTPProvider) setupEncryption(client *smtp.Client) error {
	mode := p.cfg.TLSMode
	if mode == "auto" {
		if p.cfg.Port == "465" {
			mode = "smtps"
		} else if p.cfg.Port == "25" || p.cfg.Port == "587" {
			mode = "starttls"
		} else {
			mode = "starttls"
		}
	}

	tlsCfg := &tls.Config{
		ServerName:         p.cfg.Host,
		InsecureSkipVerify: p.cfg.SkipVerify,
	}

	switch mode {
	case "smtps":
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp tls: %w", err)
		}
	case "starttls":
		ok, _ := client.Extension("STARTTLS")
		if ok {
			if err := client.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		}
	case "none":
		// plaintext — mailpit local / dev
	default:
		return fmt.Errorf("unknown smtp tls mode %q", mode)
	}
	return nil
}

// buildMIME writes an RFC 5322 message with proper MIME + address headers.
func buildMIME(from, to, subject, htmlBody, replyTo string) string {
	var b strings.Builder
	b.WriteString("From: " + encodeAddr(from) + "\r\n")
	b.WriteString("To: " + encodeAddr(to) + "\r\n")
	if replyTo != "" {
		b.WriteString("Reply-To: " + encodeAddr(replyTo) + "\r\n")
	}
	b.WriteString("Subject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: <" + mkMessageID() + ">\r\n")
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return b.String()
}

func encodeAddr(addr string) string {
	if idx := strings.Index(addr, "@"); idx > 0 {
		// Foo Bar <foo@bar.com> is kept as-is; bare foo@bar.com stays bare.
		if strings.Contains(addr, "<") && strings.Contains(addr, ">") {
			return addr
		}
	}
	return addr
}

func extractAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if i := strings.LastIndex(addr, "<"); i >= 0 {
		if j := strings.Index(addr[i:], ">"); j > 0 {
			return addr[i+1 : i+j]
		}
	}
	if !strings.Contains(addr, " ") {
		return addr
	}
	return ""
}

var msgIDCounter uint64

func mkMessageID() string {
	n := atomic.AddUint64(&msgIDCounter, 1)
	return fmt.Sprintf("%d.%d@localhost", time.Now().UnixNano(), n)
}
