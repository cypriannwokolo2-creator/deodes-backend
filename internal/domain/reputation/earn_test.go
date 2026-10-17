package reputation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/reputation"
)

type stubReputationRepo struct {
	saved *reputation.ReputationSnapshot
}

func (s *stubReputationRepo) GetByUser(_ context.Context, _ uuid.UUID) (*reputation.ReputationSnapshot, error) {
	return s.saved, nil
}

func (s *stubReputationRepo) SaveSnapshot(_ context.Context, snap *reputation.ReputationSnapshot) error {
	s.saved = snap
	return nil
}

func (s *stubReputationRepo) GetHistory(_ context.Context, _ uuid.UUID, _ int) ([]reputation.ReputationSnapshot, error) {
	return nil, nil
}

func (s *stubReputationRepo) TopScores(_ context.Context, _ int) ([]reputation.LeaderboardEntry, error) {
	return nil, nil
}

type stubStats struct {
	volume float64
	lastAt *time.Time
	err    error
}

func (s *stubStats) StatsByUser(_ context.Context, _ uuid.UUID) (float64, *time.Time, error) {
	return s.volume, s.lastAt, s.err
}

type stubStreaks struct {
	streak int
	err    error
}

func (s *stubStreaks) CurrentStreak(_ context.Context, _ string) (int, error) {
	return s.streak, s.err
}

type stubCompletions struct {
	completed int
	err       error
}

func (s *stubCompletions) CompletedCircles(_ context.Context, _ string) (int, error) {
	return s.completed, s.err
}

type stubScoreStore struct {
	scores map[uuid.UUID]int
	err    error
}
func (s *stubScoreStore) UpdateMoiScore(_ context.Context, id uuid.UUID, score int) error {
	if s.err != nil {
		return s.err
	}
	if s.scores == nil {
		s.scores = map[uuid.UUID]int{}
	}
	s.scores[id] = score
	return nil
}

func TestRefresher_RefreshUser_PersistsSnapshotAndRow(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()
	lastAt := time.Now().UTC().Add(-24 * time.Hour)

	// Real reputation service over a stub repository.
	repo := &stubReputationRepo{}
	scores := reputation.NewService(repo)
	store := &stubScoreStore{}
	r := reputation.NewRefresher(
		&stubStats{volume: 500, lastAt: &lastAt},
		&stubStreaks{streak: 4},
		&stubCompletions{completed: 2},
		scores,
		store,
	)

	score, err := r.RefreshUser(ctx, userID)

	require.NoError(t, err)
	// streaks 4*35=140, completions 2*50=100, volume log(500)*30≈186,
	// recency 150-1*5=145 → 571.
	assert.Equal(t, 571, score)
	require.NotNil(t, repo.saved)
	assert.Equal(t, 571, repo.saved.Score)
	uid, _ := uuid.Parse(userID)
	assert.Equal(t, 571, store.scores[uid])
}

func TestRefresher_RefreshUser_NoHistoryYieldsRecencyOnly(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()

	repo := &stubReputationRepo{}
	scores := reputation.NewService(repo)
	store := &stubScoreStore{}
	r := reputation.NewRefresher(
		&stubStats{volume: 0, lastAt: nil},
		&stubStreaks{streak: 0},
		&stubCompletions{completed: 0},
		scores,
		store,
	)

	score, err := r.RefreshUser(ctx, userID)

	require.NoError(t, err)
	// volume log(1)*30=0, recency 150-365*5 floored at 0 → 0.
	assert.Equal(t, 0, score)
}

func TestRefresher_RefreshUser_ProviderFailurePersistsNothing(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()

	repo := &stubReputationRepo{}
	scores := reputation.NewService(repo)
	store := &stubScoreStore{}
	r := reputation.NewRefresher(
		&stubStats{err: errors.New("db down")},
		&stubStreaks{streak: 4},
		&stubCompletions{completed: 2},
		scores,
		store,
	)

	_, err := r.RefreshUser(ctx, userID)

	require.Error(t, err)
	assert.Nil(t, repo.saved, "no snapshot on provider failure")
	assert.Empty(t, store.scores, "no row write on provider failure")
}

func TestRefresher_RefreshUser_InvalidUserID(t *testing.T) {
	repo := &stubReputationRepo{}
	r := reputation.NewRefresher(
		&stubStats{},
		&stubStreaks{},
		&stubCompletions{},
		reputation.NewService(repo),
		&stubScoreStore{},
	)

	_, err := r.RefreshUser(context.Background(), "not-a-uuid")

	require.Error(t, err)
	assert.Nil(t, repo.saved)
}
