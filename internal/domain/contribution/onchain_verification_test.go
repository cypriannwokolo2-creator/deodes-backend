package contribution_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/circle"
	"github.com/deodes/backend/internal/domain/contribution"
	contribMocks "github.com/deodes/backend/internal/domain/contribution/mocks"
	"github.com/deodes/backend/pkg/apperrors"
	"github.com/deodes/backend/pkg/arc"
)

// transferLog builds an ERC-20 Transfer log emitted by token for baseUnits.
func transferLog(token, to string, baseUnits uint64) arc.Log {
	pad := func(addr string) string {
		h := strings.TrimPrefix(strings.ToLower(addr), "0x")
		return "0x" + strings.Repeat("0", 64-len(h)) + h
	}
	return arc.Log{
		Address: token,
		Topics: []string{
			arc.TransferEventSig,
			pad("0x1111111111111111111111111111111111111111"),
			pad(to),
		},
		Data: fmt.Sprintf("0x%064x", baseUnits),
	}
}

// Post-purge semantics (no chain client): a txnHash without explicit
// overrides records as Pending for the Arc indexer (2.6); explicit caller
// states always win; replays stay idempotent.

// --------------------------------------------------------------------------
// Default: txnHash records as pending
// --------------------------------------------------------------------------

func TestContributionService_Record_PendingByDefault(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	ctx := context.Background()

	input := contribution.RecordInput{
		CircleID:    uuid.New().String(),
		UserID:      uuid.New().String(),
		RoundNumber: 1,
		Amount:      100.0,
		TxnHash:     "0xabc123",
	}

	// No prior record for this hash.
	repo.On("FindByTxnHash", ctx, "0xabc123").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	// Record is persisted as pending.
	repo.On("Create", ctx, mock.MatchedBy(func(c *contribution.Contribution) bool {
		return c.VerifiedOnchain == false &&
			c.VerificationStatus == contribution.VerificationStatusPending &&
			c.TxnHash.String == "0xabc123"
	})).Return(nil)

	c, err := svc.Record(ctx, input)

	require.NoError(t, err)
	require.NotNil(t, c)
	assert.False(t, c.VerifiedOnchain, "contribution must not be marked verified without indexer confirmation")
	assert.Equal(t, contribution.VerificationStatusPending, c.VerificationStatus)

	repo.AssertExpectations(t)
}

// --------------------------------------------------------------------------
// Non-0x hashes record as pending too (never rejected without a verifier)
// --------------------------------------------------------------------------

func TestContributionService_Record_NonArcHashPendingNotRejected(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	ctx := context.Background()

	input := contribution.RecordInput{
		CircleID:    uuid.New().String(),
		UserID:      uuid.New().String(),
		RoundNumber: 1,
		Amount:      500.0,
		TxnHash:     "forged-txn-hash",
	}

	repo.On("FindByTxnHash", ctx, "forged-txn-hash").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(c *contribution.Contribution) bool {
		return c.VerificationStatus == contribution.VerificationStatusPending &&
			c.VerifiedOnchain == false
	})).Return(nil)

	c, err := svc.Record(ctx, input)

	require.NoError(t, err, "without a verifier nothing may be rejected on-chain grounds")
	require.NotNil(t, c)
	assert.Equal(t, contribution.VerificationStatusPending, c.VerificationStatus)

	repo.AssertExpectations(t)
}

// --------------------------------------------------------------------------
// Security: replay protection — same txnHash submitted twice
// --------------------------------------------------------------------------

func TestContributionService_Record_IdempotentOnReplay(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	ctx := context.Background()

	existing := &contribution.Contribution{
		ID:                 uuid.New(),
		CircleID:           uuid.New(),
		UserID:             uuid.New(),
		RoundNumber:        1,
		Amount:             100.0,
		TxnHash:            sql.NullString{String: "replayed-txn", Valid: true},
		VerifiedOnchain:    true,
		VerificationStatus: contribution.VerificationStatusVerified,
		Status:             contribution.StatusConfirmed,
	}

	input := contribution.RecordInput{
		CircleID:    existing.CircleID.String(),
		UserID:      existing.UserID.String(),
		RoundNumber: 1,
		Amount:      100.0,
		TxnHash:     "replayed-txn",
	}

	// FindByTxnHash returns the existing record — replay detected.
	repo.On("FindByTxnHash", ctx, "replayed-txn").Return(existing, nil)

	c, err := svc.Record(ctx, input)

	require.NoError(t, err, "replay must succeed (idempotent)")
	require.NotNil(t, c)
	assert.Equal(t, existing.ID, c.ID, "must return the existing record")

	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
}

func TestContributionService_Record_CrossScopeReplayConflicts(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	ctx := context.Background()

	// The hash belongs to another circle: replaying it here must conflict,
	// not leak the other row.
	existing := &contribution.Contribution{
		ID: uuid.New(), CircleID: uuid.New(), UserID: uuid.New(), RoundNumber: 1,
		Amount: 100.0, TxnHash: sql.NullString{String: "victim-txn", Valid: true},
	}
	input := contribution.RecordInput{
		CircleID:    uuid.New().String(),
		UserID:      uuid.New().String(),
		RoundNumber: 1,
		Amount:      100.0,
		TxnHash:     "victim-txn",
	}
	repo.On("FindByTxnHash", ctx, "victim-txn").Return(existing, nil)

	c, err := svc.Record(ctx, input)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "different contribution")
	assert.Nil(t, c)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
}

func TestContributionService_Record_SmallTransferLargeClaimStaysPending(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	cwc, ok := svc.(contribution.ServiceWithCircle)
	require.True(t, ok)
	// Receipt proves a 1 USDC movement while 100 USDC is claimed: the row
	// must record as pending (never verified) for the indexer to settle.
	cwc.SetTxVerifier(&stubTxVerifier{receipt: &arc.Receipt{
		TransactionHash: "0xsmallhash", To: testConfiguredCircle, Status: 1, BlockNumber: 99, Found: true,
		Logs: []arc.Log{transferLog(testConfiguredCircle, "0x742d35Cc6634C0532925a3b844Bc454e4438f44e", 1_000_000)},
	}})
	ctx := context.Background()

	repo.On("FindByTxnHash", ctx, "0xsmallhash").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(c *contribution.Contribution) bool {
		return !c.VerifiedOnchain && c.VerificationStatus == contribution.VerificationStatusPending
	})).Return(nil)

	c, err := svc.Record(ctx, contribution.RecordInput{
		CircleID:    uuid.New().String(),
		UserID:      uuid.New().String(),
		RoundNumber: 1,
		Amount:      100.0,
		TxnHash:     "0xsmallhash",
	})

	require.NoError(t, err)
	require.NotNil(t, c)
	assert.False(t, c.VerifiedOnchain)
	assert.Equal(t, contribution.VerificationStatusPending, c.VerificationStatus)
	repo.AssertExpectations(t)
}

// --------------------------------------------------------------------------
// Caller-supplied verification state is used (indexer path)
// --------------------------------------------------------------------------

func TestContributionService_Record_UsesCallerVerificationState(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	ctx := context.Background()

	verified := true
	status := contribution.VerificationStatusVerified

	input := contribution.RecordInput{
		CircleID:           uuid.New().String(),
		UserID:             uuid.New().String(),
		RoundNumber:        1,
		Amount:             200.0,
		TxnHash:            "indexer-confirmed-txn",
		VerifiedOnchain:    &verified,
		VerificationStatus: &status,
	}

	repo.On("FindByTxnHash", ctx, "indexer-confirmed-txn").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(c *contribution.Contribution) bool {
		return c.VerifiedOnchain == true &&
			c.VerificationStatus == contribution.VerificationStatusVerified
	})).Return(nil)

	c, err := svc.Record(ctx, input)

	require.NoError(t, err)
	assert.True(t, c.VerifiedOnchain)
	assert.Equal(t, contribution.VerificationStatusVerified, c.VerificationStatus)
	repo.AssertExpectations(t)
}

// --------------------------------------------------------------------------
// Conflict error on Create (race) is handled gracefully
// --------------------------------------------------------------------------

func TestContributionService_Record_HandlesCreateConflict(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	ctx := context.Background()

	circleID := uuid.New()
	userID := uuid.New()
	existing := &contribution.Contribution{
		ID: uuid.New(), CircleID: circleID, UserID: userID, RoundNumber: 1,
		Amount: 100.0,
	}

	input := contribution.RecordInput{
		CircleID:    circleID.String(),
		UserID:      userID.String(),
		RoundNumber: 1,
		Amount:      100.0,
		TxnHash:     "race-condition-txn",
	}

	repo.On("FindByTxnHash", ctx, "race-condition-txn").Return(nil, apperrors.ErrNotFound).Once()
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.AnythingOfType("*contribution.Contribution")).Return(apperrors.ErrConflict)
	// After the conflict, FindByTxnHash returns the row created by the other request.
	repo.On("FindByTxnHash", ctx, "race-condition-txn").Return(existing, nil).Once()
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)

	c, err := svc.Record(ctx, input)

	require.NoError(t, err)
	assert.Equal(t, existing.ID, c.ID)
	repo.AssertExpectations(t)
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// Compile-time import check to keep sql in scope.
var _ sql.NullString

// --------------------------------------------------------------------------
// 2.1 record-time verification (wired TxVerifier)
// --------------------------------------------------------------------------

type stubTxVerifier struct {
	receipt *arc.Receipt
	err     error
	calls   int
}

func (s *stubTxVerifier) GetTransactionReceipt(_ context.Context, _ string) (*arc.Receipt, error) {
	s.calls++
	return s.receipt, s.err
}

func verifiedTestInput() contribution.RecordInput {
	return contribution.RecordInput{
		CircleID:    uuid.New().String(),
		UserID:      uuid.New().String(),
		RoundNumber: 1,
		Amount:      100.0,
		TxnHash:     "0xverifierhash",
	}
}

func TestContributionService_Record_VerifierConfirmed(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	cwc, ok := svc.(contribution.ServiceWithCircle)
	require.True(t, ok)
	verifier := &stubTxVerifier{receipt: &arc.Receipt{
		TransactionHash: "0xverifierhash", To: testConfiguredCircle, Status: 1, BlockNumber: 99, Found: true,
		Logs: []arc.Log{transferLog(testConfiguredCircle, "0x742d35Cc6634C0532925a3b844Bc454e4438f44e", 100_000_000)},
	}}
	cwc.SetTxVerifier(verifier)
	ctx := context.Background()

	repo.On("FindByTxnHash", ctx, "0xverifierhash").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(c *contribution.Contribution) bool {
		return c.VerifiedOnchain && c.VerificationStatus == contribution.VerificationStatusVerified &&
			c.Status == contribution.StatusConfirmed
	})).Return(nil)

	c, err := svc.Record(ctx, verifiedTestInput())

	require.NoError(t, err)
	require.NotNil(t, c)
	assert.True(t, c.VerifiedOnchain)
	assert.Equal(t, 1, verifier.calls)
	repo.AssertExpectations(t)
}

func TestContributionService_Record_VerifierRevertedRejected(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	svc.(contribution.ServiceWithCircle).SetTxVerifier(&stubTxVerifier{
		receipt: &arc.Receipt{TransactionHash: "0xverifierhash", To: testConfiguredCircle, Status: 0, BlockNumber: 99, Found: true},
	})
	ctx := context.Background()

	repo.On("FindByTxnHash", ctx, "0xverifierhash").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)

	got, err := svc.Record(ctx, verifiedTestInput())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "on-chain verification failed")
	assert.Contains(t, err.Error(), "reverted")
	assert.Nil(t, got)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
}

func TestContributionService_Record_VerifierWrongContractRejected(t *testing.T) {
	repo := new(contribMocks.Repository)
	ctx := context.Background()
	circ := contractBoundCircle(testConfiguredCircle, circle.CircleStatusActive)
	svc := contribution.NewService(repo, nil, nil, contractCircleService{circ: circ})
	cwc := svc.(contribution.ServiceWithCircle)
	require.NoError(t, cwc.SetArcCircleAddress(testConfiguredCircle))
	cwc.SetTxVerifier(&stubTxVerifier{
		receipt: &arc.Receipt{TransactionHash: "0xverifierhash", To: testOtherCircle, Status: 1, BlockNumber: 99, Found: true},
	})

	repo.On("FindByTxnHash", ctx, "0xverifierhash").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)

	got, err := svc.Record(ctx, contribution.RecordInput{
		CircleID: circ.ID.String(), UserID: uuid.New().String(),
		RoundNumber: 1, Amount: 100.0, TxnHash: "0xverifierhash",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "on-chain verification failed")
	assert.Contains(t, err.Error(), "not the circle contract")
	assert.Nil(t, got)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
}

func TestContributionService_Record_VerifierRPCFlakeStaysPending(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	svc.(contribution.ServiceWithCircle).SetTxVerifier(&stubTxVerifier{err: context.DeadlineExceeded})
	ctx := context.Background()

	repo.On("FindByTxnHash", ctx, "0xverifierhash").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(c *contribution.Contribution) bool {
		return !c.VerifiedOnchain && c.VerificationStatus == contribution.VerificationStatusPending
	})).Return(nil)

	c, err := svc.Record(ctx, verifiedTestInput())

	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, contribution.VerificationStatusPending, c.VerificationStatus)
	repo.AssertExpectations(t)
}

func TestContributionService_Record_VerifierUnminedStaysPending(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	svc.(contribution.ServiceWithCircle).SetTxVerifier(&stubTxVerifier{receipt: nil, err: nil})
	ctx := context.Background()

	repo.On("FindByTxnHash", ctx, "0xverifierhash").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(c *contribution.Contribution) bool {
		return !c.VerifiedOnchain && c.VerificationStatus == contribution.VerificationStatusPending
	})).Return(nil)

	c, err := svc.Record(ctx, verifiedTestInput())

	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, contribution.VerificationStatusPending, c.VerificationStatus)
	repo.AssertExpectations(t)
}

func TestContributionService_Record_ExplicitOverrideSkipsVerifier(t *testing.T) {
	repo := new(contribMocks.Repository)
	svc := contribution.NewService(repo, nil, nil, nil)
	verifier := &stubTxVerifier{
		receipt: &arc.Receipt{TransactionHash: "0xverifierhash", Status: 0, BlockNumber: 1, Found: true},
	}
	svc.(contribution.ServiceWithCircle).SetTxVerifier(verifier)
	ctx := context.Background()

	verified := true
	status := contribution.VerificationStatusVerified
	input := verifiedTestInput()
	input.VerifiedOnchain = &verified
	input.VerificationStatus = &status

	repo.On("FindByTxnHash", ctx, "0xverifierhash").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleUserRound", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(c *contribution.Contribution) bool {
		return c.VerifiedOnchain && c.VerificationStatus == contribution.VerificationStatusVerified
	})).Return(nil)

	c, err := svc.Record(ctx, input)

	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, 0, verifier.calls, "explicit overrides must skip the receipt lookup")
	repo.AssertExpectations(t)
}
