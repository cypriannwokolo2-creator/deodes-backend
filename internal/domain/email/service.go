package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/deodes/backend/pkg/logger"
	"github.com/deodes/backend/pkg/metrics"
)

// Config holds the Brevo email sending configuration.
type Config struct {
	APIKey      string
	FromAddress string
	FromName    string
	BaseURL     string
	MaxRetries  int
	// TempSenderNotice, when true, appends a footer explaining the mail
	// arrives from a temporary sender address while the canonical domain
	// is being repaired. Flip via DEODES_BREVO_TEMP_SENDER_NOTICE; the
	// switch-back needs zero code changes (From + this flag only).
	TempSenderNotice bool
}

// Service handles sending transactional emails via Brevo API.
type Service struct {
	config Config
	client *http.Client
}

func NewService(cfg Config) *Service {
	if cfg.FromAddress == "" {
		cfg.FromAddress = "noreply@deodes.com"
	}
	if cfg.FromName == "" {
		cfg.FromName = "Deodes"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.brevo.com/v3/smtp/email"
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	return &Service{
		config: cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// WithHTTPClient sets a custom http.Client (e.g. for testing).
func (s *Service) WithHTTPClient(client *http.Client) *Service {
	if client != nil {
		s.client = client
	}
	return s
}

// SendOTP sends a 6-digit verification code to the user's email.
func (s *Service) SendOTP(ctx context.Context, email, code string) error {
	subject := "Your Deodes verification code"
	body := fmt.Sprintf(`<p>Your Deodes verification code is:</p>
<h2 style="font-size:28px;letter-spacing:6px;text-align:center;padding:16px;background:#f5f5f5;border-radius:8px;font-family:monospace">%s</h2>
<p>This code expires in <strong>5 minutes</strong>. If you did not request this code, please ignore this email.</p>%s`, code, s.senderNotice())
	return s.sendBrevo(ctx, email, subject, body, "otp")
}

// SendBackupCodes sends backup codes to the user's email.
func (s *Service) SendBackupCodes(ctx context.Context, email string, codes []string) error {
	subject := "Your Deodes backup codes"
	body := `<p>Save these backup codes in a secure place. Each code can be used <strong>only once</strong> to access your account if you lose your authenticator device.</p><br>`
	for _, c := range codes {
		body += fmt.Sprintf(`<code style="display:block;font-size:16px;padding:4px 8px;background:#f5f5f5;border-radius:4px;margin:4px 0;font-family:monospace">%s</code>`, c)
	}
	body += `<br><p><strong>Keep these codes safe. They will not be shown again.</strong></p>`
	return s.sendBrevo(ctx, email, subject, withNotice(body, s.senderNotice()), "backup_codes")
}

// SendNotification sends an arbitrary notification (issue #191's email
// delivery channel) as a plain HTML email. Unlike SendOTP/SendBackupCodes/
// SendRecoveryCode, subject and body are caller-supplied rather than
// templated here, since notification content varies by NotificationType.
func (s *Service) SendNotification(ctx context.Context, to, subject, body string) error {
	return s.sendBrevo(ctx, to, subject, body, "notification")
}

// SendRecoveryCode sends a one-time recovery code.
func (s *Service) SendRecoveryCode(ctx context.Context, email, code string) error {
	subject := "Your Deodes account recovery code"
	body := fmt.Sprintf(`<p>Your Deodes recovery code is:</p>
<h2 style="font-size:28px;letter-spacing:6px;text-align:center;padding:16px;background:#f5f5f5;border-radius:8px;font-family:monospace">%s</h2>
<p>This code expires in <strong>15 minutes</strong>. If you did not request this code, please secure your account immediately.</p>%s`, code, s.senderNotice())
	return s.sendBrevo(ctx, email, subject, body, "recovery")
}

// senderNotice renders the temporary-sender footer, or "" when the flag is
// off. It names the actual From address dynamically so the note stays
// correct across the switch-back with zero code changes.
func (s *Service) senderNotice() string {
	if !s.config.TempSenderNotice {
		return ""
	}
	return fmt.Sprintf(`<hr style="border:none;border-top:1px solid #e5e5e5;margin:16px 0">
<p style="font-size:12px;color:#666">Please note: this email arrives from our temporary sender address (%s) while we resolve a mail-domain issue. This is still <strong>Deodes</strong> — never share your code with anyone.</p>`,
		html.EscapeString(s.config.FromAddress))
}

func withNotice(body, notice string) string {
	if notice == "" {
		return body
	}
	return body + notice
}

var htmlTagRe = regexp.MustCompile(`(?s)<[^>]*>`)

// htmlToText renders a plain-text fallback for the HTML body (Brevo
// textContent). HTML-only mail scores worse on spam filters; the fallback
// costs one regex pass and no new dependencies.
func htmlToText(htmlBody string) string {
	text := htmlTagRe.ReplaceAllString(htmlBody, " ")
	text = html.UnescapeString(text)
	text = strings.Join(strings.Fields(text), " ")
	return text
}

func (s *Service) sendBrevo(ctx context.Context, to, subject, htmlBody, kind string) error {
	log := logger.Ctx(ctx)
	if strings.TrimSpace(s.config.APIKey) == "" {
		log.Warn().Str("to", to).Str("subject", subject).Str("kind", kind).Msg("brevo API key is not configured — email sending skipped (dev mode)")
		metrics.EmailsTotal.WithLabelValues(kind, "skipped_dev_mode").Inc()
		return nil
	}

	payload := map[string]any{
		"sender": map[string]string{
			"name":  s.config.FromName,
			"email": s.config.FromAddress,
		},
		"to": []map[string]string{
			{"email": to},
		},
		"subject":     subject,
		"htmlContent": htmlBody,
		"textContent": htmlToText(htmlBody),
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling payload: %w", err)
	}

	maxAttempts := s.config.MaxRetries
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	var lastErr error
	backoff := 25 * time.Millisecond

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", s.config.BaseURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("api-key", s.config.APIKey)
		if reqID, ok := ctx.Value("requestID").(string); ok && reqID != "" {
			req.Header.Set("X-Request-ID", reqID)
		}

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("network error sending via brevo (attempt %d/%d): %w", attempt, maxAttempts, err)
			log.Warn().Err(lastErr).Int("attempt", attempt).Msg("brevo delivery failed, retrying...")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
				continue
			}
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode < 400 {
			messageID := parseBrevoMessageID(respBody)
			log.Info().Str("to", to).Str("subject", subject).Str("kind", kind).Str("brevo_message_id", messageID).Msg("email sent via brevo")
			metrics.EmailsTotal.WithLabelValues(kind, "sent").Inc()
			return nil
		}

		// Non-retryable client errors (except 429 Too Many Requests).
		// 4xx from Brevo almost always means sender/domain rejection
		// (unverified sender) or a bad payload — retrying is pointless, and
		// the message names the fix (verify the sender domain in Brevo).
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			metrics.EmailsTotal.WithLabelValues(kind, "rejected").Inc()
			return fmt.Errorf("brevo API error (status %d): %s — if 4xx, verify the sender domain %q is a verified sender in Brevo",
				resp.StatusCode, string(respBody), s.config.FromAddress)
		}

		lastErr = fmt.Errorf("brevo API server error (status %d, attempt %d/%d): %s", resp.StatusCode, attempt, maxAttempts, string(respBody))
		log.Warn().Err(lastErr).Int("attempt", attempt).Int("status", resp.StatusCode).Msg("brevo server error, retrying...")

		select {
		case <-ctx.Done():
			metrics.EmailsTotal.WithLabelValues(kind, "cancelled").Inc()
			return ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}

	metrics.EmailsTotal.WithLabelValues(kind, "failed").Inc()
	return fmt.Errorf("all %d attempts to send email via brevo failed: %w", maxAttempts, lastErr)
}

// parseBrevoMessageID extracts messageId from a Brevo send response
// ({"messageId":"<...>"}). Empty when the body is not the success shape —
// callers log it opportunistically, never fail on it.
func parseBrevoMessageID(body []byte) string {
	var out struct {
		MessageID string `json:"messageId"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return ""
	}
	return out.MessageID
}

// brevoSender is one entry of Brevo GET /v3/senders.
type brevoSender struct {
	Email  string `json:"email"`
	Active bool   `json:"active"`
}

// VerifySender checks the configured From address against Brevo's verified
// sender list. It is warn-only by design: Brevo downtime must never block
// API boot — main.go logs an ERROR with the fix and continues.
func (s *Service) VerifySender(ctx context.Context) error {
	if strings.TrimSpace(s.config.APIKey) == "" {
		return fmt.Errorf("brevo API key unset — cannot verify sender (emails are skipped in dev mode)")
	}
	base := strings.TrimRight(s.config.BaseURL, "/")
	endpoint := base
	if !strings.HasSuffix(base, "/senders") {
		// BaseURL points at the send endpoint (.../smtp/email); senders live
		// at the API root. Derive it, but tolerate a custom test server that
		// serves both paths on one URL.
		if idx := strings.Index(base, "/smtp/"); idx != -1 {
			endpoint = base[:idx] + "/senders"
		} else {
			endpoint = base + "/senders"
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building senders request: %w", err)
	}
	req.Header.Set("api-key", s.config.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("listing brevo senders: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("listing brevo senders (status %d): %s", resp.StatusCode, string(body))
	}
	var senders []brevoSender
	if err := json.Unmarshal(body, &senders); err != nil {
		// Some Brevo plans wrap the list; try {"senders":[...]} before failing.
		var wrapped struct {
			Senders []brevoSender `json:"senders"`
		}
		if werr := json.Unmarshal(body, &wrapped); werr != nil || len(wrapped.Senders) == 0 {
			return fmt.Errorf("decoding brevo senders: %w", err)
		}
		senders = wrapped.Senders
	}
	want := strings.ToLower(strings.TrimSpace(s.config.FromAddress))
	for _, sender := range senders {
		if strings.ToLower(strings.TrimSpace(sender.Email)) == want && sender.Active {
			return nil
		}
	}
	return fmt.Errorf("sender %q is not an active verified sender in Brevo — verify the domain/sender in Brevo (Senders & IPs) or set DEODES_BREVO_FROM_EMAIL to a verified address", s.config.FromAddress)
}
