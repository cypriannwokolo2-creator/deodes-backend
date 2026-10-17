package subscription

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/deodes/backend/internal/domain/circle"
	"github.com/deodes/backend/internal/domain/contribution"
	"github.com/deodes/backend/internal/domain/wallet"
	"github.com/deodes/backend/pkg/logger"
)

// DebitOutcome classifies what happened to one due subscription.
type DebitOutcome string

const (
	OutcomeDebited           DebitOutcome = "debited"
	OutcomeCaughtUp          DebitOutcome = "caught_up"
	OutcomeSkippedLowBalance DebitOutcome = "skipped_low_balance"
	OutcomeSkippedCapped     DebitOutcome = "skipped_capped"
	OutcomeFailed            DebitOutcome = "failed"
)

// DebitEvent reports one keeper decision for observability and notifications.
// Failed sends always carry TxHash when money moved, so an operator can
// reconcile against the escrow balance (same pattern as deposit webhooks).
type DebitEvent struct {
	SubscriptionID string
	UserID         string
	CircleID       string
	Round          int
	Amount         string
	TxHash         string
	Outcome        DebitOutcome
	Reason         string
}

// RunSummary counts one keeper pass.
type RunSummary struct {
	Due     int
	Debited int
	Skipped int
	Failed  int
}

// Keeper pulls scheduled contributions for active subscriptions.
// Single-owner by deployment (one replica runs the loop, jittered): the
// last_round compare-and-swap in RecordDebit is a second line of defense,
// not a distributed lock. Concurrent keepers could double-debit a round —
// do not run two.
//
// Dependencies are narrow interfaces so the keeper stays decoupled from the
// full domain services it orchestrates.
type Keeper struct {
	repo    Repository
	wallets WalletDebiter
	contrib ContributionRecorder
	circles CircleReader
	// escrow is the custodial Arc collection address holding circle funds
	// until the Circle escrow contract exists. Tracked per member/round in
	// the contributions table.
	escrow   string
	notifier Notifier
	clock    func() time.Time
	onEvent  func(context.Context, DebitEvent)
}

// WalletDebiter is the wallet surface the keeper needs: balance guard plus
// custodial send. Satisfied by *wallet.Service.
type WalletDebiter interface {
	SendPayment(ctx context.Context, userID string, passkeySeed []byte, destination, asset string, amount float64, memo, ipAddress, userAgent string) (string, error)
	GetBalance(ctx context.Context, userID string) (*wallet.Balance, error)
}

// ContributionRecorder persists settled debits. Satisfied by
// *contribution.Service; Record's txnHash-conflict path makes replays safe.
type ContributionRecorder interface {
	Record(ctx context.Context, input contribution.RecordInput) (*contribution.Contribution, error)
}

// Notifier delivers member-facing messages (reminders, debit outcomes).
// Satisfied by an adapter over *notification.Service (see NewNotifier).
type Notifier interface {
	NotifyContributionDue(ctx context.Context, userID, circleID, amount string, dueIn time.Duration) error
	NotifyDebited(ctx context.Context, userID, circleID, amount string, round int, txHash string) error
	NotifyDebitFailed(ctx context.Context, userID, circleID, amount string, round int, reason string) error
}

// CircleReader supplies schedule state. Satisfied by *circle.Service.
type CircleReader interface {
	Get(ctx context.Context, id string) (*circle.Circle, error)
}

type KeeperConfig struct {
	Repo    Repository
	Wallets WalletDebiter
	Contrib ContributionRecorder
	Circles CircleReader
	// EscrowAddress is the 0x collection address for debits.
	EscrowAddress string
	// TreasuryAddress is the protocol-fee vault (DeodesCircle treasury).
	// Phase 0.6 freeze: it MUST differ from EscrowAddress. The V0 mainnet
	// deployment used one EOA for both roles (0x51b45B...); commingling
	// protocol revenue with user-debit collection means one compromised key
	// drains both, and accounting cannot separate them. Empty disables the
	// check (single-role test setups).
	TreasuryAddress string
	// Notifier is optional (nil = log-only); reminders and outcome
	// notifications are skipped without one.
	Notifier Notifier
	Clock    func() time.Time
	OnEvent  func(context.Context, DebitEvent)
}

// NewKeeper validates wiring. Escrow must be a 0x address — misdirected
// debits are irrecoverable — and must not equal the protocol treasury.
func NewKeeper(cfg KeeperConfig) (*Keeper, error) {
	if cfg.Repo == nil || cfg.Wallets == nil || cfg.Contrib == nil || cfg.Circles == nil {
		return nil, fmt.Errorf("keeper requires repo, wallets, contrib and circles")
	}
	addr := strings.TrimSpace(cfg.EscrowAddress)
	if len(addr) != 42 || !(strings.HasPrefix(addr, "0x") || strings.HasPrefix(addr, "0X")) {
		return nil, fmt.Errorf("invalid escrow address %q", cfg.EscrowAddress)
	}
	if treasury := strings.TrimSpace(cfg.TreasuryAddress); treasury != "" &&
		strings.EqualFold(addr, treasury) {
		return nil, fmt.Errorf("escrow address must differ from protocol treasury address (Phase 0.6 freeze: fee vault and debit collector must be separate keys)")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Keeper{
		repo: cfg.Repo, wallets: cfg.Wallets, contrib: cfg.Contrib,
		circles: cfg.Circles, escrow: addr, clock: clock, onEvent: cfg.OnEvent,
		notifier: cfg.Notifier,
	}, nil
}

func (k *Keeper) emit(ctx context.Context, e DebitEvent) {
	if k.onEvent != nil {
		k.onEvent(ctx, e)
	}
}

func (k *Keeper) notify() Notifier {
	if k.notifier == nil {
		return nilNotifier{}
	}
	return k.notifier
}

// RemindUpcoming notifies members debited within window (T-24h style).
// Each subscription is reminded once per debit: MarkReminded stamps it, and
// ListRemindable only returns rows whose debit moved since the last stamp.
func (k *Keeper) RemindUpcoming(ctx context.Context, window time.Duration) (int, error) {
	now := k.clock().UTC()
	subs, err := k.repo.ListRemindable(ctx, now, window)
	if err != nil {
		return 0, err
	}
	sent := 0
	for i := range subs {
		sub := &subs[i]
		dueIn := sub.NextDebitAt.Sub(now)
		if dueIn < 0 {
			dueIn = 0
		}
		if err := k.notify().NotifyContributionDue(ctx, sub.UserID, sub.CircleID, sub.Amount, dueIn); err != nil {
			l := logger.Ctx(ctx)
			l.Error().Err(err).Str("subscription", sub.ID).Msg("keeper: reminder notify failed")
			continue
		}
		if err := k.repo.MarkReminded(ctx, sub.ID, now); err != nil {
			l := logger.Ctx(ctx)
			l.Error().Err(err).Str("subscription", sub.ID).Msg("keeper: mark reminded failed")
			continue
		}
		sent++
	}
	return sent, nil
}

// RunOnce processes up to limit due subscriptions.
func (k *Keeper) RunOnce(ctx context.Context, limit int) RunSummary {
	now := k.clock().UTC()
	subs, err := k.repo.ListDue(ctx, now, limit)
	if err != nil {
		l := logger.Ctx(ctx)
		l.Error().Err(err).Msg("keeper: listing due subscriptions")
		return RunSummary{}
	}
	sum := RunSummary{Due: len(subs)}
	for i := range subs {
		switch k.processOne(ctx, now, &subs[i]) {
		case OutcomeDebited:
			sum.Debited++
		case OutcomeCaughtUp, OutcomeSkippedLowBalance, OutcomeSkippedCapped:
			sum.Skipped++
		default:
			sum.Failed++
		}
	}
	return sum
}

func (k *Keeper) processOne(ctx context.Context, now time.Time, sub *Subscription) DebitOutcome {
	base := DebitEvent{SubscriptionID: sub.ID, UserID: sub.UserID, CircleID: sub.CircleID, Amount: sub.Amount}

	cir, err := k.circles.Get(ctx, sub.CircleID)
	if err != nil {
		k.emit(ctx, withReason(base, 0, OutcomeFailed, "getting circle: "+err.Error()))
		return OutcomeFailed
	}
	if cir.Status == circle.CircleStatusCompleted || cir.Status == circle.CircleStatusCancelled ||
		cir.Status == circle.CircleStatusBanned {
		if err := k.repo.UpdateStatus(ctx, sub.ID, StatusCompleted); err != nil {
			k.emit(ctx, withReason(base, 0, OutcomeFailed, err.Error()))
			return OutcomeFailed
		}
		k.emit(ctx, withReason(base, 0, OutcomeSkippedCapped, "circle "+string(cir.Status)))
		return OutcomeSkippedCapped
	}
	if cir.Status == circle.CircleStatusPaused {
		// Pause is temporary: postpone this debit but keep the consent row
		// so resuming the circle restarts debits cleanly.
		_ = k.repo.Postpone(ctx, sub.ID, k.nextAfter(now, cir.Frequency))
		k.emit(ctx, withReason(base, 0, OutcomeSkippedCapped, "circle paused"))
		return OutcomeSkippedCapped
	}
	if !cir.AutoDebit {
		// Organizer flipped the circle back to Flexible: stop pulling but
		// keep the consent row so flipping back to Auto resumes cleanly.
		_ = k.repo.Postpone(ctx, sub.ID, k.nextAfter(now, cir.Frequency))
		k.emit(ctx, withReason(base, 0, OutcomeCaughtUp, "circle is flexible (manual)"))
		return OutcomeCaughtUp
	}
	round := cir.CurrentRound
	if round < 1 {
		_ = k.repo.Postpone(ctx, sub.ID, k.nextAfter(now, cir.Frequency))
		k.emit(ctx, withReason(base, 0, OutcomeCaughtUp, "circle has not started"))
		return OutcomeCaughtUp
	}
	if sub.LastRound >= round {
		// Already paid this round (manual payment or earlier pass): advance
		// the cadence without moving money.
		_ = k.repo.Postpone(ctx, sub.ID, k.nextAfter(maxTime(now, sub.NextDebitAt), cir.Frequency))
		k.emit(ctx, withReason(base, round, OutcomeCaughtUp, "round already paid"))
		return OutcomeCaughtUp
	}
	if k.reachesCap(sub) {
		if err := k.repo.UpdateStatus(ctx, sub.ID, StatusCompleted); err != nil {
			k.emit(ctx, withReason(base, round, OutcomeFailed, err.Error()))
			return OutcomeFailed
		}
		k.emit(ctx, withReason(base, round, OutcomeSkippedCapped, "consent cap reached"))
		return OutcomeSkippedCapped
	}
	ok, err := k.hasBalance(ctx, sub)
	if err != nil {
		k.emit(ctx, withReason(base, round, OutcomeFailed, err.Error()))
		return OutcomeFailed
	}
	if !ok {
		// Never overdraft: skip to next round and notify. The member can
		// still pay manually; the normal late-fee path applies if they don't.
		_ = k.repo.Postpone(ctx, sub.ID, k.nextAfter(now, cir.Frequency))
		k.emit(ctx, withReason(base, round, OutcomeSkippedLowBalance, "insufficient USDC balance"))
		return OutcomeSkippedLowBalance
	}

	amtFloat, err := parseAmountFloat(sub.Amount)
	if err != nil {
		k.emit(ctx, withReason(base, round, OutcomeFailed, err.Error()))
		return OutcomeFailed
	}
	txHash, err := k.wallets.SendPayment(ctx, sub.UserID, nil, k.escrow, "USDC", amtFloat, "", "", "subscription-keeper")
	if err != nil {
		_ = k.repo.Postpone(ctx, sub.ID, k.nextAfter(now, cir.Frequency))
		k.emit(ctx, DebitEvent{SubscriptionID: sub.ID, UserID: sub.UserID, CircleID: sub.CircleID,
			Round: round, Amount: sub.Amount, Outcome: OutcomeFailed, Reason: "send: " + err.Error()})
		return OutcomeFailed
	}

	verified := true
	verifiedStatus := contribution.VerificationStatusVerified
	if _, err := k.contrib.Record(ctx, contribution.RecordInput{
		CircleID:           sub.CircleID,
		UserID:             sub.UserID,
		RoundNumber:        round,
		Amount:             amtFloat,
		TxnHash:            txHash,
		VerifiedOnchain:    &verified,
		VerificationStatus: &verifiedStatus,
	}); err != nil {
		// Money moved but the ledger write failed: do NOT advance. The event
		// carries the tx hash for manual reconciliation (deposit-webhook
		// pattern). Next pass retries; the contributions UNIQUE backstop
		// turns a repeated round into a conflict rather than silent loss.
		k.emit(ctx, DebitEvent{SubscriptionID: sub.ID, UserID: sub.UserID, CircleID: sub.CircleID,
			Round: round, Amount: sub.Amount, TxHash: txHash,
			Outcome: OutcomeFailed, Reason: "record contribution: " + err.Error()})
		return OutcomeFailed
	}

	next := k.nextAfter(maxTime(now, sub.NextDebitAt), cir.Frequency)
	applied, err := k.repo.RecordDebit(ctx, sub.ID, sub.LastRound, round, sub.Amount, next)
	if err != nil || !applied {
		reason := "lost race advancing subscription"
		if err != nil {
			reason = err.Error()
		}
		k.emit(ctx, DebitEvent{SubscriptionID: sub.ID, UserID: sub.UserID, CircleID: sub.CircleID,
			Round: round, Amount: sub.Amount, TxHash: txHash,
			Outcome: OutcomeFailed, Reason: reason})
		return OutcomeFailed
	}
	k.emit(ctx, DebitEvent{SubscriptionID: sub.ID, UserID: sub.UserID, CircleID: sub.CircleID,
		Round: round, Amount: sub.Amount, TxHash: txHash, Outcome: OutcomeDebited})
	return OutcomeDebited
}

func withReason(e DebitEvent, round int, o DebitOutcome, reason string) DebitEvent {
	e.Round = round
	e.Outcome = o
	e.Reason = reason
	return e
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// nextAfter advances t by one circle period. Unknown frequencies fail loud:
// silently guessing a debit cadence moves real money on a wrong schedule.
func (k *Keeper) nextAfter(t time.Time, freq circle.CircleFrequency) time.Time {
	switch freq {
	case circle.FrequencyDaily:
		return t.Add(24 * time.Hour)
	case circle.FrequencyWeekly:
		return t.Add(7 * 24 * time.Hour)
	case circle.FrequencyBiweekly:
		return t.Add(14 * 24 * time.Hour)
	case circle.FrequencyMonthly:
		return t.AddDate(0, 1, 0)
	default:
		l := logger.Ctx(context.Background())
		l.Error().
			Str("frequency", string(freq)).Msg("keeper: unknown frequency, defaulting to monthly")
		return t.AddDate(0, 1, 0)
	}
}

// reachesCap reports whether one more debit would exceed the consent cap,
// compared exactly (big.Rat, never float).
func (k *Keeper) reachesCap(sub *Subscription) bool {
	collected, ok1 := parseDecimal(sub.CollectedTotal)
	amount, ok2 := parseDecimal(sub.Amount)
	cap, ok3 := parseDecimal(sub.CapTotal)
	if !ok1 || !ok2 || !ok3 {
		return true // Unparseable ledger: refuse to debit (fail closed).
	}
	next := new(big.Rat).Add(collected, amount)
	return next.Cmp(cap) > 0
}

// hasBalance checks the hosted USDC balance covers the debit, exactly.
func (k *Keeper) hasBalance(ctx context.Context, sub *Subscription) (bool, error) {
	bal, err := k.wallets.GetBalance(ctx, sub.UserID)
	if err != nil {
		return false, fmt.Errorf("reading balance: %w", err)
	}
	have, ok1 := parseDecimal(trimFloat(bal.USDC))
	want, ok2 := parseDecimal(sub.Amount)
	if !ok1 || !ok2 {
		return false, fmt.Errorf("unparseable balance or amount")
	}
	return have.Cmp(want) >= 0, nil
}

func parseDecimal(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() < 0 {
		return nil, false
	}
	return r, true
}

func parseAmountFloat(s string) (float64, error) {
	r, ok := parseDecimal(s)
	if !ok {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	f, _ := r.Float64()
	if f <= 0 {
		return 0, fmt.Errorf("amount must be positive")
	}
	return f, nil
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', 18, 64)
}
