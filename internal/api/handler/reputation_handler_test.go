package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/reputation"
)

type stubReputationService struct {
	reputation.Service
	leaders []reputation.LeaderboardEntry
}
func (s *stubReputationService) TopScores(_ context.Context, _ int) ([]reputation.LeaderboardEntry, error) {
	return s.leaders, nil
}

func TestReputationHandler_Leaderboard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	name := "Ada"
	h := handler.NewReputationHandler(&stubReputationService{leaders: []reputation.LeaderboardEntry{
		{UserID: uuid.New(), Username: &name, Score: 900, Level: "gold"},
	}})
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", "user-1"); c.Next() })
	r.GET("/reputation/leaderboard", h.Leaderboard)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/reputation/leaderboard?limit=10", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Ada")
	assert.NotContains(t, w.Body.String(), "email")
}

type stubTierService struct {
	reputation.Service
	stored *reputation.ReputationSnapshot
}

func (s *stubTierService) LatestScore(_ context.Context, _ string) (*reputation.ReputationSnapshot, error) {
	if s.stored == nil {
		return nil, assert.AnError
	}
	return s.stored, nil
}

// The tier endpoint must serve the stored snapshot (earned history), not a
// fresh zero-input calculation (which always yields recency-only 150).
func TestReputationHandler_TierByAddress_PrefersStoredSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	addr := uuid.New()
	h := handler.NewReputationHandler(&stubTierService{stored: &reputation.ReputationSnapshot{
		UserID: addr,
		Score:  640,
		Level:  "Platinum",
	}})
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", "user-1"); c.Next() })
	r.GET("/reputation/tier/:address", h.GetTierByAddress)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/reputation/tier/"+addr.String(), nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "640")
	assert.Contains(t, w.Body.String(), "Platinum")
}
