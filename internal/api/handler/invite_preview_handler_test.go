package handler_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/invite"
	"github.com/deodes/backend/pkg/apperrors"
)

type previewStubService struct {
	invite.Service
	inv *invite.Invite
	err error
}

func (s *previewStubService) Validate(_ context.Context, _ string) (*invite.Invite, error) {
	return s.inv, s.err
}

func TestInviteHandler_Preview(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("valid code returns the frontend preview shape", func(t *testing.T) {
		h := handler.NewInviteHandler(&previewStubService{inv: &invite.Invite{
			ID:        uuid.New(),
			CircleID:  uuid.New(),
			Code:      "ABC123",
			MaxUses:   5,
			UseCount:  1,
			ExpiresAt: sql.NullTime{Time: time.Now().Add(time.Hour).UTC(), Valid: true},
		}})
		r := gin.New()
		r.GET("/circles/invites/:code", h.Preview)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/circles/invites/ABC123", nil)
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "ABC123")
		assert.Contains(t, w.Body.String(), "useCount")
		// sql.NullTime must never leak as an object — frontend needs ISO or null.
		assert.NotContains(t, w.Body.String(), `"Valid"`)
	})

	t.Run("unknown code is 404", func(t *testing.T) {
		h := handler.NewInviteHandler(&previewStubService{err: apperrors.ErrInvalidInvite})
		r := gin.New()
		r.GET("/circles/invites/:code", h.Preview)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/circles/invites/NOPE", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}
