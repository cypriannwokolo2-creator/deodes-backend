package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/mobilemoney"
	"github.com/deodes/backend/internal/domain/wallet"
	"github.com/deodes/backend/pkg/apperrors"
)

func setupMobileMoneyRouter(svc mobilemoney.Service, walletSvc wallet.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := handler.NewMobileMoneyHandler(svc, walletSvc)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", "user-1"); c.Next() })
	r.POST("/v1/wallet/mobile-money/onramp", h.InitiateOnramp)
	r.POST("/v1/wallet/mobile-money/offramp", h.InitiateOfframp)
	r.GET("/v1/wallet/mobile-money/providers", h.ListProviders)
	r.GET("/v1/wallet/mobile-money/:id", h.GetTransaction)
	return r
}

type fakeMMService struct {
	onrampFn func(ctx context.Context, userID string, req mobilemoney.OnrampRequest, key string) (*mobilemoney.Transaction, error)
	getFn    func(ctx context.Context, id string) (*mobilemoney.Transaction, error)
}
func (f *fakeMMService) InitiateOnramp(ctx context.Context, userID string, req mobilemoney.OnrampRequest, key string) (*mobilemoney.Transaction, error) {
	return f.onrampFn(ctx, userID, req, key)
}
func (f *fakeMMService) InitiateOfframp(ctx context.Context, userID string, req mobilemoney.OfframpRequest, key string) (*mobilemoney.Transaction, error) {
	return nil, nil
}
func (f *fakeMMService) GetTransaction(ctx context.Context, id string) (*mobilemoney.Transaction, error) {
	return f.getFn(ctx, id)
}
func (f *fakeMMService) Reconcile(ctx context.Context) (int, error) { return 0, nil }
func (f *fakeMMService) ListProviders(ctx context.Context) []mobilemoney.ProviderInfo {
	return []mobilemoney.ProviderInfo{
		{Name: "mpesa", Currency: "KES"},
		{Name: "mtn", Currency: "UGX"},
	}
}
func (f *fakeMMService) GetSupportedCurrencies(ctx context.Context) []string {
	return []string{"KES", "UGX"}
}

// mockMMWalletService is a minimal wallet.Service for mobile-money tests.
type mockMMWalletService struct {
	wallets []wallet.Wallet
}

func (m *mockMMWalletService) DeriveWalletSeed(ctx context.Context, email string) (string, error) {
	return "mock-seed", nil
}
func (m *mockMMWalletService) CreateWallet(ctx context.Context, userID string, passkeySeed []byte) (*wallet.Wallet, error) {
	return nil, nil
}
func (m *mockMMWalletService) CreateHostedArcWallet(ctx context.Context, userID string, passkeySeed []byte) (*wallet.Wallet, error) {
	return nil, nil
}
func (m *mockMMWalletService) LinkWallet(ctx context.Context, userID, address string) (*wallet.Wallet, error) {
	return nil, nil
}
func (m *mockMMWalletService) GetWalletByAddress(ctx context.Context, address string) (*wallet.Wallet, error) {
	return nil, nil
}
func (m *mockMMWalletService) SignTransaction(ctx context.Context, walletID string, passkeySeed []byte, txnXDR string) (string, error) {
	return "", nil
}
func (m *mockMMWalletService) GetWallets(ctx context.Context, userID string) ([]wallet.Wallet, error) {
	return m.wallets, nil
}
func (m *mockMMWalletService) GetBalance(ctx context.Context, userID string) (*wallet.Balance, error) {
	return nil, nil
}
func (m *mockMMWalletService) SendPayment(ctx context.Context, userID string, passkeySeed []byte, destination, asset string, amount float64, memo, ipAddress, userAgent string) (string, error) {
	return "", nil
}
func (m *mockMMWalletService) DeleteWallet(ctx context.Context, userID, walletID string) error {
	return nil
}

func (m *mockMMWalletService) DailyAllowance(ctx context.Context, userID string) (float64, float64, float64, error) {
	return 0, 1000, 1000, nil
}

func (m *mockMMWalletService) CheckTransferSettlement(ctx context.Context, txHash string) (bool, bool) {
	return false, false
}

func TestMobileMoneyHandler_InitiateOnramp_RequiresIdempotencyKey(t *testing.T) {
	mockWallet := &mockMMWalletService{wallets: []wallet.Wallet{{PublicKey: "GABC"}}}
	svc := &fakeMMService{}
	r := setupMobileMoneyRouter(svc, mockWallet)

	body, _ := json.Marshal(map[string]any{"amount": 100, "currency": "KES", "phoneNumber": "254712345678"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/wallet/mobile-money/onramp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "idempotency key")
}

func TestMobileMoneyHandler_InitiateOnramp_Success(t *testing.T) {
	mockWallet := &mockMMWalletService{wallets: []wallet.Wallet{{PublicKey: "GABC"}}}
	svc := &fakeMMService{
		onrampFn: func(ctx context.Context, userID string, req mobilemoney.OnrampRequest, key string) (*mobilemoney.Transaction, error) {
			assert.Equal(t, "user-1", userID)
			assert.Equal(t, "GABC", req.DestinationAddress)
			return &mobilemoney.Transaction{ID: "txn-1", ProviderRef: "ref-1"}, nil
		},
	}
	r := setupMobileMoneyRouter(svc, mockWallet)

	body, _ := json.Marshal(map[string]any{
		"amount": 100, "currency": "KES", "phoneNumber": "254712345678", "idempotencyKey": "key-1",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/wallet/mobile-money/onramp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), "ref-1")
}

func TestMobileMoneyHandler_InitiateOnramp_NoWallet(t *testing.T) {
	mockWallet := &mockMMWalletService{wallets: nil}
	svc := &fakeMMService{}
	r := setupMobileMoneyRouter(svc, mockWallet)

	body, _ := json.Marshal(map[string]any{
		"amount": 100, "currency": "KES", "phoneNumber": "254712345678", "idempotencyKey": "key-1",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/wallet/mobile-money/onramp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestMobileMoneyHandler_GetTransaction_HidesOtherUsersTransaction(t *testing.T) {
	mockWallet := &mockMMWalletService{}
	svc := &fakeMMService{
		getFn: func(ctx context.Context, id string) (*mobilemoney.Transaction, error) {
			return &mobilemoney.Transaction{ID: id, UserID: "someone-else"}, nil
		},
	}
	r := setupMobileMoneyRouter(svc, mockWallet)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/wallet/mobile-money/txn-1", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestMobileMoneyHandler_GetTransaction_NotFound(t *testing.T) {
	mockWallet := &mockMMWalletService{}
	svc := &fakeMMService{
		getFn: func(ctx context.Context, id string) (*mobilemoney.Transaction, error) {
			return nil, apperrors.ErrNotFound
		},
	}
	r := setupMobileMoneyRouter(svc, mockWallet)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/wallet/mobile-money/missing", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestMobileMoneyHandler_ListProviders(t *testing.T) {
	mockWallet := &mockMMWalletService{}
	svc := &fakeMMService{}
	r := setupMobileMoneyRouter(svc, mockWallet)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/wallet/mobile-money/providers", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	data, ok := resp["data"].(map[string]any)
	assert.True(t, ok)
	providers, ok := data["providers"].([]any)
	assert.True(t, ok)
	assert.Len(t, providers, 2)
}
