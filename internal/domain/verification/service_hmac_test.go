package verification

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupPeppered(t *testing.T, pepper string) (*miniredis.Miniredis, *Service) {
	t.Helper()
	svc, mr := setupTestService(t)
	return mr, svc.WithPepper(pepper)
}

// C1: OTP hashes must be peppered and recipient-bound, so a Redis dump is
// useless without the server pepper and unusable across recipients.
func TestHashCode_PepperedPerRecipient(t *testing.T) {
	_, svc := setupPeppered(t, "pepper-1")
	other := NewService(svc.rdb).WithPepper("pepper-2")

	h1 := svc.hashCode("ada@example.com", "123456")
	h2 := svc.hashCode("bob@example.com", "123456")
	h3 := other.hashCode("ada@example.com", "123456")

	assert.Len(t, h1, 64)
	assert.NotEqual(t, h1, h2, "email must salt the hash")
	assert.NotEqual(t, h1, h3, "pepper must change the hash")
	assert.NotContains(t, h1, "123456")
}

func TestSendVerifyOTP_RoundTripWithPepper(t *testing.T) {
	_, svc := setupPeppered(t, "pepper-1")

	// Extract the plaintext via the sender hook, then verify it back.
	var sent string
	svc.WithEmailSender(func(_, code string) error { sent = code; return nil }, nil)
	require.NoError(t, svc.SendOTP(context.Background(), "ada@example.com"))
	require.NotEmpty(t, sent)

	ok, err := svc.VerifyOTP(context.Background(), "ada@example.com", sent)
	require.NoError(t, err)
	assert.True(t, ok)

	// Wrong code still fails (fresh code: success above consumed the first).
	// Bypass the 60s resend cooldown — white-box test.
	require.NoError(t, svc.rdb.Del(context.Background(), "otp:resend:ada@example.com").Err())
	require.NoError(t, svc.SendOTP(context.Background(), "ada@example.com"))
	ok, err = svc.VerifyOTP(context.Background(), "ada@example.com", "000000")
	require.NoError(t, err)
	assert.False(t, ok)
}
