package handler

import (
	"database/sql"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/deodes/backend/internal/api/middleware"
	"github.com/deodes/backend/internal/domain/audit"
	"github.com/deodes/backend/internal/domain/support"
	"github.com/deodes/backend/pkg/pagination"
	"github.com/deodes/backend/pkg/response"
)

// SupportHandler serves the contact-form inbox: anyone can open a ticket,
// admins triage it. Reporters never see other tickets.
type SupportHandler struct {
	svc       support.Service
	auditRepo audit.Repository
}

// NewSupportHandler wires the ticket endpoints.
func NewSupportHandler(svc support.Service, auditRepo audit.Repository) *SupportHandler {
	return &SupportHandler{svc: svc, auditRepo: auditRepo}
}

// @Summary Open a support ticket
// @Description Persists a contact-form message for the admin inbox. Public; throttled. Authenticated reporters are linked to their account.
// @Tags Support
// @Accept json
// @Produce json
// @Param body body object{email=string,message=string} true "Reply email and message"
// @Success 201 {object} response.Envelope
// @Router /support [post]
func (h *SupportHandler) CreateTicket(c *gin.Context) {
	var req struct {
		Email   string `json:"email" binding:"required"`
		Message string `json:"message" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "email and message are required")
		return
	}
	var userID *uuid.UUID
	if raw := middleware.GetUserID(c); raw != "" {
		if uid, err := uuid.Parse(raw); err == nil {
			userID = &uid
		}
	}
	t, err := h.svc.CreateTicket(c.Request.Context(), userID, req.Email, req.Message)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Created(c, gin.H{"id": t.ID.String(), "status": t.Status})
}

// @Summary [Admin] List support tickets
// @Description Pages the contact-form inbox, newest first, optionally filtered by status. Admin only.
// @Tags Admin
// @Produce json
// @Security BearerAuth
// @Param status query string false "Filter: open or resolved"
// @Param page query int false "Page number" default(1)
// @Param limit query int false "Items per page" default(20)
// @Success 200 {object} response.Envelope
// @Router /admin/support-tickets [get]
func (h *SupportHandler) ListTickets(c *gin.Context) {
	page, limit, _ := pagination.Parse(c)
	filter := support.TicketFilter{Status: c.Query("status"), Page: page, Limit: limit}
	tickets, total, err := h.svc.ListTickets(c.Request.Context(), filter)
	if err != nil {
		response.InternalError(c, "failed to list tickets")
		return
	}
	response.OKWithMeta(c, gin.H{"tickets": tickets}, response.NewPaginationMeta(page, limit, total))
}

// @Summary [Admin] Resolve or reopen a support ticket
// @Description Sets a ticket to resolved or back to open. Admin only.
// @Tags Admin
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Ticket ID"
// @Param body body object{status=string} true "resolved or open"
// @Success 200 {object} response.Envelope
// @Router /admin/support-tickets/{id} [patch]
func (h *SupportHandler) UpdateTicket(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "invalid ticket ID")
		return
	}
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "status is required")
		return
	}
	switch support.TicketStatus(req.Status) {
	case support.TicketStatusResolved:
		err = h.svc.ResolveTicket(c.Request.Context(), id)
	case support.TicketStatusOpen:
		err = h.svc.ReopenTicket(c.Request.Context(), id)
	default:
		response.BadRequest(c, "status must be open or resolved")
		return
	}
	if err != nil {
		response.InternalError(c, "failed to update ticket")
		return
	}
	if h.auditRepo != nil {
		actorID, _ := uuid.Parse(c.GetString("userID"))
		_ = h.auditRepo.Log(c.Request.Context(), &audit.AuditEntry{
			ID:           uuid.New(),
			ActorID:      actorID,
			Action:       "support.ticket." + req.Status,
			ResourceType: "support_ticket",
			ResourceID:   sql.NullString{String: id.String(), Valid: true},
			IPAddress:    sql.NullString{String: c.ClientIP(), Valid: c.ClientIP() != ""},
			UserAgent:    sql.NullString{String: c.Request.UserAgent(), Valid: c.Request.UserAgent() != ""},
			CreatedAt:    time.Now().UTC(),
		})
	}
	response.OK(c, gin.H{"id": id.String(), "status": req.Status})
}
