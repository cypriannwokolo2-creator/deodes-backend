package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/deodes/backend/internal/api/middleware"
	"github.com/deodes/backend/internal/domain/circle"
	"github.com/deodes/backend/internal/domain/contribution"
	"github.com/deodes/backend/internal/domain/invite"
	"github.com/deodes/backend/internal/domain/payout"
	"github.com/deodes/backend/pkg/apperrors"
	"github.com/deodes/backend/pkg/money"
	"github.com/deodes/backend/pkg/pagination"
	"github.com/deodes/backend/pkg/response"
	"github.com/deodes/backend/pkg/validator"
)

type CircleHandler struct {
	circleService  circle.Service
	inviteService  invite.Service
	contribService contribution.Service
	payoutService  payout.Service
}

func NewCircleHandler(circleSvc circle.Service, inviteSvc invite.Service, contribSvc contribution.Service, payoutSvc payout.Service) *CircleHandler {
	return &CircleHandler{
		circleService:  circleSvc,
		inviteService:  inviteSvc,
		contribService: contribSvc,
		payoutService:  payoutSvc,
	}
}

// ExportCircle streams a portable JSON snapshot in bounded pages.
func (h *CircleHandler) ExportCircle(c *gin.Context) {
	ctx := c.Request.Context()
	circleID, userID := c.Param("id"), middleware.GetUserID(c)
	cir, err := h.circleService.Get(ctx, circleID)
	if err != nil {
		response.NotFound(c, "circle not found")
		return
	}
	uid, err := uuid.Parse(userID)
	if err != nil {
		response.Forbidden(c, "circle membership required")
		return
	}
	organizer := cir.OrganizerID == uid
	if !organizer {
		member, memberErr := h.circleService.IsMember(ctx, circleID, userID)
		if memberErr != nil || !member {
			response.Forbidden(c, "circle membership required")
			return
		}
	}
	members, err := h.circleService.GetMembers(ctx, circleID)
	if err != nil {
		response.InternalError(c, "failed to export circle")
		return
	}
	enc := json.NewEncoder(c.Writer)
	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"circle-%s-export.json\"", cir.ID))
	c.Status(http.StatusOK)
	write := func(s string) bool { _, e := c.Writer.WriteString(s); return e == nil }
	if !write("{\"version\":1,\"exportedAt\":") || enc.Encode(time.Now().UTC()) != nil || !write(",\"circle\":") || enc.Encode(cir) != nil || !write(fmt.Sprintf(",\"scope\":%q,\"rounds\":{\"current\":%d,\"total\":%d},\"members\":[", map[bool]string{true: "circle", false: "member"}[organizer], cir.CurrentRound, cir.CurrentRound)) {
		return
	}
	first := true
	for _, record := range members {
		if !organizer && record.UserID != uid {
			continue
		}
		if !first && !write(",") {
			return
		}
		first = false
		if enc.Encode(record) != nil {
			return
		}
	}
	if !write("],\"contributions\":[") {
		return
	}
	first = true
	const pageSize = 500
	for page := 1; ; page++ {
		records, total, e := h.contribService.GetCircleHistory(ctx, circleID, page, pageSize)
		if e != nil {
			return
		}
		for _, record := range records {
			if !organizer && record.UserID != uid {
				continue
			}
			if !first && !write(",") {
				return
			}
			first = false
			if enc.Encode(record) != nil {
				return
			}
		}
		if page*pageSize >= total || len(records) == 0 {
			break
		}
	}
	if !write("],\"payouts\":[") {
		return
	}
	first = true
	for page := 1; ; page++ {
		records, total, e := h.payoutService.GetCircleHistory(ctx, circleID, page, pageSize)
		if e != nil {
			return
		}
		for _, record := range records {
			if !organizer && record.RecipientID != uid {
				continue
			}
			if !first && !write(",") {
				return
			}
			first = false
			if enc.Encode(record) != nil {
				return
			}
		}
		if page*pageSize >= total || len(records) == 0 {
			break
		}
	}
	write("]}")
}

// @Summary List circles
// @Description Returns a paginated list of savings circles with optional search, status, and type filters.
// @Tags Circles
// @Produce json
// @Param search query string false "Search term"
// @Param status query string false "Filter by status" Enums(pending,active,completed,cancelled)
// @Param type query string false "Filter by type" Enums(fixed,flexible,auction)
// @Param page query int false "Page number" default(1)
// @Param limit query int false "Items per page" default(20)
// @Success 200 {object} response.Envelope{data=object{circles=array},meta=response.PaginationMeta}
// @Router /circles [get]
func (h *CircleHandler) ListCircles(c *gin.Context) {
	page, limit, _ := pagination.Parse(c)
	filter := circle.CircleFilter{
		Search: c.Query("search"),
		Status: circle.CircleStatus(c.Query("status")),
		Page:   page,
		Limit:  limit,
	}
	// Unknown `type` values (e.g. the frontend's "my-circles" tab) must not
	// become a WHERE clause that matches nothing — ignore them so discovery
	// degrades to unfiltered instead of empty.
	switch t := circle.CircleType(c.Query("type")); t {
	case circle.CircleTypePublic, circle.CircleTypePrivate, circle.CircleTypeCommunity, circle.CircleTypePremium:
		filter.Type = t
	}
	// Arc USDC only: the browser's currency toggle maps straight through.
	// Unknown values are ignored rather than filtering everything out.
	switch cur := circle.CircleCurrency(c.Query("currency")); cur {
	case circle.CurrencyUSDC:
		filter.Currency = cur
	}
	// Sort keys mirror the frontend browser (date/name/amount/members); the
	// repository whitelists them, so anything else falls back to newest-first.
	if sortBy := c.Query("sortBy"); sortBy != "" {
		filter.SortBy = sortBy
	} else if sort := c.Query("sort"); sort != "" {
		// Accept the combined "date-desc" style value too.
		switch {
		case strings.HasPrefix(sort, "date"):
			filter.SortBy = "date"
		case strings.HasPrefix(sort, "name"):
			filter.SortBy = "name"
		case strings.HasPrefix(sort, "amount"):
			filter.SortBy = "amount"
		case strings.HasPrefix(sort, "members"):
			filter.SortBy = "members"
		}
	}
	switch o := c.Query("sortOrder"); o {
	case "asc", "desc":
		filter.SortOrder = o
	default:
		if s := c.Query("sort"); strings.HasSuffix(s, "-asc") {
			filter.SortOrder = "asc"
		} else if strings.HasSuffix(s, "-desc") {
			filter.SortOrder = "desc"
		}
	}
	if v := c.Query("openOnly"); v == "true" || v == "1" {
		filter.OpenOnly = true
		// openOnly wins over an explicit status: it means pending+active.
		filter.Status = ""
	}
	if communityID := c.Query("communityId"); communityID != "" {
		if id, err := uuid.Parse(communityID); err == nil {
			filter.CommunityID = &id
		}
	}
	if organizerID := c.Query("organizerId"); organizerID != "" {
		if id, err := uuid.Parse(organizerID); err == nil {
			filter.OrganizerID = &id
		}
	}
	circles, total, err := h.circleService.List(c.Request.Context(), filter)
	if err != nil {
		response.InternalError(c, "failed to list circles")
		return
	}
	if circles == nil {
		circles = []circle.Circle{}
	}
	response.OKWithMeta(c, gin.H{"circles": circles}, response.NewPaginationMeta(page, limit, total))
}

// writeCircleError maps circle failures to status codes so bad input never
// surfaces as a 500: validation problems are 400 VALIDATION_ERROR, unknown
// ids are 404, permission problems are 403, name conflicts are 409, and only
// genuine infrastructure failures are 500.
func writeCircleError(c *gin.Context, err error, action string) {
	switch {
	case errors.Is(err, apperrors.ErrInvalidInput):
		response.ValidationError(c, err.Error(), nil)
	case errors.Is(err, circle.ErrCircleNotFound):
		response.NotFound(c, "circle not found")
	case errors.Is(err, circle.ErrNotOrganizer):
		response.Forbidden(c, err.Error())
	case errors.Is(err, circle.ErrCircleNotActive):
		response.BadRequest(c, err.Error())
	case errors.Is(err, circle.ErrCircleFull):
		response.BadRequest(c, err.Error())
	case errors.Is(err, circle.ErrInsufficientFunds):
		response.ErrorWithCode(c, http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS", err.Error())
	case errors.Is(err, circle.ErrAlreadyMember):
		response.Conflict(c, err.Error())
	case errors.Is(err, circle.ErrNotMember):
		response.Forbidden(c, err.Error())
	case errors.Is(err, circle.ErrInvalidInvite):
		response.BadRequest(c, err.Error())
	case errors.Is(err, circle.ErrInvalidUUID):
		response.ValidationError(c, err.Error(), nil)
	case errors.Is(err, apperrors.ErrConflict):
		response.Conflict(c, err.Error())
	case errors.Is(err, apperrors.ErrNotFound):
		response.NotFound(c, "circle not found")
	default:
		response.InternalError(c, action)
	}
}

// @Summary Create a circle
// @Description Creates a new savings circle. Requires authentication.
// @Tags Circles
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body circle.CreateCircleInput true "Circle configuration"
// @Success 201 {object} response.Envelope{data=object{circle=object}}
// @Failure 400 {object} response.Envelope
// @Failure 422 {object} response.Envelope
// @Router /circles [post]
func (h *CircleHandler) CreateCircle(c *gin.Context) {
	userID := middleware.GetUserID(c)
	var input circle.CreateCircleInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ValidationError(c, err.Error(), nil)
		return
	}
	if err := validator.Validate.Struct(input); err != nil {
		response.ValidationError(c, "validation failed: "+err.Error(), validator.FormatError(err))
		return
	}
	cir, err := h.circleService.Create(c.Request.Context(), userID, input)
	if err != nil {
		writeCircleError(c, err, "failed to create circle")
		return
	}
	response.Created(c, gin.H{"circle": cir})
}

// @Summary Get a circle
// @Description Returns a single savings circle by ID.
// @Tags Circles
// @Produce json
// @Param id path string true "Circle ID"
// @Success 200 {object} response.Envelope{data=object{circle=object}}
// @Failure 404 {object} response.Envelope
// @Router /circles/{id} [get]
func (h *CircleHandler) GetCircle(c *gin.Context) {
	id := c.Param("id")
	cir, err := h.circleService.Get(c.Request.Context(), id)
	if err != nil {
		response.NotFound(c, "circle not found")
		return
	}
	response.OK(c, gin.H{"circle": cir})
}

func (h *CircleHandler) UpdateCircle(c *gin.Context) {
	id := c.Param("id")
	userID := middleware.GetUserID(c)
	var input circle.UpdateCircleInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ValidationError(c, err.Error(), nil)
		return
	}
	if err := validator.Validate.Struct(input); err != nil {
		response.ValidationError(c, "validation failed: "+err.Error(), validator.FormatError(err))
		return
	}
	cir, err := h.circleService.Update(c.Request.Context(), id, userID, input)
	if err != nil {
		writeCircleError(c, err, "failed to update circle")
		return
	}
	response.OK(c, gin.H{"circle": cir})
}

func (h *CircleHandler) StartCircle(c *gin.Context) {
	id := c.Param("id")
	userID := middleware.GetUserID(c)
	if err := h.circleService.Start(c.Request.Context(), id, userID); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"success": true})
}

func (h *CircleHandler) CancelCircle(c *gin.Context) {
	id := c.Param("id")
	userID := middleware.GetUserID(c)
	if err := h.circleService.Cancel(c.Request.Context(), id, userID); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"success": true})
}

// RecipientDistribution describes the calculated payout amounts for one recipient.
type RecipientDistribution struct {
	RecipientID string  `json:"recipientId"`
	GrossAmount float64 `json:"grossAmount"`
	FeeAmount   float64 `json:"feeAmount"`
	NetAmount   float64 `json:"netAmount"`
}

// PayoutPreviewResponse is the dry-run breakdown of an upcoming payout round.
type PayoutPreviewResponse struct {
	CircleID           string                  `json:"circleId"`
	RoundNumber        int                     `json:"roundNumber"`
	Currency           string                  `json:"currency"`
	TotalGrossAmount   float64                 `json:"totalGrossAmount"`
	TotalNetAmount     float64                 `json:"totalNetAmount"`
	ProtocolFee        float64                 `json:"protocolFee"`
	// ArcFeeEstimate is the estimated Arc network fee in USDC (USDC is gas,
	// 18 decimals, 20 Gwei floor).
	ArcFeeEstimate float64 `json:"arcFeeEstimate"`
	Recipients     []RecipientDistribution `json:"recipients"`
	DryRun         bool                    `json:"dryRun"`
	// IsDue reports whether the previewed round is currently distributable:
	// the round has started (round <= current round) and no payout has been
	// recorded for it yet.
	IsDue bool `json:"isDue"`
}

func (h *CircleHandler) buildPayoutPreview(cir *circle.Circle, reqAmount *float64, reqFee *float64, recipientID string, roundNumber int) PayoutPreviewResponse {
	round := roundNumber
	if round < 1 {
		round = cir.CurrentRound
		if round < 1 {
			round = 1
		}
	}

	recipient := recipientID
	if recipient == "" {
		recipient = cir.OrganizerID.String()
	}

	gross := 0.0
	if reqAmount != nil && *reqAmount > 0 {
		gross = *reqAmount
	} else if cir.ContributionAmount > 0 {
		members := cir.MemberCount
		if members < 1 {
			members = cir.MaxMembers
			if members < 1 {
				members = 1
			}
		}
		gross = cir.ContributionAmount * float64(members)
	}
	// Two-decimal money rounding so gross/fee/net stay consistent in USDC.
	// Floor (not round) to match the on-chain SplitFee truncation exactly.
	gross = math.Floor(gross*100) / 100

	protocolFee := 0.0
	if reqFee != nil && *reqFee >= 0 {
		protocolFee = *reqFee
	} else {
		// 0.5% default protocol fee on payouts only (spec + transparency page).
		protocolFee = math.Floor(gross*0.005*100) / 100
	}

	net := gross - protocolFee
	if net < 0 {
		net = 0
	}

	// Arc network fee estimate in USDC (USDC is gas, ~$0.001 per tx).
	arcFeeEstimate := 0.001

	return PayoutPreviewResponse{
		CircleID:           cir.ID.String(),
		RoundNumber:        round,
		Currency:           string(cir.Currency),
		TotalGrossAmount:   gross,
		TotalNetAmount:     net,
		ProtocolFee:        protocolFee,
		ArcFeeEstimate:     arcFeeEstimate,
		Recipients: []RecipientDistribution{
			{
				RecipientID: recipient,
				GrossAmount: gross,
				FeeAmount:   protocolFee,
				NetAmount:   net,
			},
		},
		DryRun: true,
	}
}

// @Summary Dry-run preview of circle payout distribution
// @Description Calculates and returns the computed payout distribution for an active circle without executing money movement.
// @Tags Circles
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Circle ID"
// @Param body body object{recipientId=string,roundNumber=int,amount=number,feeAmount=number} false "Optional overrides for preview"
// @Success 200 {object} response.Envelope{data=object{preview=PayoutPreviewResponse}}
// @Failure 400 {object} response.Envelope
// @Failure 403 {object} response.Envelope
// @Failure 404 {object} response.Envelope
// @Router /circles/{id}/payout/preview [post]
func (h *CircleHandler) PreviewPayout(c *gin.Context) {
	circleID := c.Param("id")
	userID := middleware.GetUserID(c)
	cir, err := h.circleService.Get(c.Request.Context(), circleID)
	if err != nil {
		response.NotFound(c, "circle not found")
		return
	}
	if cir.OrganizerID.String() != userID {
		response.Forbidden(c, "only the organizer can preview or trigger a payout")
		return
	}
	if cir.Status != circle.CircleStatusActive {
		response.BadRequest(c, "circle is not active")
		return
	}

	var req struct {
		RecipientID string   `json:"recipientId"`
		RoundNumber int      `json:"roundNumber"`
		Amount      *float64 `json:"amount"`
		FeeAmount   *float64 `json:"feeAmount"`
	}
	_ = c.ShouldBindJSON(&req)

	if req.Amount != nil && *req.Amount <= 0 {
		response.BadRequest(c, "preview amount must be greater than zero")
		return
	}
	if req.FeeAmount != nil && *req.FeeAmount < 0 {
		response.BadRequest(c, "preview fee amount must not be negative")
		return
	}
	if req.Amount != nil && req.FeeAmount != nil && *req.FeeAmount > *req.Amount {
		response.BadRequest(c, "preview fee amount must not exceed the payout amount")
		return
	}
	if req.RecipientID != "" {
		member, err := h.circleService.IsMember(c.Request.Context(), circleID, req.RecipientID)
		if err != nil {
			response.InternalError(c, "failed to verify recipient membership")
			return
		}
		if !member {
			response.BadRequest(c, "recipient is not a member of this circle")
			return
		}
	}

	preview := h.buildPayoutPreview(cir, req.Amount, req.FeeAmount, req.RecipientID, req.RoundNumber)
	// A round is due when it has started and has not been paid out yet.
	preview.IsDue = preview.RoundNumber >= 1 && preview.RoundNumber <= cir.CurrentRound
	if preview.IsDue {
		if existing, err := h.payoutService.GetRoundPayout(c.Request.Context(), circleID, preview.RoundNumber); err == nil && existing != nil {
			preview.IsDue = false
		}
	}
	response.OK(c, gin.H{"preview": preview})
}

// TriggerPayout records the payout for an active circle. Payout submission to
// Arc remains upstream of this endpoint; txnHash is the on-chain receipt.
// If ?dry_run=true or body.dryRun=true is provided, returns the distribution preview without executing.
func (h *CircleHandler) TriggerPayout(c *gin.Context) {
	circleID := c.Param("id")
	userID := middleware.GetUserID(c)
	cir, err := h.circleService.Get(c.Request.Context(), circleID)
	if err != nil {
		response.NotFound(c, "circle not found")
		return
	}
	if cir.OrganizerID.String() != userID {
		response.Forbidden(c, "only the organizer can trigger a payout")
		return
	}
	if cir.Status != circle.CircleStatusActive {
		response.BadRequest(c, "circle is not active")
		return
	}

	if c.Query("dry_run") == "true" || c.Query("dryRun") == "true" {
		var previewReq struct {
			RecipientID string   `json:"recipientId"`
			RoundNumber int      `json:"roundNumber"`
			Amount      *float64 `json:"amount"`
			FeeAmount   *float64 `json:"feeAmount"`
		}
		_ = c.ShouldBindJSON(&previewReq)
		preview := h.buildPayoutPreview(cir, previewReq.Amount, previewReq.FeeAmount, previewReq.RecipientID, previewReq.RoundNumber)
		response.OK(c, gin.H{"preview": preview})
		return
	}

	var req payout.RecordInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	req.CircleID = circleID
	// Default the payout type to the circle's so clients that only send
	// recipient/amount/txnHash keep working across payout modes.
	if req.PayoutType == "" {
		req.PayoutType = payout.PayoutType(cir.PayoutType)
	}
	// An omitted round targets the current (due) round.
	if req.RoundNumber < 1 {
		req.RoundNumber = cir.CurrentRound
		if req.RoundNumber < 1 {
			req.RoundNumber = 1
		}
	}
	// Round-due check: only the current round or a past unpaid round can be
	// distributed. Future rounds are never due.
	if req.RoundNumber > cir.CurrentRound {
		response.BadRequest(c, fmt.Sprintf("round %d is not due yet: circle is on round %d", req.RoundNumber, cir.CurrentRound))
		return
	}
	if err := validator.Validate.Struct(req); err != nil {
		response.ValidationErrors(c, "validation failed: "+err.Error())
		return
	}
	// Arc USDC only: the distribution receipt is always an Arc EVM
	// transaction hash. Wrong-rail hashes fail fast with a 422 instead of
	// being recorded as perpetually-pending (same rule as Contribute).
	if !isArcTxHash(req.TxnHash) {
		response.ValidationErrors(c, "txnHash must be an Arc EVM transaction hash (0x followed by 64 hex characters)")
		return
	}
	// The recipient must be a circle member — an organizer cannot distribute
	// the pool to an arbitrary address.
	member, err := h.circleService.IsMember(c.Request.Context(), circleID, req.RecipientID)
	if err != nil {
		response.InternalError(c, "failed to verify recipient membership")
		return
	}
	if !member {
		response.Forbidden(c, "recipient is not a member of this circle")
		return
	}
	// Double-payout guard: a round distributes exactly once. A replay for the
	// same recipient is idempotent (200 + existing record); a different
	// recipient for an already-paid round is a conflict (409).
	existing, err := h.payoutService.GetRoundPayout(c.Request.Context(), circleID, req.RoundNumber)
	if err != nil {
		response.InternalError(c, "failed to verify round payout state")
		return
	}
	if existing != nil {
		if existing.RecipientID.String() == req.RecipientID {
			response.OK(c, gin.H{"payout": existing, "status": cir.Status, "duplicate": true})
			return
		}
		response.Conflict(c, "payout already recorded for this circle round")
		return
	}
	// Rotation guard (RULE 2 — random payouts rotate without repeat): a
	// member who has already been paid in this circle cannot be paid again
	// until the rotation completes. The payout service re-checks this
	// authoritatively at record time (which also covers the indexer path);
	// this pre-check maps the violation to a precise 409 here instead of a
	// generic 500 from the Record error.
	if cir.PayoutType == circle.PayoutTypeRandom {
		paidRecipients, err := h.allCirclePayoutRecipients(c.Request.Context(), circleID)
		if err != nil {
			response.InternalError(c, "failed to verify rotation state")
			return
		}
		if paidRecipients[req.RecipientID] {
			response.Conflict(c, "member has already received a payout in this rotation: random payouts pay every member exactly once before anyone is paid again")
			return
		}
	}
	record, err := h.payoutService.Record(c.Request.Context(), req)
	if err != nil {
		if errors.Is(err, payout.ErrRoundAlreadyPaid) {
			response.Conflict(c, err.Error())
			return
		}
		if errors.Is(err, payout.ErrRecipientAlreadyPaid) {
			response.Conflict(c, err.Error())
			return
		}
		if errors.Is(err, apperrors.ErrCircleSuspended) {
			response.ErrorWithCode(c, http.StatusUnprocessableEntity, "CIRCLE_SUSPENDED", err.Error())
			return
		}
		if strings.Contains(err.Error(), "on-chain verification failed") {
			response.ErrorWithCode(c, http.StatusUnprocessableEntity, "ONCHAIN_VERIFICATION_FAILED", err.Error())
			return
		}
		response.InternalError(c, "failed to record payout: "+err.Error())
		return
	}
	response.Created(c, gin.H{"payout": record, "status": cir.Status})
}

func (h *CircleHandler) CloseCircle(c *gin.Context) {
	circleID := c.Param("id")
	userID := middleware.GetUserID(c)
	_, payoutCount, err := h.payoutService.GetCircleHistory(
		c.Request.Context(), circleID, 1, 1,
	)
	if err != nil {
		response.InternalError(c, "failed to verify circle payouts")
		return
	}
	if payoutCount == 0 {
		response.BadRequest(c, "circle cannot close before its payout")
		return
	}
	if err := h.circleService.Close(c.Request.Context(), circleID, userID); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"status": circle.CircleStatusCompleted})
}

// @Summary Join a circle
// @Description Joins an existing savings circle. Requires an invite code if the circle is private.
// @Tags Circles
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Circle ID"
// @Param body body object{inviteCode=string} false "Invite code (optional for public circles)"
// @Success 200 {object} response.Envelope{data=object{success=bool}}
// @Failure 400 {object} response.Envelope
// @Router /circles/{id}/join [post]
//
// Contract: POST /circles/:id/join, body {inviteCode?}. 200 {success:true,
// circleId, duplicate} on fresh and idempotent re-join; 400 invalid
// id/invite, 401 unauthenticated, 403 score/community/invite forbidden,
// 404 unknown circle, 409 circle full, 422 circle ended.
func (h *CircleHandler) JoinCircle(c *gin.Context) {
	circleID := c.Param("id")
	userID := middleware.GetUserID(c)
	if userID == "" {
		response.Unauthorized(c, "authentication required")
		return
	}
	if _, err := uuid.Parse(circleID); err != nil {
		response.BadRequest(c, "invalid circle ID")
		return
	}
	if _, err := uuid.Parse(userID); err != nil {
		response.Unauthorized(c, "invalid session identity")
		return
	}
	var req struct {
		InviteCode string `json:"inviteCode"`
	}
	// An empty body is a normal public-circle join; only real JSON errors
	// (and non-object shapes) are rejected.
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			// Tolerate a literal `null`/empty body the same as {}.
			req.InviteCode = ""
		}
	}
	req.InviteCode = strings.TrimSpace(req.InviteCode)
	if req.InviteCode != "" && h.inviteService != nil {
		inv, err := h.inviteService.Validate(c.Request.Context(), req.InviteCode)
		if err != nil {
			response.BadRequest(c, "invalid or expired invite code")
			return
		}
		if inv.CircleID.String() != circleID {
			response.BadRequest(c, "invite code is not for this circle")
			return
		}
	}
	if err := h.circleService.Join(c.Request.Context(), circleID, userID, req.InviteCode); err != nil {
		mapJoinError(c, err)
		return
	}
	// Consume the invite after the membership commits. Join stands even if
	// this fails (the member falls back to a used-but-uncounted code).
	if req.InviteCode != "" && h.inviteService != nil {
		_ = func() error {
			_, aerr := h.inviteService.Accept(c.Request.Context(), req.InviteCode, userID)
			return aerr
		}()
	}
	response.OK(c, gin.H{"success": true, "circleId": circleID, "duplicate": false})
}

// mapJoinError translates join failures into the status the frontend's
// requirements messaging keys off — instead of collapsing everything into a
// 400 with a raw service string.
func mapJoinError(c *gin.Context, err error) {
	msg := err.Error()
	switch {
	case errors.Is(err, circle.ErrAlreadyMember):
		// Idempotent re-join: the caller is a member either way.
		response.OK(c, gin.H{"success": true, "duplicate": true})
	case errors.Is(err, circle.ErrCircleNotFound):
		response.NotFound(c, "circle not found")
	case errors.Is(err, circle.ErrInvalidUUID):
		response.BadRequest(c, "invalid circle ID")
	case errors.Is(err, circle.ErrCircleFull):
		response.Conflict(c, "this circle is full")
	case errors.Is(err, circle.ErrInsufficientFunds):
		response.ErrorWithCode(c, http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS", err.Error())
	case errors.Is(err, circle.ErrInvalidInvite):
		response.BadRequest(c, "this circle is invite-only: enter the organizer's invite code to join")
	case errors.Is(err, circle.ErrMoiScoreTooLow):
		response.Forbidden(c, "your DeoScore is too low for this circle")
	case errors.Is(err, circle.ErrNotCommunityMember):
		response.Forbidden(c, "this community circle is limited to community members")
	case errors.Is(err, circle.ErrCircleNotActive):
		response.ErrorWithCode(c, http.StatusUnprocessableEntity, "CIRCLE_ENDED", "this circle has ended and is no longer joinable")
	case strings.Contains(msg, "at least 50 MoiScore"):
		response.Forbidden(c, "joining premium circles requires at least 50 DeoScore")
	case strings.Contains(msg, "MoiScore is too low"):
		response.Forbidden(c, "your DeoScore is too low for this circle")
	case strings.Contains(msg, "only USDC"):
		response.ErrorWithCode(c, http.StatusUnprocessableEntity, "LEGACY_RAIL", "only USDC circles on Arc can be joined")
	case strings.Contains(msg, "invite code is not for this circle"):
		response.BadRequest(c, msg)
	default:
		response.BadRequest(c, msg)
	}
}

func (h *CircleHandler) Contribute(c *gin.Context) {
	circleID := c.Param("id")
	userID := middleware.GetUserID(c)
	// Auth: the route is behind AuthMiddleware, but a missing userID must be
	// a 401 rather than a 500 from the service's UUID parse. Never trust a
	// client-supplied walletAddress/userID — identity always comes from the
	// bearer session. CSRF + Idempotency-Key are enforced by middleware
	// (see router.go: requireIdem + CSRFTokenValidator); this handler performs
	// no state change without them.
	if userID == "" {
		response.Unauthorized(c, "authentication required")
		return
	}
	if _, err := uuid.Parse(circleID); err != nil {
		response.BadRequest(c, "invalid circle ID")
		return
	}
	if _, err := uuid.Parse(userID); err != nil {
		response.Unauthorized(c, "invalid session identity")
		return
	}
	var req struct {
		Amount      float64 `json:"amount" validate:"required,gt=0,lte=10000000"`
		TxnHash     string  `json:"txnHash" validate:"required"`
		RoundNumber int     `json:"roundNumber" validate:"required,gte=1,lte=10000"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := validator.Validate.Struct(req); err != nil {
		response.ValidationErrors(c, "validation failed: "+err.Error())
		return
	}
	// Arc USDC only: the EVM tx hash is always 0x + 64 hex. Legacy-style
	// hashes are rejected here so a wrong-rail submission fails fast with a
	// 422 instead of being recorded as perpetually-pending.
	if !isArcTxHash(req.TxnHash) {
		response.ValidationErrors(c, "txnHash must be an Arc EVM transaction hash (0x followed by 64 hex characters)")
		return
	}
	// money.FromFloat64 rejects NaN/Inf and overflow without panicking
	// (Must* is forbidden in request paths).
	if _, err := money.FromFloat64(req.Amount); err != nil {
		response.BadRequest(c, "invalid contribution amount: "+err.Error())
		return
	}

	cir, err := h.circleService.Get(c.Request.Context(), circleID)
	if err != nil {
		response.NotFound(c, "circle not found")
		return
	}
	// Arc product rail is USDC-only. Legacy XLM circles cannot receive
	// EVM contributions.
	if cir.Currency != circle.CurrencyUSDC {
		response.BadRequest(c, "only USDC contributions are supported on Arc")
		return
	}
	if cir.CircleType == circle.CircleTypePremium && req.Amount < 50 {
		response.BadRequest(c, "premium circles require minimum 50 USDC contribution")
		return
	}
	// The round payment must equal the circle's configured amount (within a
	// minor unit of tolerance for float transport). Under/over-payments are a
	// 422 so the member can retry with the exact figure.
	if math.Abs(req.Amount-cir.ContributionAmount) > 0.0000001 {
		response.ValidationErrors(c, fmt.Sprintf("amount must equal the circle contribution amount of %v USDC", cir.ContributionAmount))
		return
	}

	contrib, err := h.contribService.Record(c.Request.Context(), contribution.RecordInput{
		CircleID:    circleID,
		UserID:      userID,
		RoundNumber: req.RoundNumber,
		Amount:      req.Amount,
		TxnHash:     req.TxnHash,
	})
	if err != nil {
		mapContributeError(c, err)
		return
	}

	response.Created(c, gin.H{"success": true, "contribution": contrib})
}

// isArcTxHash reports whether s is an EVM transaction hash (0x + 64 hex).
func isArcTxHash(s string) bool {
	if len(s) != 66 || len(s) < 2 || s[0] != '0' || s[1] != 'x' && s[1] != 'X' {
		return false
	}
	for _, r := range s[2:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// mapContributeError translates domain/service errors into the correct HTTP
// status instead of collapsing everything into a 500.
func mapContributeError(c *gin.Context, err error) {
	msg := err.Error()
	switch {
	case errors.Is(err, apperrors.ErrCircleEnded):
		response.ErrorWithCode(c, http.StatusUnprocessableEntity, "CIRCLE_ENDED", msg)
	case errors.Is(err, apperrors.ErrCircleSuspended):
		response.ErrorWithCode(c, http.StatusUnprocessableEntity, "CIRCLE_SUSPENDED", msg)
	case errors.Is(err, apperrors.ErrLateContributionRejected):
		response.ErrorWithCode(c, http.StatusUnprocessableEntity, "LATE_CONTRIBUTION", msg)
	case errors.Is(err, apperrors.ErrDuplicateContribution):
		response.ErrorWithCode(c, http.StatusConflict, "DUPLICATE_CONTRIBUTION", msg)
	case strings.Contains(msg, "user is not a member"):
		response.Forbidden(c, msg)
	case strings.Contains(msg, "on-chain verification failed"):
		response.ErrorWithCode(c, http.StatusUnprocessableEntity, "ONCHAIN_VERIFICATION_FAILED", msg)
	case strings.Contains(msg, "invalid circleID") || strings.Contains(msg, "invalid userID") || strings.Contains(msg, "invalid contribution amount"):
		response.BadRequest(c, msg)
	case strings.Contains(msg, "getting circle"):
		response.NotFound(c, "circle not found")
	default:
		response.InternalError(c, "failed to record contribution: "+msg)
	}
}

func (h *CircleHandler) ExitCircle(c *gin.Context) {
	id := c.Param("id")
	userID := middleware.GetUserID(c)
	ctx := c.Request.Context()

	// RULE 1 — no withdrawals until every member has been paid. This is the
	// circle-scoped withdraw path (a member leaving the circle and taking
	// their position out). The generic wallet withdraw
	// (POST /v1/wallets/withdraw) carries no circle context and only moves
	// the user's own funds, so it is intentionally NOT gated here.
	// The lock applies to active circles only: pending circles have no
	// rounds yet, and completed/cancelled circles are already settled.
	// Complete means every active member has received at least one payout.
	if cir, err := h.circleService.Get(ctx, id); err == nil && cir != nil &&
		cir.Status == circle.CircleStatusActive {
		members, merr := h.circleService.GetMembers(ctx, id)
		paid, perr := h.allCirclePayoutRecipients(ctx, id)
		if merr != nil || perr != nil {
			response.InternalError(c, "failed to verify circle payout state")
			return
		}
		paidCount, totalCount := circlePayoutCompletion(members, paid)
		if totalCount == 0 || paidCount < totalCount {
			response.Error(c, http.StatusForbidden, "PAYOUTS_INCOMPLETE",
				fmt.Sprintf("withdrawals unlock after every member has been paid — %d of %d rounds paid so far", paidCount, totalCount),
				gin.H{"paidRounds": paidCount, "totalRounds": totalCount})
			return
		}
	}

	if err := h.circleService.Exit(ctx, id, userID); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"success": true})
}

// allCirclePayoutRecipients returns the set of recipient user-ID strings that
// have received at least one payout in the circle. It pages through the full
// history so rotation (Rule 2) and exit-gate (Rule 1) checks never miss rows
// past a page boundary.
func (h *CircleHandler) allCirclePayoutRecipients(ctx context.Context, circleID string) (map[string]bool, error) {
	const pageSize = 100
	paid := make(map[string]bool)
	for page := 1; ; page++ {
		payouts, total, err := h.payoutService.GetCircleHistory(ctx, circleID, page, pageSize)
		if err != nil {
			return nil, err
		}
		for _, p := range payouts {
			paid[p.RecipientID.String()] = true
		}
		if len(payouts) == 0 || page*pageSize >= total {
			break
		}
	}
	return paid, nil
}

// circlePayoutCompletion counts how many of the circle's active members have
// been paid at least once. Exited/removed members are out of the rotation
// and no longer block withdrawals.
func circlePayoutCompletion(members []circle.CircleMember, paid map[string]bool) (paidCount, totalCount int) {
	for _, m := range members {
		if m.Status != circle.MemberStatusActive {
			continue
		}
		totalCount++
		if paid[m.UserID.String()] {
			paidCount++
		}
	}
	return paidCount, totalCount
}

func (h *CircleHandler) GetMembers(c *gin.Context) {
	circleID := c.Param("id")
	var members []circle.CircleMember
	var err error
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		members, err = h.circleService.SearchMembers(c.Request.Context(), circleID, search)
	} else {
		members, err = h.circleService.GetMembers(c.Request.Context(), circleID)
	}
	if err != nil {
		response.InternalError(c, "failed to get members")
		return
	}

	limit := 20
	if l := c.Query("limit"); l != "" {
		if parsedLimit, err := strconv.Atoi(l); err == nil && parsedLimit > 0 {
			limit = parsedLimit
		}
	}
	if limit > 100 {
		limit = 100
	}

	cursorOffset := 0
	if cur := c.Query("cursor"); cur != "" {
		if offset, err := strconv.Atoi(cur); err == nil && offset >= 0 {
			cursorOffset = offset
		}
	}

	total := len(members)
	if cursorOffset > total {
		cursorOffset = total
	}

	end := cursorOffset + limit
	hasMore := false
	nextCursor := ""
	if end < total {
		hasMore = true
		nextCursor = strconv.Itoa(end)
	} else {
		end = total
	}

	paged := members[cursorOffset:end]
	response.OKWithMeta(c, gin.H{"members": paged}, gin.H{
		"total":      total,
		"limit":      limit,
		"cursor":     c.Query("cursor"),
		"nextCursor": nextCursor,
		"hasMore":    hasMore,
	})
}

func (h *CircleHandler) GetRounds(c *gin.Context) {
	circleID := c.Param("id")
	page, limit, _ := pagination.Parse(c)

	cir, err := h.circleService.Get(c.Request.Context(), circleID)
	if err != nil {
		response.NotFound(c, "circle not found")
		return
	}

	contribs, contribTotal, err := h.contribService.GetCircleHistory(c.Request.Context(), circleID, page, limit)
	if err != nil {
		contribs = nil
	}

	payouts, payoutTotal, err := h.payoutService.GetCircleHistory(c.Request.Context(), circleID, page, limit)
	if err != nil {
		payouts = nil
	}

	total := contribTotal
	if payoutTotal > total {
		total = payoutTotal
	}

	roundMap := make(map[int]map[string]any)
	for _, c := range contribs {
		entry, ok := roundMap[c.RoundNumber]
		if !ok {
			entry = map[string]any{
				"roundNumber":   c.RoundNumber,
				"contributions": []any{},
			}
			roundMap[c.RoundNumber] = entry
		}
		entry["contributions"] = append(entry["contributions"].([]any), c)
	}
	for _, p := range payouts {
		entry, ok := roundMap[p.RoundNumber]
		if !ok {
			entry = map[string]any{
				"roundNumber":   p.RoundNumber,
				"contributions": []any{},
			}
			roundMap[p.RoundNumber] = entry
		}
		entry["payout"] = p
	}

	roundNumbers := make([]int, 0, len(roundMap))
	for roundNum := range roundMap {
		roundNumbers = append(roundNumbers, roundNum)
	}

	for roundNum, entry := range roundMap {
		configSnapshot, err := h.circleService.QueryRoundConfig(c.Request.Context(), circleID, roundNum)
		if err == nil && configSnapshot != nil {
			entry["configSnapshot"] = configSnapshot
		}
	}

	rounds := make([]map[string]any, 0, len(roundMap))
	for _, v := range roundMap {
		rounds = append(rounds, v)
	}

	response.OKWithMeta(c, gin.H{
		"rounds":       rounds,
		"currentRound": cir.CurrentRound,
		"totalMembers": cir.MaxMembers,
	}, response.NewPaginationMeta(page, limit, total))
}

func (h *CircleHandler) GetPayouts(c *gin.Context) {
	circleID := c.Param("id")
	page, limit, _ := pagination.Parse(c)
	payouts, total, err := h.payoutService.GetCircleHistory(c.Request.Context(), circleID, page, limit)
	if err != nil {
		response.InternalError(c, "failed to get payouts")
		return
	}
	if payouts == nil {
		payouts = []payout.Payout{}
	}
	response.OKWithMeta(c, gin.H{"payouts": payouts}, response.NewPaginationMeta(page, limit, total))
}

func (h *CircleHandler) Dispute(c *gin.Context) {
	circleID := c.Param("id")
	userID := middleware.GetUserID(c)
	var req circle.DisputeInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := validator.Validate.Struct(req); err != nil {
		response.ValidationErrors(c, "validation failed: "+err.Error())
		return
	}

	dispute, err := h.circleService.RaiseDispute(c.Request.Context(), circleID, userID, req)
	if err != nil {
		if err == circle.ErrCircleNotFound {
			response.NotFound(c, "circle not found")
			return
		}
		if err == circle.ErrNotMember {
			response.Forbidden(c, "only active circle members can raise disputes")
			return
		}
		if err == circle.ErrCircleNotActive {
			response.BadRequest(c, "circle is not active")
			return
		}
		response.InternalError(c, "failed to raise dispute: "+err.Error())
		return
	}

	response.Created(c, gin.H{"success": true, "dispute": dispute})
}

func (h *CircleHandler) Vote(c *gin.Context) {
	circleID := c.Param("id")
	userID := middleware.GetUserID(c)
	var req circle.VoteInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := validator.Validate.Struct(req); err != nil {
		response.ValidationErrors(c, "validation failed: "+err.Error())
		return
	}

	vote, allVoted, winnerID, err := h.circleService.CastVote(c.Request.Context(), circleID, userID, req)
	if err != nil {
		if err == circle.ErrCircleNotFound {
			response.NotFound(c, "circle not found")
			return
		}
		if err == circle.ErrNotMember {
			response.Forbidden(c, "only active circle members can vote")
			return
		}
		if err == circle.ErrCircleNotActive {
			response.BadRequest(c, "circle is not active")
			return
		}
		response.BadRequest(c, err.Error())
		return
	}

	res := gin.H{
		"success":  true,
		"vote":     vote,
		"allVoted": allVoted,
	}
	if allVoted {
		res["winnerId"] = winnerID
	}
	response.OK(c, res)
}

// RemoveMember allows the circle organizer to remove a member by their user ID (address).
// The member's status is set to 'removed' and a MemberLeft event is broadcast.
// @Summary Remove a member from a circle
// @Description Organizer-only. Removes a member, broadcasts event for stake redistribution.
// @Tags Circles
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Circle ID"
// @Param address path string true "Member user ID (address)"
// @Param body body object{reason=string} false "Optional removal reason"
// @Success 200 {object} response.Envelope{data=object{success=bool}}
// @Failure 400 {object} response.Envelope
// @Failure 403 {object} response.Envelope
// @Failure 404 {object} response.Envelope
// @Router /circles/{id}/members/{address}/remove [post]
func (h *CircleHandler) RemoveMember(c *gin.Context) {
	circleID := c.Param("id")
	memberAddress := c.Param("address")
	callerID := middleware.GetUserID(c)

	var req struct {
		Reason string `json:"reason"`
	}
	// reason is optional; ignore bind errors
	_ = c.ShouldBindJSON(&req)

	if err := h.circleService.RemoveMember(c.Request.Context(), circleID, callerID, memberAddress, req.Reason); err != nil {
		switch err {
		case circle.ErrNotOrganizer:
			response.Forbidden(c, "only the organizer can remove members")
		case circle.ErrNotMember:
			response.NotFound(c, "member not found or already inactive")
		default:
			response.BadRequest(c, err.Error())
		}
		return
	}
	response.OK(c, gin.H{"success": true})
}

func (h *CircleHandler) AuctionBid(c *gin.Context) {
	circleID := c.Param("id")
	userID := middleware.GetUserID(c)
	var req circle.AuctionBidInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := validator.Validate.Struct(req); err != nil {
		response.ValidationErrors(c, "validation failed: "+err.Error())
		return
	}

	bid, err := h.circleService.SubmitAuctionBid(c.Request.Context(), circleID, userID, req)
	if err != nil {
		if err == circle.ErrCircleNotFound {
			response.NotFound(c, "circle not found")
			return
		}
		if err == circle.ErrNotMember {
			response.Forbidden(c, "only active circle members can bid")
			return
		}
		if err == circle.ErrCircleNotActive {
			response.BadRequest(c, "circle is not active")
			return
		}
		response.BadRequest(c, err.Error())
		return
	}

	response.Created(c, gin.H{"success": true, "bid": bid})
}

// GetRoundConfig returns the immutable configuration snapshot captured for a round.
// @Summary Get circle configuration snapshot for a round
// @Tags Circles
// @Produce json
// @Security BearerAuth
// @Param id path string true "Circle ID"
// @Param round path int true "Round Number"
// @Success 200 {object} response.Envelope{data=circle.RoundConfigSnapshot}
// @Failure 400 {object} response.Envelope
// @Failure 404 {object} response.Envelope
// @Router /circles/{id}/rounds/{round}/config [get]
func (h *CircleHandler) GetRoundConfig(c *gin.Context) {
	circleID := c.Param("id")
	roundStr := c.Param("round")
	round, err := strconv.Atoi(roundStr)
	if err != nil || round <= 0 {
		response.BadRequest(c, "invalid round number")
		return
	}

	snapshot, err := h.circleService.QueryRoundConfig(c.Request.Context(), circleID, round)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) || errors.Is(err, circle.ErrCircleNotFound) {
			response.NotFound(c, "round configuration snapshot not found")
			return
		}
		response.InternalError(c, "failed to query round config: "+err.Error())
		return
	}

	response.OK(c, snapshot)
}

// GetSnapshot godoc
// @Summary      Circle state snapshot
// @Description  Returns a single-transaction consistent view of a circle's full state for the authenticated user:
//
//	circle details, members, all rounds (contributions + payouts), the user's balance, and the circle's vault balance.
//
// @Tags         Circles
// @Produce      json
// @Param        id   path      string  true  "Circle UUID"
// @Success      200  {object}  response.Envelope{data=object{snapshot=circle.CircleSnapshot}}
// @Failure      400  {object}  response.Envelope
// @Failure      404  {object}  response.Envelope
// @Failure      500  {object}  response.Envelope
// @Router       /circles/{id}/snapshot [get]
// @Security     BearerAuth
func (h *CircleHandler) GetSnapshot(c *gin.Context) {
	circleID := c.Param("id")
	userID := middleware.GetUserID(c)

	// A malformed id is the caller's mistake, not a server fault: answer 400
	// here rather than letting the service's parse failure fall through to the
	// 500 branch below.
	if _, err := uuid.Parse(circleID); err != nil {
		response.BadRequest(c, "invalid circle id")
		return
	}

	snap, err := h.circleService.GetCircleSnapshot(c.Request.Context(), circleID, userID)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			response.NotFound(c, "circle not found")
			return
		}
		// A snapshot is circle-private: a caller who is neither organizer nor an
		// active member is refused rather than served a redacted one (#440).
		if errors.Is(err, apperrors.ErrForbidden) {
			response.Forbidden(c, "not a member of this circle")
			return
		}
		response.InternalError(c, "failed to build circle snapshot")
		return
	}
	response.OK(c, gin.H{"snapshot": snap})
}

// GetBulkSnapshots godoc
// @Summary      Bulk circle state snapshots
// @Description  Returns consistent snapshots for all circles the authenticated user belongs to or organizes.
//
//	Optionally accepts a comma-separated list of circle UUIDs via the `ids` query parameter to filter.
//	Each snapshot is fetched in a separate repeatable-read transaction, so results are internally consistent.
//
// @Tags         Circles
// @Produce      json
// @Param        ids  query     string  false  "Comma-separated circle UUIDs to fetch (omit for all user circles)"
// @Success      200  {object}  response.Envelope{data=object{snapshots=[]circle.CircleSnapshot}}
// @Failure      400  {object}  response.Envelope
// @Failure      500  {object}  response.Envelope
// @Router       /circles/snapshots [get]
// @Security     BearerAuth
func (h *CircleHandler) GetBulkSnapshots(c *gin.Context) {
	userID := middleware.GetUserID(c)

	var circleIDs []string
	if raw := c.Query("ids"); raw != "" {
		for _, id := range splitComma(raw) {
			if id != "" {
				// Same reasoning as GetSnapshot: a malformed filter is a bad
				// request, not a server fault.
				if _, err := uuid.Parse(id); err != nil {
					response.BadRequest(c, "invalid circle id")
					return
				}
				circleIDs = append(circleIDs, id)
			}
		}
	}

	snaps, err := h.circleService.GetBulkCircleSnapshots(c.Request.Context(), userID, circleIDs)
	if err != nil {
		if errors.Is(err, apperrors.ErrForbidden) {
			response.Forbidden(c, "not a member of one or more of these circles")
			return
		}
		response.InternalError(c, "failed to build circle snapshots")
		return
	}
	response.OK(c, gin.H{"snapshots": snaps})
}

// splitComma splits a comma-separated string and trims whitespace from each element.
func splitComma(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
