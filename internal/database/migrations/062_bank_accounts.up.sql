-- Fiat payout rails: saved bank accounts per user.
--
-- The payment settings screen posted to /bank-accounts, which never existed,
-- so adds silently 404'd behind a hardcoded mock account. This table backs
-- GET/POST /v1/bank-accounts, DELETE /v1/bank-accounts/:id and the
-- per-user default marker. Bank codes are validated by shape (3 digits) in
-- the domain, not by a CHECK constraint, so newly supported banks need no
-- migration. Account numbers are stored as given (user's own data, returned
-- only to its owner); verification against the bank rails is a later step.
CREATE TABLE IF NOT EXISTS bank_accounts (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    bank_code TEXT NOT NULL,
    bank_name TEXT NOT NULL,
    account_number TEXT NOT NULL,
    account_name TEXT NOT NULL,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bank_accounts_user_id ON bank_accounts (user_id);
