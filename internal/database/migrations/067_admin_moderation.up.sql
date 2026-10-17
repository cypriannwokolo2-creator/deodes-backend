-- 067: admin moderation (pause/ban circles + communities, freeze funds).
--
-- 1. circle_status gains paused/banned. Rebuilt (not ADD VALUE) because ADD
--    VALUE cannot run inside the migrator's transaction. Existing gates
--    whitelist pending/active, so the new states fail closed automatically.
-- 2. communities gain suspend columns (nullable; no enum involved).
-- 3. wallets gain a freeze flag enforced by SendPayment (outbound only;
--    inbound deposits are unaffected).

CREATE TYPE circle_status_new AS ENUM (
    'pending',
    'active',
    'completed',
    'cancelled',
    'disputed',
    'paused',
    'banned'
);

ALTER TABLE circles ALTER COLUMN status DROP DEFAULT;

ALTER TABLE circles
    ALTER COLUMN status TYPE circle_status_new
    USING status::text::circle_status_new;

DROP TYPE circle_status;
ALTER TYPE circle_status_new RENAME TO circle_status;

ALTER TABLE circles ALTER COLUMN status SET DEFAULT 'pending'::circle_status;

ALTER TABLE communities
    ADD COLUMN IF NOT EXISTS suspended_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS suspended_reason TEXT;

ALTER TABLE wallets
    ADD COLUMN IF NOT EXISTS frozen BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS frozen_reason TEXT,
    ADD COLUMN IF NOT EXISTS frozen_at TIMESTAMPTZ;
