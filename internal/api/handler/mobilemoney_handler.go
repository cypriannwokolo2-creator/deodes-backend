package handler

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/deodes/backend/internal/api/middleware"
	"github.com/deodes/backend/internal/domain/mobilemoney"
	"github.com/deodes/backend/internal/domain/wallet"
	"github.com/deodes/backend/pkg/apperrors"
	"github.com/deodes/backend/pkg/response"
)

// MobileMoneyHandler exposes the mobile-money bridge (#190) — on-ramp
// (mobile money -> USDC) and off-ramp (USDC -> mobile money) for non-NGN
// markets.
type MobileMoneyHandler struct {
	svc    mobilemoney.Service
	wallet wallet.Service
}

func NewMobileMoneyHandler(svc mobilemoney.Service, walletSvc wallet.Service) *MobileMoneyHandler {
	return &MobileMoneyHandler{svc: svc, wallet: walletSvc}
}

// getIdempotencyKey prefers an explicit body key, falling back to the
// Idempotency-Key / X-Idempotency-Key headers. (Moved here when the fiat
// ramp handlers were removed; mobile-money flows still need it.)
func getIdempotencyKey(c *gin.Context, bodyKey string) string {
	if bodyKey != "" {
		return strings.TrimSpace(bodyKey)
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" {
		key = strings.TrimSpace(c.GetHeader("X-Idempotency-Key"))
	}
	return key
}

// @Summary Initiate mobile-money on-ramp
// @Description Collects funds from the caller's mobile money wallet and credits USDC to their Arc wallet once settled. Requires an idempotency key.
// @Tags Wallet
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body object{amount=number,currency=string,phoneNumber=string,idempotencyKey=string} true "On-ramp request"
// @Success 201 {object} response.Envelope{data=object{transaction=object}}
// @Failure 400 {object} response.Envelope
// @Router /wallet/mobile-money/onramp [post]
func (h *MobileMoneyHandler) InitiateOnramp(c *gin.Context) {
	userID := middleware.GetUserID(c)

	var req struct {
		Amount         float64 `json:"amount" binding:"required,gt=0"`
		Currency       string  `json:"currency" binding:"required"`
		PhoneNumber    string  `json:"phoneNumber" binding:"required"`
		IdempotencyKey string  `json:"idempotencyKey"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "amount, currency, and phoneNumber are required")
		return
	}

	idempotencyKey := getIdempotencyKey(c, req.IdempotencyKey)
	if idempotencyKey == "" {
		response.BadRequest(c, "an idempotency key is required (Idempotency-Key header or idempotencyKey field)")
		return
	}

	ctx := c.Request.Context()
	wallets, err := h.wallet.GetWallets(ctx, userID)
	if err != nil || len(wallets) == 0 {
		response.BadRequest(c, "no wallet found. Create a wallet first.")
		return
	}
	// Prefer the hosted Arc wallet like every other rail: wallets[0] is
	// merely the primary row, which can be a legacy non-0x wallet while a
	// hosted address exists further down the list.
	destination := wallets[0].PublicKey
	for _, w := range wallets {
		if w.WalletType == wallet.WalletTypeHosted && strings.HasPrefix(w.PublicKey, "0x") {
			destination = w.PublicKey
			break
		}
	}

	txn, err := h.svc.InitiateOnramp(ctx, userID, mobilemoney.OnrampRequest{
		Amount:             req.Amount,
		Currency:           req.Currency,
		PhoneNumber:        req.PhoneNumber,
		DestinationAddress: destination,
		Reference:          idempotencyKey,
	}, idempotencyKey)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	response.Created(c, gin.H{"transaction": txn})
}

// @Summary Initiate mobile-money off-ramp
// @Description Disburses USDC from the caller's account to their mobile money wallet. Requires an idempotency key.
// @Tags Wallet
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body object{amount=number,currency=string,phoneNumber=string,accountName=string,idempotencyKey=string} true "Off-ramp request"
// @Success 201 {object} response.Envelope{data=object{transaction=object}}
// @Failure 400 {object} response.Envelope
// @Router /wallet/mobile-money/offramp [post]
func (h *MobileMoneyHandler) InitiateOfframp(c *gin.Context) {
	userID := middleware.GetUserID(c)

	var req struct {
		Amount         float64 `json:"amount" binding:"required,gt=0"`
		Currency       string  `json:"currency" binding:"required"`
		PhoneNumber    string  `json:"phoneNumber" binding:"required"`
		AccountName    string  `json:"accountName" binding:"required"`
		IdempotencyKey string  `json:"idempotencyKey"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "amount, currency, phoneNumber, and accountName are required")
		return
	}

	idempotencyKey := getIdempotencyKey(c, req.IdempotencyKey)
	if idempotencyKey == "" {
		response.BadRequest(c, "an idempotency key is required (Idempotency-Key header or idempotencyKey field)")
		return
	}

	txn, err := h.svc.InitiateOfframp(c.Request.Context(), userID, mobilemoney.OfframpRequest{
		Amount:      req.Amount,
		Currency:    req.Currency,
		PhoneNumber: req.PhoneNumber,
		AccountName: req.AccountName,
		Reference:   idempotencyKey,
	}, idempotencyKey)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	response.Created(c, gin.H{"transaction": txn})
}

// @Summary Get mobile-money transaction
// @Description Returns a single mobile-money on-ramp/off-ramp transaction by ID. Only the owning user may view it.
// @Tags Wallet
// @Produce json
// @Security BearerAuth
// @Param id path string true "Transaction ID"
// @Success 200 {object} response.Envelope{data=object{transaction=object}}
// @Failure 404 {object} response.Envelope
// @Router /wallet/mobile-money/{id} [get]
func (h *MobileMoneyHandler) GetTransaction(c *gin.Context) {
	txn, err := h.svc.GetTransaction(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			response.NotFound(c, "transaction not found")
			return
		}
		response.InternalError(c, "failed to get transaction")
		return
	}

	if txn.UserID != middleware.GetUserID(c) {
		response.NotFound(c, "transaction not found")
		return
	}

	response.OK(c, gin.H{"transaction": txn})
}

// @Summary List supported mobile-money providers and currencies
// @Description Returns active mobile-money providers and their supported fiat currencies for on-ramp/off-ramp options.
// @Tags Wallet
// @Produce json
// @Success 200 {object} response.Envelope{data=object{providers=array,supportedCurrencies=array}}
// @Router /wallet/mobile-money/providers [get]
func (h *MobileMoneyHandler) ListProviders(c *gin.Context) {
	providers := h.svc.ListProviders(c.Request.Context())
	currencies := h.svc.GetSupportedCurrencies(c.Request.Context())
	response.OK(c, gin.H{
		"providers":           providers,
		"supportedCurrencies": currencies,
	})
}
