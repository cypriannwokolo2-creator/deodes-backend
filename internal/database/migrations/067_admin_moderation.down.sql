-- Reverse 067. Fails if any circle uses paused/banned (by design: downgrading
-- must not silently resurrect moderated circles); clear those rows first if
-- a downgrade is ever truly required.

CREATE TYPE circle_status_old AS ENUM (
    'pending',
    'active',
    'completed',
    'cancelled',
    'disputed'
);

ALTER TABLE circles ALTER COLUMN status DROP DEFAULT;

ALTER TABLE circles
    ALTER COLUMN status TYPE circle_status_old
    USING status::text::circle_status_old;

DROP TYPE circle_status;
ALTER TYPE circle_status_old RENAME TO circle_status;

ALTER TABLE circles ALTER COLUMN status SET DEFAULT 'pending'::circle_status;

ALTER TABLE communities
    DROP COLUMN IF EXISTS suspended_at,
    DROP COLUMN IF EXISTS suspended_reason;

ALTER TABLE wallets
    DROP COLUMN IF EXISTS frozen,
    DROP COLUMN IF EXISTS frozen_reason,
    DROP COLUMN IF EXISTS frozen_at;
