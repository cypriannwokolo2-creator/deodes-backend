package handler

import (
	"github.com/deodes/backend/internal/api/middleware"
	"github.com/deodes/backend/internal/domain/incentives"
	"github.com/deodes/backend/pkg/response"
	"github.com/gin-gonic/gin"
)

// ReferralHandler exposes the referral-system endpoints.
type ReferralHandler struct {
	service incentives.Service
}

func NewReferralHandler(svc incentives.Service) *ReferralHandler {
	return &ReferralHandler{service: svc}
}

// GenerateCode generates (or returns an existing) referral code for the authenticated user.
// @Summary Generate referral code
// @Description Returns the user's unique referral code, creating one if it doesn't exist yet.
// @Tags Referral
// @Produce json
// @Security BearerAuth
// @Success 201 {object} response.Envelope{data=object{referralCode=string}}
// @Failure 400 {object} response.Envelope
// @Failure 401 {object} response.Envelope
// @Router /referral/code [post]
func (h *ReferralHandler) GenerateCode(c *gin.Context) {
	userID := middleware.GetUserID(c)

	code, err := h.service.GenerateReferralCode(c.Request.Context(), userID)
	if err != nil {
		response.BadRequest(c, "failed to generate referral code: "+err.Error())
		return
	}

	response.Created(c, gin.H{"referralCode": code})
}

// GetStats returns referral statistics (total, completed, pending) for the authenticated user.
// @Summary Get referral stats
// @Description Returns referral count and incentive summary for the authenticated user.
// @Tags Referral
// @Produce json
// @Security BearerAuth
// @Success 200 {object} response.Envelope{data=object}
// @Failure 401 {object} response.Envelope
// @Failure 500 {object} response.Envelope
// @Router /referral/stats [get]
func (h *ReferralHandler) GetStats(c *gin.Context) {
	userID := middleware.GetUserID(c)

	summary, err := h.service.GetUserSummary(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "failed to retrieve referral stats")
		return
	}

	response.OK(c, gin.H{
		"totalReferrals": summary.ReferralCount,
		"totalEarned":    summary.TotalEarned,
		"totalClaimed":   summary.TotalClaimed,
		"pendingAmount":  summary.PendingAmount,
	})
}

// GetHistory returns the full list of referrals made by the authenticated user.
// @Summary Get referral history
// @Description Returns all referrals created by the authenticated user.
// @Tags Referral
// @Produce json
// @Security BearerAuth
// @Success 200 {object} response.Envelope{data=object{referrals=array}}
// @Failure 401 {object} response.Envelope
// @Failure 500 {object} response.Envelope
// @Router /referral/history [get]
func (h *ReferralHandler) GetHistory(c *gin.Context) {
	userID := middleware.GetUserID(c)

	referrals, err := h.service.GetReferrals(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "failed to retrieve referral history")
		return
	}

	if referrals == nil {
		referrals = []incentives.Referral{}
	}

	response.OK(c, gin.H{"referrals": referrals})
}

// @Summary Apply a referral code
// @Description Attributes the caller to the referrer holding the code.
// @Tags Referral
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body object true "Referral code" {"referralCode":"string"}
// @Success 200 {object} response.Envelope{data=object{success=bool}}
// @Failure 400 {object} response.Envelope
// @Router /referral/apply [post]
func (h *ReferralHandler) ApplyCode(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		response.Unauthorized(c, "authentication required")
		return
	}
	var req struct {
		ReferralCode string `json:"referralCode" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.service.ApplyReferralCode(c.Request.Context(), userID, req.ReferralCode); err != nil {
		response.BadRequest(c, "invalid or already-used referral code")
		return
	}
	response.OK(c, gin.H{"success": true})
}
