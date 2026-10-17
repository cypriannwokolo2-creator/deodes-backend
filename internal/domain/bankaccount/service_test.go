package bankaccount_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/bankaccount"
	"github.com/deodes/backend/pkg/apperrors"
)

// fakeRepo is an in-memory Repository: ownership is enforced by keying on
// (id, userID), mirroring the WHERE id = $1 AND user_id = $2 clauses.
type fakeRepo struct {
	accounts map[uuid.UUID]bankaccount.BankAccount
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{accounts: map[uuid.UUID]bankaccount.BankAccount{}}
}

func (f *fakeRepo) ListByUserID(_ context.Context, userID uuid.UUID) ([]bankaccount.BankAccount, error) {
	var out []bankaccount.BankAccount
	for _, a := range f.accounts {
		if a.UserID == userID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeRepo) FindByID(_ context.Context, id, userID uuid.UUID) (*bankaccount.BankAccount, error) {
	a, ok := f.accounts[id]
	if !ok || a.UserID != userID {
		return nil, apperrors.ErrNotFound
	}
	return &a, nil
}

func (f *fakeRepo) Create(_ context.Context, a *bankaccount.BankAccount) error {
	f.accounts[a.ID] = *a
	return nil
}

func (f *fakeRepo) Delete(_ context.Context, id, userID uuid.UUID) error {
	a, ok := f.accounts[id]
	if !ok || a.UserID != userID {
		return apperrors.ErrNotFound
	}
	delete(f.accounts, id)
	return nil
}

func (f *fakeRepo) ClearDefault(_ context.Context, userID uuid.UUID) error {
	for id, a := range f.accounts {
		if a.UserID == userID && a.IsDefault {
			a.IsDefault = false
			f.accounts[id] = a
		}
	}
	return nil
}

func (f *fakeRepo) SetDefault(_ context.Context, id, userID uuid.UUID) error {
	a, ok := f.accounts[id]
	if !ok || a.UserID != userID {
		return apperrors.ErrNotFound
	}
	a.IsDefault = true
	f.accounts[id] = a
	return nil
}

func TestBankAccountService_Add_FirstIsDefault(t *testing.T) {
	svc := bankaccount.NewService(newFakeRepo())
	ctx := context.Background()
	uid := uuid.New().String()

	a, err := svc.Add(ctx, uid, bankaccount.CreateAccountRequest{
		BankCode: "058", BankName: "GTBank", AccountNumber: "0123456789", AccountName: "Ada",
	})
	require.NoError(t, err)
	assert.True(t, a.IsDefault)

	b, err := svc.Add(ctx, uid, bankaccount.CreateAccountRequest{
		BankCode: "033", BankName: "UBA", AccountNumber: "9876543210", AccountName: "Ada",
	})
	require.NoError(t, err)
	assert.False(t, b.IsDefault)
}

func TestBankAccountService_Add_Validation(t *testing.T) {
	svc := bankaccount.NewService(newFakeRepo())
	ctx := context.Background()
	uid := uuid.New().String()

	for _, req := range []bankaccount.CreateAccountRequest{
		{BankCode: "05", BankName: "X", AccountNumber: "0123456789", AccountName: "Ada"},
		{BankCode: "058", BankName: "X", AccountNumber: "123", AccountName: "Ada"},
		{BankCode: "058", BankName: "X", AccountNumber: "0123456789", AccountName: ""},
	} {
		_, err := svc.Add(ctx, uid, req)
		assert.ErrorIs(t, err, apperrors.ErrInvalidInput)
	}
}

func TestBankAccountService_SetDefault_Switches(t *testing.T) {
	repo := newFakeRepo()
	svc := bankaccount.NewService(repo)
	ctx := context.Background()
	uid := uuid.New().String()

	_, err := svc.Add(ctx, uid, bankaccount.CreateAccountRequest{BankCode: "058", AccountNumber: "0123456789", AccountName: "Ada"})
	require.NoError(t, err)
	b, err := svc.Add(ctx, uid, bankaccount.CreateAccountRequest{BankCode: "033", AccountNumber: "9876543210", AccountName: "Ada"})
	require.NoError(t, err)

	require.NoError(t, svc.SetDefault(ctx, uid, b.ID.String()))

	accounts, err := svc.List(ctx, uid)
	require.NoError(t, err)
	require.Len(t, accounts, 2)
	for _, acc := range accounts {
		if acc.ID == b.ID {
			assert.True(t, acc.IsDefault)
		} else {
			assert.False(t, acc.IsDefault)
		}
	}
}

func TestBankAccountService_OwnershipIsolated(t *testing.T) {
	repo := newFakeRepo()
	svc := bankaccount.NewService(repo)
	ctx := context.Background()

	a, err := svc.Add(ctx, uuid.New().String(), bankaccount.CreateAccountRequest{BankCode: "058", AccountNumber: "0123456789", AccountName: "Ada"})
	require.NoError(t, err)

	other := uuid.New().String()
	assert.ErrorIs(t, svc.Remove(ctx, other, a.ID.String()), apperrors.ErrNotFound)
	assert.ErrorIs(t, svc.SetDefault(ctx, other, a.ID.String()), apperrors.ErrNotFound)

	mine, err := svc.List(ctx, other)
	require.NoError(t, err)
	assert.Empty(t, mine)
}
