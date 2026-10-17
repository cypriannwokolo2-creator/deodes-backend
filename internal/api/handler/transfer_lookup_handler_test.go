package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/user"
	"github.com/deodes/backend/internal/domain/wallet"
	"github.com/deodes/backend/pkg/apperrors"
)

type stubUserService struct {
	user.Service
	lookup *user.User
	lookupErr error
}

func (s *stubUserService) GetByEmail(_ context.Context, _ string) (*user.User, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	return s.lookup, nil
}

func TestUserHandler_LookupByEmail_Found(t *testing.T) {
	gin.SetMode(gin.TestMode)
	name := "Ada"
	svc := &stubUserService{lookup: &user.User{ID: uuid.New(), DisplayName: &name, EmailVerified: true}}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", "user-1"); c.Next() })
	// Wire through the real constructor path used in production.
	uh := handler.NewUserHandler(svc)
	r.GET("/users/lookup", uh.LookupByEmail)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/users/lookup?email=ada@example.com", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Ada")
}

func TestUserHandler_LookupByEmail_BadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	uh := handler.NewUserHandler(&stubUserService{})
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", "user-1"); c.Next() })
	r.GET("/users/lookup", uh.LookupByEmail)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/users/lookup", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

type stubWalletService struct {
	wallet.Service
	walletsByUser map[string][]wallet.Wallet
	sentTo        string
	sentAmount    float64
}

func (s *stubWalletService) GetWallets(_ context.Context, userID string) ([]wallet.Wallet, error) {
	return s.walletsByUser[userID], nil
}

func (s *stubWalletService) SendPayment(_ context.Context, _ string, _ []byte, destination, _ string, amount float64, _, _, _ string) (string, error) {
	s.sentTo = destination
	s.sentAmount = amount
	return "0xtxhash", nil
}

func (s *stubWalletService) DailyAllowance(_ context.Context, _ string) (float64, float64, float64, error) {
	return 0, 1000, 1000, nil
}

type stubUserRepo struct {
	user.Repository
	byEmail map[string]*user.User
}

func (r *stubUserRepo) FindByEmail(_ context.Context, email string) (*user.User, error) {
	if u, ok := r.byEmail[email]; ok {
		return u, nil
	}
	return nil, apperrors.ErrNotFound
}

func newTransferRouter(svc *stubWalletService, repo *stubUserRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := handler.NewWalletHandler(svc, nil).WithUserRepository(repo)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", "sender-1"); c.Next() })
	r.POST("/wallets/transfer", h.Transfer)
	return r
}

func TestWalletHandler_Transfer_ExternalAddress(t *testing.T) {
	svc := &stubWalletService{walletsByUser: map[string][]wallet.Wallet{}}
	r := newTransferRouter(svc, &stubUserRepo{byEmail: map[string]*user.User{}})

	body, _ := json.Marshal(map[string]interface{}{"to": "0x70A7CCB7b5fe856384Aab3aAF553E09A10AF14A4", "amount": 10})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/wallets/transfer", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "0x70A7CCB7b5fe856384Aab3aAF553E09A10AF14A4", svc.sentTo)
	assert.Equal(t, 10.0, svc.sentAmount)
	assert.Contains(t, w.Body.String(), "0xtxhash")
}

func TestWalletHandler_Transfer_InternalEmail(t *testing.T) {
	rid := uuid.New()
	name := "Ada"
	svc := &stubWalletService{walletsByUser: map[string][]wallet.Wallet{
		rid.String(): {{PublicKey: "0xRecipient00000000000000000000000000000001", WalletType: wallet.WalletTypeHosted}},
	}}
	repo := &stubUserRepo{byEmail: map[string]*user.User{
		"ada@example.com": {ID: rid, DisplayName: &name, EmailVerified: true},
	}}
	r := newTransferRouter(svc, repo)

	body, _ := json.Marshal(map[string]interface{}{"to": "ada@example.com", "amount": 5})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/wallets/transfer", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "0xRecipient00000000000000000000000000000001", svc.sentTo)
	assert.Contains(t, w.Body.String(), "Ada")
}

func TestWalletHandler_Transfer_UnknownEmail(t *testing.T) {
	svc := &stubWalletService{walletsByUser: map[string][]wallet.Wallet{}}
	r := newTransferRouter(svc, &stubUserRepo{byEmail: map[string]*user.User{}})

	body, _ := json.Marshal(map[string]interface{}{"to": "ghost@example.com", "amount": 5})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/wallets/transfer", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Empty(t, svc.sentTo)
}

func TestWalletHandler_Transfer_InvalidTo(t *testing.T) {
	svc := &stubWalletService{walletsByUser: map[string][]wallet.Wallet{}}
	r := newTransferRouter(svc, &stubUserRepo{byEmail: map[string]*user.User{}})

	body, _ := json.Marshal(map[string]interface{}{"to": "not-an-address", "amount": 5})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/wallets/transfer", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
