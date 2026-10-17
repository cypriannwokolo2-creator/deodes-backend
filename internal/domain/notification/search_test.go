package notification

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestBuildSearchConditions(t *testing.T) {
	uid := uuid.New()

	where, args := buildSearchConditions(uid, SearchFilter{})
	assert.Equal(t, "WHERE user_id = $1 AND is_archived = false", where)
	assert.Equal(t, []any{uid}, args)

	where, args = buildSearchConditions(uid, SearchFilter{
		Query:           " 50%_off ",
		Type:            TypeContributionDue,
		UnreadOnly:      true,
		IncludeArchived: true,
	})
	assert.Equal(t, "WHERE user_id = $1 AND is_read = false AND type = $2 AND (title ILIKE $3 OR body ILIKE $3)", where)
	assert.Equal(t, []any{uid, "contribution_due", `%50\%\_off%`}, args)
}

func TestDBNotificationType(t *testing.T) {
	// The enum stores underscores; Go constants use dots. Both directions
	// below must persist and filter against the same stored labels.
	assert.Equal(t, NotificationType("transfer_confirmed"), dbNotificationType(TypeTransferConfirmed))
	assert.Equal(t, NotificationType("transfer_failed"), dbNotificationType(TypeTransferFailed))
	assert.Equal(t, NotificationType("transfer_received"), dbNotificationType(TypeTransferReceived))
	assert.Equal(t, NotificationType("contribution_due"), dbNotificationType(TypeContributionDue))
	assert.Equal(t, NotificationType("system"), dbNotificationType("system"))
}
