package savings

import (
	"context"
	"fmt"
	"time"

	"github.com/deodes/backend/pkg/logger"
)

// Auto-save keeper: tops up goal current_amount on schedule. Funds stay in
// the user's own wallet (goals earmark allocation — the delete copy already
// promises "saved USDC stays in your wallet"), so a top-up verifies the
// wallet holds the amount and then bumps the allocation, capped at target.
// The global users.savings_auto_reserve toggle is the master switch and is
// filtered in the due query itself.
var autoFrequencyIntervals = map[string]time.Duration{
	"daily":    24 * time.Hour,
	"weekly":   7 * 24 * time.Hour,
	"biweekly": 14 * 24 * time.Hour,
	"monthly":  30 * 24 * time.Hour,
}

// BalanceChecker is the narrow wallet interface the keeper needs (same shape
// as the circle funds gate — main wires the same adapter).
type BalanceChecker interface {
	GetBalance(ctx context.Context, userID string) (float64, error)
}

type AutoSaver struct {
	repo    Repository
	wallets BalanceChecker
}

func NewAutoSaver(repo Repository, wallets BalanceChecker) *AutoSaver {
	return &AutoSaver{repo: repo, wallets: wallets}
}

// RunOnce tops up every due goal. The claim inside RecordTopUp is atomic
// (last_auto_at compare-and-set), so concurrent replicas cannot double-apply.
func (a *AutoSaver) RunOnce(ctx context.Context) (int, error) {
	due, err := a.repo.DueAutoGoals(ctx, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("listing due auto-save goals: %w", err)
	}
	applied := 0
	for i := range due {
		g := &due[i]
		if g.AutoAmount <= 0 || g.Status != "active" {
			continue
		}
		bal, err := a.wallets.GetBalance(ctx, g.UserID)
		if err != nil || bal < g.AutoAmount {
			continue
		}
		claimed, err := a.repo.RecordTopUp(ctx, g.ID, g.AutoAmount)
		if err != nil {
			l := logger.Ctx(ctx)
			l.Warn().Err(err).Str("goal", g.ID).Msg("auto-save top-up failed")
			continue
		}
		if claimed {
			applied++
		}
	}
	return applied, nil
}
