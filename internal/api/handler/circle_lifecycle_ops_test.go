package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/circle"
	circleMocks "github.com/deodes/backend/internal/domain/circle/mocks"
	"github.com/deodes/backend/internal/domain/payout"
	payoutMocks "github.com/deodes/backend/internal/domain/payout/mocks"
	"github.com/deodes/backend/pkg/apperrors"
)

func lifecycleTestRouter(h *handler.CircleHandler, userID uuid.UUID, method, path string, route string, handle gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", userID.String())
		c.Next()
	})
	r.Handle(method, route, handle)
	_ = path
	return r
}

func TestCircleHandler_StartCircle_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{})
	cid := uuid.New()
	orgID := uuid.New()

	c := &circle.Circle{
		ID: cid, Name: "Test", Status: circle.CircleStatusPending,
		OrganizerID: orgID,
	}
	repo.On("FindByID", mock.Anything, cid).Return(c, nil)
	repo.On("GetMemberCount", mock.Anything, cid).Return(2, nil)
	repo.On("Update", mock.Anything, mock.AnythingOfType("*circle.Circle")).Return(nil)
	repo.On("SaveRoundConfigSnapshot", mock.Anything, mock.AnythingOfType("*circle.RoundConfigSnapshot")).Return(nil)

	h := handler.NewCircleHandler(svc, nil, nil, nil)
	r := lifecycleTestRouter(h, orgID, "POST", "", "/circles/:id/start", h.StartCircle)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/circles/"+cid.String()+"/start", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), `"success":true`)
	assert.Equal(t, circle.CircleStatusActive, c.Status)
	repo.AssertExpectations(t)
}

func TestCircleHandler_StartCircle_NotOrganizer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{})
	cid := uuid.New()

	c := &circle.Circle{
		ID: cid, Name: "Test", Status: circle.CircleStatusPending,
		OrganizerID: uuid.New(),
	}
	repo.On("FindByID", mock.Anything, cid).Return(c, nil)

	h := handler.NewCircleHandler(svc, nil, nil, nil)
	r := lifecycleTestRouter(h, uuid.New(), "POST", "", "/circles/:id/start", h.StartCircle)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/circles/"+cid.String()+"/start", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, 400, w.Code)
	repo.AssertExpectations(t)
}

func TestCircleHandler_CloseCircle_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{})
	prow := new(payoutMocks.Repository)
	psvc := payout.NewService(prow, nil, nil)
	cid := uuid.New()
	orgID := uuid.New()

	c := &circle.Circle{
		ID: cid, Name: "Test", Status: circle.CircleStatusActive,
		OrganizerID: orgID,
	}
	prow.On("ListByCircle", mock.Anything, mock.AnythingOfType("uuid.UUID"), 1, 1).Return(
		[]payout.Payout{{ID: uuid.New()}}, 1, nil)
	repo.On("FindByID", mock.Anything, cid).Return(c, nil)
	repo.On("Update", mock.Anything, mock.AnythingOfType("*circle.Circle")).Return(nil)

	h := handler.NewCircleHandler(svc, nil, nil, psvc)
	r := lifecycleTestRouter(h, orgID, "POST", "", "/circles/:id/close", h.CloseCircle)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/circles/"+cid.String()+"/close", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), "completed")
	prow.AssertExpectations(t)
	repo.AssertExpectations(t)
}

func TestCircleHandler_CloseCircle_BeforePayout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{})
	prow := new(payoutMocks.Repository)
	psvc := payout.NewService(prow, nil, nil)
	cid := uuid.New()

	prow.On("ListByCircle", mock.Anything, mock.AnythingOfType("uuid.UUID"), 1, 1).Return(
		[]payout.Payout{}, 0, nil)

	h := handler.NewCircleHandler(svc, nil, nil, psvc)
	r := lifecycleTestRouter(h, uuid.New(), "POST", "", "/circles/:id/close", h.CloseCircle)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/circles/"+cid.String()+"/close", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "before its payout")
	prow.AssertExpectations(t)
}

func activeFixedCircle(cid, orgID uuid.UUID) *circle.Circle {
	return &circle.Circle{
		ID: cid, Name: "Test", Status: circle.CircleStatusActive,
		OrganizerID: orgID, PayoutType: circle.PayoutTypeFixed,
		CurrentRound: 2, MaxMembers: 4, ContributionAmount: 100,
	}
}

func TestCircleHandler_TriggerPayout_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{})
	prow := new(payoutMocks.Repository)
	psvc := payout.NewService(prow, nil, svc)
	cid := uuid.New()
	orgID := uuid.New()
	recipient := uuid.New()
	txHash := "0x" + string(bytes.Repeat([]byte("ab"), 32))

	c := activeFixedCircle(cid, orgID)
	member := &circle.CircleMember{CircleID: cid, UserID: recipient, Status: circle.MemberStatusActive}
	repo.On("FindByID", mock.Anything, cid).Return(c, nil)
	repo.On("FindMemberByCircleAndUser", mock.Anything, cid, recipient).Return(member, nil)
	prow.On("FindByTxnHash", mock.Anything, txHash).Return(nil, apperrors.ErrNotFound)
	prow.On("FindByCircleAndRound", mock.Anything, cid, 2).Return(nil, apperrors.ErrNotFound)
	prow.On("Create", mock.Anything, mock.AnythingOfType("*payout.Payout")).Return(nil)

	h := handler.NewCircleHandler(svc, nil, nil, psvc)
	r := lifecycleTestRouter(h, orgID, "POST", "", "/circles/:id/payout", h.TriggerPayout)

	body, _ := json.Marshal(map[string]any{
		"recipientId": recipient.String(),
		"roundNumber": 2,
		"amount":      400.0,
		"feeAmount":   20.0,
		"txnHash":     txHash,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/circles/"+cid.String()+"/payout", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, 201, w.Code)
	prow.AssertExpectations(t)
	repo.AssertExpectations(t)
}

func TestCircleHandler_TriggerPayout_FutureRound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{})
	prow := new(payoutMocks.Repository)
	psvc := payout.NewService(prow, nil, svc)
	cid := uuid.New()
	orgID := uuid.New()

	repo.On("FindByID", mock.Anything, cid).Return(activeFixedCircle(cid, orgID), nil)

	h := handler.NewCircleHandler(svc, nil, nil, psvc)
	r := lifecycleTestRouter(h, orgID, "POST", "", "/circles/:id/payout", h.TriggerPayout)

	body, _ := json.Marshal(map[string]any{
		"recipientId": uuid.New().String(),
		"roundNumber": 9,
		"amount":      400.0,
		"txnHash":     "0x" + string(bytes.Repeat([]byte("ab"), 32)),
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/circles/"+cid.String()+"/payout", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "not due yet")
	repo.AssertExpectations(t)
}

func TestCircleHandler_RemoveMember_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{})
	cid := uuid.New()
	orgID := uuid.New()
	memberID := uuid.New()

	c := &circle.Circle{ID: cid, Name: "Test", Status: circle.CircleStatusActive, OrganizerID: orgID}
	repo.On("FindByID", mock.Anything, cid).Return(c, nil)
	repo.On("FindMemberByCircleAndUser", mock.Anything, cid, memberID).Return(&circle.CircleMember{
		CircleID: cid, UserID: memberID, Status: circle.MemberStatusActive,
	}, nil)
	repo.On("UpdateMemberStatus", mock.Anything, cid, memberID, circle.MemberStatusRemoved).Return(nil)

	h := handler.NewCircleHandler(svc, nil, nil, nil)
	r := lifecycleTestRouter(h, orgID, "POST", "", "/circles/:id/members/:address/remove", h.RemoveMember)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/circles/"+cid.String()+"/members/"+memberID.String()+"/remove", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), `"success":true`)
	repo.AssertExpectations(t)
}

func TestCircleHandler_RemoveMember_NotOrganizer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{})
	cid := uuid.New()
	memberID := uuid.New()

	c := &circle.Circle{ID: cid, Name: "Test", Status: circle.CircleStatusActive, OrganizerID: uuid.New()}
	repo.On("FindByID", mock.Anything, cid).Return(c, nil)

	h := handler.NewCircleHandler(svc, nil, nil, nil)
	r := lifecycleTestRouter(h, uuid.New(), "POST", "", "/circles/:id/members/:address/remove", h.RemoveMember)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/circles/"+cid.String()+"/members/"+memberID.String()+"/remove", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, 403, w.Code)
	repo.AssertExpectations(t)
}
