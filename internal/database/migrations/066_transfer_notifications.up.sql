-- 066: in-app notifications for wallet transfer outcomes.
--
-- 1. Extend notification_type with the transfer outcome values. The enum is
--    rebuilt (instead of ALTER TYPE ... ADD VALUE) because ADD VALUE cannot
--    run inside the migrator's transaction.
-- 2. Track outcome-notification delivery on the withdrawal audit row so a
--    client reporting the same receipt twice cannot create duplicates: the
--    claim UPDATE (... WHERE outcome_notified_at IS NULL) is atomic.

CREATE TYPE notification_type_new AS ENUM (
    'circle_created',
    'member_joined',
    'contribution_due',
    'contribution_received',
    'contribution_late',
    'payout_received',
    'circle_completed',
    'member_exited',
    'dispute_raised',
    'system',
    'transfer_confirmed',
    'transfer_failed',
    'transfer_received'
);

ALTER TABLE notifications
    ALTER COLUMN type TYPE notification_type_new
    USING type::text::notification_type_new;

DROP TYPE notification_type;
ALTER TYPE notification_type_new RENAME TO notification_type;

ALTER TABLE withdrawal_audit
    ADD COLUMN IF NOT EXISTS outcome_notified_at TIMESTAMPTZ;
