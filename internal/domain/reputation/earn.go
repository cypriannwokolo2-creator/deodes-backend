package reputation

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ContributionStats aggregates a user's confirmed-contribution volume and
// recency. Implemented by the contribution repository.
type ContributionStats interface {
	StatsByUser(ctx context.Context, userID uuid.UUID) (volume float64, lastAt *time.Time, err error)
}

// StreakReader reports the user's current savings streak. Implemented by
// the incentives service (a missing streak reads as zero, not an error
// at the Refresher level — adapters normalize).
type StreakReader interface {
	CurrentStreak(ctx context.Context, userID string) (int, error)
}

// CompletionsReader counts the user's completed circles. Implemented by
// the circle service.
type CompletionsReader interface {
	CompletedCircles(ctx context.Context, userID string) (int, error)
}

// ScoreStore persists the computed score onto the user row (the number
// every gate and surface reads). Implemented by the user repository.
type ScoreStore interface {
	UpdateMoiScore(ctx context.Context, id uuid.UUID, score int) error
}

// Refresher recomputes one user's DeoScore from live domain data and
// persists it to both the reputation snapshot table and the users row.
// Any provider failure aborts the refresh without persisting, so a
// partial outage never writes a degraded score.
type Refresher struct {
	stats       ContributionStats
	streaks     StreakReader
	completions CompletionsReader
	scores      Service
	store       ScoreStore
}

func NewRefresher(stats ContributionStats, streaks StreakReader, completions CompletionsReader, scores Service, store ScoreStore) *Refresher {
	return &Refresher{stats: stats, streaks: streaks, completions: completions, scores: scores, store: store}
}

// RefreshUser recomputes and persists the user's score, returning it.
func (r *Refresher) RefreshUser(ctx context.Context, userID string) (int, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return 0, err
	}

	volume, lastAt, err := r.stats.StatsByUser(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("contribution stats: %w", err)
	}
	streaks, err := r.streaks.CurrentStreak(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("streak: %w", err)
	}
	completed, err := r.completions.CompletedCircles(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("completed circles: %w", err)
	}
	var daysSinceLast int
	if lastAt == nil {
		daysSinceLast = 365
	} else {
		daysSinceLast = int(time.Since(*lastAt).Hours() / 24)
		if daysSinceLast < 0 {
			daysSinceLast = 0
		}
	}

	snapshot, err := r.scores.UpdateScore(ctx, userID, streaks, completed, volume, daysSinceLast)
	if err != nil {
		return 0, fmt.Errorf("saving snapshot: %w", err)
	}
	if err := r.store.UpdateMoiScore(ctx, uid, snapshot.Score); err != nil {
		return 0, fmt.Errorf("persisting user score: %w", err)
	}
	return snapshot.Score, nil
}

// RefreshScore adapts Refresher to contribution.ScoreUpdater.
func (r *Refresher) RefreshScore(ctx context.Context, userID string) error {
	_, err := r.RefreshUser(ctx, userID)
	return err
}
