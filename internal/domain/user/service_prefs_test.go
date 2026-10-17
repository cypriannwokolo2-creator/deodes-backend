package user_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/user"
	userMocks "github.com/deodes/backend/internal/domain/user/mocks"
	"github.com/deodes/backend/pkg/apperrors"
)

func TestUserService_UpdateProfile_SavingsPrefs(t *testing.T) {
	repo := new(userMocks.Repository)
	svc := user.NewService(repo, nil)
	ctx := context.Background()
	uid := uuid.New()

	existing := &user.User{ID: uid, WalletAddress: "GABC..."}
	repo.On("FindByID", ctx, uid).Return(existing, nil)
	repo.On("Update", ctx, mock.AnythingOfType("*user.User")).Return(nil)

	autoReserve := true
	target := 250.0
	freq := "weekly"
	notify := false
	threshold := 75
	currency := "USDC"

	u, err := svc.UpdateProfile(ctx, uid.String(), user.UpdateProfileInput{
		SavingsAutoReserve:        &autoReserve,
		SavingsDefaultTarget:      &target,
		SavingsDefaultFrequency:   &freq,
		SavingsNotifyOnMilestone:  &notify,
		SavingsMilestoneThreshold: &threshold,
		PreferredCurrency:         &currency,
	})

	require.NoError(t, err)
	assert.True(t, u.SavingsAutoReserve)
	assert.Equal(t, 250.0, u.SavingsDefaultTarget)
	assert.Equal(t, "weekly", u.SavingsDefaultFrequency)
	assert.False(t, u.SavingsNotifyOnMilestone)
	assert.Equal(t, 75, u.SavingsMilestoneThreshold)
	assert.Equal(t, "USDC", u.PreferredCurrency)
	repo.AssertExpectations(t)
}

func TestUserService_UpdateProfile_SavingsPrefsValidation(t *testing.T) {
	repo := new(userMocks.Repository)
	svc := user.NewService(repo, nil)
	ctx := context.Background()
	uid := uuid.New()

	existing := &user.User{ID: uid, WalletAddress: "GABC..."}
	repo.On("FindByID", ctx, uid).Return(existing, nil)

	badFreq := "yearly"
	_, err := svc.UpdateProfile(ctx, uid.String(), user.UpdateProfileInput{SavingsDefaultFrequency: &badFreq})
	assert.ErrorIs(t, err, apperrors.ErrInvalidInput)

	badThreshold := 30
	_, err = svc.UpdateProfile(ctx, uid.String(), user.UpdateProfileInput{SavingsMilestoneThreshold: &badThreshold})
	assert.ErrorIs(t, err, apperrors.ErrInvalidInput)

	badTarget := -5.0
	_, err = svc.UpdateProfile(ctx, uid.String(), user.UpdateProfileInput{SavingsDefaultTarget: &badTarget})
	assert.ErrorIs(t, err, apperrors.ErrInvalidInput)

	badCurrency := "EUR"
	_, err = svc.UpdateProfile(ctx, uid.String(), user.UpdateProfileInput{PreferredCurrency: &badCurrency})
	assert.ErrorIs(t, err, apperrors.ErrInvalidInput)

	repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
}
