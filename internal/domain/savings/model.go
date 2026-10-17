package savings

import "time"

// ValidGoalStatuses mirrors the CHECK constraint in migration
// 027_savings_goals (status IN ('active', 'completed', 'cancelled')).
var ValidGoalStatuses = map[string]bool{
	"active":    true,
	"completed": true,
	"cancelled": true,
}

// IsValidStatus reports whether s is a persistable goal status.
func IsValidStatus(s string) bool {
	return ValidGoalStatuses[s]
}

// ParseTargetDate validates the optional YYYY-MM-DD deadline accepted by
// CreateGoalRequest/UpdateGoalRequest. A nil or empty value means "no
// deadline" and is valid; anything else must be a real calendar date.
func ParseTargetDate(s *string) (time.Time, bool, error) {
	if s == nil || *s == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse("2006-01-02", *s)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

type SavingsGoal struct {
	ID            string    `json:"id" db:"id"`
	UserID        string    `json:"userId" db:"user_id"`
	Name          string    `json:"name" db:"name"`
	Description   string    `json:"description" db:"description"`
	TargetAmount  float64   `json:"targetAmount" db:"target_amount"`
	CurrentAmount float64   `json:"currentAmount" db:"current_amount"`
	TargetDate    *string   `json:"targetDate,omitempty" db:"target_date"`
	CircleID      *string   `json:"circleId,omitempty" db:"circle_id"`
	AutoReserve   bool      `json:"autoReserve" db:"auto_reserve"`
	// Auto-save schedule: fixed amount per frequency, topped up by the
	// keeper while the goal is active. LastAutoAt tracks the last run.
	AutoAmount    float64    `json:"autoAmount" db:"auto_amount"`
	AutoFrequency string     `json:"autoFrequency" db:"auto_frequency"`
	LastAutoAt    *time.Time `json:"lastAutoAt,omitempty" db:"last_auto_at"`
	Status        string    `json:"status" db:"status"`
	CreatedAt     time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt     time.Time `json:"updatedAt" db:"updated_at"`
}

type CreateGoalRequest struct {
	Name         string  `json:"name" binding:"required"`
	Description  string  `json:"description"`
	TargetAmount float64 `json:"targetAmount" binding:"required,gt=0"`
	TargetDate   *string `json:"targetDate"`
	CircleID     *string `json:"circleId"`
	AutoReserve  bool    `json:"autoReserve"`
	AutoAmount   float64 `json:"autoAmount"`
	AutoFrequency string `json:"autoFrequency"`
}

type UpdateGoalRequest struct {
	Name          *string  `json:"name"`
	Description   *string  `json:"description"`
	TargetAmount  *float64 `json:"targetAmount"`
	CurrentAmount *float64 `json:"currentAmount"`
	TargetDate    *string  `json:"targetDate"`
	CircleID      *string  `json:"circleId"`
	AutoAmount    *float64 `json:"autoAmount"`
	AutoFrequency *string  `json:"autoFrequency"`
	AutoReserve   *bool    `json:"autoReserve"`
	Status        *string  `json:"status"`
}

type GoalSummary struct {
	TotalGoals     int     `json:"totalGoals"`
	ActiveGoals    int     `json:"activeGoals"`
	CompletedGoals int     `json:"completedGoals"`
	TotalTarget    float64 `json:"totalTarget"`
	TotalSaved     float64 `json:"totalSaved"`
	OverallPercent float64 `json:"overallPercent"`
	SavingsStreak  int     `json:"savingsStreak"`
}
