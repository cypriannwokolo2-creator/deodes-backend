package contribution_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/contribution"
	contribMocks "github.com/deodes/backend/internal/domain/contribution/mocks"
	"github.com/deodes/backend/pkg/apperrors"
)

const arcHash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestContributionService_Record_RejectsNonPositiveAmount(t *testing.T) {
	for _, amt := range []float64{0, -10} {
		repo := new(contribMocks.Repository)
		svc := contribution.NewService(repo, nil, nil, nil)
		_, err := svc.Record(context.Background(), contribution.RecordInput{
			CircleID: uuid.New().String(), UserID: uuid.New().String(),
			RoundNumber: 1, Amount: amt, TxnHash: arcHash,
		})
		assert.Error(t, err, "amount %v must be rejected", amt)
		repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	}
}

func TestContributionService_Record_RejectsBadRound(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	_, err := svc.Record(context.Background(), contribution.RecordInput{
		CircleID: uuid.New().String(), UserID: uuid.New().String(),
		RoundNumber: 0, Amount: 100, TxnHash: arcHash,
	})
	assert.Error(t, err)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
}

func TestContributionService_Record_ArcHashPending(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	ctx := context.Background()
	repo.On("FindByTxnHash", ctx, arcHash).Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(c *contribution.Contribution) bool {
		return c.VerificationStatus == contribution.VerificationStatusPending && !c.VerifiedOnchain
	})).Return(nil)
	c, err := svc.Record(ctx, contribution.RecordInput{
		CircleID: uuid.New().String(), UserID: uuid.New().String(),
		RoundNumber: 1, Amount: 100, TxnHash: arcHash,
	})
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, contribution.VerificationStatusPending, c.VerificationStatus)
	repo.AssertExpectations(t)
}
