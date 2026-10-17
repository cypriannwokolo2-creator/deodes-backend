package support_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/support"
)

type stubTicketRepo struct {
	rows []support.Ticket
}

func (s *stubTicketRepo) Create(_ context.Context, t *support.Ticket) error {
	s.rows = append(s.rows, *t)
	return nil
}

func (s *stubTicketRepo) List(_ context.Context, f support.TicketFilter) ([]support.Ticket, int, error) {
	return s.rows, len(s.rows), nil
}

func (s *stubTicketRepo) SetStatus(_ context.Context, id uuid.UUID, status support.TicketStatus) error {
	for i := range s.rows {
		if s.rows[i].ID == id {
			s.rows[i].Status = status
			return nil
		}
	}
	return assert.AnError
}

func TestSupportService_CreateTicketValidation(t *testing.T) {
	svc := support.NewService(&stubTicketRepo{})
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		email   string
		message string
	}{
		{"empty email", "", "this message is long enough"},
		{"bad email", "not-an-email", "this message is long enough"},
		{"short message", "a@b.co", "hi"},
		{"long message", "a@b.co", string(make([]byte, 5001))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateTicket(ctx, nil, tc.email, tc.message)
			require.Error(t, err)
		})
	}

	uid := uuid.New()
	tk, err := svc.CreateTicket(ctx, &uid, "user@example.com", "my funds never arrived, please help")
	require.NoError(t, err)
	require.Equal(t, support.TicketStatusOpen, tk.Status)
	require.NotNil(t, tk.UserID)
	assert.Equal(t, uid, *tk.UserID)

	anon, err := svc.CreateTicket(ctx, nil, "anon@example.com", "cannot log in to my account")
	require.NoError(t, err)
	assert.Nil(t, anon.UserID)
}

func TestSupportService_ResolveReopen(t *testing.T) {
	repo := &stubTicketRepo{}
	svc := support.NewService(repo)
	ctx := context.Background()

	tk, err := svc.CreateTicket(ctx, nil, "a@b.co", "please look into my payout")
	require.NoError(t, err)

	require.NoError(t, svc.ResolveTicket(ctx, tk.ID))
	assert.Equal(t, support.TicketStatusResolved, repo.rows[0].Status)

	require.NoError(t, svc.ReopenTicket(ctx, tk.ID))
	assert.Equal(t, support.TicketStatusOpen, repo.rows[0].Status)
}
