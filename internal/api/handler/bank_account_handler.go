package handler

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/deodes/backend/internal/api/middleware"
	"github.com/deodes/backend/internal/domain/bankaccount"
	"github.com/deodes/backend/pkg/apperrors"
	"github.com/deodes/backend/pkg/response"
)

// BankAccountHandler serves the payment-settings saved rails:
// GET/POST /v1/bank-accounts, POST /v1/bank-accounts/:id/default,
// DELETE /v1/bank-accounts/:id. All rows are scoped to the caller, and
// unknown ids and other users' ids both surface as 404.
type BankAccountHandler struct {
	svc bankaccount.Service
}

func NewBankAccountHandler(svc bankaccount.Service) *BankAccountHandler {
	return &BankAccountHandler{svc: svc}
}

func writeBankAccountError(c *gin.Context, err error, action string) {
	switch {
	case errors.Is(err, apperrors.ErrInvalidInput):
		response.ValidationError(c, err.Error(), nil)
	case errors.Is(err, apperrors.ErrNotFound):
		response.NotFound(c, "bank account not found")
	default:
		response.InternalError(c, action)
	}
}

func (h *BankAccountHandler) List(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		response.Unauthorized(c, "authentication required")
		return
	}
	accounts, err := h.svc.List(c.Request.Context(), userID)
	if err != nil {
		writeBankAccountError(c, err, "failed to list bank accounts")
		return
	}
	if accounts == nil {
		accounts = []bankaccount.BankAccount{}
	}
	response.OK(c, gin.H{"accounts": accounts})
}

func (h *BankAccountHandler) Create(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		response.Unauthorized(c, "authentication required")
		return
	}
	var req bankaccount.CreateAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, err.Error(), nil)
		return
	}
	account, err := h.svc.Add(c.Request.Context(), userID, req)
	if err != nil {
		writeBankAccountError(c, err, "failed to add bank account")
		return
	}
	response.Created(c, gin.H{"account": account})
}

func (h *BankAccountHandler) Delete(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		response.Unauthorized(c, "authentication required")
		return
	}
	if err := h.svc.Remove(c.Request.Context(), userID, c.Param("id")); err != nil {
		writeBankAccountError(c, err, "failed to remove bank account")
		return
	}
	response.OK(c, gin.H{"success": true})
}

func (h *BankAccountHandler) SetDefault(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		response.Unauthorized(c, "authentication required")
		return
	}
	if err := h.svc.SetDefault(c.Request.Context(), userID, c.Param("id")); err != nil {
		writeBankAccountError(c, err, "failed to set default bank account")
		return
	}
	response.OK(c, gin.H{"success": true})
}
