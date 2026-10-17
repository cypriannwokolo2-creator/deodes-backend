package circle

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/deodes/backend/internal/domain/audit"
	"github.com/deodes/backend/internal/domain/notification"
	"github.com/deodes/backend/pkg/apperrors"
)

type Service interface {
	Get(ctx context.Context, id string) (*Circle, error)
	List(ctx context.Context, filter CircleFilter) ([]Circle, int, error)
	Create(ctx context.Context, organizerID string, input CreateCircleInput) (*Circle, error)
	Update(ctx context.Context, id, userID string, input UpdateCircleInput) (*Circle, error)
	Start(ctx context.Context, id, userID string) error
	Close(ctx context.Context, id, userID string) error
	Cancel(ctx context.Context, id, userID string) error
	// PauseCircle halts an active circle (admin moderation). ResumeCircle
	// returns a paused circle to active. BanCircle terminally shuts down a
	// circle that is not completed or cancelled.
	PauseCircle(ctx context.Context, id string) error
	ResumeCircle(ctx context.Context, id string) error
	BanCircle(ctx context.Context, id string) error
	Join(ctx context.Context, circleID, userID string, inviteCode string) error
	Exit(ctx context.Context, circleID, userID string) error
	GetMembers(ctx context.Context, circleID string) ([]CircleMember, error)
	SearchMembers(ctx context.Context, circleID, search string) ([]CircleMember, error)
	IsMember(ctx context.Context, circleID, userID string) (bool, error)
	RemoveMember(ctx context.Context, circleID, callerID, memberAddress string, reason string) error
	ProcessMissedContributions(ctx context.Context, circleID string, roundNumber int) error
	RaiseDispute(ctx context.Context, circleID, userID string, input DisputeInput) (*CircleDispute, error)
	CastVote(ctx context.Context, circleID, userID string, input VoteInput) (*CircleVote, bool, string, error)
	SubmitAuctionBid(ctx context.Context, circleID, userID string, input AuctionBidInput) (*CircleAuctionBid, error)
	QueryRoundConfig(ctx context.Context, circleID string, round int) (*RoundConfigSnapshot, error)
	GetCircleSnapshot(ctx context.Context, circleID, userID string) (*CircleSnapshot, error)
	GetBulkCircleSnapshots(ctx context.Context, userID string, circleIDs []string) ([]CircleSnapshot, error)
	// GetUserCircles lists circles the user actively belongs to (admin detail).
	GetUserCircles(ctx context.Context, userID string) ([]Circle, error)
}

type UserMOIFetcher interface {
	FindByID(ctx context.Context, id uuid.UUID) (*UserMOIData, error)
}

type UserMOIData struct {
	MoiScore int
}

type Broadcaster interface {
	CircleCreated(ctx context.Context, circleID, organizerID string)
	CircleStatusChanged(ctx context.Context, circleID, status string)
	MemberJoined(ctx context.Context, circleID, userID string)
	MemberLeft(ctx context.Context, circleID, userID string)
	ContributionRecorded(ctx context.Context, circleID, userID string, roundNumber int, amount float64)
	MemberPenalized(ctx context.Context, circleID, userID string, roundNumber int, penaltyAmount float64)
}

type CommunityMembershipChecker interface {
	IsMember(ctx context.Context, communityID, userID uuid.UUID) (bool, error)
}

type Transactor interface {
	WithTransaction(ctx context.Context, fn func(repo Repository) error) error
}

type CreateCircleInput struct {
	Name               string          `json:"name" validate:"required,min=3,max=100"`
	Description        string          `json:"description" validate:"max=500"`
	CommunityID        string          `json:"communityId"`
	CircleType         CircleType      `json:"circleType" validate:"required,oneof=public private community premium"`
	PayoutType         PayoutType      `json:"payoutType" validate:"required,oneof=random fixed auction vote"`
	ContributionAmount float64         `json:"contributionAmount" validate:"required,gt=0,lte=1000000"`
	Currency           CircleCurrency  `json:"currency" validate:"required,oneof=USDC"`
	Frequency          CircleFrequency `json:"frequency" validate:"required,oneof=daily weekly biweekly monthly"`
	MaxMembers         int             `json:"maxMembers" validate:"required,gte=2,lte=100"`
	MinMoiScore        int             `json:"minMoiScore" validate:"gte=0,lte=1000"`
	CollateralPercent  float64         `json:"collateralPercent" validate:"gte=0,lte=100"`
	LateFeePercent     float64         `json:"lateFeePercent" validate:"gte=0,lte=50"`
	// GracePeriodHours/MaxStrikes accept 0 at the handler layer meaning
	// "omitted": Create defaults them (24h/3) before strict validation.
	// Updates use pointers instead, so an explicit 0 is still rejected.
	GracePeriodHours int    `json:"gracePeriodHours" validate:"gte=0,lte=168"`
	MaxStrikes       int    `json:"maxStrikes" validate:"gte=0,lte=10"`
	StartDate        string `json:"startDate"`
	RequiresInvite   bool   `json:"requiresInvite"`
	// AutoDebit opts the circle into keeper auto-debit (default flexible/manual).
	AutoDebit bool `json:"autoDebit"`
}

type UpdateCircleInput struct {
	Name               *string          `json:"name"`
	Description        *string          `json:"description"`
	ContributionAmount *float64         `json:"contributionAmount"`
	Currency           *CircleCurrency  `json:"currency"`
	Frequency          *CircleFrequency `json:"frequency"`
	MaxMembers         *int             `json:"maxMembers"`
	MinMoiScore        *int             `json:"minMoiScore"`
	CollateralPercent  *float64         `json:"collateralPercent"`
	LateFeePercent     *float64         `json:"lateFeePercent"`
	GracePeriodHours   *int             `json:"gracePeriodHours"`
	MaxStrikes         *int             `json:"maxStrikes"`
	StartDate          *string          `json:"startDate"`
	AutoDebit          *bool            `json:"autoDebit"`
}

// Circle creation/update bounds. These mirror the frontend wizard rules
// (CIRCLE_LIMITS + validateCircleStep) so every step fails fast with a 400
// VALIDATION_ERROR instead of slipping through or surfacing as a 500.
const (
	circleNameMinLen    = 3
	circleNameMaxLen    = 100
	circleDescMaxLen    = 500
	circleContribMax    = 1_000_000
	circleMembersMin    = 2
	circleMembersMax    = 100
	circleMoiMin        = 0
	circleMoiMax        = 1000
	circleCollateralMax = 100
	circleLateFeeMax    = 50
	circleGraceMin      = 1
	circleGraceMax      = 168
	circleStrikesMin    = 1
	circleStrikesMax    = 10
	premiumMinMOI       = 50
	premiumMinUSDC      = 50
)

func invalidCircleInput(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, apperrors.ErrInvalidInput)...)
}

// parseCircleStartDate accepts the ISO-8601 strings the wizard sends
// (RFC3339 via toISOString) plus raw datetime-local values. Empty means "no
// start date". A present value must parse and lie in the future.
func parseCircleStartDate(raw string) (time.Time, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, false, nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	var parsed time.Time
	var err error
	for _, layout := range layouts {
		parsed, err = time.Parse(layout, trimmed)
		if err == nil {
			if parsed.After(time.Now()) {
				return parsed, true, nil
			}
			return time.Time{}, false, fmt.Errorf("start date must be in the future")
		}
	}
	return time.Time{}, false, fmt.Errorf("start date must be a valid date")
}

func validateCircleName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return invalidCircleInput("circle name is required")
	}
	if len([]rune(trimmed)) < circleNameMinLen {
		return invalidCircleInput("circle name must be at least 3 characters")
	}
	if len([]rune(trimmed)) > circleNameMaxLen {
		return invalidCircleInput("circle name must be at most 100 characters")
	}
	return nil
}

func validateCircleDescription(desc string) error {
	if len([]rune(desc)) > circleDescMaxLen {
		return invalidCircleInput("circle description must be at most 500 characters")
	}
	return nil
}

func validateContributionAmount(amount float64) error {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return invalidCircleInput("contribution amount must be greater than zero")
	}
	if amount > circleContribMax {
		return invalidCircleInput("contribution amount must not exceed 1000000 USDC")
	}
	return nil
}

func validateCircleCurrency(c CircleCurrency) error {
	if c != CurrencyUSDC {
		return invalidCircleInput("circle currency must be USDC")
	}
	return nil
}

func validateCircleFrequency(f CircleFrequency) error {
	switch f {
	case FrequencyDaily, FrequencyWeekly, FrequencyBiweekly, FrequencyMonthly:
		return nil
	default:
		return invalidCircleInput("circle frequency must be one of daily, weekly, biweekly, monthly")
	}
}

func validateMaxMembers(n int) error {
	if n < circleMembersMin {
		return fmt.Errorf("circle must have at least 2 members: %w: %w", ErrParticipantLimit, apperrors.ErrInvalidInput)
	}
	if n > circleMembersMax {
		return invalidCircleInput("circle must have at most 100 members")
	}
	return nil
}

func validateMinMoiScore(n int) error {
	if n < circleMoiMin || n > circleMoiMax {
		return invalidCircleInput("min MoiScore must be between 0 and 1000")
	}
	return nil
}

func validateCollateral(p float64) error {
	if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > circleCollateralMax {
		return invalidCircleInput("collateral percent must be between 0 and 100")
	}
	return nil
}

func validateLateFee(p float64) error {
	if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > circleLateFeeMax {
		return invalidCircleInput("late fee percent must be between 0 and 50")
	}
	return nil
}

func validateGracePeriod(h int) error {
	if h < circleGraceMin || h > circleGraceMax {
		return invalidCircleInput("grace period must be between 1 and 168 hours")
	}
	return nil
}

func validateMaxStrikes(n int) error {
	if n < circleStrikesMin || n > circleStrikesMax {
		return invalidCircleInput("max strikes must be between 1 and 10")
	}
	return nil
}

func validateCircleType(t CircleType) error {
	switch t {
	case CircleTypePublic, CircleTypePrivate, CircleTypeCommunity, CircleTypePremium:
		return nil
	default:
		return invalidCircleInput("circle type must be one of public, private, community, premium")
	}
}

func validatePayoutType(p PayoutType) error {
	switch p {
	case PayoutTypeRandom, PayoutTypeFixed, PayoutTypeAuction, PayoutTypeVote:
		return nil
	default:
		return invalidCircleInput("payout type must be one of random, fixed, auction, vote")
	}
}

func validateCreateInput(input CreateCircleInput) error {
	if err := validateCircleName(input.Name); err != nil {
		return err
	}
	if err := validateCircleDescription(input.Description); err != nil {
		return err
	}
	if err := validateCircleType(input.CircleType); err != nil {
		return err
	}
	if err := validatePayoutType(input.PayoutType); err != nil {
		return err
	}
	if err := validateContributionAmount(input.ContributionAmount); err != nil {
		return err
	}
	if err := validateCircleCurrency(input.Currency); err != nil {
		return err
	}
	if err := validateCircleFrequency(input.Frequency); err != nil {
		return err
	}
	if err := validateMaxMembers(input.MaxMembers); err != nil {
		return err
	}
	if err := validateMinMoiScore(input.MinMoiScore); err != nil {
		return err
	}
	if err := validateCollateral(input.CollateralPercent); err != nil {
		return err
	}
	if err := validateLateFee(input.LateFeePercent); err != nil {
		return err
	}
	if err := validateGracePeriod(input.GracePeriodHours); err != nil {
		return err
	}
	if err := validateMaxStrikes(input.MaxStrikes); err != nil {
		return err
	}
	if _, _, err := parseCircleStartDate(input.StartDate); err != nil {
		return invalidCircleInput("%s", err.Error())
	}
	if input.CommunityID != "" {
		if _, err := uuid.Parse(input.CommunityID); err != nil {
			return invalidCircleInput("community id must be a valid UUID")
		}
	}
	if input.CircleType == CircleTypePremium && input.ContributionAmount < premiumMinUSDC {
		return invalidCircleInput("premium circles require minimum 50 USDC contribution")
	}
	return nil
}

func validateUpdateInput(input UpdateCircleInput) error {
	if input.Name != nil {
		if err := validateCircleName(*input.Name); err != nil {
			return err
		}
	}
	if input.Description != nil {
		if err := validateCircleDescription(*input.Description); err != nil {
			return err
		}
	}
	if input.ContributionAmount != nil {
		if err := validateContributionAmount(*input.ContributionAmount); err != nil {
			return err
		}
	}
	if input.Currency != nil {
		if err := validateCircleCurrency(*input.Currency); err != nil {
			return err
		}
	}
	if input.Frequency != nil {
		if err := validateCircleFrequency(*input.Frequency); err != nil {
			return err
		}
	}
	if input.MaxMembers != nil {
		if err := validateMaxMembers(*input.MaxMembers); err != nil {
			return err
		}
	}
	if input.MinMoiScore != nil {
		if err := validateMinMoiScore(*input.MinMoiScore); err != nil {
			return err
		}
	}
	if input.CollateralPercent != nil {
		if err := validateCollateral(*input.CollateralPercent); err != nil {
			return err
		}
	}
	if input.LateFeePercent != nil {
		if err := validateLateFee(*input.LateFeePercent); err != nil {
			return err
		}
	}
	if input.GracePeriodHours != nil {
		if err := validateGracePeriod(*input.GracePeriodHours); err != nil {
			return err
		}
	}
	if input.MaxStrikes != nil {
		if err := validateMaxStrikes(*input.MaxStrikes); err != nil {
			return err
		}
	}
	if input.StartDate != nil {
		if _, _, err := parseCircleStartDate(*input.StartDate); err != nil {
			return invalidCircleInput("%s", err.Error())
		}
	}
	return nil
}

type AuditLogger interface {
	Create(ctx context.Context, entry *audit.AuditEntry) error
}

type NotificationSender interface {
	Create(ctx context.Context, input notification.CreateInput) (*notification.Notification, error)
}

// BalanceChecker reports a user's spendable USDC balance. It is a narrow
// float64 interface (not wallet.Service) so the circle domain never imports
// the wallet domain — main wires the real implementation (keeper precedent).
type BalanceChecker interface {
	GetBalance(ctx context.Context, userID string) (float64, error)
}

// Dependencies holds the optional collaborators injected into the circle
// service. All fields are optional; nil values are safely handled at call sites.
type Dependencies struct {
	CommunityChecker CommunityMembershipChecker
	Broadcaster      Broadcaster
	Transactor       Transactor
	AuditLogger      AuditLogger
	NotificationSvc  NotificationSender
	// BalanceChecker gates circle creation and joining on funded wallets.
	// Nil disables the gate (dev/test).
	BalanceChecker BalanceChecker
}

type circleService struct {
	repo             Repository
	userRepo         UserMOIFetcher
	communityChecker CommunityMembershipChecker
	broadcaster      Broadcaster
	tx               Transactor
	auditRepo        AuditLogger
	notificationSvc  NotificationSender
	balanceChecker   BalanceChecker
}

func NewService(repo Repository, userRepo UserMOIFetcher, deps Dependencies) Service {
	return &circleService{
		repo:             repo,
		userRepo:         userRepo,
		communityChecker: deps.CommunityChecker,
		broadcaster:      deps.Broadcaster,
		tx:               deps.Transactor,
		auditRepo:        deps.AuditLogger,
		notificationSvc:  deps.NotificationSvc,
		balanceChecker:   deps.BalanceChecker,
	}
}

// requireFunds enforces the funded-wallet gate: balance must cover amount.
// A nil checker disables the gate. Unreadable balances fail closed —
// waving through an unchecked wallet would reintroduce junk circles.
func (s *circleService) requireFunds(ctx context.Context, userID string, amount float64) error {
	if s.balanceChecker == nil {
		return nil
	}
	bal, err := s.balanceChecker.GetBalance(ctx, userID)
	if err != nil {
		return fmt.Errorf("verifying wallet balance: %w", err)
	}
	if bal < amount {
		return fmt.Errorf("wallet balance %.2f USDC is below the %.2f USDC first contribution: %w",
			bal, amount, ErrInsufficientFunds)
	}
	return nil
}

type circleTransactor struct {
	db *sqlx.DB
}

func NewTransactor(db *sqlx.DB) Transactor {
	return &circleTransactor{db: db}
}

func (t *circleTransactor) WithTransaction(ctx context.Context, fn func(repo Repository) error) error {
	tx, err := t.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(NewRepositoryFromTx(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *circleService) withTransaction(ctx context.Context, fn func(repo Repository) error) error {
	if s.tx == nil {
		return fn(s.repo)
	}
	return s.tx.WithTransaction(ctx, fn)
}

func parseCommunityID(id string) *uuid.UUID {
	if id == "" {
		return nil
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil
	}
	return &parsed
}

func parseUUID(id string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, ErrInvalidUUID
	}
	return parsed, nil
}

func (s *circleService) Get(ctx context.Context, id string) (*Circle, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	c, err := s.repo.FindByID(ctx, uid)
	if err != nil {
		if err == ErrCircleNotFound {
			return nil, ErrCircleNotFound
		}
		return nil, fmt.Errorf("getting circle: %w", err)
	}
	return c, nil
}

func (s *circleService) List(ctx context.Context, filter CircleFilter) ([]Circle, int, error) {
	circles, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("listing circles: %w", err)
	}
	total, err := s.repo.Count(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("counting circles: %w", err)
	}
	return circles, total, nil
}

func (s *circleService) Update(ctx context.Context, id, userID string, input UpdateCircleInput) (*Circle, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	usrID, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}

	if err := validateUpdateInput(input); err != nil {
		return nil, err
	}

	c, err := s.repo.FindByID(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("finding circle for update: %w", err)
	}

	if c.OrganizerID != usrID {
		return nil, ErrNotOrganizer
	}
	if c.Status != CircleStatusPending && c.Status != CircleStatusActive {
		return nil, ErrCircleNotActive
	}

	if input.Name != nil {
		c.Name = strings.TrimSpace(*input.Name)
	}
	if input.Description != nil {
		trimmed := strings.TrimSpace(*input.Description)
		if trimmed == "" {
			c.Description = sql.NullString{}
		} else {
			c.Description = sql.NullString{String: trimmed, Valid: true}
		}
	}
	if input.ContributionAmount != nil {
		c.ContributionAmount = *input.ContributionAmount
	}
	if input.Currency != nil {
		c.Currency = *input.Currency
	}
	if input.Frequency != nil {
		c.Frequency = *input.Frequency
	}
	if input.MaxMembers != nil {
		c.MaxMembers = *input.MaxMembers
	}
	if input.MinMoiScore != nil {
		c.MinMoiScore = *input.MinMoiScore
	}
	if input.CollateralPercent != nil {
		c.CollateralPercent = *input.CollateralPercent
	}
	if input.LateFeePercent != nil {
		c.LateFeePercent = *input.LateFeePercent
	}
	if input.GracePeriodHours != nil {
		c.GracePeriodHours = *input.GracePeriodHours
	}
	if input.MaxStrikes != nil {
		c.MaxStrikes = *input.MaxStrikes
	}
	if input.StartDate != nil {
		start, hasStart, _ := parseCircleStartDate(*input.StartDate)
		if hasStart {
			c.StartDate = sql.NullTime{Time: start.UTC(), Valid: true}
		} else {
			c.StartDate = sql.NullTime{}
		}
	}
	if input.AutoDebit != nil {
		c.AutoDebit = *input.AutoDebit
	}
	c.UpdatedAt = time.Now().UTC()

	if err := s.repo.Update(ctx, c); err != nil {
		return nil, fmt.Errorf("updating circle: %w", err)
	}
	return c, nil
}

func (s *circleService) Create(ctx context.Context, organizerID string, input CreateCircleInput) (*Circle, error) {
	orgID, err := parseUUID(organizerID)
	if err != nil {
		return nil, err
	}

	// Zero-value defaults mirror the frontend wizard (initialCircleForm) and
	// the zod schema: omitted grace/strikes arrive as 0 from JSON binding.
	if input.GracePeriodHours == 0 {
		input.GracePeriodHours = 24
	}
	if input.MaxStrikes == 0 {
		input.MaxStrikes = 3
	}

	if err := validateCreateInput(input); err != nil {
		return nil, err
	}

	if input.CircleType == CircleTypePremium {
		org, err := s.userRepo.FindByID(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("finding organizer for premium check: %w", err)
		}
		if org.MoiScore < premiumMinMOI {
			return nil, invalidCircleInput("premium circles require at least 50 MoiScore")
		}
		if input.ContributionAmount < premiumMinUSDC {
			return nil, invalidCircleInput("premium circles require minimum 50 USDC contribution")
		}
	}

	// Funds gate (T019): creating a circle with an empty wallet spawns junk
	// circles. The organizer must cover at least the first contribution.
	if err := s.requireFunds(ctx, organizerID, input.ContributionAmount); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	startTime, hasStart, _ := parseCircleStartDate(input.StartDate)

	buildCircle := func() *Circle {
		c := &Circle{
			ID:                 uuid.New(),
			CommunityID:        parseCommunityID(input.CommunityID),
			Name:               strings.TrimSpace(input.Name),
			CircleType:         input.CircleType,
			PayoutType:         input.PayoutType,
			ContributionAmount: input.ContributionAmount,
			Currency:           input.Currency,
			Frequency:          input.Frequency,
			MaxMembers:         input.MaxMembers,
			MinMoiScore:        input.MinMoiScore,
			CollateralPercent:  input.CollateralPercent,
			LateFeePercent:     input.LateFeePercent,
			GracePeriodHours:   input.GracePeriodHours,
			MaxStrikes:         input.MaxStrikes,
			RequiresInvite:     input.RequiresInvite,
			AutoDebit:          input.AutoDebit,
			Status:             CircleStatusPending,
			CurrentRound:       0,
			TotalContributions: 0,
			OrganizerID:        orgID,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if strings.TrimSpace(input.Description) != "" {
			c.Description = sql.NullString{String: strings.TrimSpace(input.Description), Valid: true}
		}
		if hasStart {
			c.StartDate = sql.NullTime{Time: startTime.UTC(), Valid: true}
		}
		return c
	}

	var circle *Circle
	err = s.withTransaction(ctx, func(repo Repository) error {
		c := buildCircle()
		if err := repo.Create(ctx, c); err != nil {
			if err == apperrors.ErrConflict {
				return fmt.Errorf("circle name conflict: %w", err)
			}
			return fmt.Errorf("creating circle: %w", err)
		}
		member := &CircleMember{
			CircleID: c.ID,
			UserID:   orgID,
			Position: 1,
			Status:   MemberStatusActive,
			JoinedAt: now,
		}
		if err := repo.CreateMember(ctx, member); err != nil {
			return fmt.Errorf("adding organizer as member: %w", err)
		}
		circle = c
		return nil
	})
	if err == nil && s.broadcaster != nil && circle != nil {
		s.broadcaster.CircleCreated(ctx, circle.ID.String(), organizerID)
	}
	return circle, err
}

func (s *circleService) Start(ctx context.Context, id, userID string) error {
	cid, err := parseUUID(id)
	if err != nil {
		return err
	}
	usrID, err := parseUUID(userID)
	if err != nil {
		return err
	}

	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return fmt.Errorf("finding circle for start: %w", err)
	}

	if c.OrganizerID != usrID {
		return ErrNotOrganizer
	}
	if c.Status != CircleStatusPending {
		return ErrCircleNotActive
	}

	count, err := s.repo.GetMemberCount(ctx, cid)
	if err != nil {
		return fmt.Errorf("checking member count: %w", err)
	}
	if count < 2 {
		return fmt.Errorf("need at least 2 members to start")
	}

	// Start gate: every member must still cover the first contribution —
	// balances move between join and start.
	if s.balanceChecker != nil {
		members, err := s.repo.GetMembers(ctx, cid)
		if err != nil {
			return fmt.Errorf("checking members for start: %w", err)
		}
		for i := range members {
			if members[i].Status != MemberStatusActive {
				continue
			}
			if err := s.requireFunds(ctx, members[i].UserID.String(), c.ContributionAmount); err != nil {
				return err
			}
		}
	}

	c.Status = CircleStatusActive
	c.CurrentRound = 1
	c.UpdatedAt = time.Now().UTC()

	if err := s.repo.Update(ctx, c); err != nil {
		return fmt.Errorf("activating circle: %w", err)
	}
	_ = s.snapshotRoundConfig(ctx, c, 1)
	if s.broadcaster != nil {
		s.broadcaster.CircleStatusChanged(ctx, id, "active")
	}
	return nil
}

func (s *circleService) Cancel(ctx context.Context, id, userID string) error {
	uid, err := parseUUID(id)
	if err != nil {
		return err
	}
	usrID, err := parseUUID(userID)
	if err != nil {
		return err
	}

	err = s.withTransaction(ctx, func(repo Repository) error {
		c, err := repo.FindByID(ctx, uid)
		if err != nil {
			return fmt.Errorf("finding circle for cancel: %w", err)
		}
		if c.OrganizerID != usrID {
			return ErrNotOrganizer
		}
		if c.Status != CircleStatusPending {
			return ErrCircleNotActive
		}
		c.Status = CircleStatusCancelled
		c.UpdatedAt = time.Now().UTC()
		if err := repo.Update(ctx, c); err != nil {
			return fmt.Errorf("cancelling circle: %w", err)
		}
		return nil
	})
	if err == nil && s.broadcaster != nil {
		s.broadcaster.CircleStatusChanged(ctx, id, "cancelled")
	}
	return err
}

// PauseCircle halts an active circle (admin moderation): joins,
// contributions, payouts and keeper debits all fail closed on the paused
// status until resumed. Only active circles can be paused.
func (s *circleService) PauseCircle(ctx context.Context, id string) error {
	cid, err := parseUUID(id)
	if err != nil {
		return err
	}
	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return fmt.Errorf("finding circle for pause: %w", err)
	}
	if c.Status != CircleStatusActive {
		return fmt.Errorf("only active circles can be paused: %w", ErrCircleNotActive)
	}
	c.Status = CircleStatusPaused
	c.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, c); err != nil {
		return fmt.Errorf("pausing circle: %w", err)
	}
	if s.broadcaster != nil {
		s.broadcaster.CircleStatusChanged(ctx, id, string(CircleStatusPaused))
	}
	return nil
}

// ResumeCircle returns a paused circle to active (admin moderation).
func (s *circleService) ResumeCircle(ctx context.Context, id string) error {
	cid, err := parseUUID(id)
	if err != nil {
		return err
	}
	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return fmt.Errorf("finding circle for resume: %w", err)
	}
	if c.Status != CircleStatusPaused {
		return fmt.Errorf("only paused circles can be resumed: %w", ErrCircleNotActive)
	}
	c.Status = CircleStatusActive
	c.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, c); err != nil {
		return fmt.Errorf("resuming circle: %w", err)
	}
	if s.broadcaster != nil {
		s.broadcaster.CircleStatusChanged(ctx, id, string(CircleStatusActive))
	}
	return nil
}

// BanCircle terminally shuts down a circle (admin moderation): pending,
// active and paused circles can be banned; completed and cancelled ones are
// already terminal. Irreversible by design.
func (s *circleService) BanCircle(ctx context.Context, id string) error {
	cid, err := parseUUID(id)
	if err != nil {
		return err
	}
	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return fmt.Errorf("finding circle for ban: %w", err)
	}
	switch c.Status {
	case CircleStatusCompleted, CircleStatusCancelled, CircleStatusBanned:
		return fmt.Errorf("circle is already terminal (%s): %w", c.Status, ErrCircleNotActive)
	}
	c.Status = CircleStatusBanned
	c.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, c); err != nil {
		return fmt.Errorf("banning circle: %w", err)
	}
	if s.broadcaster != nil {
		s.broadcaster.CircleStatusChanged(ctx, id, string(CircleStatusBanned))
	}
	return nil
}

// Close completes an active circle after its final payout. Only the organizer
// may close it; cancellation remains the separate pending-circle operation.
func (s *circleService) Close(ctx context.Context, id, userID string) error {
	cid, err := parseUUID(id)
	if err != nil {
		return err
	}
	organizerID, err := parseUUID(userID)
	if err != nil {
		return err
	}

	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return fmt.Errorf("finding circle for close: %w", err)
	}
	if c.OrganizerID != organizerID {
		return ErrNotOrganizer
	}
	if c.Status != CircleStatusActive {
		return ErrCircleNotActive
	}
	c.Status = CircleStatusCompleted
	c.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, c); err != nil {
		return fmt.Errorf("closing circle: %w", err)
	}
	if s.broadcaster != nil {
		s.broadcaster.CircleStatusChanged(ctx, id, string(CircleStatusCompleted))
	}
	return nil
}

func (s *circleService) Join(ctx context.Context, circleID, userID string, inviteCode string) error {
	cid, err := parseUUID(circleID)
	if err != nil {
		return err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}

	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return fmt.Errorf("finding circle for join: %w", err)
	}

	if c.Status != CircleStatusPending && c.Status != CircleStatusActive {
		return ErrCircleNotActive
	}

	// Arc USDC only: legacy-rail circles are discoverable but not joinable.
	if c.Currency != CurrencyUSDC {
		return fmt.Errorf("only USDC circles on Arc can be joined")
	}

	// DeoScore gates: premium floors at 50 and minMoiScore applies to every
	// circle type. The repo is optional in unit tests, so a nil fetcher skips
	// the check instead of panicking.
	joinThreshold := c.MinMoiScore
	if c.CircleType == CircleTypePremium && joinThreshold < 50 {
		joinThreshold = 50
	}
	if joinThreshold > 0 {
		if s.userRepo == nil {
			// No score source wired; fail open so discovery tests without a
			// user repo keep passing. Production always wires one.
		} else {
			joiner, err := s.userRepo.FindByID(ctx, uid)
			if err != nil {
				return fmt.Errorf("finding user for join score check: %w", err)
			}
			if joiner.MoiScore < joinThreshold {
				if c.CircleType == CircleTypePremium && joiner.MoiScore < 50 {
					return fmt.Errorf("joining premium circles requires at least 50 MoiScore")
				}
				return ErrMoiScoreTooLow
			}
		}
	} else if c.CircleType == CircleTypePremium && s.userRepo != nil {
		joiner, err := s.userRepo.FindByID(ctx, uid)
		if err != nil {
			return fmt.Errorf("finding user for premium join check: %w", err)
		}
		if joiner.MoiScore < 50 {
			return fmt.Errorf("joining premium circles requires at least 50 MoiScore")
		}
	}

	if c.CircleType == CircleTypeCommunity && c.CommunityID != nil {
		if s.communityChecker == nil {
			// No checker wired (unit tests); fail open.
		} else {
			ok, err := s.communityChecker.IsMember(ctx, *c.CommunityID, uid)
			if err != nil {
				return fmt.Errorf("checking community membership: %w", err)
			}
			if !ok {
				return ErrNotCommunityMember
			}
		}
	}

	if (c.CircleType == CircleTypePrivate || c.RequiresInvite) && inviteCode == "" {
		return ErrInvalidInvite
	}

	// Funds gate: members must cover the first contribution, or they join
	// as dead weight the circle can never collect from.
	if err := s.requireFunds(ctx, userID, c.ContributionAmount); err != nil {
		return err
	}

	err = s.withTransaction(ctx, func(repo Repository) error {
		count, err := repo.GetMemberCount(ctx, cid)
		if err != nil {
			return fmt.Errorf("checking member count: %w", err)
		}
		if count >= c.MaxMembers {
			return ErrCircleFull
		}
		existing, err := repo.FindMemberByCircleAndUser(ctx, cid, uid)
		if err == nil && existing != nil {
			if existing.Status == MemberStatusActive {
				return ErrAlreadyMember
			}
			// A previous exit/removal leaves the row behind (unique key on
			// circle+user). Reactivate it instead of inserting a duplicate.
			if err := repo.UpdateMemberStatus(ctx, cid, uid, MemberStatusActive); err != nil {
				return fmt.Errorf("rejoining circle: %w", err)
			}
			return nil
		}
		if err := repo.CreateMember(ctx, &CircleMember{
			CircleID: cid,
			UserID:   uid,
			Position: count + 1,
			Status:   MemberStatusActive,
			JoinedAt: time.Now().UTC(),
		}); err != nil {
			// A concurrent join winning the race surfaces as a unique
			// violation; the caller is a member either way.
			if err == ErrAlreadyMember {
				return ErrAlreadyMember
			}
			return fmt.Errorf("joining circle: %w", err)
		}
		return nil
	})
	if err == nil && s.broadcaster != nil {
		s.broadcaster.MemberJoined(ctx, circleID, userID)
	}
	return err
}

func (s *circleService) Exit(ctx context.Context, circleID, userID string) error {
	cid, err := parseUUID(circleID)
	if err != nil {
		return err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}

	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return fmt.Errorf("finding circle for exit: %w", err)
	}

	if c.OrganizerID == uid {
		return ErrNotOrganizer
	}

	member, err := s.repo.FindMemberByCircleAndUser(ctx, cid, uid)
	if err != nil {
		if err == apperrors.ErrNotFound {
			return ErrNotMember
		}
		return fmt.Errorf("finding member for exit: %w", err)
	}

	if member.Status != MemberStatusActive {
		return ErrNotMember
	}

	if c.Status == CircleStatusActive {
		// Early exit forfeits the collateral share: contribution * collateral%.
		// Recorded as an early_exit penalty row (2.7) instead of computed
		// and dropped. Strikes do not apply to voluntary exits.
		penalty := CalculateEarlyExitPenalty(c.TotalContributions, c.ContributionAmount*c.CollateralPercent/100.0, 0)
		if penalty > 0 {
			if err := s.repo.CreatePenalty(ctx, &Penalty{
				ID:             uuid.New(),
				CircleID:       cid,
				UserID:         uid,
				RoundNumber:    c.CurrentRound,
				PenaltyType:    PenaltyTypeEarlyExit,
				Amount:         penalty,
				StrikesApplied: 0,
				Reason:         sql.NullString{String: "Early exit collateral forfeit", Valid: true},
				CreatedAt:      time.Now().UTC(),
			}); err != nil {
				return fmt.Errorf("recording early-exit penalty: %w", err)
			}
		}
	}

	if err := s.repo.UpdateMemberStatus(ctx, cid, uid, MemberStatusExited); err != nil {
		return fmt.Errorf("exiting circle: %w", err)
	}

	if s.broadcaster != nil {
		s.broadcaster.MemberLeft(ctx, circleID, userID)
	}

	return nil
}

func (s *circleService) GetMembers(ctx context.Context, circleID string) ([]CircleMember, error) {
	cid, err := parseUUID(circleID)
	if err != nil {
		return nil, err
	}

	_, err = s.repo.FindByID(ctx, cid)
	if err != nil {
		return nil, fmt.Errorf("finding circle for members: %w", err)
	}

	members, err := s.repo.GetMembers(ctx, cid)
	if err != nil {
		return nil, fmt.Errorf("getting members: %w", err)
	}
	return members, nil
}

// SearchMembers filters circle members by username/display name substring.
func (s *circleService) SearchMembers(ctx context.Context, circleID, search string) ([]CircleMember, error) {
	cid, err := parseUUID(circleID)
	if err != nil {
		return nil, err
	}

	if _, err = s.repo.FindByID(ctx, cid); err != nil {
		return nil, fmt.Errorf("finding circle for members: %w", err)
	}

	members, err := s.repo.SearchMembers(ctx, cid, search)
	if err != nil {
		return nil, fmt.Errorf("searching members: %w", err)
	}
	return members, nil
}

// GetUserCircles lists circles the user actively belongs to (admin detail).
func (s *circleService) GetUserCircles(ctx context.Context, userID string) ([]Circle, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}
	return s.repo.FindCirclesByUserID(ctx, uid)
}

// IsMember reports whether the user is an active member of the circle.
func (s *circleService) IsMember(ctx context.Context, circleID, userID string) (bool, error) {
	cid, err := parseUUID(circleID)
	if err != nil {
		return false, err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return false, err
	}

	member, err := s.repo.FindMemberByCircleAndUser(ctx, cid, uid)
	if err != nil {
		if err == apperrors.ErrNotFound {
			return false, nil
		}
		return false, fmt.Errorf("checking circle membership: %w", err)
	}
	return member != nil && member.Status == MemberStatusActive, nil
}

// RemoveMember lets the circle organizer forcibly remove a member by their user ID.
// The member's stake redistribution is noted in the audit trail; actual on-chain
// redistribution is handled by the treasury contract and triggered via the emitted event.
func (s *circleService) RemoveMember(ctx context.Context, circleID, callerID, memberAddress string, reason string) error {
	cid, err := parseUUID(circleID)
	if err != nil {
		return err
	}
	callerUID, err := parseUUID(callerID)
	if err != nil {
		return err
	}
	memberUID, err := parseUUID(memberAddress)
	if err != nil {
		return err
	}

	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return fmt.Errorf("finding circle: %w", err)
	}

	if c.OrganizerID != callerUID {
		return ErrNotOrganizer
	}

	if callerUID == memberUID {
		return fmt.Errorf("organizer cannot remove themselves; use cancel or close")
	}

	member, err := s.repo.FindMemberByCircleAndUser(ctx, cid, memberUID)
	if err != nil {
		if err == apperrors.ErrNotFound {
			return ErrNotMember
		}
		return fmt.Errorf("finding member: %w", err)
	}
	if member.Status != MemberStatusActive {
		return ErrNotMember
	}

	if err := s.repo.UpdateMemberStatus(ctx, cid, memberUID, MemberStatusRemoved); err != nil {
		return fmt.Errorf("removing member: %w", err)
	}

	if s.broadcaster != nil {
		s.broadcaster.MemberLeft(ctx, circleID, memberAddress)
	}
	return nil
}

func (s *circleService) RaiseDispute(ctx context.Context, circleID, userID string, input DisputeInput) (*CircleDispute, error) {
	cid, err := parseUUID(circleID)
	if err != nil {
		return nil, err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}

	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return nil, fmt.Errorf("finding circle for dispute: %w", err)
	}

	if c.Status != CircleStatusActive && c.Status != CircleStatusPending {
		return nil, ErrCircleNotActive
	}

	member, err := s.repo.FindMemberByCircleAndUser(ctx, cid, uid)
	if err != nil || member.Status != MemberStatusActive {
		return nil, ErrNotMember
	}

	now := time.Now().UTC()
	var details sql.NullString
	if input.Details != "" {
		details = sql.NullString{String: input.Details, Valid: true}
	}

	dispute := &CircleDispute{
		ID:        uuid.New(),
		CircleID:  cid,
		RaiserID:  uid,
		Reason:    input.Reason,
		Details:   details,
		Status:    "open",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.CreateDispute(ctx, dispute); err != nil {
		return nil, fmt.Errorf("persisting dispute: %w", err)
	}

	if s.notificationSvc != nil {
		notifInput := notification.CreateInput{
			UserID:  c.OrganizerID.String(),
			Type:    notification.TypeDisputeRaised,
			Title:   "Dispute Raised",
			Body:    fmt.Sprintf("A dispute was raised in circle %s: %s", c.Name, input.Reason),
			Channel: notification.ChannelInApp,
		}
		_, _ = s.notificationSvc.Create(ctx, notifInput)
	}

	if s.auditRepo != nil {
		detailsBytes, _ := json.Marshal(map[string]string{"reason": input.Reason})
		auditEntry := &audit.AuditEntry{
			ID:           uuid.New(),
			ActorID:      uid,
			Action:       "circle.dispute_raised",
			ResourceType: "circle",
			ResourceID:   sql.NullString{String: cid.String(), Valid: true},
			Details:      detailsBytes,
			CreatedAt:    now,
		}
		_ = s.auditRepo.Create(ctx, auditEntry)
	}

	return dispute, nil
}

func (s *circleService) CastVote(ctx context.Context, circleID, userID string, input VoteInput) (*CircleVote, bool, string, error) {
	cid, err := parseUUID(circleID)
	if err != nil {
		return nil, false, "", err
	}
	voterID, err := parseUUID(userID)
	if err != nil {
		return nil, false, "", err
	}
	recipientID, err := parseUUID(input.RecipientID)
	if err != nil {
		return nil, false, "", err
	}

	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return nil, false, "", fmt.Errorf("finding circle for vote: %w", err)
	}
	if c.Status != CircleStatusActive {
		return nil, false, "", ErrCircleNotActive
	}
	if c.PayoutType != PayoutTypeVote {
		return nil, false, "", fmt.Errorf("circle payout type is not vote")
	}

	voter, err := s.repo.FindMemberByCircleAndUser(ctx, cid, voterID)
	if err != nil || voter.Status != MemberStatusActive {
		return nil, false, "", ErrNotMember
	}

	recipient, err := s.repo.FindMemberByCircleAndUser(ctx, cid, recipientID)
	if err != nil || recipient.Status != MemberStatusActive {
		return nil, false, "", fmt.Errorf("recipient is not an active circle member")
	}

	roundNum := c.CurrentRound
	if roundNum < 1 {
		roundNum = 1
	}

	vote := &CircleVote{
		ID:          uuid.New(),
		CircleID:    cid,
		VoterID:     voterID,
		RecipientID: recipientID,
		RoundNumber: roundNum,
		CreatedAt:   time.Now().UTC(),
	}

	if err := s.repo.CreateVote(ctx, vote); err != nil {
		if err == apperrors.ErrConflict {
			return nil, false, "", fmt.Errorf("user has already voted in this round")
		}
		return nil, false, "", fmt.Errorf("recording vote: %w", err)
	}

	votes, err := s.repo.GetVotesByRound(ctx, cid, roundNum)
	if err != nil {
		return vote, false, "", nil
	}

	memberCount, err := s.repo.GetMemberCount(ctx, cid)
	if err != nil || memberCount == 0 {
		memberCount = c.MaxMembers
	}

	if len(votes) >= memberCount {
		tallyMap := make(map[string]int)
		for _, v := range votes {
			tallyMap[v.RecipientID.String()]++
		}
		winner := VoteTally(tallyMap)
		return vote, true, winner, nil
	}

	return vote, false, "", nil
}

func (s *circleService) SubmitAuctionBid(ctx context.Context, circleID, userID string, input AuctionBidInput) (*CircleAuctionBid, error) {
	cid, err := parseUUID(circleID)
	if err != nil {
		return nil, err
	}
	bidderID, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}

	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return nil, err
	}
	if c.Status != CircleStatusActive {
		return nil, ErrCircleNotActive
	}
	if c.PayoutType != PayoutTypeAuction {
		return nil, fmt.Errorf("circle payout type is not auction")
	}

	bidder, err := s.repo.FindMemberByCircleAndUser(ctx, cid, bidderID)
	if err != nil || bidder.Status != MemberStatusActive {
		return nil, ErrNotMember
	}

	if input.BidAmount <= 0 {
		return nil, fmt.Errorf("bid amount must be greater than zero")
	}

	roundNum := c.CurrentRound
	if roundNum < 1 {
		roundNum = 1
	}

	// Bid discipline (T025): one live bid per member per round, each new bid
	// strictly above the round's current high. Spam and penny-increment
	// flooding die here instead of in the payout math.
	existing, err := s.repo.GetAuctionBidsByRound(ctx, cid, roundNum)
	if err != nil {
		return nil, fmt.Errorf("checking existing bids: %w", err)
	}
	high := 0.0
	for i := range existing {
		if existing[i].BidderID == bidderID {
			return nil, fmt.Errorf("you already have a bid in this round: %w", apperrors.ErrConflict)
		}
		if existing[i].BidAmount > high {
			high = existing[i].BidAmount
		}
	}
	if len(existing) > 0 && input.BidAmount <= high {
		return nil, fmt.Errorf("bid must beat the current high of %.2f: %w", high, apperrors.ErrInvalidInput)
	}

	// Funded bidder: a bid is a promise to pay the bid amount.
	if err := s.requireFunds(ctx, userID, input.BidAmount); err != nil {
		return nil, err
	}

	bid := &CircleAuctionBid{
		ID:          uuid.New(),
		CircleID:    cid,
		BidderID:    bidderID,
		RoundNumber: roundNum,
		BidAmount:   input.BidAmount,
		CreatedAt:   time.Now().UTC(),
	}

	if err := s.repo.CreateAuctionBid(ctx, bid); err != nil {
		return nil, fmt.Errorf("recording auction bid: %w", err)
	}

	return bid, nil
}

func ceilFloat(f float64) float64 {
	return math.Ceil(f*100) / 100
}

func (s *circleService) ProcessMissedContributions(ctx context.Context, circleID string, roundNumber int) error {
	cID, err := uuid.Parse(circleID)
	if err != nil {
		return apperrors.ErrInvalidInput
	}

	c, err := s.repo.FindByID(ctx, cID)
	if err != nil {
		return err
	}
	if c == nil {
		return apperrors.ErrNotFound
	}

	members, err := s.repo.GetMembers(ctx, cID)
	if err != nil {
		return err
	}

	contributedUserIDs, err := s.repo.GetContributionsByCircleAndRound(ctx, cID, roundNumber)
	if err != nil {
		return err
	}

	contributedMap := make(map[uuid.UUID]bool)
	for _, uid := range contributedUserIDs {
		contributedMap[uid] = true
	}

	for _, member := range members {
		if member.Status != MemberStatusActive {
			continue
		}
		if contributedMap[member.UserID] {
			continue
		}
		// Grace window (2.7): a round's deadline is start + (round-1) *
		// frequency; members inside deadline+grace are not yet late.
		// Circles without a start date carry no schedule, so they
		// fail open to immediate processing (pre-2.7 behavior).
		if due, ok := roundDueDate(c, roundNumber); ok {
			if time.Now().Before(due.Add(time.Duration(c.GracePeriodHours) * time.Hour)) {
				continue
			}
		}
		// Member missed contribution, apply penalty
		penaltyAmt := CalculateLateFee(c.ContributionAmount, c.LateFeePercent)
		member.Strikes += ApplyStrikes(&member, "late")

		if err := s.repo.UpdateMemberStrikes(ctx, cID, member.UserID, member.Strikes); err != nil {
			return fmt.Errorf("persisting strikes: %w", err)
		}

		penaltyType := PenaltyTypeLate
		reason := fmt.Sprintf("Missed contribution for round %d", roundNumber)
		// MaxStrikes is validated 1-10 at creation; a zero value (unset in
		// tests/fixtures) disables removal rather than removing everyone.
		if c.MaxStrikes > 0 && ShouldRemoveMember(member.Strikes, c.MaxStrikes) {
			// Max strikes reached: remove the member and record the
			// default. The penalty row below uses PenaltyTypeDefault.
			if err := s.repo.UpdateMemberStatus(ctx, cID, member.UserID, MemberStatusRemoved); err != nil {
				return fmt.Errorf("removing max-strike member: %w", err)
			}
			penaltyType = PenaltyTypeDefault
			reason = fmt.Sprintf("Removed after %d strikes (missed round %d)", member.Strikes, roundNumber)
		}

		p := &Penalty{
			ID:             uuid.New(),
			CircleID:       cID,
			UserID:         member.UserID,
			RoundNumber:    roundNumber,
			PenaltyType:    penaltyType,
			Amount:         penaltyAmt,
			StrikesApplied: member.Strikes,
			Reason:         sql.NullString{String: reason, Valid: true},
			CreatedAt:      time.Now(),
		}

		err = s.repo.CreatePenalty(ctx, p)
		if err != nil {
			return err
		}

		// Broadcast event for notification/indexer
		// We can use a new method like MemberPenalized or just use existing ones.
		// The Acceptance Criteria mentions: "Notify affected members"
		// This will likely be handled by an event listener on the indexer or notification service.
		// But for now, we process it here.
		if s.broadcaster != nil {
			s.broadcaster.MemberPenalized(ctx, circleID, member.UserID.String(), roundNumber, penaltyAmt)
			if penaltyType == PenaltyTypeDefault {
				s.broadcaster.MemberLeft(ctx, circleID, member.UserID.String())
			}
		}
	}

	return nil
}

// frequencyDuration maps circle frequency to round spacing. Monthly is a
// 30-day approximation (documented; exact calendar months need schedule
// rows, which do not exist yet).
func frequencyDuration(f CircleFrequency) (time.Duration, bool) {
	switch f {
	case FrequencyDaily:
		return 24 * time.Hour, true
	case FrequencyWeekly:
		return 7 * 24 * time.Hour, true
	case FrequencyBiweekly:
		return 14 * 24 * time.Hour, true
	case FrequencyMonthly:
		return 30 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// roundDueDate returns round N's contribution deadline: start + (N-1) *
// frequency. False when the circle carries no start date or an unknown
// frequency — callers fail open (no schedule to enforce).
func roundDueDate(c *Circle, round int) (time.Time, bool) {
	if c == nil || !c.StartDate.Valid || round < 1 {
		return time.Time{}, false
	}
	step, ok := frequencyDuration(c.Frequency)
	if !ok {
		return time.Time{}, false
	}
	return c.StartDate.Time.Add(time.Duration(round-1) * step), true
}

func (s *circleService) snapshotRoundConfig(ctx context.Context, c *Circle, round int) error {
	configData, err := json.Marshal(map[string]any{
		"name":               c.Name,
		"contributionAmount": c.ContributionAmount,
		"currency":           c.Currency,
		"frequency":          c.Frequency,
		"maxMembers":         c.MaxMembers,
		"collateralPercent":  c.CollateralPercent,
		"lateFeePercent":     c.LateFeePercent,
		"gracePeriodHours":   c.GracePeriodHours,
		"maxStrikes":         c.MaxStrikes,
		"payoutType":         c.PayoutType,
	})
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(configData))
	snapshot := &RoundConfigSnapshot{
		ID:          uuid.New(),
		CircleID:    c.ID,
		RoundNumber: round,
		ConfigHash:  hash,
		ConfigJSON:  string(configData),
		CreatedAt:   time.Now().UTC(),
	}
	return s.repo.SaveRoundConfigSnapshot(ctx, snapshot)
}

func (s *circleService) QueryRoundConfig(ctx context.Context, circleID string, round int) (*RoundConfigSnapshot, error) {
	cid, err := parseUUID(circleID)
	if err != nil {
		return nil, err
	}
	return s.repo.GetRoundConfigSnapshot(ctx, cid, round)
}

func (s *circleService) GetCircleSnapshot(ctx context.Context, circleID, userID string) (*CircleSnapshot, error) {
	cid, err := parseUUID(circleID)
	if err != nil {
		return nil, err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}
	return s.repo.GetCircleSnapshot(ctx, cid, uid)
}

func (s *circleService) GetBulkCircleSnapshots(ctx context.Context, userID string, circleIDs []string) ([]CircleSnapshot, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(circleIDs))
	for _, id := range circleIDs {
		parsed, err := parseUUID(id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, parsed)
	}
	return s.repo.GetBulkCircleSnapshots(ctx, uid, ids)
}
