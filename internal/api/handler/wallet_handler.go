package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/deodes/backend/internal/api/middleware"
	"github.com/deodes/backend/internal/domain/auth"
	"github.com/deodes/backend/internal/domain/notification"
	"github.com/deodes/backend/internal/domain/user"
	"github.com/deodes/backend/internal/domain/wallet"
	"github.com/deodes/backend/pkg/arc"
	"github.com/deodes/backend/pkg/response"
	"github.com/gin-gonic/gin"
)

type WalletHandler struct {
	walletSvc       wallet.Service
	authSvc         auth.Service
	userRepo        user.Repository
	walletRepo      transferOutcomeStore
	notificationSvc transferOutcomeNotifier
}

func NewWalletHandler(svc wallet.Service, authSvc auth.Service) *WalletHandler {
	return &WalletHandler{walletSvc: svc, authSvc: authSvc}
}

// WithUserRepository attaches the user lookup used by internal (email)
// transfers. Kept as an option so existing constructors keep working.
func (h *WalletHandler) WithUserRepository(repo user.Repository) *WalletHandler {
	h.userRepo = repo
	return h
}

// transferOutcomeStore is the narrow slice of wallet.Repository the transfer
// outcome endpoint needs. Depending on this (instead of the full repo)
// keeps the handler unit-testable without a database; wallet.Repository
// satisfies it structurally.
type transferOutcomeStore interface {
	FindWithdrawalAuditByTxHash(ctx context.Context, txHash string) (*wallet.WithdrawalRecord, error)
	ClaimTransferOutcomeNotification(ctx context.Context, txHash string) (*wallet.WithdrawalRecord, error)
}

// transferOutcomeNotifier creates the in-app notifications for settled
// transfer outcomes. notification.Service satisfies it structurally.
type transferOutcomeNotifier interface {
	Create(ctx context.Context, input notification.CreateInput) (*notification.Notification, error)
}

// WithWalletRepository attaches the send-audit store used to verify reported
// transfer outcomes belong to the caller.
func (h *WalletHandler) WithWalletRepository(repo wallet.Repository) *WalletHandler {
	h.walletRepo = repo
	return h
}

// WithNotificationService attaches the notifier used for transfer outcome
// alerts. Absent, the outcome endpoint reports unavailability.
func (h *WalletHandler) WithNotificationService(svc notification.Service) *WalletHandler {
	h.notificationSvc = svc
	return h
}

// sendFailure maps SendPayment failures to actionable statuses: user-fixable
// conditions (short balance, missing wallet, throttles) are 4xx with the
// reason in the body, only unexpected failures stay 500.
func sendFailure(c *gin.Context, action string, err error) {
	msg := action + " failed: " + err.Error()
	switch {
	case errors.Is(err, wallet.ErrInsufficientFunds),
		errors.Is(err, wallet.ErrNoHostedWallet),
		errors.Is(err, wallet.ErrAmountTooSmall):
		response.BadRequest(c, msg)
	case errors.Is(err, wallet.ErrWalletFrozen):
		response.Forbidden(c, msg)
	case errors.Is(err, wallet.ErrSendRateLimited),
		errors.Is(err, wallet.ErrDailySendLimitExceeded):
		response.RateLimitExceeded(c, msg)
	default:
		response.InternalError(c, msg)
	}
}

// CreateWallet creates a new Ed25519 wallet for the authenticated user, or returns existing
// POST /v1/wallets
func (h *WalletHandler) CreateWallet(c *gin.Context) {
	userID := middleware.GetUserID(c)

	// Check if wallet already exists — idempotent
	existing, err := h.walletSvc.GetWallets(c.Request.Context(), userID)
	if err == nil && len(existing) > 0 {
		response.OK(c, gin.H{"wallet": existing[0]})
		return
	}

	var req struct {
		PasskeySeed string `json:"passkeySeed" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "passkeySeed is required")
		return
	}

	w, err := h.walletSvc.CreateWallet(c.Request.Context(), userID, []byte(req.PasskeySeed))
	if err != nil {
		response.InternalError(c, "failed to create wallet: "+err.Error())
		return
	}

	response.Created(c, gin.H{"wallet": w})
}

// EnsureHostedArcWallet guarantees the caller has a custodial Arc (EVM 0x)
// wallet, creating it on first call. The seed derives deterministically from
// the user ID (never the email — emails change, IDs don't), so this is
// idempotent and recoverable: same user, same key, every time. No request
// body; safe to call after every login (best-effort, never blocks auth).
// POST /v1/wallets/hosted-arc/ensure
func (h *WalletHandler) EnsureHostedArcWallet(c *gin.Context) {
	userID := middleware.GetUserID(c)
	ctx := c.Request.Context()

	if existing, err := h.walletSvc.GetWallets(ctx, userID); err == nil {
		for _, w := range existing {
			if w.WalletType == wallet.WalletTypeHosted {
				response.OK(c, gin.H{"wallet": w})
				return
			}
		}
	}

	seed, err := h.walletSvc.DeriveWalletSeed(ctx, "hosted-arc:"+userID)
	if err != nil {
		response.InternalError(c, "wallet derivation unavailable")
		return
	}
	w, err := h.walletSvc.CreateHostedArcWallet(ctx, userID, []byte(seed))
	if err != nil {
		// Lost race with a concurrent ensure: return the winner's wallet.
		if existing, rerr := h.walletSvc.GetWallets(ctx, userID); rerr == nil {
			for _, ew := range existing {
				if ew.WalletType == wallet.WalletTypeHosted {
					response.OK(c, gin.H{"wallet": ew})
					return
				}
			}
		}
		response.InternalError(c, "failed to ensure hosted wallet: "+err.Error())
		return
	}
	response.Created(c, gin.H{"wallet": w})
}

// CreateHostedArcWallet creates the custodial Arc (EVM 0x) wallet under us,
// or returns the existing hosted wallet (idempotent, one per user).
// POST /v1/wallets/hosted-arc
func (h *WalletHandler) CreateHostedArcWallet(c *gin.Context) {
	userID := middleware.GetUserID(c)

	if existing, err := h.walletSvc.GetWallets(c.Request.Context(), userID); err == nil {
		for _, w := range existing {
			if w.WalletType == wallet.WalletTypeHosted {
				response.OK(c, gin.H{"wallet": w})
				return
			}
		}
	}

	var req struct {
		PasskeySeed string `json:"passkeySeed" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "passkeySeed is required")
		return
	}

	w, err := h.walletSvc.CreateHostedArcWallet(c.Request.Context(), userID, []byte(req.PasskeySeed))
	if err != nil {
		response.InternalError(c, "failed to create hosted wallet: "+err.Error())
		return
	}

	response.Created(c, gin.H{"wallet": w})
}

// LinkWallet links the caller's own self-custody Arc (EVM 0x) wallet after
// they prove ownership by signing the login nonce (same flow as wallet
// login: fetch POST /v1/auth/nonce {walletAddress}, personal_sign it, send
// the signature here). The address must be passed exactly as it was when the
// nonce was requested, because nonces are keyed by that string.
// Idempotent: re-linking your own address returns it with 200.
// POST /v1/wallets/link
func (h *WalletHandler) LinkWallet(c *gin.Context) {
	userID := middleware.GetUserID(c)
	ctx := c.Request.Context()

	var req struct {
		Address       string `json:"address"`
		WalletAddress string `json:"walletAddress"`
		Signature     string `json:"signature" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	address := strings.TrimSpace(req.Address)
	if address == "" {
		address = strings.TrimSpace(req.WalletAddress)
	}
	if err := arc.ValidateAddress(address); err != nil {
		response.BadRequest(c, "invalid wallet address: must be Arc 0x + 40 hex chars")
		return
	}

	valid, err := h.authSvc.VerifySignature(ctx, address, req.Signature)
	if err != nil || !valid {
		response.Unauthorized(c, "signature verification failed")
		return
	}

	existing, err := h.walletSvc.GetWalletByAddress(ctx, address)
	if err != nil {
		response.InternalError(c, "failed to look up wallet")
		return
	}
	if existing != nil {
		if existing.UserID != userID {
			response.Conflict(c, "address is already linked to another account")
			return
		}
		response.OK(c, gin.H{"wallet": existing})
		return
	}

	// One linked wallet per user (UNIQUE on user_id+wallet_type): a second,
	// different address is a conflict, not a second row. SameSigner covers a
	// checksum-case variant that slipped past the normalized global lookup.
	if mine, err := h.walletSvc.GetWallets(ctx, userID); err == nil {
		for _, w := range mine {
			if w.WalletType != wallet.WalletTypeLinked {
				continue
			}
			if arc.SameSigner(w.PublicKey, address) {
				response.OK(c, gin.H{"wallet": w})
				return
			}
			response.Conflict(c, "account already has a different wallet linked")
			return
		}
	}

	w, err := h.walletSvc.LinkWallet(ctx, userID, address)
	if err != nil {
		response.InternalError(c, "failed to link wallet: "+err.Error())
		return
	}
	response.Created(c, gin.H{"wallet": w})
}

// ListWallets returns all wallets for the authenticated user
// GET /v1/wallets
func (h *WalletHandler) ListWallets(c *gin.Context) {
	userID := middleware.GetUserID(c)
	wallets, err := h.walletSvc.GetWallets(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "failed to list wallets")
		return
	}
	response.OK(c, gin.H{"wallets": wallets})
}

// Withdraw sends crypto from the user's wallet to a destination address
// POST /v1/wallets/withdraw
func (h *WalletHandler) Withdraw(c *gin.Context) {
	userID := middleware.GetUserID(c)
	var req struct {
		Destination string  `json:"destination" binding:"required"`
		Asset       string  `json:"asset" binding:"required,oneof=USDC"`
		Amount      float64 `json:"amount" binding:"required,gt=0"`
		PasskeySeed string  `json:"passkeySeed"`
		Memo        string  `json:"memo"`
		Label       string  `json:"label"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := wallet.ValidateMemo(req.Memo); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	label, err := wallet.NormalizeLabel(req.Label)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if !isArcAddress(req.Destination) {
		response.BadRequest(c, "destination must be a 0x Arc address")
		return
	}
	ipAddress := c.ClientIP()
	userAgent := c.GetHeader("User-Agent")
	txHash, err := h.walletSvc.SendPayment(c.Request.Context(), userID, []byte(req.PasskeySeed), req.Destination, req.Asset, req.Amount, req.Memo, ipAddress, userAgent)
	if err != nil {
		sendFailure(c, "withdrawal", err)
		return
	}
	response.OK(c, gin.H{"txHash": txHash, "memo": req.Memo, "label": label})
}

// GetBalance returns the USDC balance (Arc EVM) for the user's primary wallet
// GET /v1/wallets/balance
func (h *WalletHandler) GetBalance(c *gin.Context) {
	userID := middleware.GetUserID(c)
	bal, err := h.walletSvc.GetBalance(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "failed to get balance")
		return
	}
	response.OK(c, gin.H{"balance": bal})
}

// DeleteWallet deletes a wallet by ID
// DELETE /v1/wallets/:id
func (h *WalletHandler) DeleteWallet(c *gin.Context) {
	userID := middleware.GetUserID(c)
	walletID := c.Param("id")
	if err := h.walletSvc.DeleteWallet(c.Request.Context(), userID, walletID); err != nil {
		response.InternalError(c, "failed to delete wallet")
		return
	}
	response.OK(c, gin.H{"success": true})
}

// ReportTransferOutcome records the on-chain outcome of a broadcast transfer,
// reported by the sender's client once its receipt settles, and fans out
// in-app notifications: confirmation/failure to the sender, received to an
// internal (Deodes) recipient. Only the first report per tx hash notifies,
// so clients that re-poll on revisit cannot create duplicates.
// POST /v1/wallets/transfers/:txHash/outcome  {status: "finalized"|"failed"}
func (h *WalletHandler) ReportTransferOutcome(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		response.Unauthorized(c, "authentication required")
		return
	}
	if h.walletRepo == nil || h.notificationSvc == nil {
		response.InternalError(c, "transfer outcome reporting is not configured")
		return
	}
	txHash := strings.TrimSpace(c.Param("txHash"))
	if !isTxHash(txHash) {
		response.BadRequest(c, "txHash must be a 0x transaction hash")
		return
	}
	var body struct {
		Status string `json:"status" binding:"required,oneof=finalized failed"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, "status must be finalized or failed")
		return
	}
	ctx := c.Request.Context()
	rec, err := h.walletRepo.FindWithdrawalAuditByTxHash(ctx, txHash)
	if err != nil {
		response.InternalError(c, "failed to look up transaction")
		return
	}
	if rec == nil || !strings.EqualFold(rec.UserID.String(), userID) {
		response.NotFound(c, "transaction not found")
		return
	}
	claimed, err := h.walletRepo.ClaimTransferOutcomeNotification(ctx, txHash)
	if err != nil {
		response.InternalError(c, "failed to record transaction outcome")
		return
	}
	if claimed == nil {
		response.OK(c, gin.H{"notified": false})
		return
	}
	amount := strconv.FormatFloat(rec.Amount, 'f', -1, 64)
	shortDest := shortAddress(rec.Destination)
	data, _ := json.Marshal(gin.H{
		"txHash": txHash, "amount": amount, "destination": rec.Destination,
	})
	// Best-effort delivery: the claim above is already consumed, so a failed
	// insert here must not fail the request (a retry could never re-notify).
	// A chain second-opinion guards spoofed reports: a "finalized" claim the
	// chain proves reverted is notified as failed instead — never mint a
	// false confirmation. An unreachable node leaves the report as claimed.
	outcome := body.Status
	if outcome == "finalized" {
		if _, reverted := h.walletSvc.CheckTransferSettlement(ctx, txHash); reverted {
			outcome = "failed"
		}
	}
	if outcome == "finalized" {
		_, _ = h.notificationSvc.Create(ctx, notification.CreateInput{
			UserID: userID, Type: notification.TypeTransferConfirmed,
			Channel: notification.ChannelInApp,
			Title:   "Transfer confirmed",
			Body: fmt.Sprintf("%s USDC to %s is finalized on Arc.",
				amount, shortDest),
			Data: data,
		})
		// Internal recipient: the destination wallet belongs to a Deodes user.
		if w, rerr := h.walletSvc.GetWalletByAddress(ctx, rec.Destination); rerr == nil &&
			w != nil && !strings.EqualFold(w.UserID, userID) {
			_, _ = h.notificationSvc.Create(ctx, notification.CreateInput{
				UserID: w.UserID, Type: notification.TypeTransferReceived,
				Channel: notification.ChannelInApp,
				Title:   "USDC received",
				Body:    fmt.Sprintf("You received %s USDC on Arc.", amount),
				Data:    data,
			})
		}
	} else {
		_, _ = h.notificationSvc.Create(ctx, notification.CreateInput{
			UserID: userID, Type: notification.TypeTransferFailed,
			Channel: notification.ChannelInApp,
			Title:   "Transfer failed",
			Body: fmt.Sprintf("%s USDC to %s was rejected by the Arc network. Your funds never left your wallet.",
				amount, shortDest),
			Data: data,
		})
	}
	response.OK(c, gin.H{"notified": true})
}

// isTxHash reports whether s is a 0x EVM transaction hash.
func isTxHash(s string) bool {
	if len(s) != 66 || !strings.HasPrefix(s, "0x") {
		return false
	}
	for _, ch := range s[2:] {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
			return false
		}
	}
	return true
}

// shortAddress truncates an address like the frontend formatAddress helper.
func shortAddress(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:6] + "..." + s[len(s)-4:]
}

// Allowance reports the rolling-24h send volume and remaining cap so the
// transfer form can cap input client-side (server still enforces).
// GET /v1/wallets/allowance
func (h *WalletHandler) Allowance(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		response.Unauthorized(c, "authentication required")
		return
	}
	spent, remaining, limit, err := h.walletSvc.DailyAllowance(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "failed to read send allowance")
		return
	}
	response.OK(c, gin.H{
		"spentUsd":     spent,
		"remainingUsd": remaining,
		"limitUsd":     limit,
	})
}

// isArcAddress reports whether s is a 0x Arc address (external destination).
func isArcAddress(s string) bool {
	if len(s) != 42 || !strings.HasPrefix(s, "0x") {
		return false
	}
	for _, ch := range s[2:] {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
			return false
		}
	}
	return true
}

// hostedArcAddress returns the recipient's custodial Arc (0x) wallet, or "".
func hostedArcAddress(wallets []wallet.Wallet) string {
	for i := range wallets {
		if wallets[i].WalletType == wallet.WalletTypeHosted && strings.HasPrefix(wallets[i].PublicKey, "0x") {
			return wallets[i].PublicKey
		}
	}
	return ""
}

// Transfer sends USDC to another Deodes user (internal: email or address)
// or any Arc address (external: address only). Internal email recipients
// resolve server-side to their hosted Arc wallet; the same on-chain send
// settles both cases.
// POST /v1/wallets/transfer
func (h *WalletHandler) Transfer(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == "" {
		response.Unauthorized(c, "authentication required")
		return
	}
	var req struct {
		To     string  `json:"to" binding:"required"`
		Amount float64 `json:"amount" binding:"required,gt=0"`
		Memo   string  `json:"memo"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := wallet.ValidateMemo(req.Memo); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	to := strings.TrimSpace(req.To)
	destination := ""
	internal := false
	recipientName := ""

	if isArcAddress(to) {
		destination = to
	} else if strings.Contains(to, "@") {
		if h.userRepo == nil {
			response.InternalError(c, "internal transfers are not configured")
			return
		}
		email := strings.ToLower(strings.TrimSpace(to))
		recipient, err := h.userRepo.FindByEmail(c.Request.Context(), email)
		if err != nil || recipient == nil {
			response.NotFound(c, "no Deodes user found for that email")
			return
		}
		wallets, err := h.walletSvc.GetWallets(c.Request.Context(), recipient.ID.String())
		if err != nil {
			response.InternalError(c, "failed to resolve recipient wallet")
			return
		}
		destination = hostedArcAddress(wallets)
		if destination == "" {
			response.NotFound(c, "recipient has no Arc wallet yet")
			return
		}
		internal = true
		if recipient.DisplayName != nil {
			recipientName = *recipient.DisplayName
		}
	} else {
		response.BadRequest(c, "to must be an email address or a 0x Arc address")
		return
	}

	txHash, err := h.walletSvc.SendPayment(c.Request.Context(), userID, nil, destination, "USDC", req.Amount, req.Memo, c.ClientIP(), c.GetHeader("User-Agent"))
	if err != nil {
		sendFailure(c, "transfer", err)
		return
	}
	_, remaining, limit, _ := h.walletSvc.DailyAllowance(c.Request.Context(), userID)
	response.OK(c, gin.H{
		"txHash":            txHash,
		"internal":          internal,
		"destination":       destination,
		"recipient":         recipientName,
		"dailyRemainingUsd": remaining,
		"dailyLimitUsd":     limit,
	})
}
