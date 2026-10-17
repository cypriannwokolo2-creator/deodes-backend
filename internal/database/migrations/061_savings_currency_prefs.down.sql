ALTER TABLE users
    DROP COLUMN IF EXISTS preferred_currency;
ALTER TABLE users
    DROP COLUMN IF EXISTS savings_milestone_threshold;
ALTER TABLE users
    DROP COLUMN IF EXISTS savings_notify_on_milestone;
ALTER TABLE users
    DROP COLUMN IF EXISTS savings_default_frequency;
ALTER TABLE users
    DROP COLUMN IF EXISTS savings_default_target;
ALTER TABLE users
    DROP COLUMN IF EXISTS savings_auto_reserve;
