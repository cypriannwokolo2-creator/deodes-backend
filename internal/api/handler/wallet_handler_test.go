package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/auth"
	"github.com/deodes/backend/internal/domain/notification"
	"github.com/deodes/backend/internal/domain/wallet"
)

// stubLinkAuthService embeds auth.Service so only VerifySignature needs an
// override; any unexpected call fails loudly instead of nil-panicking.
type stubLinkAuthService struct {
	auth.Service
	verify func(ctx context.Context, address, signature string) (bool, error)
}

func (s *stubLinkAuthService) VerifySignature(ctx context.Context, address, signature string) (bool, error) {
	if s.verify == nil {
		return false, fmt.Errorf("unexpected VerifySignature call")
	}
	return s.verify(ctx, address, signature)
}

func TestWalletHandler_Withdraw_WithoutPasskeySeed(t *testing.T) {
	gin.SetMode(gin.TestMode)

	svc := new(mockWalletService)
	svc.On(
		"SendPayment",
		mock.Anything,
		"user-123",
		mock.AnythingOfType("[]uint8"), // empty seed when omitted
		"0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
		"USDC",
		float64(10.5),
		"",
		mock.AnythingOfType("string"),
		mock.AnythingOfType("string"),
	).Return("txhash-wallet", nil)

	h := handler.NewWalletHandler(svc, &stubLinkAuthService{})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "user-123")
		c.Next()
	})
	r.POST("/v1/wallets/withdraw", h.Withdraw)

	// No passkeySeed in the body — must not be rejected.
	body, _ := json.Marshal(map[string]any{
		"destination": "0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
		"asset":       "USDC",
		"amount":      10.5,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/wallets/withdraw", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), "txhash-wallet")
	assert.NotContains(t, w.Body.String(), "passkeySeed")
	svc.AssertExpectations(t)
}

func TestWalletHandler_Withdraw_MemoAndLabel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	post := func(svc *mockWalletService, payload map[string]any) *httptest.ResponseRecorder {
		h := handler.NewWalletHandler(svc, &stubLinkAuthService{})
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("userID", "user-123"); c.Next() })
		r.POST("/v1/wallets/withdraw", h.Withdraw)
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/v1/wallets/withdraw", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	base := func() map[string]any {
		return map[string]any{"destination": "0x742d35Cc6634C0532925a3b844Bc454e4438f44e", "asset": "USDC", "amount": 1.0}
	}

	t.Run("memo and label are passed through and echoed", func(t *testing.T) {
		svc := new(mockWalletService)
		svc.On("SendPayment", mock.Anything, "user-123", mock.Anything, "0x742d35Cc6634C0532925a3b844Bc454e4438f44e", "USDC", float64(1),
			"inv-42", mock.Anything, mock.Anything).Return("txhash-memo", nil)
		p := base()
		p["memo"], p["label"] = "inv-42", "  Rent  "

		w := post(svc, p)

		assert.Equal(t, 200, w.Code)
		assert.Contains(t, w.Body.String(), `"memo":"inv-42"`)
		assert.Contains(t, w.Body.String(), `"label":"Rent"`)
		svc.AssertExpectations(t)
	})

	t.Run("over-long memo is rejected before sending", func(t *testing.T) {
		svc := new(mockWalletService)
		p := base()
		p["memo"] = "this memo is far too long for the memo cap"

		w := post(svc, p)

		assert.Equal(t, 400, w.Code)
		svc.AssertNotCalled(t, "SendPayment")
	})
}

var _ wallet.Service = (*mockWalletService)(nil)

func TestWalletHandler_EnsureHostedArcWallet(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("returns existing hosted wallet without creating", func(t *testing.T) {
		svc := new(mockWalletService)
		existing := &wallet.Wallet{ID: "w-1", UserID: "user-123", PublicKey: "0xabc", WalletType: wallet.WalletTypeHosted}
		svc.On("GetWallets", mock.Anything, "user-123").Return([]wallet.Wallet{*existing}, nil)

		h := handler.NewWalletHandler(svc, &stubLinkAuthService{})
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("userID", "user-123"); c.Next() })
		r.POST("/v1/wallets/hosted-arc/ensure", h.EnsureHostedArcWallet)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/v1/wallets/hosted-arc/ensure", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, 200, w.Code)
		assert.Contains(t, w.Body.String(), "0xabc")
		svc.AssertNotCalled(t, "CreateHostedArcWallet", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("creates hosted wallet when missing", func(t *testing.T) {
		svc := new(mockWalletService)
		svc.On("GetWallets", mock.Anything, "user-123").Return([]wallet.Wallet{}, nil)
		svc.On("DeriveWalletSeed", mock.Anything, "hosted-arc:user-123").Return("seedhex", nil)
		created := &wallet.Wallet{ID: "w-2", UserID: "user-123", PublicKey: "0xdef", WalletType: wallet.WalletTypeHosted}
		svc.On("CreateHostedArcWallet", mock.Anything, "user-123", mock.Anything).Return(created, nil)

		h := handler.NewWalletHandler(svc, &stubLinkAuthService{})
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("userID", "user-123"); c.Next() })
		r.POST("/v1/wallets/hosted-arc/ensure", h.EnsureHostedArcWallet)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/v1/wallets/hosted-arc/ensure", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, 201, w.Code)
		assert.Contains(t, w.Body.String(), "0xdef")
		svc.AssertExpectations(t)
	})
}

const testLinkAddress = "0x742d35Cc6634C0532925a3b844Bc454e4438f44e"

func setupLinkRouter(svc *mockWalletService, authSvc auth.Service) *gin.Engine {
	h := handler.NewWalletHandler(svc, authSvc)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", "user-123"); c.Next() })
	r.POST("/v1/wallets/link", h.LinkWallet)
	return r
}

func postLink(t *testing.T, r *gin.Engine, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(payload)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/wallets/link", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func linkVerifyOK() *stubLinkAuthService {
	return &stubLinkAuthService{verify: func(ctx context.Context, address, sig string) (bool, error) {
		return true, nil
	}}
}

func TestWalletHandler_LinkWallet(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("links a new address with 201", func(t *testing.T) {
		svc := new(mockWalletService)
		svc.On("GetWalletByAddress", mock.Anything, testLinkAddress).Return(nil, nil)
		svc.On("GetWallets", mock.Anything, "user-123").Return([]wallet.Wallet{}, nil)
		linked := &wallet.Wallet{ID: "w-link-1", UserID: "user-123", PublicKey: "0x742d35cc6634c0532925a3b844bc454e4438f44e", WalletType: wallet.WalletTypeLinked}
		svc.On("LinkWallet", mock.Anything, "user-123", testLinkAddress).Return(linked, nil)

		w := postLink(t, setupLinkRouter(svc, linkVerifyOK()), map[string]any{
			"address": testLinkAddress, "signature": "0xdeadbeef",
		})

		assert.Equal(t, 201, w.Code)
		assert.Contains(t, w.Body.String(), "w-link-1")
		assert.Contains(t, w.Body.String(), `"walletType":"linked"`)
		svc.AssertExpectations(t)
	})

	t.Run("rejects an invalid address with 400 before verifying", func(t *testing.T) {
		svc := new(mockWalletService)
		called := false
		authSvc := &stubLinkAuthService{verify: func(ctx context.Context, address, sig string) (bool, error) {
			called = true
			return true, nil
		}}

		w := postLink(t, setupLinkRouter(svc, authSvc), map[string]any{
			"address": "not-an-address", "signature": "0xdeadbeef",
		})

		assert.Equal(t, 400, w.Code)
		assert.False(t, called, "VerifySignature must not run for a malformed address")
		svc.AssertNotCalled(t, "LinkWallet", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("bad signature yields 401 without linking", func(t *testing.T) {
		svc := new(mockWalletService)
		authSvc := &stubLinkAuthService{verify: func(ctx context.Context, address, sig string) (bool, error) {
			return false, nil
		}}

		w := postLink(t, setupLinkRouter(svc, authSvc), map[string]any{
			"address": testLinkAddress, "signature": "0xbad",
		})

		assert.Equal(t, 401, w.Code)
		svc.AssertNotCalled(t, "GetWalletByAddress", mock.Anything, mock.Anything)
		svc.AssertNotCalled(t, "LinkWallet", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("verify error yields 401 without linking", func(t *testing.T) {
		svc := new(mockWalletService)
		authSvc := &stubLinkAuthService{verify: func(ctx context.Context, address, sig string) (bool, error) {
			return false, fmt.Errorf("nonce expired")
		}}

		w := postLink(t, setupLinkRouter(svc, authSvc), map[string]any{
			"address": testLinkAddress, "signature": "0xbad",
		})

		assert.Equal(t, 401, w.Code)
		svc.AssertNotCalled(t, "LinkWallet", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("re-linking your own address is idempotent 200", func(t *testing.T) {
		svc := new(mockWalletService)
		existing := &wallet.Wallet{ID: "w-link-1", UserID: "user-123", PublicKey: "0x742d35cc6634c0532925a3b844bc454e4438f44e", WalletType: wallet.WalletTypeLinked}
		svc.On("GetWalletByAddress", mock.Anything, testLinkAddress).Return(existing, nil)

		w := postLink(t, setupLinkRouter(svc, linkVerifyOK()), map[string]any{
			"address": testLinkAddress, "signature": "0xdeadbeef",
		})

		assert.Equal(t, 200, w.Code)
		assert.Contains(t, w.Body.String(), "w-link-1")
		svc.AssertNotCalled(t, "LinkWallet", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("address linked to another user yields 409", func(t *testing.T) {
		svc := new(mockWalletService)
		taken := &wallet.Wallet{ID: "w-other", UserID: "user-999", PublicKey: "0x742d35cc6634c0532925a3b844bc454e4438f44e", WalletType: wallet.WalletTypeLinked}
		svc.On("GetWalletByAddress", mock.Anything, testLinkAddress).Return(taken, nil)

		w := postLink(t, setupLinkRouter(svc, linkVerifyOK()), map[string]any{
			"address": testLinkAddress, "signature": "0xdeadbeef",
		})

		assert.Equal(t, 409, w.Code)
		svc.AssertNotCalled(t, "LinkWallet", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("a second different address for the same user yields 409", func(t *testing.T) {
		svc := new(mockWalletService)
		other := "0x0000000000000000000000000000000000000001"
		svc.On("GetWalletByAddress", mock.Anything, other).Return(nil, nil)
		mine := wallet.Wallet{ID: "w-link-1", UserID: "user-123", PublicKey: "0x742d35cc6634c0532925a3b844bc454e4438f44e", WalletType: wallet.WalletTypeLinked}
		svc.On("GetWallets", mock.Anything, "user-123").Return([]wallet.Wallet{mine}, nil)

		w := postLink(t, setupLinkRouter(svc, linkVerifyOK()), map[string]any{
			"address": other, "signature": "0xdeadbeef",
		})

		assert.Equal(t, 409, w.Code)
		svc.AssertNotCalled(t, "LinkWallet", mock.Anything, mock.Anything, mock.Anything)
	})
}

func TestWalletHandler_Withdraw_RejectsNonArcDestination(t *testing.T) {
	gin.SetMode(gin.TestMode)

	svc := new(mockWalletService)
	h := handler.NewWalletHandler(svc, &stubLinkAuthService{})
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", "user-123"); c.Next() })
	r.POST("/v1/wallets/withdraw", h.Withdraw)

	body, _ := json.Marshal(map[string]any{
		"destination": "GDEST...",
		"asset":       "USDC",
		"amount":      1.0,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/wallets/withdraw", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "0x Arc address")
	svc.AssertNotCalled(t, "SendPayment", mock.Anything, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestWalletHandler_SendFailureMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	withdrawPayload := map[string]any{"destination": "0x742d35Cc6634C0532925a3b844Bc454e4438f44e", "asset": "USDC", "amount": 5.0}
	transferPayload := map[string]any{"to": "0x1111111111111111111111111111111111111111", "amount": 5.0}

	cases := []struct {
		name     string
		route    string
		payload  map[string]any
		svcErr   error
		wantCode int
		wantBody string
	}{
		{"withdraw insufficient funds is 400", "/v1/wallets/withdraw", withdrawPayload,
			fmt.Errorf("insufficient wallet balance 0.20 USDC for 5.00 USDC send: %w", wallet.ErrInsufficientFunds),
			400, "insufficient wallet balance"},
		{"withdraw missing wallet is 400", "/v1/wallets/withdraw", withdrawPayload,
			fmt.Errorf("%w. Create one first.", wallet.ErrNoHostedWallet),
			400, "no hosted Arc wallet found"},
		{"withdraw rate limit is 429", "/v1/wallets/withdraw", withdrawPayload,
			fmt.Errorf("%w: max 3 per hour", wallet.ErrSendRateLimited),
			429, "withdrawal rate limit exceeded"},
		{"withdraw daily limit is 429", "/v1/wallets/withdraw", withdrawPayload,
			fmt.Errorf("%w: max 100.00 USDC per 24h", wallet.ErrDailySendLimitExceeded),
			429, "daily send limit exceeded"},
		{"withdraw dust amount is 400", "/v1/wallets/withdraw", withdrawPayload,
			fmt.Errorf("%w: minimum is $0.000001 USDC", wallet.ErrAmountTooSmall),
			400, "too small"},
		{"withdraw frozen wallet is 403", "/v1/wallets/withdraw", withdrawPayload,
			wallet.ErrWalletFrozen,
			403, "pending review"},
		{"withdraw chain failure stays 500", "/v1/wallets/withdraw", withdrawPayload,
			fmt.Errorf("broadcast rejected: rpc boom"),
			500, "withdrawal failed"},
		{"transfer insufficient funds is 400", "/v1/wallets/transfer", transferPayload,
			fmt.Errorf("insufficient wallet balance 0.20 USDC for 5.00 USDC send: %w", wallet.ErrInsufficientFunds),
			400, "insufficient wallet balance"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := new(mockWalletService)
			svc.On("SendPayment", mock.Anything, "user-123", mock.Anything, mock.Anything,
				"USDC", float64(5), mock.Anything, mock.Anything, mock.Anything).Return("", tc.svcErr)

			h := handler.NewWalletHandler(svc, &stubLinkAuthService{})
			r := gin.New()
			r.Use(func(c *gin.Context) { c.Set("userID", "user-123"); c.Next() })
			if tc.route == "/v1/wallets/transfer" {
				r.POST(tc.route, h.Transfer)
			} else {
				r.POST(tc.route, h.Withdraw)
			}
			body, _ := json.Marshal(tc.payload)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", tc.route, bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			assert.Equal(t, tc.wantCode, w.Code)
			assert.Contains(t, w.Body.String(), tc.wantBody)
			svc.AssertExpectations(t)
		})
	}
}

// fakeOutcomeWalletRepo embeds wallet.Repository so only the audit-claim
// methods need overrides; any other call nil-panics loudly.
type fakeOutcomeWalletRepo struct {
	wallet.Repository
	rec     *wallet.WithdrawalRecord
	claimed map[string]bool
}

func (f *fakeOutcomeWalletRepo) FindWithdrawalAuditByTxHash(_ context.Context, txHash string) (*wallet.WithdrawalRecord, error) {
	if f.rec != nil && f.rec.TxHash == txHash {
		return f.rec, nil
	}
	return nil, nil
}

func (f *fakeOutcomeWalletRepo) ClaimTransferOutcomeNotification(_ context.Context, txHash string) (*wallet.WithdrawalRecord, error) {
	if f.claimed == nil {
		f.claimed = map[string]bool{}
	}
	if f.rec == nil || f.rec.TxHash != txHash || f.claimed[txHash] {
		return nil, nil
	}
	f.claimed[txHash] = true
	return f.rec, nil
}

type fakeOutcomeNotifier struct {
	notification.Service
	created []notification.CreateInput
}

func (f *fakeOutcomeNotifier) Create(_ context.Context, in notification.CreateInput) (*notification.Notification, error) {
	f.created = append(f.created, in)
	return &notification.Notification{}, nil
}

const testOutcomeTx = "0xb4d7a52d6cef108ff8708bf45bbde67c708482ad0a8092fbf3fb6aa4becf1df7"

func setupOutcomeRouter(svc *mockWalletService, repo *fakeOutcomeWalletRepo, notifier *fakeOutcomeNotifier) *gin.Engine {
	h := handler.NewWalletHandler(svc, &stubLinkAuthService{}).
		WithWalletRepository(repo).
		WithNotificationService(notifier)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", "11111111-1111-1111-1111-111111111111"); c.Next() })
	r.POST("/v1/wallets/transfers/:txHash/outcome", h.ReportTransferOutcome)
	return r
}

func postOutcome(t *testing.T, r *gin.Engine, txHash string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/wallets/transfers/"+txHash+"/outcome", bytes.NewBuffer(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func outcomeRecord() *wallet.WithdrawalRecord {
	return &wallet.WithdrawalRecord{
		UserID:      uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Destination: "0x51b45Ba8570551e0D1fe818227d8bAa8b556694D",
		Asset:       "USDC",
		Amount:      0.19,
		Status:      "completed",
		TxHash:      testOutcomeTx,
	}
}

func TestWalletHandler_ReportTransferOutcome(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("finalized external transfer notifies sender only", func(t *testing.T) {
		svc := new(mockWalletService)
		svc.On("CheckTransferSettlement", mock.Anything, testOutcomeTx).Return(false, false)
		svc.On("GetWalletByAddress", mock.Anything, "0x51b45Ba8570551e0D1fe818227d8bAa8b556694D").Return(nil, nil)
		repo := &fakeOutcomeWalletRepo{rec: outcomeRecord()}
		notifier := &fakeOutcomeNotifier{}

		w := postOutcome(t, setupOutcomeRouter(svc, repo, notifier), testOutcomeTx,
			map[string]any{"status": "finalized"})

		assert.Equal(t, 200, w.Code)
		assert.Contains(t, w.Body.String(), `"notified":true`)
		require.Len(t, notifier.created, 1)
		assert.Equal(t, "11111111-1111-1111-1111-111111111111", notifier.created[0].UserID)
		assert.Equal(t, notification.TypeTransferConfirmed, notifier.created[0].Type)
		assert.Equal(t, notification.ChannelInApp, notifier.created[0].Channel)
		assert.Contains(t, notifier.created[0].Body, "0.19 USDC")
		svc.AssertExpectations(t)
	})

	t.Run("finalized internal transfer also notifies recipient", func(t *testing.T) {
		svc := new(mockWalletService)
		svc.On("CheckTransferSettlement", mock.Anything, testOutcomeTx).Return(false, false)
		svc.On("GetWalletByAddress", mock.Anything, mock.Anything).Return(
			&wallet.Wallet{ID: "w-r", UserID: "22222222-2222-2222-2222-222222222222", PublicKey: "0x51b4"}, nil)
		repo := &fakeOutcomeWalletRepo{rec: outcomeRecord()}
		notifier := &fakeOutcomeNotifier{}

		w := postOutcome(t, setupOutcomeRouter(svc, repo, notifier), testOutcomeTx,
			map[string]any{"status": "finalized"})

		assert.Equal(t, 200, w.Code)
		require.Len(t, notifier.created, 2)
		assert.Equal(t, notification.TypeTransferConfirmed, notifier.created[0].Type)
		assert.Equal(t, "22222222-2222-2222-2222-222222222222", notifier.created[1].UserID)
		assert.Equal(t, notification.TypeTransferReceived, notifier.created[1].Type)
		svc.AssertExpectations(t)
	})

	t.Run("failed transfer notifies sender with reason", func(t *testing.T) {
		svc := new(mockWalletService)
		repo := &fakeOutcomeWalletRepo{rec: outcomeRecord()}
		notifier := &fakeOutcomeNotifier{}

		w := postOutcome(t, setupOutcomeRouter(svc, repo, notifier), testOutcomeTx,
			map[string]any{"status": "failed"})

		assert.Equal(t, 200, w.Code)
		require.Len(t, notifier.created, 1)
		assert.Equal(t, notification.TypeTransferFailed, notifier.created[0].Type)
		assert.Contains(t, notifier.created[0].Body, "rejected by the Arc network")
	})

	t.Run("chain-contradicted finalized claim notifies failed instead", func(t *testing.T) {
		svc := new(mockWalletService)
		svc.On("CheckTransferSettlement", mock.Anything, testOutcomeTx).Return(false, true)
		repo := &fakeOutcomeWalletRepo{rec: outcomeRecord()}
		notifier := &fakeOutcomeNotifier{}

		w := postOutcome(t, setupOutcomeRouter(svc, repo, notifier), testOutcomeTx,
			map[string]any{"status": "finalized"})

		assert.Equal(t, 200, w.Code)
		require.Len(t, notifier.created, 1)
		assert.Equal(t, notification.TypeTransferFailed, notifier.created[0].Type)
		assert.Contains(t, notifier.created[0].Body, "rejected by the Arc network")
		svc.AssertExpectations(t)
	})

	t.Run("duplicate report does not notify twice", func(t *testing.T) {
		svc := new(mockWalletService)
		svc.On("CheckTransferSettlement", mock.Anything, testOutcomeTx).Return(true, false)
		svc.On("GetWalletByAddress", mock.Anything, mock.Anything).Return(nil, nil)
		repo := &fakeOutcomeWalletRepo{rec: outcomeRecord()}
		notifier := &fakeOutcomeNotifier{}
		r := setupOutcomeRouter(svc, repo, notifier)
		body := map[string]any{"status": "finalized"}

		first := postOutcome(t, r, testOutcomeTx, body)
		assert.Equal(t, 200, first.Code)
		second := postOutcome(t, r, testOutcomeTx, body)
		assert.Equal(t, 200, second.Code)
		assert.Contains(t, second.Body.String(), `"notified":false`)
		assert.Len(t, notifier.created, 1)
	})

	t.Run("unknown transaction is 404", func(t *testing.T) {
		svc := new(mockWalletService)
		repo := &fakeOutcomeWalletRepo{}
		notifier := &fakeOutcomeNotifier{}

		w := postOutcome(t, setupOutcomeRouter(svc, repo, notifier), testOutcomeTx,
			map[string]any{"status": "finalized"})

		assert.Equal(t, 404, w.Code)
		assert.Empty(t, notifier.created)
	})

	t.Run("another user's transaction is 404", func(t *testing.T) {
		svc := new(mockWalletService)
		rec := outcomeRecord()
		rec.UserID = uuid.MustParse("99999999-9999-9999-9999-999999999999")
		repo := &fakeOutcomeWalletRepo{rec: rec}
		notifier := &fakeOutcomeNotifier{}

		w := postOutcome(t, setupOutcomeRouter(svc, repo, notifier), testOutcomeTx,
			map[string]any{"status": "finalized"})

		assert.Equal(t, 404, w.Code)
		assert.Empty(t, notifier.created)
	})

	t.Run("bad status and bad hash are 400", func(t *testing.T) {
		svc := new(mockWalletService)
		repo := &fakeOutcomeWalletRepo{rec: outcomeRecord()}
		notifier := &fakeOutcomeNotifier{}
		r := setupOutcomeRouter(svc, repo, notifier)

		w := postOutcome(t, r, testOutcomeTx, map[string]any{"status": "maybe"})
		assert.Equal(t, 400, w.Code)
		w = postOutcome(t, r, "notahash", map[string]any{"status": "finalized"})
		assert.Equal(t, 400, w.Code)
		assert.Empty(t, notifier.created)
	})
}
