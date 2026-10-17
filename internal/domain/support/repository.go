package support

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Repository persists support tickets.
type Repository interface {
	Create(ctx context.Context, t *Ticket) error
	List(ctx context.Context, filter TicketFilter) ([]Ticket, int, error)
	SetStatus(ctx context.Context, id uuid.UUID, status TicketStatus) error
}

type pgRepo struct {
	db *sqlx.DB
}

// NewRepository wires the postgres ticket store.
func NewRepository(db *sqlx.DB) Repository {
	return &pgRepo{db: db}
}

func (r *pgRepo) Create(ctx context.Context, t *Ticket) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	_, err := r.db.NamedExecContext(ctx, `
		INSERT INTO support_tickets (id, user_id, email, message, status)
		VALUES (:id, :user_id, :email, :message, :status)`, t)
	if err != nil {
		return fmt.Errorf("creating support ticket: %w", err)
	}
	return nil
}

func (r *pgRepo) List(ctx context.Context, filter TicketFilter) ([]Ticket, int, error) {
	page, limit := 1, 20
	if filter.Page > 0 {
		page = filter.Page
	}
	if filter.Limit > 0 && filter.Limit <= 100 {
		limit = filter.Limit
	}
	offset := (page - 1) * limit

	conds := []string{}
	args := []any{}
	if filter.Status == string(TicketStatusOpen) || filter.Status == string(TicketStatusResolved) {
		args = append(args, filter.Status)
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := r.db.QueryRowxContext(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM support_tickets %s", where), args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting support tickets: %w", err)
	}
	var tickets []Ticket
	err := r.db.SelectContext(ctx, &tickets,
		fmt.Sprintf(`SELECT id, user_id, email, message, status, created_at, resolved_at
			FROM support_tickets %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`,
			where, len(args)+1, len(args)+2),
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing support tickets: %w", err)
	}
	if tickets == nil {
		tickets = []Ticket{}
	}
	return tickets, total, nil
}

func (r *pgRepo) SetStatus(ctx context.Context, id uuid.UUID, status TicketStatus) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE support_tickets SET status = $2, resolved_at = CASE WHEN $2 = 'resolved' THEN NOW() ELSE NULL END WHERE id = $1`,
		id, string(status))
	if err != nil {
		return fmt.Errorf("updating support ticket: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
