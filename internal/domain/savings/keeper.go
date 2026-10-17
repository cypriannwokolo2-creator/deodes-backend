package savings

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"

	deodesredis "github.com/deodes/backend/pkg/redis"
)

const (
	// DefaultAutoSaveInterval matches the subscription keeper cadence.
	DefaultAutoSaveInterval = 15 * time.Minute
	// AutoSaveLockKey serializes keeper ticks across replicas. The atomic
	// last_auto_at claim in RecordTopUp is the real safety net; the lock
	// just avoids duplicate balance reads.
	AutoSaveLockKey = "locks:savings-autosave"
)

// Keeper tops up due auto-save goals on a tick. Mirrors the swap sweeper
// lifecycle (Start/Stop, single-flight lock).
type Keeper struct {
	saver    *AutoSaver
	interval time.Duration
	lock     *deodesredis.Lock
	stopCh   chan struct{}
	doneCh   chan struct{}
	mu       sync.Mutex
	started  bool
	stopOnce sync.Once
}

func NewKeeper(repo Repository, wallets BalanceChecker, rdb *redis.Client, interval time.Duration) *Keeper {
	if interval <= 0 {
		interval = DefaultAutoSaveInterval
	}
	return &Keeper{
		saver:    NewAutoSaver(repo, wallets),
		interval: interval,
		lock:     deodesredis.NewLock(rdb, AutoSaveLockKey, deodesredis.DefaultLockTTL),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

func (k *Keeper) Start(ctx context.Context) {
	k.mu.Lock()
	if k.started {
		k.mu.Unlock()
		return
	}
	k.started = true
	k.mu.Unlock()

	go k.run(ctx)
}

func (k *Keeper) Stop() {
	k.stopOnce.Do(func() { close(k.stopCh) })
	select {
	case <-k.doneCh:
	case <-time.After(k.interval):
		log.Warn().Msg("savings auto-save worker did not stop within one interval")
	}
}

func (k *Keeper) run(ctx context.Context) {
	defer close(k.doneCh)
	log.Info().Dur("interval", k.interval).Msg("savings auto-save worker started")

	ticker := time.NewTicker(k.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			k.tick(ctx)
		case <-ctx.Done():
			return
		case <-k.stopCh:
			return
		}
	}
}

func (k *Keeper) tick(ctx context.Context) {
	if k.lock != nil {
		locked, err := k.lock.Acquire(ctx)
		if err != nil || !locked {
			return
		}
		defer k.lock.Release()
	}
	applied, err := k.saver.RunOnce(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("savings auto-save tick failed")
		return
	}
	if applied > 0 {
		log.Info().Int("applied", applied).Msg("savings auto-save tick applied top-ups")
	}
}
