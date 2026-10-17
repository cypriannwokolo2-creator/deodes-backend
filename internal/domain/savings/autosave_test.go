package savings_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/savings"
)

type fakeAutoRepo struct {
	savings.Repository
	goals   []savings.SavingsGoal
	topped  []string
	claimOK bool
}

func (f *fakeAutoRepo) DueAutoGoals(_ context.Context, _ time.Time) ([]savings.SavingsGoal, error) {
	return f.goals, nil
}

func (f *fakeAutoRepo) RecordTopUp(_ context.Context, goalID string, _ float64) (bool, error) {
	f.topped = append(f.topped, goalID)
	return f.claimOK, nil
}

type fakeAutoWallets struct {
	balance float64
}

func (f *fakeAutoWallets) GetBalance(_ context.Context, _ string) (float64, error) {
	return f.balance, nil
}

func TestAutoSaver_TopsUpFundedDueGoals(t *testing.T) {
	repo := &fakeAutoRepo{
		goals: []savings.SavingsGoal{
			{ID: "g1", UserID: "u1", Status: "active", AutoAmount: 50, CurrentAmount: 0, TargetAmount: 500},
			{ID: "g2", UserID: "u1", Status: "completed", AutoAmount: 50},
			{ID: "g3", UserID: "u1", Status: "active", AutoAmount: 0},
		},
		claimOK: true,
	}
	saver := savings.NewAutoSaver(repo, &fakeAutoWallets{balance: 1000})

	applied, err := saver.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, applied)
	assert.Equal(t, []string{"g1"}, repo.topped)
}

func TestAutoSaver_SkipsUnfundedWallets(t *testing.T) {
	repo := &fakeAutoRepo{
		goals:   []savings.SavingsGoal{{ID: "g1", UserID: "u1", Status: "active", AutoAmount: 50}},
		claimOK: true,
	}
	saver := savings.NewAutoSaver(repo, &fakeAutoWallets{balance: 10})

	applied, err := saver.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, applied)
	assert.Empty(t, repo.topped)
}

func TestAutoSaver_LostRaceCountsZero(t *testing.T) {
	repo := &fakeAutoRepo{
		goals:   []savings.SavingsGoal{{ID: "g1", UserID: "u1", Status: "active", AutoAmount: 50}},
		claimOK: false,
	}
	saver := savings.NewAutoSaver(repo, &fakeAutoWallets{balance: 1000})

	applied, err := saver.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, applied)
}
