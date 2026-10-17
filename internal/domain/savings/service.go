package savings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/deodes/backend/pkg/apperrors"
)

// Savings validation bounds. These mirror the frontend GoalForm rules
// (SAVINGS_LIMITS + validateGoalFormFields) so bad input fails fast with a
// 400 VALIDATION_ERROR instead of slipping through or surfacing as a 500.
const (
	maxGoalNameLen = 100
	minGoalNameLen = 3
	maxGoalTarget  = 1_000_000
	maxGoalNoteLen = 280
)

type Service interface {
	CreateGoal(ctx context.Context, userID string, req CreateGoalRequest) (*SavingsGoal, error)
	GetGoal(ctx context.Context, userID, goalID string) (*SavingsGoal, error)
	ListGoals(ctx context.Context, userID string) ([]SavingsGoal, error)
	ListActiveGoals(ctx context.Context, userID string) ([]SavingsGoal, error)
	UpdateGoal(ctx context.Context, userID, goalID string, req UpdateGoalRequest) (*SavingsGoal, error)
	DeleteGoal(ctx context.Context, userID, goalID string) error
	CompleteGoal(ctx context.Context, userID, goalID string) (*SavingsGoal, error)
	GetSummary(ctx context.Context, userID string) (*GoalSummary, error)
	GetUpcomingObligations(ctx context.Context, userID string) ([]SavingsGoal, error)
}

type service struct {
	repo Repository
}

func NewService(repo Repository) Service {
	return &service{repo: repo}
}

func invalidInput(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, apperrors.ErrInvalidInput)...)
}

func notFound() error {
	return fmt.Errorf("savings goal not found: %w", apperrors.ErrNotFound)
}

// mapLookupError converts a repository lookup failure into a service error.
// sql.ErrNoRows (unknown id) and ownership mismatches both surface as
// ErrNotFound so callers cannot probe for other users' goal ids; anything
// else is an internal failure the handler must report as 500.
func mapLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return notFound()
	}
	return fmt.Errorf("finding goal: %w", err)
}

// ValidAutoFrequencies mirrors the goal-form schedule options.
var ValidAutoFrequencies = map[string]bool{
	"daily": true, "weekly": true, "biweekly": true, "monthly": true,
}

func normalizeAutoFrequency(freq string) string {
	freq = strings.ToLower(strings.TrimSpace(freq))
	if !ValidAutoFrequencies[freq] {
		return "monthly"
	}
	return freq
}

func validateAutoSchedule(amount float64, freq string) error {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return invalidInput("auto amount must not be negative")
	}
	if strings.TrimSpace(freq) != "" && !ValidAutoFrequencies[strings.ToLower(strings.TrimSpace(freq))] {
		return invalidInput("auto frequency must be one of daily, weekly, biweekly, monthly")
	}
	return nil
}

func validateGoalName(name string) error {	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return invalidInput("goal name is required")
	}
	if len([]rune(trimmed)) < minGoalNameLen {
		return invalidInput("goal name must be at least 3 characters")
	}
	if len([]rune(trimmed)) > maxGoalNameLen {
		return invalidInput("goal name must be at most 100 characters")
	}
	return nil
}

func validateGoalTarget(amount float64) error {
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return invalidInput("target amount must be greater than zero")
	}
	if amount <= 0 {
		return invalidInput("target amount must be greater than zero")
	}
	if amount > maxGoalTarget {
		return invalidInput("target amount must not exceed 1000000 USDC")
	}
	return nil
}

func validateGoalDescription(desc string) error {
	if len([]rune(desc)) > maxGoalNoteLen {
		return invalidInput("goal note must be at most 280 characters")
	}
	return nil
}

// validateGoalDeadline enforces YYYY-MM-DD shape plus the product rule that a
// deadline must lie in the future. A nil/empty value means "no deadline".
func validateGoalDeadline(targetDate *string) error {
	t, hasDate, err := ParseTargetDate(targetDate)
	if err != nil {
		return invalidInput("target date must be a valid YYYY-MM-DD date")
	}
	if !hasDate {
		return nil
	}
	// Compare calendar days in UTC so "today" is rejected deterministically
	// in every timezone; only a strictly later YYYY-MM-DD is accepted.
	nowUTC := time.Now().UTC()
	startOfTodayUTC := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
	if !t.After(startOfTodayUTC) {
		return invalidInput("target date must be a future date")
	}
	return nil
}

func (s *service) CreateGoal(ctx context.Context, userID string, req CreateGoalRequest) (*SavingsGoal, error) {
	if err := validateGoalName(req.Name); err != nil {
		return nil, err
	}
	if err := validateGoalTarget(req.TargetAmount); err != nil {
		return nil, err
	}
	if err := validateGoalDescription(req.Description); err != nil {
		return nil, err
	}
	if err := validateGoalDeadline(req.TargetDate); err != nil {
		return nil, err
	}
	if err := validateAutoSchedule(req.AutoAmount, req.AutoFrequency); err != nil {
		return nil, err
	}
	g := &SavingsGoal{
		UserID:        userID,
		Name:          strings.TrimSpace(req.Name),
		Description:   strings.TrimSpace(req.Description),
		TargetAmount:  req.TargetAmount,
		CurrentAmount: 0,
		TargetDate:    req.TargetDate,
		CircleID:      req.CircleID,
		AutoReserve:   req.AutoReserve,
		AutoAmount:    req.AutoAmount,
		AutoFrequency: normalizeAutoFrequency(req.AutoFrequency),
		Status:        "active",
	}
	if err := s.repo.Create(ctx, g); err != nil {
		return nil, fmt.Errorf("creating goal: %w", err)
	}
	return g, nil
}

func (s *service) GetGoal(ctx context.Context, userID, goalID string) (*SavingsGoal, error) {
	g, err := s.repo.FindByID(ctx, goalID)
	if err != nil {
		return nil, mapLookupError(err)
	}
	if g.UserID != userID {
		return nil, notFound()
	}
	return g, nil
}

func (s *service) ListGoals(ctx context.Context, userID string) ([]SavingsGoal, error) {
	return s.repo.FindByUserID(ctx, userID)
}

func (s *service) ListActiveGoals(ctx context.Context, userID string) ([]SavingsGoal, error) {
	return s.repo.FindActiveByUserID(ctx, userID)
}

func (s *service) UpdateGoal(ctx context.Context, userID, goalID string, req UpdateGoalRequest) (*SavingsGoal, error) {
	g, err := s.repo.FindByID(ctx, goalID)
	if err != nil {
		return nil, mapLookupError(err)
	}
	if g.UserID != userID {
		return nil, notFound()
	}

	if req.Name != nil {
		if err := validateGoalName(*req.Name); err != nil {
			return nil, err
		}
		g.Name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		trimmed := strings.TrimSpace(*req.Description)
		if err := validateGoalDescription(trimmed); err != nil {
			return nil, err
		}
		g.Description = trimmed
	}
	if req.TargetAmount != nil {
		if err := validateGoalTarget(*req.TargetAmount); err != nil {
			return nil, err
		}
		g.TargetAmount = *req.TargetAmount
	}
	if req.CurrentAmount != nil {
		if math.IsNaN(*req.CurrentAmount) || math.IsInf(*req.CurrentAmount, 0) {
			return nil, invalidInput("current amount must not be negative")
		}
		if *req.CurrentAmount < 0 {
			return nil, invalidInput("current amount must not be negative")
		}
		g.CurrentAmount = *req.CurrentAmount
	}
	if req.TargetDate != nil {
		if err := validateGoalDeadline(req.TargetDate); err != nil {
			return nil, err
		}
		g.TargetDate = req.TargetDate
	}
	if req.CircleID != nil {
		g.CircleID = req.CircleID
	}
	if req.AutoReserve != nil {
		g.AutoReserve = *req.AutoReserve
	}
	if req.AutoAmount != nil || req.AutoFrequency != nil {
		amount := g.AutoAmount
		freq := g.AutoFrequency
		if req.AutoAmount != nil {
			amount = *req.AutoAmount
		}
		if req.AutoFrequency != nil {
			freq = *req.AutoFrequency
		}
		if err := validateAutoSchedule(amount, freq); err != nil {
			return nil, err
		}
		g.AutoAmount = amount
		g.AutoFrequency = normalizeAutoFrequency(freq)
	}
	if req.Status != nil {
		if !IsValidStatus(*req.Status) {
			return nil, invalidInput("status must be one of active, completed, cancelled")
		}
		g.Status = *req.Status
	}

	if err := s.repo.Update(ctx, g); err != nil {
		return nil, fmt.Errorf("updating goal: %w", err)
	}
	return g, nil
}

func (s *service) DeleteGoal(ctx context.Context, userID, goalID string) error {
	g, err := s.repo.FindByID(ctx, goalID)
	if err != nil {
		return mapLookupError(err)
	}
	if g.UserID != userID {
		return notFound()
	}
	return s.repo.Delete(ctx, goalID)
}

func (s *service) CompleteGoal(ctx context.Context, userID, goalID string) (*SavingsGoal, error) {
	g, err := s.repo.FindByID(ctx, goalID)
	if err != nil {
		return nil, mapLookupError(err)
	}
	if g.UserID != userID {
		return nil, notFound()
	}
	if g.Status == "completed" {
		return g, nil
	}
	g.Status = "completed"
	g.CurrentAmount = g.TargetAmount
	if err := s.repo.Update(ctx, g); err != nil {
		return nil, fmt.Errorf("completing goal: %w", err)
	}
	return g, nil
}

func (s *service) GetSummary(ctx context.Context, userID string) (*GoalSummary, error) {
	return s.repo.GetSummary(ctx, userID)
}

func (s *service) GetUpcomingObligations(ctx context.Context, userID string) ([]SavingsGoal, error) {
	return s.repo.GetUpcomingObligations(ctx, userID, 5)
}
