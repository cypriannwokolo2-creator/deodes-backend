package savings

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

type Repository interface {
	Create(ctx context.Context, g *SavingsGoal) error
	FindByID(ctx context.Context, id string) (*SavingsGoal, error)
	FindByUserID(ctx context.Context, userID string) ([]SavingsGoal, error)
	FindActiveByUserID(ctx context.Context, userID string) ([]SavingsGoal, error)
	FindCompletedByUserID(ctx context.Context, userID string) ([]SavingsGoal, error)
	Update(ctx context.Context, g *SavingsGoal) error
	Delete(ctx context.Context, id string) error
	GetSummary(ctx context.Context, userID string) (*GoalSummary, error)
	GetUpcomingObligations(ctx context.Context, userID string, limit int) ([]SavingsGoal, error)
	DueAutoGoals(ctx context.Context, now time.Time) ([]SavingsGoal, error)
	RecordTopUp(ctx context.Context, goalID string, amount float64) (bool, error)
}

type pgRepo struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) Repository {
	return &pgRepo{db: db}
}

func (r *pgRepo) Create(ctx context.Context, g *SavingsGoal) error {
	query := `
		INSERT INTO savings_goals (user_id, name, description, target_amount, current_amount, target_date, circle_id, auto_reserve, auto_amount, auto_frequency, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at`
	return r.db.QueryRowContext(ctx, query,
		g.UserID, g.Name, g.Description, g.TargetAmount, g.CurrentAmount,
		g.TargetDate, g.CircleID, g.AutoReserve, g.AutoAmount, g.AutoFrequency, g.Status,
	).Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt)
}

func (r *pgRepo) FindByID(ctx context.Context, id string) (*SavingsGoal, error) {
	var g SavingsGoal
	err := r.db.GetContext(ctx, &g, `
		SELECT id, user_id, name, description, target_amount, current_amount, target_date, circle_id, auto_reserve, auto_amount, auto_frequency, last_auto_at, status, created_at, updated_at
		FROM savings_goals WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("finding savings goal: %w", err)
	}
	return &g, nil
}

func (r *pgRepo) FindByUserID(ctx context.Context, userID string) ([]SavingsGoal, error) {
	var goals []SavingsGoal
	err := r.db.SelectContext(ctx, &goals, `
		SELECT id, user_id, name, description, target_amount, current_amount, target_date, circle_id, auto_reserve, auto_amount, auto_frequency, last_auto_at, status, created_at, updated_at
		FROM savings_goals WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("finding savings goals: %w", err)
	}
	return goals, nil
}

func (r *pgRepo) FindActiveByUserID(ctx context.Context, userID string) ([]SavingsGoal, error) {
	var goals []SavingsGoal
	err := r.db.SelectContext(ctx, &goals, `
		SELECT id, user_id, name, description, target_amount, current_amount, target_date, circle_id, auto_reserve, auto_amount, auto_frequency, last_auto_at, status, created_at, updated_at
		FROM savings_goals WHERE user_id = $1 AND status = 'active' ORDER BY target_date ASC NULLS LAST, created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("finding active savings goals: %w", err)
	}
	return goals, nil
}

func (r *pgRepo) FindCompletedByUserID(ctx context.Context, userID string) ([]SavingsGoal, error) {
	var goals []SavingsGoal
	err := r.db.SelectContext(ctx, &goals, `
		SELECT id, user_id, name, description, target_amount, current_amount, target_date, circle_id, auto_reserve, auto_amount, auto_frequency, last_auto_at, status, created_at, updated_at
		FROM savings_goals WHERE user_id = $1 AND status = 'completed' ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("finding completed savings goals: %w", err)
	}
	return goals, nil
}

func (r *pgRepo) Update(ctx context.Context, g *SavingsGoal) error {
	g.UpdatedAt = time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE savings_goals SET name=$1, description=$2, target_amount=$3, current_amount=$4, target_date=$5, circle_id=$6, auto_reserve=$7, auto_amount=$8, auto_frequency=$9, status=$10, updated_at=$11
		WHERE id=$10`,
		g.Name, g.Description, g.TargetAmount, g.CurrentAmount, g.TargetDate, g.CircleID, g.AutoReserve, g.AutoAmount, g.AutoFrequency, g.Status, g.UpdatedAt, g.ID)
	return err
}

func (r *pgRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM savings_goals WHERE id = $1`, id)
	return err
}

func (r *pgRepo) GetSummary(ctx context.Context, userID string) (*GoalSummary, error) {
	var s GoalSummary
	err := r.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*)::int AS total_goals,
			COUNT(*) FILTER (WHERE status = 'active')::int AS active_goals,
			COUNT(*) FILTER (WHERE status = 'completed')::int AS completed_goals,
			COALESCE(SUM(target_amount), 0) AS total_target,
			COALESCE(SUM(current_amount), 0) AS total_saved
		FROM savings_goals WHERE user_id = $1`, userID).Scan(
		&s.TotalGoals, &s.ActiveGoals, &s.CompletedGoals, &s.TotalTarget, &s.TotalSaved)
	if err != nil {
		return nil, fmt.Errorf("getting savings summary: %w", err)
	}
	if s.TotalTarget > 0 {
		s.OverallPercent = (s.TotalSaved / s.TotalTarget) * 100
	}
	if s.OverallPercent > 100 {
		s.OverallPercent = 100
	}
	// Count consecutive months with activity as streak
	err = r.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT date_trunc('month', created_at))::int
		FROM savings_goals
		WHERE user_id = $1 AND status = 'completed'
		AND created_at >= NOW() - INTERVAL '12 months'`, userID).Scan(&s.SavingsStreak)
	if err != nil {
		s.SavingsStreak = 0
	}
	return &s, nil
}

func (r *pgRepo) GetUpcomingObligations(ctx context.Context, userID string, limit int) ([]SavingsGoal, error) {
	var goals []SavingsGoal
	err := r.db.SelectContext(ctx, &goals, `
		SELECT sg.id, sg.user_id, sg.name, sg.description, sg.target_amount, sg.current_amount, sg.target_date, sg.circle_id, sg.auto_reserve, sg.auto_amount, sg.auto_frequency, sg.last_auto_at, sg.status, sg.created_at, sg.updated_at
		FROM savings_goals sg
		LEFT JOIN circles c ON sg.circle_id = c.id
		WHERE sg.user_id = $1 AND sg.status = 'active'
		AND sg.auto_reserve = true
		ORDER BY sg.target_date ASC NULLS LAST
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("finding upcoming obligations: %w", err)
	}
	return goals, nil
}

// DueAutoGoals returns active auto-reserve goals whose schedule has elapsed,
// further gated on the user's global savings_auto_reserve master switch.
func (r *pgRepo) DueAutoGoals(ctx context.Context, now time.Time) ([]SavingsGoal, error) {
	var goals []SavingsGoal
	err := r.db.SelectContext(ctx, &goals, `
		SELECT sg.id, sg.user_id, sg.name, sg.description, sg.target_amount, sg.current_amount, sg.target_date, sg.circle_id, sg.auto_reserve, sg.auto_amount, sg.auto_frequency, sg.last_auto_at, sg.status, sg.created_at, sg.updated_at
		FROM savings_goals sg
		JOIN users u ON u.id = sg.user_id
		WHERE sg.status = 'active'
		AND sg.auto_reserve = true
		AND sg.auto_amount > 0
		AND COALESCE(u.savings_auto_reserve, false) = true
		AND (
			sg.last_auto_at IS NULL OR sg.last_auto_at < $1 - CASE sg.auto_frequency
				WHEN 'daily' THEN INTERVAL '1 day'
				WHEN 'weekly' THEN INTERVAL '7 days'
				WHEN 'biweekly' THEN INTERVAL '14 days'
				ELSE INTERVAL '30 days'
			END
		)`, now)
	if err != nil {
		return nil, fmt.Errorf("listing due auto-save goals: %w", err)
	}
	return goals, nil
}

// RecordTopUp atomically claims and applies one scheduled top-up: the
// compare-and-set on last_auto_at makes concurrent keeper replicas safe.
// Current amount never exceeds target. Returns claimed=false when another
// replica won the race.
func (r *pgRepo) RecordTopUp(ctx context.Context, goalID string, amount float64) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE savings_goals SET
			current_amount = LEAST(target_amount, current_amount + $2),
			last_auto_at = NOW(),
			updated_at = NOW()
		WHERE id = $1 AND status = 'active'
		AND (last_auto_at IS NULL OR last_auto_at < NOW() - CASE auto_frequency
			WHEN 'daily' THEN INTERVAL '1 day'
			WHEN 'weekly' THEN INTERVAL '7 days'
			WHEN 'biweekly' THEN INTERVAL '14 days'
			ELSE INTERVAL '30 days'
		END)`, goalID, amount)
	if err != nil {
		return false, fmt.Errorf("recording auto-save top-up: %w", err)
	}
	rows, _ := result.RowsAffected()
	return rows > 0, nil
}
