package handler

import (
	"database/sql"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/deodes/backend/internal/domain/auth"
	"github.com/deodes/backend/internal/domain/user"
	"github.com/deodes/backend/internal/domain/verification"
	"github.com/deodes/backend/pkg/response"
)

// PasswordResetHandler serves the forgot-password flow (Phase A recovery
// story — no seed phrases anywhere). It reuses the verification service's
// hashed, rate-limited OTP codes: forgot mints one, reset redeems it for a
// new password. Both endpoints always behave identically for unknown emails
// (200 + no code stored... note SendOTP stores per-email; unknown emails
// would create orphans — so lookup happens first and misses return success
// without sending).
type PasswordResetHandler struct {
	authSvc         auth.Service
	userSvc         user.Service
	verificationSvc *verification.Service
}

// NewPasswordResetHandler builds the forgot/reset handler.
func NewPasswordResetHandler(authSvc auth.Service, userSvc user.Service, verificationSvc *verification.Service) *PasswordResetHandler {
	return &PasswordResetHandler{authSvc: authSvc, userSvc: userSvc, verificationSvc: verificationSvc}
}

// @Summary Request password reset code
// @Description Sends a 6-digit reset code to the account email. Always 200 to avoid account enumeration.
// @Tags Authentication
// @Accept json
// @Produce json
// @Param body body object{email=string} true "Account email"
// @Success 200 {object} response.Envelope
// @Router /auth/password/forgot [post]
func (h *PasswordResetHandler) ForgotPassword(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	u, err := h.userSvc.GetByEmail(c.Request.Context(), req.Email)
	if err != nil || u == nil {
		// Anti-enumeration: identical success without sending anything.
		response.OK(c, gin.H{"success": true, "message": "if an account exists for this email, a reset code is on its way"})
		return
	}
	if err := h.verificationSvc.SendOTP(c.Request.Context(), req.Email); err != nil {
		// Rate-limited sends still read as success (no enumeration oracle);
		// mailer outages are logged by request ID, never with the address (M3).
		log.Warn().Err(err).Msg("password reset code send failed")
	}
	response.OK(c, gin.H{"success": true, "message": "if an account exists for this email, a reset code is on its way"})
}

// @Summary Reset password with code
// @Description Redeems a reset code for a new password. Revokes other sessions like password change.
// @Tags Authentication
// @Accept json
// @Produce json
// @Param body body object{email=string,code=string,newPassword=string} true "Reset payload"
// @Success 200 {object} response.Envelope
// @Failure 400 {object} response.Envelope
// @Failure 422 {object} response.Envelope
// @Router /auth/password/reset [post]
func (h *PasswordResetHandler) ResetPassword(c *gin.Context) {
	var req struct {
		Email       string `json:"email" binding:"required,email"`
		Code        string `json:"code" binding:"required,len=6"`
		NewPassword string `json:"newPassword" binding:"required,min=8"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	ok, err := h.verificationSvc.VerifyOTP(c.Request.Context(), req.Email, req.Code)
	if err != nil || !ok {
		response.ErrorWithCode(c, 422, "INVALID_CODE", "reset code is invalid or expired")
		return
	}

	u, err := h.userSvc.GetByEmail(c.Request.Context(), req.Email)
	if err != nil || u == nil {
		response.ErrorWithCode(c, 422, "INVALID_CODE", "reset code is invalid or expired")
		return
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		response.InternalError(c, "failed to hash new password")
		return
	}

	u.PasswordHash = sql.NullString{String: newHash, Valid: true}
	if err := h.userSvc.Update(c.Request.Context(), u); err != nil {
		response.InternalError(c, "failed to update password")
		return
	}

	if err := h.authSvc.RevokeAllSessions(c.Request.Context(), u.ID.String(), ""); err != nil {
		log.Warn().Err(err).Str("userID", u.ID.String()).Msg("password reset: session revocation failed")
	}

	response.OK(c, gin.H{"success": true, "message": "password reset, please sign in"})
}
