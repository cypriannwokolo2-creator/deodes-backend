package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/deodes/backend/internal/domain/auth"
	"github.com/deodes/backend/internal/domain/auth/google"
	"github.com/deodes/backend/internal/domain/user"
	"github.com/deodes/backend/internal/domain/wallet"
	"github.com/deodes/backend/pkg/apperrors"
	"github.com/deodes/backend/pkg/response"
)

// GoogleAuthHandler exchanges a Google ID token (verified against Google's
// public keys) for a backend RS256 session. This is what makes Google login
// work across hosts: the frontend must never mint its own session JWT, since
// the backend only honors tokens it signed itself.
type GoogleAuthHandler struct {
	authService auth.Service
	userService user.Service
	userRepo    user.Repository
	walletSvc   wallet.Service
	verifier    *google.Verifier
}

// NewGoogleAuthHandler builds the handler. clientID is the OAuth client ID
// used as the ID-token audience check; empty disables the endpoint.
func NewGoogleAuthHandler(authSvc auth.Service, userSvc user.Service,
	userRepo user.Repository, walletSvc wallet.Service, clientID string) *GoogleAuthHandler {
	return &GoogleAuthHandler{
		authService: authSvc,
		userService: userSvc,
		userRepo:    userRepo,
		walletSvc:   walletSvc,
		verifier:    google.NewVerifier(clientID),
	}
}

// WithVerifier swaps the Google verifier. Intended for tests (point at a
// local JWKS); production always uses Google's endpoint.
func (h *GoogleAuthHandler) WithVerifier(v *google.Verifier) *GoogleAuthHandler {
	h.verifier = v
	return h
}

// @Summary Google login
// @Description Verifies a Google ID token and creates a session (new users are provisioned).
// @Tags Authentication
// @Accept json
// @Produce json
// @Param body body object true "Google payload" { "idToken": "string" }
// @Success 200 {object} response.Envelope{data=object{token=string,refreshToken=string}}
// @Failure 400 {object} response.Envelope
// @Failure 401 {object} response.Envelope
// @Failure 503 {object} response.Envelope
// @Router /auth/google [post]
func (h *GoogleAuthHandler) Google(c *gin.Context) {
	if h == nil || h.verifier == nil {
		response.ErrorWithCode(c, 503, "google_disabled", "google sign-in is not configured")
		return
	}
	var req struct {
		IDToken string `json:"idToken" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	ctx := c.Request.Context()

	profile, err := h.verifier.VerifyIDToken(ctx, req.IDToken)
	if err != nil {
		response.Unauthorized(c, "google sign-in failed")
		return
	}

	// Deterministic pseudo-wallet keyed by Google subject (stable across
	// email changes, unlike the EMAIL: scheme used for password accounts).
	walletAddr := "GOOGLE:" + profile.Sub
	u, err := h.userService.GetByWallet(ctx, walletAddr)
	if err != nil {
		u, err = h.provision(ctx, profile, walletAddr)
		if err != nil {
			response.InternalError(c, "failed to provision google account")
			return
		}
	}

	pair, err := h.authService.CreateSession(ctx, u.ID, string(u.Role), sessionTTLFromUser(u), deviceInfoFromContext(c))
	if err != nil {
		response.InternalError(c, "failed to create session")
		return
	}

	response.OK(c, gin.H{
		"token": pair.AccessToken, "refreshToken": pair.RefreshToken, "csrfToken": pair.CSRFToken, "user": u,
	})
}

func (h *GoogleAuthHandler) provision(ctx context.Context, profile *google.Profile, walletAddr string) (*user.User, error) {
	hashedEmail := user.HashEmail(strings.ToLower(strings.TrimSpace(profile.Email)))
	now := time.Now().UTC()
	u := &user.User{
		ID:                uuid.New(),
		WalletAddress:     walletAddr,
		PreferredLanguage: "en",
		Role:              user.RoleUser,
		Email:             &hashedEmail,
		EmailVerified:     true,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if strings.TrimSpace(profile.Name) != "" {
		name := strings.TrimSpace(profile.Name)
		u.DisplayName = &name
	}
	// Same email-derived username rule as email registration.
	if username, err := user.EnsureUsername(ctx, h.userRepo, profile.Email); err != nil {
		return nil, fmt.Errorf("assigning username: %w", err)
	} else {
		u.Username = &username
	}
	if err := h.userRepo.Create(ctx, u); err != nil {
		if err == apperrors.ErrConflict {
			existing, findErr := h.userRepo.FindByWalletAddress(ctx, walletAddr)
			if findErr != nil {
				return nil, findErr
			}
			return existing, nil
		}
		return nil, err
	}

	// Best-effort hosted wallet, mirroring email registration.
	if h.walletSvc != nil {
		if seed, seedErr := h.walletSvc.DeriveWalletSeed(ctx, profile.Email); seedErr == nil {
			_, _ = h.walletSvc.CreateWallet(ctx, u.ID.String(), []byte(seed))
		}
	}
	return u, nil
}
