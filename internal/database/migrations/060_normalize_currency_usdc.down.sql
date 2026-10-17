-- Data normalization is irreversible by design: XLM-denominated rows that
-- were converted to USDC by the up migration cannot be mapped back.
-- No schema change to revert.
SELECT 1;
