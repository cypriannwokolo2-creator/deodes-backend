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
	"github.com/deodes/backend/internal/domain/support"
)

type fakeTicketRepo struct {
	rows []support.Ticket
}

func (f *fakeTicketRepo) Create(_ context.Context, t *support.Ticket) error {
	f.rows = append(f.rows, *t)
	return nil
}

func (f *fakeTicketRepo) List(_ context.Context, filter support.TicketFilter) ([]support.Ticket, int, error) {
	out := []support.Ticket{}
	for _, r := range f.rows {
		if filter.Status != "" && string(r.Status) != filter.Status {
			continue
		}
		out = append(out, r)
	}
	return out, len(out), nil
}

func (f *fakeTicketRepo) SetStatus(_ context.Context, id uuid.UUID, status support.TicketStatus) error {
	for i := range f.rows {
		if f.rows[i].ID == id {
			f.rows[i].Status = status
			return nil
		}
	}
	return assert.AnError
}

func setupSupportRouter(repo *fakeTicketRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := handler.NewSupportHandler(support.NewService(repo), nil)
	r := gin.New()
	// No userID set: the create call exercises the anonymous reporter path.
	r.POST("/support", h.CreateTicket)
	r.GET("/admin/support-tickets", h.ListTickets)
	r.PATCH("/admin/support-tickets/:id", h.UpdateTicket)
	return r
}

func TestSupportHandler_CreateListResolve(t *testing.T) {
	repo := &fakeTicketRepo{}
	r := setupSupportRouter(repo)

	// Anonymous open (no auth middleware here; handler treats missing user as anon).
	body, _ := json.Marshal(map[string]any{"email": "user@example.com", "message": "my payout never arrived, please help"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/support", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"open"`)
	require.Len(t, repo.rows, 1)
	assert.Nil(t, repo.rows[0].UserID)

	// Validation failures are 400, never persisted.
	for _, bad := range []map[string]any{
		{"email": "bad", "message": "long enough message here"},
		{"email": "a@b.co", "message": "short"},
	} {
		raw, _ := json.Marshal(bad)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/support", bytes.NewBuffer(raw))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	}
	assert.Len(t, repo.rows, 1)

	// Admin list + resolve + reopen.
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/admin/support-tickets?status=open", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "user@example.com")

	id := repo.rows[0].ID.String()
	raw, _ := json.Marshal(map[string]any{"status": "resolved"})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/admin/support-tickets/"+id, bytes.NewBuffer(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, support.TicketStatusResolved, repo.rows[0].Status)

	raw, _ = json.Marshal(map[string]any{"status": "bogus"})

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/admin/support-tickets/"+id, bytes.NewBuffer(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
