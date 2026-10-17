package user

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/deodes/backend/pkg/apperrors"
)

// Username rules (T002): derived from the email local-part, unique
// case-insensitively (CITEXT column). On collision (same local-part, different
// domain) a `-` + 4-char base62 suffix is appended, retried a few times.
var (
	usernameSanitizeRe = regexp.MustCompile(`[^a-z0-9_]+`)
	usernameValidRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,28}[a-z0-9]$|^[a-z0-9]{2}$`)
)

const (
	maxUsernameAttempts = 8
	usernameSuffixLen   = 4
)

const base62Alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// DeriveUsername sanitizes an email local-part into a username base:
// lowercase, `+tag` stripped, non [a-z0-9_] dropped. Falls back to "user"
// when nothing usable remains.
func DeriveUsername(email string) string {
	local := strings.ToLower(strings.TrimSpace(email))
	if i := strings.Index(local, "@"); i >= 0 {
		local = local[:i]
	}
	if i := strings.Index(local, "+"); i >= 0 {
		local = local[:i]
	}
	base := usernameSanitizeRe.ReplaceAllString(local, "")
	if len(base) < 2 {
		base = "user"
	}
	if len(base) > 24 {
		base = base[:24]
	}
	return base
}

// ValidateUsername reports whether a user-chosen username is acceptable.
func ValidateUsername(username string) error {
	if !usernameValidRe.MatchString(username) {
		return fmt.Errorf("username must be 2-30 chars of lowercase letters, digits, _ or -: %w", apperrors.ErrInvalidInput)
	}
	return nil
}

func randomSuffix(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = base62Alphabet[int(buf[i])%len(base62Alphabet)]
	}
	return string(out), nil
}

// EnsureUsername returns a free username for email: the derived base when
// untaken, otherwise base + random suffix, retried. The CITEXT unique
// constraint is the final backstop against races.
func EnsureUsername(ctx context.Context, repo Repository, email string) (string, error) {
	base := DeriveUsername(email)
	if _, err := repo.FindByUsername(ctx, base); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			return base, nil
		}
		return "", fmt.Errorf("checking username availability: %w", err)
	}
	for attempt := 0; attempt < maxUsernameAttempts; attempt++ {
		suffix, err := randomSuffix(usernameSuffixLen)
		if err != nil {
			return "", fmt.Errorf("generating username suffix: %w", err)
		}
		candidate := fmt.Sprintf("%s-%s", base, suffix)
		if _, err := repo.FindByUsername(ctx, candidate); err != nil {
			if errors.Is(err, apperrors.ErrNotFound) {
				return candidate, nil
			}
			return "", fmt.Errorf("checking username availability: %w", err)
		}
	}
	return "", fmt.Errorf("no free username for %q after %d attempts: %w", base, maxUsernameAttempts, apperrors.ErrInvalidInput)
}
