package payout_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/circle"
	"github.com/deodes/backend/internal/domain/payout"
	payoutMocks "github.com/deodes/backend/internal/domain/payout/mocks"
	"github.com/deodes/backend/pkg/apperrors"
)

// stubRotationCircleService wires membership + circle lookups for rotation
// tests; every other circle.Service method is unused and panics.
type stubRotationCircleService struct {
	circle.Service
	circ   *circle.Circle
	member bool
}

func (s *stubRotationCircleService) Get(_ context.Context, _ string) (*circle.Circle, error) {
	return s.circ, nil
}

func (s *stubRotationCircleService) IsMember(_ context.Context, _, _ string) (bool, error) {
	return s.member, nil
}

func (s *stubRotationCircleService) List(context.Context, circle.CircleFilter) ([]circle.Circle, int, error) {
	panic("not used")
}

func (s *stubRotationCircleService) Create(context.Context, string, circle.CreateCircleInput) (*circle.Circle, error) {
	panic("not used")
}

func (s *stubRotationCircleService) Update(context.Context, string, string, circle.UpdateCircleInput) (*circle.Circle, error) {
	panic("not used")
}

func (s *stubRotationCircleService) Start(context.Context, string, string) error {
	panic("not used")
}

func (s *stubRotationCircleService) Close(context.Context, string, string) error {
	panic("not used")
}

func (s *stubRotationCircleService) Cancel(context.Context, string, string) error {
	panic("not used")
}

func (s *stubRotationCircleService) Join(context.Context, string, string, string) error {
	panic("not used")
}

func (s *stubRotationCircleService) Exit(context.Context, string, string) error {
	panic("not used")
}

func (s *stubRotationCircleService) GetMembers(context.Context, string) ([]circle.CircleMember, error) {
	panic("not used")
}

func (s *stubRotationCircleService) SearchMembers(context.Context, string, string) ([]circle.CircleMember, error) {
	panic("not used")
}

func (s *stubRotationCircleService) RemoveMember(context.Context, string, string, string, string) error {
	panic("not used")
}

func (s *stubRotationCircleService) ProcessMissedContributions(context.Context, string, int) error {
	panic("not used")
}

func (s *stubRotationCircleService) RaiseDispute(context.Context, string, string, circle.DisputeInput) (*circle.CircleDispute, error) {
	panic("not used")
}

func (s *stubRotationCircleService) CastVote(context.Context, string, string, circle.VoteInput) (*circle.CircleVote, bool, string, error) {
	panic("not used")
}

func (s *stubRotationCircleService) SubmitAuctionBid(context.Context, string, string, circle.AuctionBidInput) (*circle.CircleAuctionBid, error) {
	panic("not used")
}

func (s *stubRotationCircleService) QueryRoundConfig(context.Context, string, int) (*circle.RoundConfigSnapshot, error) {
	panic("not used")
}

func (s *stubRotationCircleService) GetCircleSnapshot(context.Context, string, string) (*circle.CircleSnapshot, error) {
	panic("not used")
}

func (s *stubRotationCircleService) GetBulkCircleSnapshots(context.Context, string, []string) ([]circle.CircleSnapshot, error) {
	panic("not used")
}

func rotationTestCircle() *circle.Circle {
	return &circle.Circle{
		ID: uuid.New(), Name: "Rotation Circle",
		Status: circle.CircleStatusActive, PayoutType: circle.PayoutTypeRandom,
		CurrentRound: 5, MaxMembers: 4, ContributionAmount: 200,
	}
}

func TestPayoutService_Record_RandomRejectsRepeatRecipient(t *testing.T) {
	repo := new(payoutMocks.Repository)
	ctx := context.Background()
	circ := rotationTestCircle()
	svc := payout.NewService(repo, nil, &stubRotationCircleService{circ: circ, member: true})

	circleID := circ.ID
	repeatRecipient := uuid.New()
	history := []payout.Payout{
		{ID: uuid.New(), CircleID: circleID, RecipientID: repeatRecipient, RoundNumber: 1},
		{ID: uuid.New(), CircleID: circleID, RecipientID: uuid.New(), RoundNumber: 2},
	}

	repo.On("FindByTxnHash", ctx, "repeat-txn").Return(nil, apperrors.ErrNotFound)
	repo.On("ListByCircle", ctx, mock.AnythingOfType("uuid.UUID"), 1, 100).Return(history, len(history), nil)

	got, err := svc.Record(ctx, payout.RecordInput{
		CircleID:    circleID.String(),
		RecipientID: repeatRecipient.String(),
		RoundNumber: 3,
		Amount:      400.0,
		FeeAmount:   2.0,
		TxnHash:     "repeat-txn",
		PayoutType:  payout.PayoutTypeRandom,
	})

	require.ErrorIs(t, err, payout.ErrRecipientAlreadyPaid)
	assert.Nil(t, got)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
}

func TestPayoutService_Record_RandomAllowsUnpaidRecipient(t *testing.T) {
	repo := new(payoutMocks.Repository)
	ctx := context.Background()
	circ := rotationTestCircle()
	svc := payout.NewService(repo, nil, &stubRotationCircleService{circ: circ, member: true})

	circleID := circ.ID
	freshRecipient := uuid.New()
	history := []payout.Payout{
		{ID: uuid.New(), CircleID: circleID, RecipientID: uuid.New(), RoundNumber: 1},
		{ID: uuid.New(), CircleID: circleID, RecipientID: uuid.New(), RoundNumber: 2},
	}

	repo.On("FindByTxnHash", ctx, "fresh-txn").Return(nil, apperrors.ErrNotFound)
	repo.On("ListByCircle", ctx, mock.AnythingOfType("uuid.UUID"), 1, 100).Return(history, len(history), nil)
	repo.On("FindByCircleAndRound", ctx, mock.AnythingOfType("uuid.UUID"), 3).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.AnythingOfType("*payout.Payout")).Return(nil)

	got, err := svc.Record(ctx, payout.RecordInput{
		CircleID:    circleID.String(),
		RecipientID: freshRecipient.String(),
		RoundNumber: 3,
		Amount:      400.0,
		FeeAmount:   2.0,
		TxnHash:     "fresh-txn",
		PayoutType:  payout.PayoutTypeRandom,
	})

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, freshRecipient, got.RecipientID)
	repo.AssertExpectations(t)
}

func TestPayoutService_Record_NonRandomAllowsRepeatRecipient(t *testing.T) {
	// The no-repeat invariant is scoped to random rotations: fixed circles
	// keep their existing semantics (organizer-directed order).
	repo := new(payoutMocks.Repository)
	ctx := context.Background()
	circ := rotationTestCircle()
	circ.PayoutType = circle.PayoutTypeFixed
	svc := payout.NewService(repo, nil, &stubRotationCircleService{circ: circ, member: true})

	circleID := circ.ID
	recipient := uuid.New()

	repo.On("FindByTxnHash", ctx, "fixed-txn").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleAndRound", ctx, mock.AnythingOfType("uuid.UUID"), 3).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.AnythingOfType("*payout.Payout")).Return(nil)

	got, err := svc.Record(ctx, payout.RecordInput{
		CircleID:    circleID.String(),
		RecipientID: recipient.String(),
		RoundNumber: 3,
		Amount:      400.0,
		FeeAmount:   2.0,
		TxnHash:     "fixed-txn",
		PayoutType:  payout.PayoutTypeFixed,
	})

	require.NoError(t, err)
	require.NotNil(t, got)
	// No rotation scan for non-random types.
	repo.AssertNotCalled(t, "ListByCircle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
}
