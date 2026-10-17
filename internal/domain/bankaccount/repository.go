package bankaccount

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/deodes/backend/pkg/apperrors"
)

type Repository interface {
	ListByUserID(ctx context.Context, userID uuid.UUID) ([]BankAccount, error)
	FindByID(ctx context.Context, id, userID uuid.UUID) (*BankAccount, error)
	Create(ctx context.Context, a *BankAccount) error
	Delete(ctx context.Context, id, userID uuid.UUID) error
	ClearDefault(ctx context.Context, userID uuid.UUID) error
	SetDefault(ctx context.Context, id, userID uuid.UUID) error
}

type pgRepo struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) Repository {
	return &pgRepo{db: db}
}

const accountColumns = `id, user_id, bank_code, bank_name, account_number, account_name, is_default, created_at, updated_at`

func (r *pgRepo) ListByUserID(ctx context.Context, userID uuid.UUID) ([]BankAccount, error) {
	accounts := []BankAccount{}
	err := r.db.SelectContext(ctx, &accounts, `
		SELECT `+accountColumns+`
		FROM bank_accounts WHERE user_id = $1 ORDER BY is_default DESC, created_at ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("listing bank accounts: %w", err)
	}
	return accounts, nil
}

func (r *pgRepo) FindByID(ctx context.Context, id, userID uuid.UUID) (*BankAccount, error) {
	var a BankAccount
	err := r.db.GetContext(ctx, &a, `
		SELECT `+accountColumns+`
		FROM bank_accounts WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return nil, fmt.Errorf("finding bank account: %w", err)
	}
	return &a, nil
}

func (r *pgRepo) Create(ctx context.Context, a *BankAccount) error {
	query := `
		INSERT INTO bank_accounts (id, user_id, bank_code, bank_name, account_number, account_name, is_default)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at, updated_at`
	return r.db.QueryRowContext(ctx, query,
		a.ID, a.UserID, a.BankCode, a.BankName, a.AccountNumber, a.AccountName, a.IsDefault,
	).Scan(&a.CreatedAt, &a.UpdatedAt)
}

func (r *pgRepo) Delete(ctx context.Context, id, userID uuid.UUID) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM bank_accounts WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("deleting bank account: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("bank account not found: %w", apperrors.ErrNotFound)
	}
	return nil
}

func (r *pgRepo) ClearDefault(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE bank_accounts SET is_default = FALSE, updated_at = NOW() WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("clearing default bank account: %w", err)
	}
	return nil
}

func (r *pgRepo) SetDefault(ctx context.Context, id, userID uuid.UUID) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE bank_accounts SET is_default = TRUE, updated_at = NOW() WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("setting default bank account: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("bank account not found: %w", apperrors.ErrNotFound)
	}
	return nil
}
