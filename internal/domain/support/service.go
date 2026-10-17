package support

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Service is the support-ticket use-case layer.
type Service interface {
	// CreateTicket persists a contact message; userID is nil for anonymous
	// reporters. Email/message are validated and capped.
	CreateTicket(ctx context.Context, userID *uuid.UUID, email, message string) (*Ticket, error)
	// ListTickets pages the admin inbox, newest first.
	ListTickets(ctx context.Context, filter TicketFilter) ([]Ticket, int, error)
	// ResolveTicket marks a ticket resolved; ReopenTicket returns it to open.
	ResolveTicket(ctx context.Context, id uuid.UUID) error
	ReopenTicket(ctx context.Context, id uuid.UUID) error
}

type service struct {
	repo Repository
}

// NewService wires the ticket use cases.
func NewService(repo Repository) Service {
	return &service{repo: repo}
}

func validEmail(email string) bool {
	email = strings.TrimSpace(email)
	if len(email) == 0 || len(email) > 254 {
		return false
	}
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return false
	}
	return strings.Contains(email[at:], ".")
}

func (s *service) CreateTicket(ctx context.Context, userID *uuid.UUID, email, message string) (*Ticket, error) {
	email = strings.TrimSpace(email)
	message = strings.TrimSpace(message)
	if !validEmail(email) {
		return nil, fmt.Errorf("a valid reply email is required")
	}
	if len(message) < 10 {
		return nil, fmt.Errorf("message must be at least 10 characters")
	}
	if len(message) > 5000 {
		return nil, fmt.Errorf("message must be at most 5000 characters")
	}
	t := &Ticket{
		ID:        uuid.New(),
		UserID:    userID,
		Email:     email,
		Message:   message,
		Status:    TicketStatusOpen,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.repo.Create(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *service) ListTickets(ctx context.Context, filter TicketFilter) ([]Ticket, int, error) {
	return s.repo.List(ctx, filter)
}

func (s *service) ResolveTicket(ctx context.Context, id uuid.UUID) error {
	return s.repo.SetStatus(ctx, id, TicketStatusResolved)
}

func (s *service) ReopenTicket(ctx context.Context, id uuid.UUID) error {
	return s.repo.SetStatus(ctx, id, TicketStatusOpen)
}
