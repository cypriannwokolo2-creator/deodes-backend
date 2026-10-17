-- Unique usernames: auto-derived from the email local-part at registration
-- (T001). CITEXT makes `Ada` and `ada` collide, so uniqueness is
-- case-insensitive by construction. Nullable so the migration applies to
-- existing rows, which the backfill (T010) fills in.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS username CITEXT UNIQUE;
CREATE INDEX IF NOT EXISTS idx_users_username ON users (username);
