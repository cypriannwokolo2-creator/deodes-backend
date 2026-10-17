-- Savings + currency preferences on users.
--
-- The settings screens for savings (auto-reserve, default target/frequency,
-- milestone nudges) and the preferred display currency previously had no
-- backing storage: the savings form saved nowhere and the currency picker was
-- local-only. These columns make PATCH /users/me the single preferences
-- writer, mirroring the digest-preference precedent (049).
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS savings_auto_reserve BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS savings_default_target NUMERIC(20, 2) NOT NULL DEFAULT 100;
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS savings_default_frequency TEXT NOT NULL DEFAULT 'monthly';
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS savings_notify_on_milestone BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS savings_milestone_threshold INTEGER NOT NULL DEFAULT 50;
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS preferred_currency TEXT NOT NULL DEFAULT 'NGN';
