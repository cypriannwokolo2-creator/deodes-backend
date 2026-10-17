package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/circle"
	"github.com/deodes/backend/internal/domain/contribution"
	"github.com/deodes/backend/pkg/apperrors"
)

// contributeCircleStub embeds circle.Service so only Get needs overriding.
type contributeCircleStub struct {
	circle.Service
	cir *circle.Circle
	err error
}

func (s *contributeCircleStub) Get(_ context.Context, _ string) (*circle.Circle, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.cir, nil
}

// contributeSvcStub embeds contribution.Service so only Record needs overriding.
type contributeSvcStub struct {
	contribution.Service
	recordFn func(ctx context.Context, input contribution.RecordInput) (*contribution.Contribution, error)
}

func (s *contributeSvcStub) Record(ctx context.Context, input contribution.RecordInput) (*contribution.Contribution, error) {
	return s.recordFn(ctx, input)
}

func newContributeRouter(circleSvc circle.Service, contribSvc contribution.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := handler.NewCircleHandler(circleSvc, nil, contribSvc, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", c.GetHeader("X-Test-User"))
		c.Next()
	})
	r.POST("/circles/:id/contribute", h.Contribute)
	return r
}

func doContribute(t *testing.T, r *gin.Engine, circleID, userID string, body any) (int, map[string]any) {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/circles/"+circleID+"/contribute", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", userID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return w.Code, resp
}

func testContributeCircle(amount float64) *circle.Circle {
	return &circle.Circle{
		ID:                 uuid.New(),
		Name:               "Test Circle",
		CircleType:         circle.CircleTypePublic,
		Currency:           circle.CurrencyUSDC,
		ContributionAmount: amount,
		Status:             circle.CircleStatusActive,
		CurrentRound:       2,
	}
}

const validArcHash = "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

func TestContribute_Success(t *testing.T) {
	cir := testContributeCircle(100)
	userID := uuid.New().String()
	circleSvc := &contributeCircleStub{cir: cir}
	contribSvc := &contributeSvcStub{recordFn: func(_ context.Context, in contribution.RecordInput) (*contribution.Contribution, error) {
		assert.Equal(t, cir.ID.String(), in.CircleID)
		assert.Equal(t, userID, in.UserID)
		assert.Equal(t, 100.0, in.Amount)
		assert.Equal(t, 2, in.RoundNumber)
		return &contribution.Contribution{ID: uuid.New(), Amount: in.Amount, RoundNumber: in.RoundNumber}, nil
	}}
	r := newContributeRouter(circleSvc, contribSvc)
	code, _ := doContribute(t, r, cir.ID.String(), userID, map[string]any{
		"amount": 100, "txnHash": validArcHash, "roundNumber": 2,
	})
	assert.Equal(t, http.StatusCreated, code)
}

func TestContribute_RejectsMissingAuth(t *testing.T) {
	cir := testContributeCircle(100)
	r := newContributeRouter(&contributeCircleStub{cir: cir}, &contributeSvcStub{recordFn: func(_ context.Context, _ contribution.RecordInput) (*contribution.Contribution, error) {
		return &contribution.Contribution{}, nil
	}})
	code, _ := doContribute(t, r, cir.ID.String(), "", map[string]any{
		"amount": 100, "txnHash": validArcHash, "roundNumber": 1,
	})
	assert.Equal(t, http.StatusUnauthorized, code)
}

func TestContribute_RejectsInvalidCircleID(t *testing.T) {
	r := newContributeRouter(&contributeCircleStub{}, &contributeSvcStub{})
	code, _ := doContribute(t, r, "not-a-uuid", uuid.New().String(), map[string]any{
		"amount": 100, "txnHash": validArcHash, "roundNumber": 1,
	})
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestContribute_RejectsNonArcTxnHash(t *testing.T) {
	cir := testContributeCircle(100)
	r := newContributeRouter(&contributeCircleStub{cir: cir}, &contributeSvcStub{})
	for _, bad := range []string{"txn-abc123", "0x123", "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"} {
		code, _ := doContribute(t, r, cir.ID.String(), uuid.New().String(), map[string]any{
			"amount": 100, "txnHash": bad, "roundNumber": 1,
		})
		assert.NotEqual(t, http.StatusCreated, code, "hash %q must not succeed", bad)
	}
}

func TestContribute_RejectsWrongAmount(t *testing.T) {
	cir := testContributeCircle(100)
	called := false
	r := newContributeRouter(&contributeCircleStub{cir: cir}, &contributeSvcStub{recordFn: func(_ context.Context, _ contribution.RecordInput) (*contribution.Contribution, error) {
		called = true
		return &contribution.Contribution{}, nil
	}})
	code, _ := doContribute(t, r, cir.ID.String(), uuid.New().String(), map[string]any{
		"amount": 50, "txnHash": validArcHash, "roundNumber": 1,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, code)
	assert.False(t, called, "service must not be called on amount mismatch")
}

func TestContribute_RejectsNonUSDC(t *testing.T) {
	cir := testContributeCircle(100)
	cir.Currency = circle.CircleCurrency("XLM")
	r := newContributeRouter(&contributeCircleStub{cir: cir}, &contributeSvcStub{})
	code, _ := doContribute(t, r, cir.ID.String(), uuid.New().String(), map[string]any{
		"amount": 100, "txnHash": validArcHash, "roundNumber": 1,
	})
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestContribute_MapsNotMemberTo403(t *testing.T) {
	cir := testContributeCircle(100)
	r := newContributeRouter(&contributeCircleStub{cir: cir}, &contributeSvcStub{recordFn: func(_ context.Context, _ contribution.RecordInput) (*contribution.Contribution, error) {
		return nil, errors.New("user is not a member of this circle")
	}})
	code, _ := doContribute(t, r, cir.ID.String(), uuid.New().String(), map[string]any{
		"amount": 100, "txnHash": validArcHash, "roundNumber": 1,
	})
	assert.Equal(t, http.StatusForbidden, code)
}

func TestContribute_MapsLateContributionTo422(t *testing.T) {
	cir := testContributeCircle(100)
	r := newContributeRouter(&contributeCircleStub{cir: cir}, &contributeSvcStub{recordFn: func(_ context.Context, _ contribution.RecordInput) (*contribution.Contribution, error) {
		return nil, apperrors.ErrLateContributionRejected
	}})
	code, _ := doContribute(t, r, cir.ID.String(), uuid.New().String(), map[string]any{
		"amount": 100, "txnHash": validArcHash, "roundNumber": 1,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, code)
}

func TestContribute_MapsVerificationFailureTo422(t *testing.T) {
	cir := testContributeCircle(100)
	r := newContributeRouter(&contributeCircleStub{cir: cir}, &contributeSvcStub{recordFn: func(_ context.Context, _ contribution.RecordInput) (*contribution.Contribution, error) {
		return nil, errors.New("on-chain verification failed: txnHash 0xabc does not match expected sender/amount")
	}})
	code, _ := doContribute(t, r, cir.ID.String(), uuid.New().String(), map[string]any{
		"amount": 100, "txnHash": validArcHash, "roundNumber": 1,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, code)
}
