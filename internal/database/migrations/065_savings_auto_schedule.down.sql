ALTER TABLE savings_goals
    DROP COLUMN IF EXISTS last_auto_at;
ALTER TABLE savings_goals
    DROP COLUMN IF EXISTS auto_frequency;
ALTER TABLE savings_goals
    DROP COLUMN IF EXISTS auto_amount;
