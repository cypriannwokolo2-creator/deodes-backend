-- 068: support tickets (contact-form inbox).
--
-- Replaces log-only triage: tickets persist with an optional user link
-- (set when the reporter is authenticated) and an open/resolved workflow
-- for the admin inbox. Email is retained as the reply path.

CREATE TABLE IF NOT EXISTS support_tickets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    email VARCHAR(254) NOT NULL,
    message TEXT NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_support_tickets_status_created
    ON support_tickets (status, created_at DESC);
