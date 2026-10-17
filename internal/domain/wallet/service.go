package wallet

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/deodes/backend/pkg/arc"
	"github.com/deodes/backend/pkg/crypto"
	"github.com/deodes/backend/pkg/logger"
	"github.com/deodes/backend/pkg/money"
)

type Service interface {
	DeriveWalletSeed(ctx context.Context, email string) (string, error)
	CreateWallet(ctx context.Context, userID string, passkeySeed []byte) (*Wallet, error)
	// CreateHostedArcWallet derives, encrypts and persists a custodial Arc
	// (EVM 0x) wallet under us. One hosted wallet per user (UNIQUE on
	// user_id+wallet_type). Requires EncryptionKey; legacy wallets stay
	// backward compatible without it.
	CreateHostedArcWallet(ctx context.Context, userID string, passkeySeed []byte) (*Wallet, error)
	// LinkWallet persists a self-custody Arc (EVM 0x) wallet link for userID.
	// Ownership must already be proven by the caller via a signed-nonce
	// verify; this only validates the address format and stores the record
	// with no secret (EncryptedSecretKey/Nonce stay empty, IsPrimary false).
	// One linked wallet per user (UNIQUE on user_id+wallet_type); the address
	// itself is globally unique (UNIQUE on public_key).
	LinkWallet(ctx context.Context, userID, address string) (*Wallet, error)
	// GetWalletByAddress returns the wallet row for a public key, or
	// (nil, nil) when no wallet is linked to that address.
	GetWalletByAddress(ctx context.Context, address string) (*Wallet, error)
	SignTransaction(ctx context.Context, walletID string, passkeySeed []byte, txnXDR string) (string, error)
	GetWallets(ctx context.Context, userID string) ([]Wallet, error)
	GetBalance(ctx context.Context, userID string) (*Balance, error)
	SendPayment(ctx context.Context, userID string, passkeySeed []byte, destination, asset string, amount float64, memo, ipAddress, userAgent string) (string, error)
	DeleteWallet(ctx context.Context, userID, walletID string) error
	// DailyAllowance reports completed-send volume and the remaining daily
	// cap for the transfer UI (spent, remaining, limit).
	DailyAllowance(ctx context.Context, userID string) (spent, remaining, limit float64, err error)
	// CheckTransferSettlement reports the on-chain settlement of a broadcast
	// transfer: confirmed (mined, status 1), reverted (mined, status 0), or
	// neither when unmined or the node is unreachable. Unknown must not block
	// callers — it is a second opinion, not a gate.
	CheckTransferSettlement(ctx context.Context, txHash string) (confirmed, reverted bool)
}

type Config struct {
	WalletPepper  string
	Argon2Time    int
	Argon2Memory  int
	Argon2Threads int
	// EncryptionKey is the hex-encoded 32-byte AES key for custodial secret
	// storage (security.encryption_key, required in prod).
	EncryptionKey string
	// Arc chain settings for hosted 0x wallets (arc.rpc_url/chain_id/usdc_contract).
	ArcRPCURL       string
	ArcChainID      uint64
	ArcUSDCContract string
	// DailySendLimitUSD caps completed sends per rolling 24h (C2): a stolen
	// session can otherwise drain the custodial wallet 3 sends/hour forever.
	// Zero or negative means uncapped (dev default is set in config).
	DailySendLimitUSD float64
}

type service struct {
	repo Repository
	cfg  Config
	arc  *arc.Client
}

// ErrInsufficientFunds is returned when the on-chain balance cannot cover a
// send. Callers surface the shortfall amounts in the wrapped message.
var ErrInsufficientFunds = errors.New("insufficient wallet balance")

// ErrNoHostedWallet is returned when the sender has no hosted Arc wallet yet.
var ErrNoHostedWallet = errors.New("no hosted Arc wallet found")

// ErrSendRateLimited is returned when the sender exceeds 3 sends per hour.
var ErrSendRateLimited = errors.New("withdrawal rate limit exceeded")

// ErrDailySendLimitExceeded is returned when a send would breach the rolling
// 24h value cap.
var ErrDailySendLimitExceeded = errors.New("daily send limit exceeded")

// ErrAmountTooSmall is returned when the amount floors to zero base units:
// broadcasting it would burn gas moving nothing.
var ErrAmountTooSmall = errors.New("amount too small to settle on-chain")

// ErrWalletFrozen is returned when the sender's hosted wallet is frozen by
// admin moderation. Inbound deposits are unaffected; only outbound sends stop.
var ErrWalletFrozen = errors.New("sending is paused on this account pending review")

// chainBalance reads the on-chain USDC balance for a 0x address. RPC flakes
// propagate as errors so callers can fail closed.
func (s *service) chainBalance(ctx context.Context, address string) (float64, error) {
	human, err := arc.USDCBalanceOf(ctx, s.arc, s.cfg.ArcUSDCContract, address)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(human, 64)
}

func NewService(repo Repository, cfg Config) (Service, error) {
	chainID := cfg.ArcChainID
	if chainID == 0 {
		chainID = arc.ChainIDTestnet
	}
	return &service{
		repo: repo,
		cfg:  cfg,
		arc:  arc.NewClient(cfg.ArcRPCURL, chainID),
	}, nil
}

func (s *service) DeriveWalletSeed(ctx context.Context, email string) (string, error) {
	// Deterministic Argon2id key derivation lives in pkg/crypto; the service
	// only supplies the configured pepper and derivation parameters.
	return crypto.DeriveWalletSeed(email, s.cfg.WalletPepper, s.cfg.Argon2Time, s.cfg.Argon2Memory, s.cfg.Argon2Threads)
}

func (s *service) CreateWallet(ctx context.Context, userID string, passkeySeed []byte) (*Wallet, error) {
	rawSeed, err := decodeSeedInput(passkeySeed)
	if err != nil {
		l := logger.Ctx(ctx)
		l.Error().Err(err).Str("userID", userID).Msg("invalid wallet seed input")
		return nil, err
	}
	_, addr := crypto.Ed25519AddressFromSeed(rawSeed)

	walletID := uuid.New().String()
	w := &Wallet{
		ID:                 walletID,
		UserID:             userID,
		PublicKey:          addr,
		WalletType:         WalletTypeAuto,
		IsPrimary:          true,
		EncryptedSecretKey: []byte{},
		EncryptionNonce:    []byte{},
	}

	// Persist the secret encrypted when an encryption key is configured.
	// Legacy callers without a key keep watch-only semantics, but store
	// empty (non-nil) bytea so the NOT NULL columns never receive NULL.
	// A configured key that fails to parse or encrypt is a hard error:
	// silently storing an unspendable wallet is worse than failing creation.
	if strings.TrimSpace(s.cfg.EncryptionKey) == "" {
		l := logger.Ctx(ctx)
		l.Warn().Str("userID", userID).Msg("creating watch-only wallet: no encryption key configured")
	} else {
		key, err := crypto.ParseEncryptionKey(strings.TrimSpace(s.cfg.EncryptionKey))
		if err != nil {
			return nil, fmt.Errorf("create wallet requires a valid encryption key: %w", err)
		}
		ct, nonce, err := crypto.Encrypt(rawSeed[:], key)
		if err != nil {
			return nil, fmt.Errorf("encrypting wallet secret: %w", err)
		}
		w.EncryptedSecretKey = ct
		w.EncryptionNonce = nonce
	}

	if s.repo != nil {
		if err := s.repo.Create(ctx, w); err != nil {
			return nil, err
		}
	}

	return w,
		nil
}

// decodeSeedInput normalizes a wallet seed input into 32 raw bytes.
// New callers pass raw 32B. Legacy callers passed []byte(hexString) (64 ASCII
// chars) — detected and hex-decoded so the derived key matches the intended
// derivation instead of ASCII bytes.
func decodeSeedInput(in []byte) ([32]byte, error) {
	var out [32]byte
	s := strings.TrimSpace(string(in))
	if decoded, err := hex.DecodeString(s); err == nil && len(decoded) == 32 {
		copy(out[:], decoded)
		return out, nil
	}
	if len(in) < 32 {
		return out, fmt.Errorf("seed input must be at least 32 bytes, got %d", len(in))
	}
	copy(out[:], in[:32])
	return out, nil
}

// CreateHostedArcWallet derives (chain-separated), encrypts and persists a
// custodial Arc EVM wallet for userID. The 0x address is returned; the
// private key is encrypted with EncryptionKey and never logged.
// Requires cfg.EncryptionKey (hex 32B) — production always sets it.
func (s *service) CreateHostedArcWallet(ctx context.Context, userID string, passkeySeed []byte) (*Wallet, error) {
	raw, err := decodeSeedInput(passkeySeed)
	if err != nil {
		return nil, err
	}
	baseHex := hex.EncodeToString(raw[:])
	arcSeed, err := crypto.DeriveArcSeed(baseHex)
	if err != nil {
		return nil, err
	}
	address, privKey, err := crypto.EVMAddressFromSeed(arcSeed)
	if err != nil {
		return nil, err
	}

	key, err := crypto.ParseEncryptionKey(strings.TrimSpace(s.cfg.EncryptionKey))
	if err != nil {
		return nil, fmt.Errorf("hosted wallets require a configured encryption key: %w", err)
	}
	ct, nonce, err := crypto.Encrypt(privKey, key)
	if err != nil {
		return nil, fmt.Errorf("encrypting hosted secret: %w", err)
	}

	w := &Wallet{
		ID:                 uuid.New().String(),
		UserID:             userID,
		PublicKey:          address,
		EncryptedSecretKey: ct,
		EncryptionNonce:    nonce,
		WalletType:         WalletTypeHosted,
		IsPrimary:          true,
	}
	if s.repo != nil {
		if err := s.repo.Create(ctx, w); err != nil {
			return nil, err
		}
	}
	// Clear key material copies; the persisted ciphertext + caller's seed remain.
	for i := range privKey {
		privKey[i] = 0
	}
	return w, nil
}

// normalizeArcAddress trims and lowercases an Arc 0x address so checksum-case
// variants map to the single stored form (hosted addresses are already stored
// lowercase hex). Callers must validate with arc.ValidateAddress first.
func normalizeArcAddress(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}

// LinkWallet validates an Arc 0x address and persists a self-custody link
// record: no secret is derived or stored, and the wallet is never primary.
func (s *service) LinkWallet(ctx context.Context, userID, address string) (*Wallet, error) {
	normalized := normalizeArcAddress(address)
	if err := arc.ValidateAddress(normalized); err != nil {
		return nil, err
	}
	w := &Wallet{
		ID:                 uuid.New().String(),
		UserID:             userID,
		PublicKey:          normalized,
		EncryptedSecretKey: []byte{},
		EncryptionNonce:    []byte{},
		WalletType:         WalletTypeLinked,
		IsPrimary:          false,
	}
	if s.repo != nil {
		if err := s.repo.Create(ctx, w); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// GetWalletByAddress looks up the wallet row for a public key (EVM case
// normalized before lookup). Returns (nil, nil) when the address is not
// linked to any wallet.
func (s *service) GetWalletByAddress(ctx context.Context, address string) (*Wallet, error) {
	if s.repo == nil {
		return nil, nil
	}
	w, err := s.repo.FindByPublicKey(ctx, normalizeArcAddress(address))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return w, nil
}

func (s *service) SignTransaction(ctx context.Context, walletID string, passkeySeed []byte, txnXDR string) (string, error) {
	return "", nil
}

func (s *service) GetWallets(ctx context.Context, userID string) ([]Wallet, error) {
	if s.repo == nil {
		return nil, nil
	}
	return s.repo.FindByUserID(ctx, userID)
}

// CheckTransferSettlement implements Service.
func (s *service) CheckTransferSettlement(ctx context.Context, txHash string) (confirmed, reverted bool) {
	if s.arc == nil || strings.TrimSpace(txHash) == "" {
		return false, false
	}
	rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	receipt, err := s.arc.GetTransactionReceipt(rctx, txHash)
	if err != nil || receipt == nil || !receipt.Found {
		return false, false
	}
	if receipt.Status == 1 {
		return true, false
	}
	return false, true
}

func (s *service) GetBalance(ctx context.Context, userID string) (*Balance, error) {
	wallets, err := s.GetWallets(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(wallets) == 0 {
		return nil, fmt.Errorf("no wallet found. Create a wallet first.")
	}
	for _, w := range wallets {
		if strings.HasPrefix(w.PublicKey, "0x") {
			human, err := arc.USDCBalanceOf(ctx, s.arc, s.cfg.ArcUSDCContract, w.PublicKey)
			if err != nil {
				return nil, err
			}
			f, err := strconv.ParseFloat(human, 64)
			if err != nil {
				return nil, fmt.Errorf("parsing balance: %w", err)
			}
			return &Balance{USDC: f}, nil
		}
	}
	// Legacy (non-0x) wallets predate the Arc migration: no chain left to
	// read them from. Surface a migration error, not a chain call.
	return nil, fmt.Errorf("legacy wallet %s is balance-unavailable post-migration (see Phase 2.11)", wallets[0].PublicKey)
}

func (s *service) SendPayment(ctx context.Context, userID string, passkeySeed []byte, destination, asset string, amount float64, memo, ipAddress, userAgent string) (string, error) {
	amt, err := money.FromFloat64(amount)
	if err != nil {
		return "", fmt.Errorf("invalid amount: %w", err)
	}
	if !amt.IsPositive() {
		return "", fmt.Errorf("amount must be positive")
	}
	if asset != "" && asset != "USDC" {
		return "", fmt.Errorf("unsupported asset %q: hosted sends settle in USDC on Arc", asset)
	}
	uid, parseErr := uuid.Parse(userID)
	if s.repo != nil && parseErr != nil {
		return "", fmt.Errorf("invalid user id for audited send")
	}
	// Every rejection below is audit-trailed (failed, with reason): shortfall
	// and probing attempts must be visible, not just successes and throttles.
	auditFail := func(failure string) {
		if s.repo != nil {
			s.auditWithdrawal(ctx, uid, destination, asset, amt.String(), ipAddress, userAgent, "", "failed", failure)
		}
	}
	wallets, err := s.GetWallets(ctx, userID)
	if err != nil {
		auditFail("listing wallets: " + err.Error())
		return "", err
	}
	var hosted *Wallet
	for i := range wallets {
		if wallets[i].WalletType == WalletTypeHosted && strings.HasPrefix(wallets[i].PublicKey, "0x") {
			hosted = &wallets[i]
			break
		}
	}
	if hosted == nil {
		auditFail("no hosted Arc wallet")
		return "", fmt.Errorf("%w. Create one first.", ErrNoHostedWallet)
	}
	// FindByUserID (behind GetWallets) intentionally omits the encrypted
	// secret columns, so the candidate above never carries key material.
	// Re-fetch the secret-bearing row by ID before signing; without this
	// every hosted send fails with "wallet has no encrypted secret key".
	// hosted is non-nil here, which implies s.repo is non-nil.
	full, err := s.repo.FindByID(ctx, hosted.ID)
	if err != nil {
		auditFail("loading hosted wallet: " + err.Error())
		return "", fmt.Errorf("loading hosted wallet for send: %w", err)
	}
	if full == nil {
		auditFail("no hosted Arc wallet")
		return "", fmt.Errorf("%w. Create one first.", ErrNoHostedWallet)
	}
	hosted = full
	// Frozen wallets cannot send (admin moderation for suspicious activity).
	// The check runs before any chain or key touch so a frozen account can
	// never sign or broadcast; deposits (inbound) are unaffected.
	if hosted.Frozen {
		auditFail("wallet frozen by moderation")
		return "", ErrWalletFrozen
	}
	// Quantize to ERC-20 precision BEFORE touching the chain: Money carries
	// 7 decimals, the interface settles 6. Dust below $0.000001 stays in the
	// wallet; everything recorded below uses the floored (actual) value.
	sendAmount, err := arc.FloorToERC20(amt.String())
	if err != nil {
		return "", fmt.Errorf("invalid amount: %w", err)
	}
	if dust, derr := arc.ParseUSDC(sendAmount); derr != nil || dust.Sign() <= 0 {
		auditFail("dust amount below $0.000001 USDC")
		return "", fmt.Errorf("%w: minimum is $0.000001 USDC", ErrAmountTooSmall)
	}
	// Sufficiency pre-check (T020): fail before signing when the on-chain
	// balance cannot cover amount PLUS the network fee. Arc charges gas in
	// USDC from the same balance, so an amount-only check lets full-balance
	// sends through to revert on-chain (burning gas) instead of failing here.
	feeUSDC, err := arc.EstimateTransferFee(ctx, s.arc, s.cfg.ArcUSDCContract, hosted.PublicKey, destination, sendAmount)
	if err != nil {
		auditFail("estimating network fee: " + err.Error())
		return "", fmt.Errorf("estimating network fee: %w", err)
	}
	bal, err := s.chainBalance(ctx, hosted.PublicKey)
	if err != nil {
		// Fail closed: without a balance reading we cannot prove sufficiency,
		// and proceeding would turn into an on-chain revert that still burns
		// gas. The caller should retry once the node answers.
		auditFail("balance read failed: " + err.Error())
		return "", fmt.Errorf("could not verify wallet balance, please try again: %w", err)
	}
	if bal < amt.Float64()+feeUSDC {
		auditFail(fmt.Sprintf("insufficient balance %.2f for %.2f plus fee", bal, amt.Float64()))
		return "", fmt.Errorf("insufficient wallet balance %.2f USDC for %.2f USDC send plus ~%.4f USDC network fee: %w",
			bal, amt.Float64(), feeUSDC, ErrInsufficientFunds)
	}
	key, err := crypto.ParseEncryptionKey(strings.TrimSpace(s.cfg.EncryptionKey))
	if err != nil {
		auditFail("encryption key misconfigured")
		return "", fmt.Errorf("hosted sends require a configured encryption key: %w", err)
	}
	_ = passkeySeed // Reserved for future step-up auth binding; decrypt uses the server key.
	_ = memo        // EVM USDC transfers carry no memo field.
	secret, err := hosted.DecryptSecret(key)
	if err != nil {
		auditFail("decrypting wallet secret: " + err.Error())
		return "", err
	}
	privKey := []byte(secret)
	if len(privKey) != 32 {
		auditFail(fmt.Sprintf("hosted secret has invalid length %d", len(privKey)))
		return "", fmt.Errorf("hosted secret has invalid length %d", len(privKey))
	}

	if s.repo != nil {
		allowed, err := s.repo.CheckRateLimit(ctx, uid)
		if err != nil {
			auditFail("checking rate limit: " + err.Error())
			return "", err
		}
		if !allowed {
			s.auditWithdrawal(ctx, uid, destination, asset, amt.String(), ipAddress, userAgent, "", "failed", "rate limited")
			return "", fmt.Errorf("%w: max 3 per hour", ErrSendRateLimited)
		}
		if err := s.repo.IncrementRateLimit(ctx, uid); err != nil {
			auditFail("recording rate limit: " + err.Error())
			return "", err
		}
	}

	// Daily value cap (C2): count-based throttling alone lets a compromised
	// session exfiltrate without bound. Failed/low-balance sends never reach
	// the chain, so only completed volume counts (see auditWithdrawal).
	if s.cfg.DailySendLimitUSD > 0 {
		spent, err := s.repo.GetDailySpending(ctx, uid)
		if err != nil {
			auditFail("checking daily send limit: " + err.Error())
			return "", fmt.Errorf("checking daily send limit: %w", err)
		}
		if amt.Float64()+spent > s.cfg.DailySendLimitUSD {
			s.auditWithdrawal(ctx, uid, destination, asset, amt.String(), ipAddress, userAgent, "", "failed", "daily send limit exceeded")
			return "", fmt.Errorf("%w: max %.2f USDC per 24h", ErrDailySendLimitExceeded, s.cfg.DailySendLimitUSD)
		}
	}

	txHash, err := arc.USDCTransfer(ctx, s.arc, s.cfg.ArcUSDCContract, privKey, destination, sendAmount)
	for i := range privKey {
		privKey[i] = 0
	}
	if s.repo != nil {
		status, failure := "completed", ""
		if err != nil {
			status, failure = "failed", err.Error()
		}
		s.auditWithdrawal(ctx, uid, destination, asset, sendAmount, ipAddress, userAgent, txHash, status, failure)
	}
	if err != nil {
		return "", err
	}
	return txHash, nil
}

func (s *service) auditWithdrawal(ctx context.Context, userID uuid.UUID, destination, asset, amount, ip, agent, txHash, status, failure string) {
	rec := &WithdrawalRecord{
		ID:          uuid.New(),
		UserID:      userID,
		Destination: destination,
		Asset:       asset,
		Amount:      mustParseAmount(amount),
		Status:      status,
		IPAddress:   ip,
		UserAgent:   agent,
		Failure:     failure,
		TxHash:      txHash,
		CreatedAt:   time.Now(),
	}
	_ = s.repo.RecordWithdrawalAudit(ctx, rec)
}

func mustParseAmount(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func (s *service) DeleteWallet(ctx context.Context, userID, walletID string) error {
	if s.repo == nil {
		return nil
	}
	return s.repo.DeleteByOwner(ctx, walletID, userID)
}

func (s *service) DailyAllowance(ctx context.Context, userID string) (float64, float64, float64, error) {
	limit := s.cfg.DailySendLimitUSD
	if limit <= 0 || s.repo == nil {
		return 0, limit, limit, nil
	}
	uid, err := uuid.Parse(userID)
	if err != nil {
		return 0, 0, limit, fmt.Errorf("invalid user id: %w", err)
	}
	spent, err := s.repo.GetDailySpending(ctx, uid)
	if err != nil {
		return 0, 0, limit, fmt.Errorf("checking daily send volume: %w", err)
	}
	remaining := limit - spent
	if remaining < 0 {
		remaining = 0
	}
	return spent, remaining, limit, nil
}
