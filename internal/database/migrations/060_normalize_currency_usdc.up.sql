-- Zero-XLM policy: Arc USDC is the only asset. Normalize any legacy
-- circles rows still denominated in XLM to USDC. The circle_currency enum
-- label 'XLM' is intentionally left in place (ALTER TYPE DROP VALUE cannot
-- run inside a migration transaction); the application layer rejects
-- non-USDC currency on all write paths (validateCircleCurrency +
-- oneof=USDC), so the label is unreachable for new rows.
UPDATE circles SET currency = 'USDC', updated_at = NOW() WHERE currency = 'XLM';
