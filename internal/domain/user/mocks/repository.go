package mocks

import (
	"context"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"

	"github.com/deodes/backend/internal/domain/user"
)

type Repository struct {
	mock.Mock
}

func (m *Repository) FindByID(ctx context.Context, id uuid.UUID) (*user.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*user.User), args.Error(1)
}

func (m *Repository) FindByWalletAddress(ctx context.Context, walletAddress string) (*user.User, error) {
	args := m.Called(ctx, walletAddress)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*user.User), args.Error(1)
}

func (m *Repository) FindByEmail(ctx context.Context, email string) (*user.User, error) {
	args := m.Called(ctx, email)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*user.User), args.Error(1)
}

func (m *Repository) FindByUsername(ctx context.Context, username string) (*user.User, error) {
	args := m.Called(ctx, username)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*user.User), args.Error(1)
}

func (m *Repository) Create(ctx context.Context, u *user.User) error {
	return m.Called(ctx, u).Error(0)
}

func (m *Repository) Update(ctx context.Context, u *user.User) error {
	return m.Called(ctx, u).Error(0)
}

func (m *Repository) UpdateMoiScore(ctx context.Context, id uuid.UUID, score int) error {
	return m.Called(ctx, id, score).Error(0)
}

func (m *Repository) List(ctx context.Context, filter user.UserFilter) ([]user.User, error) {
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]user.User), args.Error(1)
}

func (m *Repository) Count(ctx context.Context, filter user.UserFilter) (int, error) {
	args := m.Called(ctx, filter)
	return args.Int(0), args.Error(1)
}

func (m *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

func (m *Repository) Restore(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

func (m *Repository) FindByPasskeyCredentialID(ctx context.Context, credentialID string) (*user.User, error) {
	args := m.Called(ctx, credentialID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*user.User), args.Error(1)
}

func (m *Repository) ClaimNextName(ctx context.Context) (int64, error) {
	args := m.Called(ctx)
	return args.Get(0).(int64), args.Error(1)
}

func (m *Repository) FindByIDIncludingDeleted(ctx context.Context, id uuid.UUID) (*user.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*user.User), args.Error(1)
}

func (m *Repository) ListDeleted(ctx context.Context, filter user.UserFilter) ([]user.User, error) {
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]user.User), args.Error(1)
}

func (m *Repository) CountDeleted(ctx context.Context, filter user.UserFilter) (int, error) {
	args := m.Called(ctx, filter)
	return args.Int(0), args.Error(1)
}
