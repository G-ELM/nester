DROP INDEX IF EXISTS idx_chain_submissions_idempotency_key;
ALTER TABLE chain_submissions DROP COLUMN IF EXISTS idempotency_key;
