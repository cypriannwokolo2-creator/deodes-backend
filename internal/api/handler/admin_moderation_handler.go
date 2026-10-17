package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/deodes/backend/internal/domain/audit"
	"github.com/deodes/backend/internal/domain/circle"
	"github.com/deodes/backend/internal/domain/community"
	"github.com/deodes/backend/internal/domain/wallet"
	"github.com/deodes/backend/pkg/apperrors"
	"github.com/deodes/backend/pkg/pagination"
	"github.com/deodes/backend/pkg/response"
)

// WithCommunityService attaches the community domain used by admin
// suspend/unsuspend/ban. Kept as an option so existing constructors keep
// working.
func (h *AdminHandler) WithCommunityService(svc community.Service) *AdminHandler {
	h.communityService = svc
	return h
}

// WithWalletRepository attaches the wallet store used by admin fund
// freeze/unfreeze. Kept as an option so existing constructors keep working.
func (h *AdminHandler) WithWalletRepository(repo wallet.Repository) *AdminHandler {
	h.walletRepo = repo
	return h
}

// auditAdmin records an operator action with actor, resource and details. A
// logging failure never fails the request — the action already happened.
func (h *AdminHandler) auditAdmin(c *gin.Context, action, resourceType, resourceID string, details any) {
	if h.auditRepo == nil {
		return
	}
	actorID, _ := uuid.Parse(c.GetString("userID"))
	var raw json.RawMessage
	if details != nil {
		raw, _ = json.Marshal(details)
	}
	entry := &audit.AuditEntry{
		ID:           uuid.New(),
		ActorID:      actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   sql.NullString{String: resourceID, Valid: resourceID != ""},
		Details:      raw,
		IPAddress:    sql.NullString{String: c.ClientIP(), Valid: c.ClientIP() != ""},
		UserAgent:    sql.NullString{String: c.Request.UserAgent(), Valid: c.Request.UserAgent() != ""},
		CreatedAt:    time.Now().UTC(),
	}
	if err := h.auditRepo.Log(c.Request.Context(), entry); err != nil {
		log.Warn().Err(err).Str("action", action).Msg("failed to audit admin action")
	}
}

func parseModerationReason(c *gin.Context) string {
	var body struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&body)
	reason := strings.TrimSpace(body.Reason)
	if len(reason) > 500 {
		reason = reason[:500]
	}
	return reason
}

func circleModError(c *gin.Context, action string, err error) {
	switch {
	case errors.Is(err, apperrors.ErrNotFound) || errors.Is(err, circle.ErrCircleNotFound):
		response.NotFound(c, "circle not found")
	case errors.Is(err, circle.ErrCircleNotActive):
		response.BadRequest(c, err.Error())
	default:
		response.InternalError(c, "failed to "+action+" circle")
	}
}

// @Summary [Admin] Pause a circle
// @Description Halts an active circle: joins, contributions, payouts and keeper debits fail closed until resumed. Admin only.
// @Tags Admin
// @Produce json
// @Security BearerAuth
// @Param id path string true "Circle ID"
// @Param body body object{reason=string} false "Moderation reason (audited)"
// @Success 200 {object} response.Envelope
// @Router /admin/circles/{id}/pause [post]
func (h *AdminHandler) PauseCircle(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		response.BadRequest(c, "invalid circle ID")
		return
	}
	reason := parseModerationReason(c)
	if err := h.circleService.PauseCircle(c.Request.Context(), id); err != nil {
		circleModError(c, "pause", err)
		return
	}
	h.auditAdmin(c, "circle.paused", "circle", id, map[string]string{"reason": reason})
	response.OK(c, gin.H{"id": id, "status": circle.CircleStatusPaused})
}

// @Summary [Admin] Resume a circle
// @Description Returns a paused circle to active. Admin only.
// @Tags Admin
// @Produce json
// @Security BearerAuth
// @Param id path string true "Circle ID"
// @Success 200 {object} response.Envelope
// @Router /admin/circles/{id}/resume [post]
func (h *AdminHandler) ResumeCircle(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		response.BadRequest(c, "invalid circle ID")
		return
	}
	if err := h.circleService.ResumeCircle(c.Request.Context(), id); err != nil {
		circleModError(c, "resume", err)
		return
	}
	h.auditAdmin(c, "circle.resumed", "circle", id, nil)
	response.OK(c, gin.H{"id": id, "status": circle.CircleStatusActive})
}

// @Summary [Admin] Ban a circle
// @Description Terminally shuts down a pending, active or paused circle. Irreversible. Admin only.
// @Tags Admin
// @Produce json
// @Security BearerAuth
// @Param id path string true "Circle ID"
// @Param body body object{reason=string} false "Moderation reason (audited)"
// @Success 200 {object} response.Envelope
// @Router /admin/circles/{id}/ban [post]
func (h *AdminHandler) BanCircle(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		response.BadRequest(c, "invalid circle ID")
		return
	}
	reason := parseModerationReason(c)
	if err := h.circleService.BanCircle(c.Request.Context(), id); err != nil {
		circleModError(c, "ban", err)
		return
	}
	h.auditAdmin(c, "circle.banned", "circle", id, map[string]string{"reason": reason})
	response.OK(c, gin.H{"id": id, "status": circle.CircleStatusBanned})
}

// @Summary [Admin] List communities
// @Description Lists all communities including suspended ones, with pagination and search. Admin only.
// @Tags Admin
// @Produce json
// @Security BearerAuth
// @Param search query string false "Search term"
// @Param page query int false "Page number" default(1)
// @Param limit query int false "Items per page" default(20)
// @Success 200 {object} response.Envelope
// @Router /admin/communities [get]
func (h *AdminHandler) ListCommunities(c *gin.Context) {
	if h.communityService == nil {
		response.InternalError(c, "community moderation is not configured")
		return
	}
	page, limit, _ := pagination.Parse(c)
	filter := community.CommunityFilter{
		Search:           c.Query("search"),
		Page:             page,
		Limit:            limit,
		IncludeSuspended: true,
	}
	communities, total, err := h.communityService.List(c.Request.Context(), filter)
	if err != nil {
		response.InternalError(c, "failed to list communities")
		return
	}
	response.OKWithMeta(c, gin.H{"communities": communities}, response.NewPaginationMeta(page, limit, total))
}

// @Summary [Admin] Suspend a community
// @Description Freezes a community: hidden from public listing, no joins/edits/new circles until unsuspended. Admin only.
// @Tags Admin
// @Produce json
// @Security BearerAuth
// @Param id path string true "Community ID"
// @Param body body object{reason=string} false "Moderation reason (audited)"
// @Success 200 {object} response.Envelope
// @Router /admin/communities/{id}/suspend [post]
func (h *AdminHandler) SuspendCommunity(c *gin.Context) {
	if h.communityService == nil {
		response.InternalError(c, "community moderation is not configured")
		return
	}
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		response.BadRequest(c, "invalid community ID")
		return
	}
	reason := parseModerationReason(c)
	if err := h.communityService.SuspendCommunity(c.Request.Context(), id, reason); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			response.NotFound(c, "community not found")
			return
		}
		response.InternalError(c, "failed to suspend community")
		return
	}
	h.auditAdmin(c, "community.suspended", "community", id, map[string]string{"reason": reason})
	response.OK(c, gin.H{"id": id, "suspended": true})
}

// @Summary [Admin] Unsuspend a community
// @Description Clears a moderation freeze. Admin only.
// @Tags Admin
// @Produce json
// @Security BearerAuth
// @Param id path string true "Community ID"
// @Success 200 {object} response.Envelope
// @Router /admin/communities/{id}/unsuspend [post]
func (h *AdminHandler) UnsuspendCommunity(c *gin.Context) {
	if h.communityService == nil {
		response.InternalError(c, "community moderation is not configured")
		return
	}
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		response.BadRequest(c, "invalid community ID")
		return
	}
	if err := h.communityService.UnsuspendCommunity(c.Request.Context(), id); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			response.NotFound(c, "community not found")
			return
		}
		response.InternalError(c, "failed to unsuspend community")
		return
	}
	h.auditAdmin(c, "community.unsuspended", "community", id, nil)
	response.OK(c, gin.H{"id": id, "suspended": false})
}

// @Summary [Admin] Ban a community
// @Description Permanently removes a community (soft-delete, owner check bypassed). Admin only.
// @Tags Admin
// @Produce json
// @Security BearerAuth
// @Param id path string true "Community ID"
// @Param body body object{reason=string} false "Moderation reason (audited)"
// @Success 200 {object} response.Envelope
// @Router /admin/communities/{id}/ban [post]
func (h *AdminHandler) BanCommunity(c *gin.Context) {
	if h.communityService == nil {
		response.InternalError(c, "community moderation is not configured")
		return
	}
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		response.BadRequest(c, "invalid community ID")
		return
	}
	reason := parseModerationReason(c)
	if err := h.communityService.AdminRemoveCommunity(c.Request.Context(), id); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			response.NotFound(c, "community not found")
			return
		}
		response.InternalError(c, "failed to ban community")
		return
	}
	h.auditAdmin(c, "community.banned", "community", id, map[string]string{"reason": reason})
	response.OK(c, gin.H{"id": id, "banned": true})
}

// @Summary [Admin] Freeze a user's funds
// @Description Freezes the user's hosted wallet: all outbound sends reject until unfrozen. Inbound deposits unaffected. Admin only.
// @Tags Admin
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Param body body object{reason=string} false "Moderation reason (audited)"
// @Success 200 {object} response.Envelope
// @Router /admin/users/{id}/freeze [post]
func (h *AdminHandler) FreezeUserFunds(c *gin.Context) {
	if h.walletRepo == nil {
		response.InternalError(c, "fund freeze is not configured")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "invalid user ID")
		return
	}
	reason := parseModerationReason(c)
	w, err := h.walletRepo.FreezeByUserID(c.Request.Context(), id, true, reason)
	if err != nil {
		response.InternalError(c, "failed to freeze funds")
		return
	}
	if w == nil {
		response.NotFound(c, "user has no hosted wallet")
		return
	}
	h.auditAdmin(c, "user.funds_frozen", "user", id.String(), map[string]string{"reason": reason})
	response.OK(c, gin.H{"id": id.String(), "frozen": true})
}

// @Summary [Admin] Unfreeze a user's funds
// @Description Clears a moderation freeze. Admin only.
// @Tags Admin
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Success 200 {object} response.Envelope
// @Router /admin/users/{id}/unfreeze [post]
func (h *AdminHandler) UnfreezeUserFunds(c *gin.Context) {
	if h.walletRepo == nil {
		response.InternalError(c, "fund freeze is not configured")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "invalid user ID")
		return
	}
	w, err := h.walletRepo.FreezeByUserID(c.Request.Context(), id, false, "")
	if err != nil {
		response.InternalError(c, "failed to unfreeze funds")
		return
	}
	if w == nil {
		response.NotFound(c, "user has no hosted wallet")
		return
	}
	h.auditAdmin(c, "user.funds_unfrozen", "user", id.String(), nil)
	response.OK(c, gin.H{"id": id.String(), "frozen": false})
}

// @Summary [Admin] List frozen wallets
// @Description Lists all frozen hosted wallets, newest freeze first. Admin only.
// @Tags Admin
// @Produce json
// @Security BearerAuth
// @Success 200 {object} response.Envelope
// @Router /admin/frozen-wallets [get]
func (h *AdminHandler) ListFrozenWallets(c *gin.Context) {
	if h.walletRepo == nil {
		response.InternalError(c, "fund freeze is not configured")
		return
	}
	wallets, err := h.walletRepo.ListFrozen(c.Request.Context())
	if err != nil {
		response.InternalError(c, "failed to list frozen wallets")
		return
	}
	if wallets == nil {
		wallets = []wallet.Wallet{}
	}
	response.OK(c, gin.H{"wallets": wallets})
}

// @Summary [Admin] Get user detail
// @Description Returns a user's profile with wallets, circle memberships, community memberships and recent sends for moderation review. Admin only.
// @Tags Admin
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Success 200 {object} response.Envelope
// @Router /admin/users/{id} [get]
func (h *AdminHandler) GetUserDetail(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "invalid user ID")
		return
	}
	ctx := c.Request.Context()
	u, err := h.userRepo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			response.NotFound(c, "user not found")
			return
		}
		response.InternalError(c, "failed to load user")
		return
	}
	data := gin.H{"user": u}

	if h.walletRepo != nil {
		wallets, err := h.walletRepo.FindByUserID(ctx, id.String())
		if err == nil {
			if wallets == nil {
				wallets = []wallet.Wallet{}
			}
			data["wallets"] = wallets
		}
		sends, err := h.walletRepo.ListWithdrawalsByUser(ctx, id, 10)
		if err == nil {
			data["recentSends"] = sends
		}
	}
	if h.circleService != nil {
		if circles, err := h.circleService.GetUserCircles(ctx, id.String()); err == nil {
			if circles == nil {
				circles = []circle.Circle{}
			}
			data["circles"] = circles
		}
	}
	if h.communityService != nil {
		if communities, err := h.communityService.GetMyCommunities(ctx, id.String()); err == nil {
			data["communities"] = communities
		}
	}
	response.OK(c, data)
}
