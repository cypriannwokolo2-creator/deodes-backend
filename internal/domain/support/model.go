package support

import (
	"time"

	"github.com/google/uuid"
)

// TicketStatus is the admin workflow state of a support ticket.
type TicketStatus string

const (
	TicketStatusOpen     TicketStatus = "open"
	TicketStatusResolved TicketStatus = "resolved"
)

// Ticket is one contact-form message. UserID is set when the reporter was
// authenticated; anonymous reports carry only the reply-path email.
type Ticket struct {
	ID        uuid.UUID    `json:"id" db:"id"`
	UserID    *uuid.UUID   `json:"userId,omitempty" db:"user_id"`
	Email     string       `json:"email" db:"email"`
	Message   string       `json:"message" db:"message"`
	Status    TicketStatus `json:"status" db:"status"`
	CreatedAt time.Time    `json:"createdAt" db:"created_at"`
	ResolvedAt *time.Time  `json:"resolvedAt,omitempty" db:"resolved_at"`
}

// TicketFilter scopes the admin inbox listing.
type TicketFilter struct {
	Status string
	Page   int
	Limit  int
}
