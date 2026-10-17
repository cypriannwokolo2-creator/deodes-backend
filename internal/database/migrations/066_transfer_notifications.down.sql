-- Reverse 066. Fails if any transfer_* notification rows exist (by design:
-- downgrading must not silently orphan delivered notifications); delete those
-- rows first if a downgrade is ever truly required.

CREATE TYPE notification_type_old AS ENUM (
    'circle_created',
    'member_joined',
    'contribution_due',
    'contribution_received',
    'contribution_late',
    'payout_received',
    'circle_completed',
    'member_exited',
    'dispute_raised',
    'system'
);

ALTER TABLE notifications
    ALTER COLUMN type TYPE notification_type_old
    USING type::text::notification_type_old;

DROP TYPE notification_type;
ALTER TYPE notification_type_old RENAME TO notification_type;

ALTER TABLE withdrawal_audit
    DROP COLUMN IF EXISTS outcome_notified_at;
