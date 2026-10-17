package reputation

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type ReputationSnapshot struct {
	UserID    uuid.UUID       `json:"userId" db:"user_id"`
	Score     int             `json:"score" db:"score"`
	Level     string          `json:"level" db:"level"`
	Breakdown json.RawMessage `json:"breakdown" db:"breakdown"`
	Month     time.Time       `json:"month" db:"month"`
	CreatedAt time.Time       `json:"createdAt" db:"created_at"`
}

// LeaderboardEntry is the public projection: identity + score only, no
// contact data.
type LeaderboardEntry struct {
	UserID      uuid.UUID `json:"userId" db:"user_id"`
	Username    *string   `json:"username,omitempty" db:"username"`
	DisplayName *string   `json:"displayName,omitempty" db:"display_name"`
	Score       int       `json:"score" db:"score"`
	Level       string    `json:"level" db:"level"`
}

type ScoreBreakdown struct {
	Streaks     int `json:"streaks"`
	Completions int `json:"completions"`
	Volume      int `json:"volume"`
	Recency     int `json:"recency"`
}
