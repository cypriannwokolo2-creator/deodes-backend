package handler

import (
	"errors"
	"strings"

	"github.com/deodes/backend/internal/api/middleware"
	"github.com/deodes/backend/internal/domain/user"
	"github.com/deodes/backend/pkg/response"
	"github.com/gin-gonic/gin"
)

type UserHandler struct {
	userService user.Service
}

func NewUserHandler(userService user.Service) *UserHandler {
	return &UserHandler{userService: userService}
}

// @Summary Claim username
// @Description Claims a username/handle for the authenticated user (RESTful: POST /v1/users/username/claim)
// @Tags User
// @Produce json
// @Security BearerAuth
// @Success 200 {object} response.Envelope
// @Router /users/username/claim [post]
func (h *UserHandler) ClaimName(c *gin.Context) {
	_, err := h.userService.ClaimName(c.Request.Context())
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"success": true, "message": "username claimed successfully"})
}

// @Summary Update profile
// @Description Updates profile fields for the authenticated user
// @Tags User
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body user.UpdateProfileInput true "Profile updates"
// @Success 200 {object} response.Envelope{data=object{user=object}}
// @Failure 400 {object} response.Envelope
// @Failure 401 {object} response.Envelope
// @Failure 404 {object} response.Envelope
// @Router /users/me [patch]
func (h *UserHandler) UpdateProfile(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		response.Unauthorized(c, "unauthorized")
		return
	}

	var req user.UpdateProfileInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if req.Email != nil {
		response.BadRequest(c, "use the verified email-change flow")
		return
	}

	u, err := h.userService.UpdateProfile(c.Request.Context(), userID, req)
	if err != nil {
		if errors.Is(err, user.ErrUserNotFound) {
			response.NotFound(c, "user not found")
			return
		}
		response.BadRequest(c, err.Error())
		return
	}

	response.OK(c, gin.H{"user": u})
}

// @Summary Delete own account
// @Description Soft-deletes the authenticated user's account.
// @Tags User
// @Produce json
// @Security BearerAuth
// @Success 200 {object} response.Envelope{data=object{success=bool}}
// @Router /users/me [delete]
func (h *UserHandler) DeleteMe(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		response.Unauthorized(c, "unauthorized")
		return
	}
	if err := h.userService.Delete(c.Request.Context(), userID); err != nil {
		if errors.Is(err, user.ErrUserNotFound) {
			response.NotFound(c, "user not found")
			return
		}
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"success": true})
}

// @Summary Look up a Deodes user by email
// @Description Resolves an email to the recipient preview used by internal
// transfers (display name only — no wallet address is exposed). Requires auth.
// @Tags User
// @Produce json
// @Security BearerAuth
// @Param email query string true "Recipient email"
// @Success 200 {object} response.Envelope{data=object{user=object}}
// @Failure 404 {object} response.Envelope
// @Router /users/lookup [get]
func (h *UserHandler) LookupByEmail(c *gin.Context) {
	email := strings.ToLower(strings.TrimSpace(c.Query("email")))
	if email == "" || !strings.Contains(email, "@") {
		response.BadRequest(c, "a valid email query parameter is required")
		return
	}

	u, err := h.userService.GetByEmail(c.Request.Context(), email)
	if err != nil {
		response.NotFound(c, "no Deodes user found for that email")
		return
	}

	name := ""
	if u.DisplayName != nil {
		name = *u.DisplayName
	}
	response.OK(c, gin.H{"user": gin.H{
		"displayName":   name,
		"emailVerified": u.EmailVerified,
	}})
}
