package contribution_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/contribution"
	contribMocks "github.com/deodes/backend/internal/domain/contribution/mocks"
	"github.com/deodes/backend/pkg/apperrors"
)

type stubScoreUpdater struct {
	calls []string
	err   error
}

func (s *stubScoreUpdater) RefreshScore(_ context.Context, userID string) error {
	s.calls = append(s.calls, userID)
	return s.err
}

func verifiedInput() contribution.RecordInput {
	verified := true
	status := contribution.VerificationStatusVerified
	return contribution.RecordInput{
		CircleID:           uuid.New().String(),
		UserID:             uuid.New().String(),
		RoundNumber:        1,
		Amount:             50.0,
		TxnHash:            "txn-score-1",
		VerifiedOnchain:    &verified,
		VerificationStatus: &status,
	}
}

func TestContributionService_Record_VerifiedRefreshesScore(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	cwc, ok := svc.(contribution.ServiceWithCircle)
	require.True(t, ok, "service must expose score wiring")
	hook := &stubScoreUpdater{}
	cwc.SetScoreUpdater(hook)
	ctx := context.Background()

	input := verifiedInput()
	repo.On("FindByTxnHash", ctx, input.TxnHash).Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.AnythingOfType("*contribution.Contribution")).Return(nil)

	c, err := svc.Record(ctx, input)

	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, contribution.StatusConfirmed, c.Status)
	require.Len(t, hook.calls, 1)
	assert.Equal(t, input.UserID, hook.calls[0])
	repo.AssertExpectations(t)
}

func TestContributionService_Record_PendingDoesNotRefreshScore(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	cwc, ok := svc.(contribution.ServiceWithCircle)
	require.True(t, ok)
	hook := &stubScoreUpdater{}
	cwc.SetScoreUpdater(hook)
	ctx := context.Background()

	input := contribution.RecordInput{
		CircleID:    uuid.New().String(),
		UserID:      uuid.New().String(),
		RoundNumber: 1,
		Amount:      50.0,
		// No hash and no overrides: stays pending for the indexer.
	}
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.AnythingOfType("*contribution.Contribution")).Return(nil)

	_, err := svc.Record(ctx, input)

	require.NoError(t, err)
	assert.Empty(t, hook.calls)
	repo.AssertExpectations(t)
}

func TestContributionService_Record_ScoreFailureNeverFailsRecord(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	cwc, ok := svc.(contribution.ServiceWithCircle)
	require.True(t, ok)
	hook := &stubScoreUpdater{err: errors.New("score db down")}
	cwc.SetScoreUpdater(hook)
	ctx := context.Background()

	input := verifiedInput()
	repo.On("FindByTxnHash", ctx, input.TxnHash).Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.AnythingOfType("*contribution.Contribution")).Return(nil)

	c, err := svc.Record(ctx, input)

	require.NoError(t, err)
	require.NotNil(t, c)
	require.Len(t, hook.calls, 1)
	repo.AssertExpectations(t)
}

func TestContributionService_UpdateVerification_VerifiedRefreshesScore(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	cwc, ok := svc.(contribution.ServiceWithCircle)
	require.True(t, ok)
	hook := &stubScoreUpdater{}
	cwc.SetScoreUpdater(hook)
	ctx := context.Background()

	contribID := uuid.New()
	userID := uuid.New()
	repo.On("UpdateVerificationStatus", ctx, contribID, true, contribution.VerificationStatusVerified).Return(nil)
	repo.On("FindByID", ctx, contribID).Return(&contribution.Contribution{ID: contribID, UserID: userID}, nil)

	require.NoError(t, svc.UpdateVerification(ctx, contribID.String(), true, contribution.VerificationStatusVerified))
	require.Len(t, hook.calls, 1)
	assert.Equal(t, userID.String(), hook.calls[0])
	repo.AssertExpectations(t)
}

func TestContributionService_UpdateVerification_UnverifiedSkipsScore(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	cwc, ok := svc.(contribution.ServiceWithCircle)
	require.True(t, ok)
	hook := &stubScoreUpdater{}
	cwc.SetScoreUpdater(hook)
	ctx := context.Background()

	contribID := uuid.New()
	repo.On("UpdateVerificationStatus", ctx, contribID, false, contribution.VerificationStatusPending).Return(nil)

	require.NoError(t, svc.UpdateVerification(ctx, contribID.String(), false, contribution.VerificationStatusPending))
	assert.Empty(t, hook.calls)
	repo.AssertExpectations(t)
}
