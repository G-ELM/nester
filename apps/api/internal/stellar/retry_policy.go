package stellar

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// RetryPolicy bounds how long a chain submission that hasn't reached a
// terminal state (confirmed/failed) is still treated as potentially
// in-flight before a fresh attempt is allowed to replace it.
type RetryPolicy struct {
	// StaleAfter is how long a pending/submitted/unknown submission is
	// still considered live. A retry that lands within this window is
	// handed the existing submission's hash instead of sending a second
	// transaction. Past it, one on-chain reconciliation check is made
	// before a fresh attempt is allowed.
	StaleAfter time.Duration
}

// DefaultRetryPolicy is tuned for mainnet: Soroban ledgers close every few
// seconds, so a submission that hasn't resolved in 10 minutes almost always
// means the RPC node (or our process) died mid-flight rather than the
// ledger simply being slow.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{StaleAfter: 10 * time.Minute}
}

// maxIdempotentSubmitAttempts bounds the loop in SubmitIdempotent that can
// retry when it loses a race for an idempotency key to a concurrent caller.
// In practice that resolves within one or two iterations; the bound exists
// only to rule out a pathological infinite loop.
const maxIdempotentSubmitAttempts = 5

// SubmitIdempotent submits a chain transaction at most once per
// idempotencyKey while its outcome is unresolved. A retried call with the
// same key — a client retrying after an RPC timeout, a job queue
// redelivering the same task, or two goroutines racing on the same action —
// is detected against chain_submissions and handed the in-flight or
// completed result instead of building and sending a second signed
// transaction.
//
// buildFn builds and signs a fresh transaction for the given sequence
// number, returning its envelope and the transaction hash computed locally
// from the signed envelope — so the hash is known even if the RPC call that
// follows times out. sendFn submits the envelope; ambiguous=true means the
// failure was a network/RPC timeout and the transaction's on-chain fate is
// unknown, as opposed to a definitive synchronous rejection.
func (p *SubmissionPipeline) SubmitIdempotent(
	ctx context.Context,
	sourceAccount, idempotencyKey, domainAction string,
	jobID *uuid.UUID,
	policy RetryPolicy,
	buildFn func(seq int64) (envelope, txHash string, err error),
	sendFn func(ctx context.Context, envelope string) (ambiguous bool, err error),
) (string, error) {
	for attempt := 0; attempt < maxIdempotentSubmitAttempts; attempt++ {
		if idempotencyKey != "" {
			existing, err := p.GetByIdempotencyKey(ctx, sourceAccount, idempotencyKey)
			if err != nil && !errors.Is(err, ErrSubmissionNotFound) {
				return "", err
			}
			if err == nil {
				switch existing.Status {
				case StatusConfirmed:
					// Already succeeded under this exact key — this call
					// is a retry (client or network) of a request we
					// already completed. Replay the result rather than
					// submitting again.
					return existing.TransactionHash, nil
				case StatusFailed:
					// Definitively dead on-chain; the unique index on
					// (source_account, idempotency_key) excludes failed
					// rows, so a fresh attempt below is safe.
				default: // pending, submitted, unknown
					if time.Since(existing.CreatedAt) < policy.StaleAfter {
						// A submission for this action may still be
						// in-flight. Hand back its hash instead of
						// building and sending a second transaction — the
						// caller's own confirmation poll resolves it from
						// here.
						return existing.TransactionHash, nil
					}
					status, rerr := p.ResolveTimeout(ctx, existing.ID)
					if rerr != nil {
						return "", rerr
					}
					if status == StatusConfirmed {
						return existing.TransactionHash, nil
					}
					if status != StatusFailed {
						// Stale and still unresolved even after a fresh
						// chain check. Refuse to guess: sending a second
						// transaction here could double-submit if the
						// first is in fact still pending.
						return "", fmt.Errorf("%w: submission %s for key %s is stale and its on-chain status is still unresolved", ErrSubmissionTimeout, existing.ID, idempotencyKey)
					}
					// status == StatusFailed: safe to retry fresh below.
				}
			}
		}

		seq, err := p.AllocateSequence(ctx, sourceAccount)
		if err != nil {
			return "", fmt.Errorf("allocate sequence: %w", err)
		}

		envelope, txHash, err := buildFn(seq)
		if err != nil {
			return "", fmt.Errorf("build transaction: %w", err)
		}

		submission, isNew, err := p.RecordOrGetExisting(ctx, sourceAccount, idempotencyKey, seq, envelope, txHash, jobID, domainAction)
		if err != nil {
			return "", fmt.Errorf("record submission: %w", err)
		}
		if !isNew {
			// Lost a race to a concurrent caller that claimed this
			// idempotency key first — defer to its outcome instead of
			// sending our own, independently-built transaction for the
			// same action.
			continue
		}

		ambiguous, sendErr := sendFn(ctx, envelope)
		if sendErr != nil {
			msg := sendErr.Error()
			if ambiguous {
				_ = p.UpdateStatus(ctx, submission.ID, StatusUnknown, &msg)
				// The RPC call itself timed out — whether the network
				// received and will apply this transaction is genuinely
				// unknown. Hand back its hash so the caller's own
				// confirmation poll resolves it; a later retry with the
				// same idempotencyKey lands on the dedup branch above
				// instead of sending a second transaction.
				return submission.TransactionHash, nil
			}
			_ = p.UpdateStatus(ctx, submission.ID, StatusFailed, &msg)
			return "", sendErr
		}

		if err := p.MarkSubmitted(ctx, submission.ID); err != nil {
			return "", err
		}
		return submission.TransactionHash, nil
	}

	return "", fmt.Errorf("submit idempotent: exceeded %d attempts contending for idempotency key %s", maxIdempotentSubmitAttempts, idempotencyKey)
}
