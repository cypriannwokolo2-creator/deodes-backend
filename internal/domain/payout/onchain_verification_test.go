package payout_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/payout"
	payoutMocks "github.com/deodes/backend/internal/domain/payout/mocks"
	"github.com/deodes/backend/pkg/apperrors"
	"github.com/deodes/backend/pkg/arc"
)

// Post-purge semantics (no chain client): a txnHash without explicit
// overrides records as Pending for the Arc indexer (2.6); explicit caller
// states always win; replays stay idempotent.

// --------------------------------------------------------------------------
// Default: txnHash records as pending
// --------------------------------------------------------------------------

func TestPayoutService_Record_PendingByDefault(t *testing.T) {
	repo := new(payoutMocks.Repository)
	svc := payout.NewService(repo, nil, nil)
	ctx := context.Background()

	input := payout.RecordInput{
		CircleID:    uuid.New().String(),
		RecipientID: uuid.New().String(),
		RoundNumber: 1,
		Amount:      500.0,
		FeeAmount:   5.0,
		TxnHash:     "0xabc123",
		PayoutType:  payout.PayoutTypeFixed,
	}

	repo.On("FindByTxnHash", ctx, "0xabc123").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleAndRound", ctx, mock.AnythingOfType("uuid.UUID"), 1).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(p *payout.Payout) bool {
		return p.VerifiedOnchain == false &&
			p.VerificationStatus == payout.VerificationStatusPending &&
			p.TxnHash.String == "0xabc123"
	})).Return(nil)

	p, err := svc.Record(ctx, input)

	require.NoError(t, err)
	require.NotNil(t, p)
	assert.False(t, p.VerifiedOnchain)
	assert.Equal(t, payout.VerificationStatusPending, p.VerificationStatus)

	repo.AssertExpectations(t)
}

// --------------------------------------------------------------------------
// Explicit caller overrides win
// --------------------------------------------------------------------------

func TestPayoutService_Record_ExplicitOverrideHonored(t *testing.T) {
	repo := new(payoutMocks.Repository)
	svc := payout.NewService(repo, nil, nil)
	ctx := context.Background()

	verified := true
	status := payout.VerificationStatusVerified
	input := payout.RecordInput{
		CircleID:           uuid.New().String(),
		RecipientID:        uuid.New().String(),
		RoundNumber:        1,
		Amount:             500.0,
		TxnHash:            "0xabc123",
		PayoutType:         payout.PayoutTypeFixed,
		VerifiedOnchain:    &verified,
		VerificationStatus: &status,
	}

	repo.On("FindByTxnHash", ctx, "0xabc123").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleAndRound", ctx, mock.AnythingOfType("uuid.UUID"), 1).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(p *payout.Payout) bool {
		return p.VerifiedOnchain == true &&
			p.VerificationStatus == payout.VerificationStatusVerified
	})).Return(nil)

	p, err := svc.Record(ctx, input)

	require.NoError(t, err)
	require.NotNil(t, p)
	assert.True(t, p.VerifiedOnchain)
	assert.Equal(t, payout.VerificationStatusVerified, p.VerificationStatus)

	repo.AssertExpectations(t)
}

// --------------------------------------------------------------------------
// Replay protection — same payout txnHash submitted twice
// --------------------------------------------------------------------------

func TestPayoutService_Record_IdempotentOnReplay(t *testing.T) {
	repo := new(payoutMocks.Repository)
	svc := payout.NewService(repo, nil, nil)
	ctx := context.Background()

	existingPayout := &payout.Payout{
		ID:                 uuid.New(),
		CircleID:           uuid.New(),
		RecipientID:        uuid.New(),
		RoundNumber:        1,
		Amount:             500.0,
		VerifiedOnchain:    true,
		VerificationStatus: payout.VerificationStatusVerified,
	}

	input := payout.RecordInput{
		CircleID:    existingPayout.CircleID.String(),
		RecipientID: existingPayout.RecipientID.String(),
		RoundNumber: 1,
		Amount:      500.0,
		TxnHash:     "replayed-payout-txn",
		PayoutType:  payout.PayoutTypeFixed,
	}

	// FindByTxnHash returns the existing payout — replay detected.
	repo.On("FindByTxnHash", ctx, "replayed-payout-txn").Return(existingPayout, nil)

	p, err := svc.Record(ctx, input)

	require.NoError(t, err, "replay must succeed (idempotent)")
	require.NotNil(t, p)
	assert.Equal(t, existingPayout.ID, p.ID, "must return the original payout record")

	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
}

func TestPayoutService_Record_CrossScopeReplayConflicts(t *testing.T) {
	repo := new(payoutMocks.Repository)
	svc := payout.NewService(repo, nil, nil)
	ctx := context.Background()

	existingPayout := &payout.Payout{
		ID: uuid.New(), CircleID: uuid.New(), RecipientID: uuid.New(), RoundNumber: 1,
		Amount: 500.0,
	}
	input := payout.RecordInput{
		CircleID:    uuid.New().String(),
		RecipientID: uuid.New().String(),
		RoundNumber: 1,
		Amount:      500.0,
		TxnHash:     "victim-payout-txn",
		PayoutType:  payout.PayoutTypeFixed,
	}
	repo.On("FindByTxnHash", ctx, "victim-payout-txn").Return(existingPayout, nil)

	p, err := svc.Record(ctx, input)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "different payout")
	assert.Nil(t, p)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
}

func TestPayoutService_Record_SmallTransferLargeClaimStaysPending(t *testing.T) {
	repo := new(payoutMocks.Repository)
	svc := payout.NewService(repo, nil, nil)
	wireVerifier(svc, &stubTxVerifier{
		receipt: &arc.Receipt{
			TransactionHash: "0xsmallhash", To: testConfiguredCircle, Status: 1, BlockNumber: 99, Found: true,
			Logs: []arc.Log{transferLog(testConfiguredCircle, "0x742d35Cc6634C0532925a3b844Bc454e4438f44e", 1_000_000)},
		},
	})
	ctx := context.Background()

	repo.On("FindByTxnHash", ctx, "0xsmallhash").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleAndRound", ctx, mock.AnythingOfType("uuid.UUID"), 1).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(p *payout.Payout) bool {
		return !p.VerifiedOnchain && p.VerificationStatus == payout.VerificationStatusPending
	})).Return(nil)

	p, err := svc.Record(ctx, payout.RecordInput{
		CircleID:    uuid.New().String(),
		RecipientID: uuid.New().String(),
		RoundNumber: 1,
		Amount:      500.0,
		FeeAmount:   5.0,
		TxnHash:     "0xsmallhash",
		PayoutType:  payout.PayoutTypeFixed,
	})

	require.NoError(t, err)
	require.NotNil(t, p)
	assert.False(t, p.VerifiedOnchain)
	assert.Equal(t, payout.VerificationStatusPending, p.VerificationStatus)
	repo.AssertExpectations(t)
}

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

func verifiedTestInput() payout.RecordInput {
	return payout.RecordInput{
		CircleID:    uuid.New().String(),
		RecipientID: uuid.New().String(),
		RoundNumber: 1,
		Amount:      500.0,
		FeeAmount:   5.0,
		TxnHash:     "0xverifierhash",
		PayoutType:  payout.PayoutTypeFixed,
	}
}

func wireVerifier(svc payout.Service, v payout.TxVerifier) {
	svc.(payout.ServiceWithCircle).SetTxVerifier(v)
}

func TestPayoutService_Record_VerifierConfirmed(t *testing.T) {
	repo := new(payoutMocks.Repository)
	svc := payout.NewService(repo, nil, nil)
	wireVerifier(svc, &stubTxVerifier{
		receipt: &arc.Receipt{
			TransactionHash: "0xverifierhash", To: testConfiguredCircle, Status: 1, BlockNumber: 99, Found: true,
			Logs: []arc.Log{transferLog(testConfiguredCircle, "0x742d35Cc6634C0532925a3b844Bc454e4438f44e", 500_000_000)},
		},
	})
	ctx := context.Background()

	repo.On("FindByTxnHash", ctx, "0xverifierhash").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleAndRound", ctx, mock.AnythingOfType("uuid.UUID"), 1).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(p *payout.Payout) bool {
		return p.VerifiedOnchain && p.VerificationStatus == payout.VerificationStatusVerified
	})).Return(nil)

	p, err := svc.Record(ctx, verifiedTestInput())

	require.NoError(t, err)
	require.NotNil(t, p)
	assert.True(t, p.VerifiedOnchain)
	repo.AssertExpectations(t)
}

func TestPayoutService_Record_VerifierRevertedRejected(t *testing.T) {
	repo := new(payoutMocks.Repository)
	svc := payout.NewService(repo, nil, nil)
	verifier := &stubTxVerifier{
		receipt: &arc.Receipt{TransactionHash: "0xverifierhash", To: testConfiguredCircle, Status: 0, BlockNumber: 99, Found: true},
	}
	wireVerifier(svc, verifier)
	ctx := context.Background()

	repo.On("FindByTxnHash", ctx, "0xverifierhash").Return(nil, apperrors.ErrNotFound)

	got, err := svc.Record(ctx, verifiedTestInput())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "on-chain verification failed")
	assert.Contains(t, err.Error(), "reverted")
	assert.Nil(t, got)
	assert.Equal(t, 1, verifier.calls)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
}

func TestPayoutService_Record_VerifierWrongContractRejected(t *testing.T) {
	repo := new(payoutMocks.Repository)
	ctx := context.Background()
	circ := contractBoundCircle(testConfiguredCircle)
	svc := payout.NewService(repo, nil, contractCircleService{circ: circ})
	cwc := svc.(payout.ServiceWithCircle)
	require.NoError(t, cwc.SetArcCircleAddress(testConfiguredCircle))
	cwc.SetTxVerifier(&stubTxVerifier{
		receipt: &arc.Receipt{TransactionHash: "0xverifierhash", To: testOtherCircle, Status: 1, BlockNumber: 99, Found: true},
	})

	repo.On("FindByTxnHash", ctx, "0xverifierhash").Return(nil, apperrors.ErrNotFound)

	input := verifiedTestInput()
	input.CircleID = circ.ID.String()

	got, err := svc.Record(ctx, input)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "on-chain verification failed")
	assert.Contains(t, err.Error(), "not the circle contract")
	assert.Nil(t, got)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
}

func TestPayoutService_Record_VerifierRPCFlakeStaysPending(t *testing.T) {
	repo := new(payoutMocks.Repository)
	svc := payout.NewService(repo, nil, nil)
	wireVerifier(svc, &stubTxVerifier{err: context.DeadlineExceeded})
	ctx := context.Background()

	repo.On("FindByTxnHash", ctx, "0xverifierhash").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleAndRound", ctx, mock.AnythingOfType("uuid.UUID"), 1).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(p *payout.Payout) bool {
		return !p.VerifiedOnchain && p.VerificationStatus == payout.VerificationStatusPending
	})).Return(nil)

	p, err := svc.Record(ctx, verifiedTestInput())

	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, payout.VerificationStatusPending, p.VerificationStatus)
	repo.AssertExpectations(t)
}

func TestPayoutService_Record_ExplicitOverrideSkipsVerifier(t *testing.T) {
	repo := new(payoutMocks.Repository)
	svc := payout.NewService(repo, nil, nil)
	verifier := &stubTxVerifier{
		receipt: &arc.Receipt{TransactionHash: "0xverifierhash", Status: 0, BlockNumber: 1, Found: true},
	}
	wireVerifier(svc, verifier)
	ctx := context.Background()

	verified := true
	status := payout.VerificationStatusVerified
	input := verifiedTestInput()
	input.VerifiedOnchain = &verified
	input.VerificationStatus = &status

	repo.On("FindByTxnHash", ctx, "0xverifierhash").Return(nil, apperrors.ErrNotFound)
	repo.On("FindByCircleAndRound", ctx, mock.AnythingOfType("uuid.UUID"), 1).Return(nil, apperrors.ErrNotFound)
	repo.On("Create", ctx, mock.MatchedBy(func(p *payout.Payout) bool {
		return p.VerifiedOnchain && p.VerificationStatus == payout.VerificationStatusVerified
	})).Return(nil)

	p, err := svc.Record(ctx, input)

	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, 0, verifier.calls, "explicit overrides must skip the receipt lookup")
	repo.AssertExpectations(t)
}
