-- Auto-save schedule per goal: fixed amount + frequency. The keeper tops up
-- current_amount on schedule while the goal is active and auto_reserve is on.
ALTER TABLE savings_goals
    ADD COLUMN IF NOT EXISTS auto_amount NUMERIC(20, 2) NOT NULL DEFAULT 0;
ALTER TABLE savings_goals
    ADD COLUMN IF NOT EXISTS auto_frequency TEXT NOT NULL DEFAULT 'monthly';
ALTER TABLE savings_goals
    ADD COLUMN IF NOT EXISTS last_auto_at TIMESTAMPTZ;
