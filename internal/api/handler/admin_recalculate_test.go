package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/user"
	userMocks "github.com/deodes/backend/internal/domain/user/mocks"
)

type stubRefresher struct {
	scores map[string]int
	errFor map[string]error
	calls  []string
}

func (s *stubRefresher) RefreshUser(_ context.Context, userID string) (int, error) {
	s.calls = append(s.calls, userID)
	if err, ok := s.errFor[userID]; ok {
		return 0, err
	}
	score := s.scores[userID]
	return score, nil
}

func TestAdminHandler_RecalculateScores_UnwiredReportsError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handler.NewAdminHandler(nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.POST("/admin/reputation/recalculate", h.RecalculateScores)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/admin/reputation/recalculate", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "score pipeline not configured")
}

func TestAdminHandler_RecalculateScores_SingleUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	userRepo := new(userMocks.Repository)
	h := handler.NewAdminHandler(nil, userRepo, nil, nil, nil, nil, nil).
		WithScoreRefresher(&stubRefresher{scores: map[string]int{}})
	r := gin.New()
	r.POST("/admin/reputation/recalculate", h.RecalculateScores)

	target := uuid.New().String()
	refresher := &stubRefresher{scores: map[string]int{target: 420}}
	h.WithScoreRefresher(refresher)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/admin/reputation/recalculate?user_id="+target, nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data struct {
			Score   int `json:"score"`
			Updated int `json:"updated"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, 420, body.Data.Score)
	assert.Equal(t, 1, body.Data.Updated)
	require.Len(t, refresher.calls, 1)
}

func TestAdminHandler_RecalculateScores_SingleUserBadUUID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	userRepo := new(userMocks.Repository)
	h := handler.NewAdminHandler(nil, userRepo, nil, nil, nil, nil, nil).
		WithScoreRefresher(&stubRefresher{})
	r := gin.New()
	r.POST("/admin/reputation/recalculate", h.RecalculateScores)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/admin/reputation/recalculate?user_id=nope", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAdminHandler_RecalculateScores_AllUsersContinuesPastFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	userRepo := new(userMocks.Repository)
	h := handler.NewAdminHandler(nil, userRepo, nil, nil, nil, nil, nil)
	r := gin.New()
	r.POST("/admin/reputation/recalculate", h.RecalculateScores)

	u1 := user.User{ID: uuid.New()}
	u2 := user.User{ID: uuid.New()}
	userRepo.On("List", mock.Anything, user.UserFilter{Page: 1, Limit: 100}).Return([]user.User{u1, u2}, nil)
	refresher := &stubRefresher{
		scores: map[string]int{u1.ID.String(): 300},
		errFor: map[string]error{u2.ID.String(): assert.AnError},
	}
	h.WithScoreRefresher(refresher)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/admin/reputation/recalculate", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data struct {
			Updated int `json:"updated"`
			Failed  int `json:"failed"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, 1, body.Data.Updated)
	assert.Equal(t, 1, body.Data.Failed)
	userRepo.AssertExpectations(t)
}
