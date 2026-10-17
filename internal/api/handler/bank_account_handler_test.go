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
	"github.com/deodes/backend/internal/domain/bankaccount"
)

type fakeBankAccountService struct {
	accounts []bankaccount.BankAccount
	addErr   error
}

func (f *fakeBankAccountService) List(_ context.Context, _ string) ([]bankaccount.BankAccount, error) {
	return f.accounts, nil
}

func (f *fakeBankAccountService) Add(_ context.Context, _ string, req bankaccount.CreateAccountRequest) (*bankaccount.BankAccount, error) {
	if f.addErr != nil {
		return nil, f.addErr
	}
	return &bankaccount.BankAccount{
		ID: uuid.New(), BankCode: req.BankCode, BankName: req.BankName,
		AccountNumber: req.AccountNumber, AccountName: req.AccountName, IsDefault: len(f.accounts) == 0,
	}, nil
}

func (f *fakeBankAccountService) Remove(_ context.Context, _, _ string) error { return nil }

func (f *fakeBankAccountService) SetDefault(_ context.Context, _, _ string) error { return nil }

func newBankAccountRouter(svc bankaccount.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := handler.NewBankAccountHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", "user-1"); c.Next() })
	r.GET("/bank-accounts", h.List)
	r.POST("/bank-accounts", h.Create)
	r.POST("/bank-accounts/:id/default", h.SetDefault)
	r.DELETE("/bank-accounts/:id", h.Delete)
	return r
}

func TestBankAccountHandler_List_ReturnsAccounts(t *testing.T) {
	svc := &fakeBankAccountService{accounts: []bankaccount.BankAccount{
		{ID: uuid.New(), BankCode: "058", BankName: "GTBank", AccountNumber: "0123456789", AccountName: "Ada", IsDefault: true},
	}}
	r := newBankAccountRouter(svc)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/bank-accounts", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "GTBank")
}

func TestBankAccountHandler_Create_Valid(t *testing.T) {
	r := newBankAccountRouter(&fakeBankAccountService{})

	body, _ := json.Marshal(map[string]interface{}{
		"bankCode": "058", "bankName": "GTBank", "accountNumber": "0123456789", "accountName": "Ada",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/bank-accounts", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), "0123456789")
}

func TestBankAccountHandler_SetDefault_AndDelete(t *testing.T) {
	r := newBankAccountRouter(&fakeBankAccountService{})
	id := uuid.New().String()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/bank-accounts/"+id+"/default", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/bank-accounts/"+id, nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestBankAccountHandler_UnauthorizedWithoutUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handler.NewBankAccountHandler(&fakeBankAccountService{})
	r := gin.New()
	r.GET("/bank-accounts", h.List)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/bank-accounts", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
