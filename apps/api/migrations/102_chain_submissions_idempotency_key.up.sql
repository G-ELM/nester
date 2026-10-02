ALTER TABLE chain_submissions ADD COLUMN idempotency_key TEXT;

-- A retried client request or redelivered job carries the same
-- idempotency_key. While a submission for that key hasn't definitively
-- failed, a second row for it must be rejected so the retry is detected
-- and handed the existing (in-flight or confirmed) result instead of
-- sending a second on-chain transaction. The predicate excludes 'failed'
-- rows so a submission that's definitively dead frees the key for a
-- genuine fresh attempt.
CREATE UNIQUE INDEX idx_chain_submissions_idempotency_key
  ON chain_submissions(source_account, idempotency_key)
  WHERE idempotency_key IS NOT NULL AND status <> 'failed';
