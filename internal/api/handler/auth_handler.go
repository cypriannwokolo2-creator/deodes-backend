package handler

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/deodes/backend/internal/domain/auth"
	"github.com/deodes/backend/internal/domain/email"
	"github.com/deodes/backend/internal/domain/totp"
	"github.com/deodes/backend/internal/domain/user"
	"github.com/deodes/backend/internal/domain/verification"
	"github.com/deodes/backend/internal/domain/wallet"
)

// AuthHandler aggregates the focused auth sub-handlers while preserving the
// public method surface used by the router and existing tests. The wallet
// authentication flows (nonce/verify) live in WalletAuthHandler, session
// management (refresh/me/logout/revoke) in SessionHandler, the email
// registration flow in RegistrationHandler, and authenticator enrollment
// (enroll/enable/verify/disable/recovery codes) in TOTPEnrollmentHandler.
type AuthHandler struct {
	*WalletAuthHandler
	*SessionHandler
	*RegistrationHandler
	*TOTPEnrollmentHandler
	*EmailChangeHandler
	*GoogleAuthHandler
	*PasswordResetHandler
}

// NewAuthHandler builds the auth handler aggregate. The signature is kept for
// backward compatibility; each focused sub-handler consumes the dependencies
// it actually needs.
func NewAuthHandler(authSvc auth.Service, userSvc user.Service, walletSvc wallet.Service,
	totpSvc *totp.Service, verificationSvc *verification.Service, emailSvc *email.Service,
	redisClient *redis.Client, userRepo user.Repository) *AuthHandler {
	if emailSvc != nil {
		// Bounded delivery context: the OTP store path already uses the
		// request context; only the Brevo HTTP call rode on
		// context.Background() (no timeout/trace). 15s cap, then the
		// verification error surfaces instead of hanging the request.
		verificationSvc.WithEmailSender(func(address, code string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return emailSvc.SendOTP(ctx, address, code)
		}, nil)
	}
	return &AuthHandler{
		WalletAuthHandler:     NewWalletAuthHandler(authSvc, userSvc),
		SessionHandler:        NewSessionHandler(authSvc, userSvc, redisClient),
		RegistrationHandler:   NewRegistrationHandler(authSvc, userRepo, verificationSvc, walletSvc),
		TOTPEnrollmentHandler: NewTOTPEnrollmentHandler(userSvc, userRepo, totpSvc),
		EmailChangeHandler:    NewEmailChangeHandler(userSvc, userRepo, verificationSvc, redisClient),
		PasswordResetHandler:  NewPasswordResetHandler(authSvc, userSvc, verificationSvc),
	}
}

// WithGoogle attaches Google sign-in. clientID empty keeps the endpoint
// fail-closed (503). Called from main only, so existing callers are untouched.
func (h *AuthHandler) WithGoogle(authSvc auth.Service, userSvc user.Service, userRepo user.Repository, walletSvc wallet.Service, clientID string) *AuthHandler {
	h.GoogleAuthHandler = NewGoogleAuthHandler(authSvc, userSvc, userRepo, walletSvc, clientID)
	return h
}

// sessionTTLFromUser returns the configured session TTL, falling back to the
// default 240 minutes when the user has not set one.
func sessionTTLFromUser(u *user.User) time.Duration {
	ttl := u.SessionTTLMinutes
	if ttl < 60 {
		ttl = 240
	}
	return time.Duration(ttl) * time.Minute
}

// deviceInfoFromContext builds a stable device fingerprint from the request.
func deviceInfoFromContext(c *gin.Context) string {
	ua := c.GetHeader("User-Agent")
	if ua == "" {
		ua = "unknown"
	}
	ip := c.ClientIP()
	if ip == "" {
		ip = "unknown"
	}
	return fmt.Sprintf("%s|%s", ua, ip)
}
