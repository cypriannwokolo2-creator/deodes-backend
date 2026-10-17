package bankaccount

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/deodes/backend/pkg/apperrors"
)

// Validation bounds mirror the settings-screen add form: 3-digit bank code,
// 10-digit NGN account number, short human names.
var (
	bankCodeRe      = regexp.MustCompile(`^[0-9]{3}$`)
	accountNumberRe = regexp.MustCompile(`^[0-9]{10}$`)
)

const (
	maxBankNameLen    = 80
	maxAccountNameLen = 60
)

type Service interface {
	List(ctx context.Context, userID string) ([]BankAccount, error)
	Add(ctx context.Context, userID string, req CreateAccountRequest) (*BankAccount, error)
	Remove(ctx context.Context, userID, id string) error
	SetDefault(ctx context.Context, userID, id string) error
}

type service struct {
	repo Repository
}

func NewService(repo Repository) Service {
	return &service{repo: repo}
}

func invalidInput(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, apperrors.ErrInvalidInput)...)
}

func parseUserID(userID string) (uuid.UUID, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid user id: %w", apperrors.ErrInvalidInput)
	}
	return uid, nil
}

func mapLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, apperrors.ErrNotFound) {
		return fmt.Errorf("bank account not found: %w", apperrors.ErrNotFound)
	}
	return err
}

func (s *service) List(ctx context.Context, userID string) ([]BankAccount, error) {
	uid, err := parseUserID(userID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListByUserID(ctx, uid)
}

func (s *service) Add(ctx context.Context, userID string, req CreateAccountRequest) (*BankAccount, error) {
	uid, err := parseUserID(userID)
	if err != nil {
		return nil, err
	}

	bankCode := strings.TrimSpace(req.BankCode)
	if !bankCodeRe.MatchString(bankCode) {
		return nil, invalidInput("bankCode must be a 3-digit bank code")
	}
	accountNumber := strings.TrimSpace(req.AccountNumber)
	if !accountNumberRe.MatchString(accountNumber) {
		return nil, invalidInput("accountNumber must be exactly 10 digits")
	}
	accountName := strings.TrimSpace(req.AccountName)
	if accountName == "" || len(accountName) > maxAccountNameLen {
		return nil, invalidInput("accountName is required (max %d chars)", maxAccountNameLen)
	}
	bankName := strings.TrimSpace(req.BankName)
	if bankName == "" {
		bankName = bankCode
	}
	if len(bankName) > maxBankNameLen {
		return nil, invalidInput("bankName is too long (max %d chars)", maxBankNameLen)
	}

	existing, err := s.repo.ListByUserID(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("listing bank accounts: %w", err)
	}

	a := &BankAccount{
		ID:            uuid.New(),
		UserID:        uid,
		BankCode:      bankCode,
		BankName:      bankName,
		AccountNumber: accountNumber,
		AccountName:   accountName,
		IsDefault:     len(existing) == 0,
	}
	if err := s.repo.Create(ctx, a); err != nil {
		return nil, fmt.Errorf("saving bank account: %w", err)
	}
	return a, nil
}

func (s *service) Remove(ctx context.Context, userID, id string) error {
	uid, err := parseUserID(userID)
	if err != nil {
		return err
	}
	aid, err := uuid.Parse(id)
	if err != nil {
		return invalidInput("invalid bank account id")
	}
	if err := s.repo.Delete(ctx, aid, uid); err != nil {
		return mapLookupError(err)
	}
	return nil
}

func (s *service) SetDefault(ctx context.Context, userID, id string) error {
	uid, err := parseUserID(userID)
	if err != nil {
		return err
	}
	aid, err := uuid.Parse(id)
	if err != nil {
		return invalidInput("invalid bank account id")
	}
	if _, err := s.repo.FindByID(ctx, aid, uid); err != nil {
		return mapLookupError(err)
	}
	if err := s.repo.ClearDefault(ctx, uid); err != nil {
		return fmt.Errorf("clearing default bank account: %w", err)
	}
	if err := s.repo.SetDefault(ctx, aid, uid); err != nil {
		return mapLookupError(err)
	}
	return nil
}
