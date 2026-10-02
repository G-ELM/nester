// Package costmonitor tracks outbound call volume to metered third parties
// (Soroban RPC, Horizon, DeFiLlama, Paystack, Flutterwave, …) and fires an
// alert when a configured daily budget is crossed — so an unexpected spike
// in mainnet-scale RPC traffic or a runaway integration is caught from
// inside the app, before it shows up as a surprise on an invoice.
//
// This is call-volume monitoring, not a dollar-accurate cost ledger: it
// counts requests per (category, provider) per day in Redis and compares
// against a configured limit. Pairing a limit with each provider's own
// pricing (per-request or per-compute-unit) is what turns "calls today" into
// "estimated dollars today" — see FromEnv's doc for how COST_BUDGETS
// expresses that.
//
// Degrades the same way the rest of this codebase's optional integrations
// do: with no REDIS_ADDR configured, Tracker is still usable but every
// RecordCall is a no-op — cost monitoring is simply off, never a source of
// request failures (see WrapTransport).
package costmonitor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// keyTTL outlives the longest a single day's counter needs to exist —
	// generous so a short clock skew between instances never drops a key
	// mid-day, but still bounded so Redis doesn't accumulate keys forever.
	keyTTL = 48 * time.Hour
)

// Tracker counts outbound calls per (category, provider) per UTC day.
type Tracker struct {
	redis  *redis.Client
	logger *slog.Logger
}

// NewTracker builds a Tracker. redisClient may be nil (no REDIS_ADDR
// configured) — RecordCall and DailyUsage become no-ops/zero rather than
// erroring, so cost monitoring is opt-in by configuration alone.
func NewTracker(redisClient *redis.Client, logger *slog.Logger) *Tracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Tracker{redis: redisClient, logger: logger}
}

func dayKey(category, provider string, day time.Time) string {
	return fmt.Sprintf("costmonitor:usage:%s:%s:%s", category, provider, day.UTC().Format("2006-01-02"))
}

// RecordCall adds n to today's (category, provider) counter. A Redis error
// is logged, never returned to a caller on the request hot path — see
// WrapTransport, the primary caller.
func (t *Tracker) RecordCall(ctx context.Context, category, provider string, n int64) {
	if t == nil || t.redis == nil || n == 0 {
		return
	}
	key := dayKey(category, provider, time.Now())
	pipe := t.redis.Pipeline()
	pipe.IncrBy(ctx, key, n)
	pipe.Expire(ctx, key, keyTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		t.logger.Debug("costmonitor: record call failed", "category", category, "provider", provider, "error", err)
	}
}

// RecordCallAsync fires RecordCall in a background goroutine with its own
// short-lived context, so instrumenting an RPC/HTTP call site never adds
// Redis latency (or a Redis outage) to that call's own critical path — see
// WrapTransport.
func (t *Tracker) RecordCallAsync(category, provider string) {
	if t == nil || t.redis == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		t.RecordCall(ctx, category, provider, 1)
	}()
}

// DailyUsage returns today's call count for (category, provider). Returns 0
// (not an error) when no calls have been recorded yet, or when the tracker
// has no Redis client configured.
func (t *Tracker) DailyUsage(ctx context.Context, category, provider string) (int64, error) {
	if t == nil || t.redis == nil {
		return 0, nil
	}
	n, err := t.redis.Get(ctx, dayKey(category, provider, time.Now())).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return n, nil
}

// markAlerted records that a breach was already alerted on today, so a
// checker ticking every few minutes sends at most one alert per
// (category, provider, severity) per day. Returns true the first time it's
// called for a given key today; false on every subsequent call.
func (t *Tracker) markAlerted(ctx context.Context, category, provider, severity string) (firstTime bool, err error) {
	if t == nil || t.redis == nil {
		// No Redis means no dedup state either way; treat every check as
		// "first time" so BudgetChecker still logs/alerts rather than
		// silently doing nothing twice over.
		return true, nil
	}
	key := fmt.Sprintf("costmonitor:alerted:%s:%s:%s:%s", category, provider, severity, time.Now().UTC().Format("2006-01-02"))
	ok, err := t.redis.SetNX(ctx, key, "1", keyTTL).Result()
	if err != nil {
		return false, err
	}
	return ok, nil
}
