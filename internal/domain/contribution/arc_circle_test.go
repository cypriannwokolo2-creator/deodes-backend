package contribution_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/circle"
	"github.com/deodes/backend/internal/domain/contribution"
	contribMocks "github.com/deodes/backend/internal/domain/contribution/mocks"
	"github.com/deodes/backend/pkg/apperrors"
)

const (
	testConfiguredCircle = "0x70A7CCB7b5fe856384Aab3aAF553E09A10AF14A4"
	testOtherCircle      = "0x51b45Ba8570551e0D1fe818227d8bAa8b556694D"
)

// contractCircleService returns a circle carrying the given contract_id.
type contractCircleService struct {
	circle.Service
	circ *circle.Circle
}

func (s contractCircleService) Get(_ context.Context, _ string) (*circle.Circle, error) {
	return s.circ, nil
}

func (s contractCircleService) IsMember(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}

func contractBoundCircle(contractID string, status circle.CircleStatus) *circle.Circle {
	return &circle.Circle{
		ID:           uuid.New(),
		Name:         "Contract Circle",
		Status:       status,
		CurrentRound: 5,
		ContractID:   sql.NullString{String: contractID, Valid: contractID != ""},
	}
}

func TestContributionService_SetArcCircleAddress_Validation(t *testing.T) {
	svc := contribution.NewService(new(contribMocks.Repository), nil, nil, nil)
	cwc, ok := svc.(contribution.ServiceWithCircle)
	require.True(t, ok, "service must expose ServiceWithCircle")

	require.Error(t, cwc.SetArcCircleAddress("not-an-address"))
	require.Error(t, cwc.SetArcCircleAddress("0x1234"))
	require.NoError(t, cwc.SetArcCircleAddress(""))
	require.NoError(t, cwc.SetArcCircleAddress(testConfiguredCircle))
}

func TestContributionService_Record_RejectsForeignCircleContract(t *testing.T) {
	repo := new(contribMocks.Repository)
	circ := contractBoundCircle(testOtherCircle, circle.CircleStatusActive)
	svc := contribution.NewService(repo, nil, nil, contractCircleService{circ: circ})
	require.NoError(t, svc.(contribution.ServiceWithCircle).SetArcCircleAddress(testConfiguredCircle))

	got, err := svc.Record(context.Background(), contribution.RecordInput{
		CircleID:    circ.ID.String(),
		UserID:      uuid.New().String(),
		RoundNumber: 1,
		Amount:      100,
		TxnHash:     "foreign-txn",
	})

	require.ErrorIs(t, err, apperrors.ErrCircleContractMismatch)
	assert.Nil(t, got)
	repo.AssertNotCalled(t, "FindByTxnHash", mock.Anything, mock.Anything)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
}

func TestContributionService_Record_AcceptsMatchingCircleContract(t *testing.T) {
	repo := new(contribMocks.Repository)
	ctx := context.Background()
	circ := contractBoundCircle(testConfiguredCircle, circle.CircleStatusActive)
	svc := contribution.NewService(repo, nil, nil, contractCircleService{circ: circ})
	require.NoError(t, svc.(contribution.ServiceWithCircle).SetArcCircleAddress(testConfiguredCircle))

	repo.On("FindByTxnHash", ctx, "matching-txn").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.AnythingOfType("*contribution.Contribution")).Return(nil)

	got, err := svc.Record(ctx, contribution.RecordInput{
		CircleID:    circ.ID.String(),
		UserID:      uuid.New().String(),
		RoundNumber: 1,
		Amount:      100,
		TxnHash:     "matching-txn",
	})

	require.NoError(t, err)
	require.NotNil(t, got)
	repo.AssertExpectations(t)
}
