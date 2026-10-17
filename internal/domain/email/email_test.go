package email_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/email"
)

func TestEmailService_DevModeFallbackWhenNoAPIKey(t *testing.T) {
	svc := email.NewService(email.Config{
		APIKey: "",
	})

	err := svc.SendOTP(context.Background(), "user@example.com", "123456")
	assert.NoError(t, err, "empty api key should gracefully fallback in dev mode without error")

	err = svc.SendRecoveryCode(context.Background(), "user@example.com", "12345678")
	assert.NoError(t, err)

	err = svc.SendBackupCodes(context.Background(), "user@example.com", []string{"code1", "code2"})
	assert.NoError(t, err)
}

func TestEmailService_SendOTPSuccess(t *testing.T) {
	var receivedBody map[string]any
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messageId":"<msg123@brevo>"}`))
	}))
	defer server.Close()

	svc := email.NewService(email.Config{
		APIKey:      "test-brevo-key",
		FromAddress: "support@deodes.com",
		FromName:    "Deodes App",
		BaseURL:     server.URL,
	})

	err := svc.SendOTP(context.Background(), "recipient@example.com", "654321")
	require.NoError(t, err)

	assert.Equal(t, "test-brevo-key", receivedHeaders.Get("api-key"))
	assert.Equal(t, "application/json", receivedHeaders.Get("Content-Type"))

	sender := receivedBody["sender"].(map[string]any)
	assert.Equal(t, "Deodes App", sender["name"])
	assert.Equal(t, "support@deodes.com", sender["email"])

	to := receivedBody["to"].([]any)
	assert.Len(t, to, 1)
	assert.Equal(t, "recipient@example.com", to[0].(map[string]any)["email"])

	htmlContent := receivedBody["htmlContent"].(string)
	assert.Contains(t, htmlContent, "654321")
}

func TestEmailService_RetryOnTransientServerError(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt32(&attempts, 1)
		if current < 3 {
			// Fail first two attempts with 503 Service Unavailable
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"temporarily unavailable"}`))
			return
		}
		// Succeed on 3rd attempt
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messageId":"<ok123>"}`))
	}))
	defer server.Close()

	svc := email.NewService(email.Config{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		MaxRetries: 3,
	})

	err := svc.SendRecoveryCode(context.Background(), "user@example.com", "87654321")
	require.NoError(t, err)
	assert.Equal(t, int32(3), atomic.LoadInt32(&attempts))
}

func TestEmailService_PermanentFailureWhenRetriesExhausted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal crash"}`))
	}))
	defer server.Close()

	svc := email.NewService(email.Config{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		MaxRetries: 2,
	})

	err := svc.SendOTP(context.Background(), "user@example.com", "123456")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "attempts to send email via brevo failed")
}

func TestEmailService_TempSenderNoticeFooter(t *testing.T) {
	var receivedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messageId":"<notice123>"}`))
	}))
	defer server.Close()

	newSvc := func(notice bool) *email.Service {
		return email.NewService(email.Config{
			APIKey:           "test-key",
			FromAddress:      "admin@codexhange.com",
			FromName:         "Deodes",
			BaseURL:          server.URL,
			TempSenderNotice: notice,
		})
	}

	// Flag on: footer names the temp sender and reassures it is Deodes.
	require.NoError(t, newSvc(true).SendOTP(context.Background(), "u@example.com", "111222"))
	htmlOn := receivedBody["htmlContent"].(string)
	assert.Contains(t, htmlOn, "111222")
	assert.Contains(t, htmlOn, "temporary sender address")
	assert.Contains(t, htmlOn, "admin@codexhange.com")
	assert.Contains(t, htmlOn, "still")
	assert.Contains(t, htmlOn, "Deodes")
	textOn := receivedBody["textContent"].(string)
	assert.Contains(t, textOn, "111222")
	assert.NotContains(t, textOn, "<h2", "text fallback must not contain HTML tags")

	// Flag off: no footer, code still delivered with text fallback.
	require.NoError(t, newSvc(false).SendOTP(context.Background(), "u@example.com", "333444"))
	htmlOff := receivedBody["htmlContent"].(string)
	assert.Contains(t, htmlOff, "333444")
	assert.NotContains(t, htmlOff, "temporary sender address")
	assert.Contains(t, receivedBody["textContent"].(string), "333444")
}

func TestEmailService_SenderRejectionNamesTheFix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"sender not verified"}`))
	}))
	defer server.Close()

	svc := email.NewService(email.Config{
		APIKey:      "test-key",
		FromAddress: "noreply@deodes.com",
		BaseURL:     server.URL,
	})

	err := svc.SendOTP(context.Background(), "u@example.com", "123456")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verify the sender domain")
}

func TestEmailService_VerifySender(t *testing.T) {
	verifiedPayload := `[{"email":"admin@codexhange.com","active":true},{"email":"other@x.com","active":false}]`

	newServer := func(body string, status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "test-key", r.Header.Get("api-key"))
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}

	t.Run("verified sender passes", func(t *testing.T) {
		srv := newServer(verifiedPayload, http.StatusOK)
		defer srv.Close()
		svc := email.NewService(email.Config{APIKey: "test-key", FromAddress: "admin@codexhange.com", BaseURL: srv.URL})
		assert.NoError(t, svc.VerifySender(context.Background()))
	})

	t.Run("unverified sender fails with the fix", func(t *testing.T) {
		srv := newServer(verifiedPayload, http.StatusOK)
		defer srv.Close()
		svc := email.NewService(email.Config{APIKey: "test-key", FromAddress: "noreply@deodes.com", BaseURL: srv.URL})
		err := svc.VerifySender(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not an active verified sender")
	})

	t.Run("inactive sender fails", func(t *testing.T) {
		srv := newServer(verifiedPayload, http.StatusOK)
		defer srv.Close()
		svc := email.NewService(email.Config{APIKey: "test-key", FromAddress: "other@x.com", BaseURL: srv.URL})
		require.Error(t, svc.VerifySender(context.Background()))
	})

	t.Run("server error surfaces", func(t *testing.T) {
		srv := newServer(`{"message":"boom"}`, http.StatusInternalServerError)
		defer srv.Close()
		svc := email.NewService(email.Config{APIKey: "test-key", FromAddress: "a@b.com", BaseURL: srv.URL})
		require.Error(t, svc.VerifySender(context.Background()))
	})

	t.Run("no API key cannot verify", func(t *testing.T) {
		svc := email.NewService(email.Config{})
		require.Error(t, svc.VerifySender(context.Background()))
	})
}
