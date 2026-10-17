package circle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/circle"
	circleMocks "github.com/deodes/backend/internal/domain/circle/mocks"
)

type stubBalanceChecker struct {
	balance float64
	err     error
}

func (s *stubBalanceChecker) GetBalance(_ context.Context, _ string) (float64, error) {
	return s.balance, s.err
}

func fundedCreateInput() circle.CreateCircleInput {
	return circle.CreateCircleInput{
		Name:               "Funded Circle",
		CircleType:         circle.CircleTypePublic,
		PayoutType:         circle.PayoutTypeRandom,
		ContributionAmount: 100,
		Currency:           circle.CurrencyUSDC,
		Frequency:          circle.FrequencyWeekly,
		MaxMembers:         10,
		MaxStrikes:         3,
	}
}

func TestCircleService_Create_FundedWalletPasses(t *testing.T) {
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{
		BalanceChecker: &stubBalanceChecker{balance: 150},
	})
	ctx := context.Background()

	repo.On("Create", ctx, mock.AnythingOfType("*circle.Circle")).Return(nil)
	repo.On("CreateMember", ctx, mock.AnythingOfType("*circle.CircleMember")).Return(nil)

	c, err := svc.Create(ctx, uuid.New().String(), fundedCreateInput())
	require.NoError(t, err)
	assert.NotNil(t, c)
	repo.AssertExpectations(t)
}

func TestCircleService_Create_EmptyWalletBlocked(t *testing.T) {
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{
		BalanceChecker: &stubBalanceChecker{balance: 10},
	})
	ctx := context.Background()

	_, err := svc.Create(ctx, uuid.New().String(), fundedCreateInput())
	require.Error(t, err)
	assert.ErrorIs(t, err, circle.ErrInsufficientFunds)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
}

func TestCircleService_Create_BalanceUnreadableFailsClosed(t *testing.T) {
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{
		BalanceChecker: &stubBalanceChecker{err: errors.New("rpc down")},
	})
	ctx := context.Background()

	_, err := svc.Create(ctx, uuid.New().String(), fundedCreateInput())
	require.Error(t, err)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
}

func TestCircleService_Create_NoCheckerSkipsGate(t *testing.T) {
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{})
	ctx := context.Background()

	repo.On("Create", ctx, mock.AnythingOfType("*circle.Circle")).Return(nil)
	repo.On("CreateMember", ctx, mock.AnythingOfType("*circle.CircleMember")).Return(nil)

	_, err := svc.Create(ctx, uuid.New().String(), fundedCreateInput())
	require.NoError(t, err)
}
