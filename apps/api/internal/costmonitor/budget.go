package costmonitor

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// SeverityWarning means usage crossed WarnThresholdPct of the daily
	// limit but not the limit itself — an early signal, not yet a breach.
	SeverityWarning = "warning"
	// SeverityCritical means usage reached or exceeded the daily limit.
	SeverityCritical = "critical"

	defaultWarnThresholdPct = 80.0
	defaultCheckInterval    = 15 * time.Minute
)

// Budget is one category/provider's configured daily call-volume ceiling —
// e.g. {Category: "stellar_rpc", Provider: "soroban", DailyLimit: 500_000}.
// Pick DailyLimit from the provider's own pricing (requests or compute units
// per dollar) so "usage crossed the budget" tracks "spend crossed the
// budget you set", even though this package only ever counts calls.
type Budget struct {
	Category   string
	Provider   string
	DailyLimit int64
}

// Breach is one budget found over its warning or critical threshold on a
// single check.
type Breach struct {
	Budget   Budget
	Usage    int64
	Severity string
}

// Alerter delivers a Breach to wherever humans will see it (Slack, email,
// PagerDuty, …). See WebhookAlerter for the one built-in implementation.
type Alerter interface {
	Alert(ctx context.Context, b Breach) error
}

// Config controls the BudgetChecker.
type Config struct {
	Enabled          bool
	Interval         time.Duration
	Budgets          []Budget
	WarnThresholdPct float64
}

// FromEnv builds Config from:
//
//	COST_MONITOR_ENABLED=true|false        (default: true if COST_BUDGETS is set, else false)
//	COST_MONITOR_INTERVAL_MINUTES=15       (default 15)
//	COST_ALERT_WARN_THRESHOLD_PCT=80       (default 80)
//	COST_BUDGETS=category:provider:dailyLimit[,category:provider:dailyLimit...]
//
// e.g. COST_BUDGETS="stellar_rpc:soroban:500000,stellar_rpc:horizon:500000,third_party_api:defillama:100000"
//
// An entry that fails to parse is skipped (logged by the caller, not here)
// rather than failing startup — a typo in one budget shouldn't take down
// monitoring for the rest.
func FromEnv() (Config, []error) {
	var errs []error

	budgets, parseErrs := parseBudgets(os.Getenv("COST_BUDGETS"))
	errs = append(errs, parseErrs...)

	enabled := len(budgets) > 0
	if v := os.Getenv("COST_MONITOR_ENABLED"); v != "" {
		enabled = v == "true"
	}

	interval := defaultCheckInterval
	if v := os.Getenv("COST_MONITOR_INTERVAL_MINUTES"); v != "" {
		if mins, err := strconv.Atoi(v); err == nil && mins > 0 {
			interval = time.Duration(mins) * time.Minute
		}
	}

	warnPct := defaultWarnThresholdPct
	if v := os.Getenv("COST_ALERT_WARN_THRESHOLD_PCT"); v != "" {
		if pct, err := strconv.ParseFloat(v, 64); err == nil && pct > 0 && pct <= 100 {
			warnPct = pct
		}
	}

	return Config{
		Enabled:          enabled,
		Interval:         interval,
		Budgets:          budgets,
		WarnThresholdPct: warnPct,
	}, errs
}

func parseBudgets(raw string) ([]Budget, []error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var budgets []Budget
	var errs []error
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) != 3 {
			errs = append(errs, fmt.Errorf("costmonitor: invalid COST_BUDGETS entry %q (want category:provider:dailyLimit)", entry))
			continue
		}
		limit, err := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		if err != nil || limit <= 0 {
			errs = append(errs, fmt.Errorf("costmonitor: invalid daily limit in COST_BUDGETS entry %q", entry))
			continue
		}
		budgets = append(budgets, Budget{
			Category:   strings.TrimSpace(parts[0]),
			Provider:   strings.TrimSpace(parts[1]),
			DailyLimit: limit,
		})
	}
	return budgets, errs
}

// BudgetChecker periodically compares tracked usage against Config.Budgets
// and alerts on crossings, at most once per (category, provider, severity)
// per day (see Tracker.markAlerted).
type BudgetChecker struct {
	cfg     Config
	tracker *Tracker
	alerter Alerter
}

// NewBudgetChecker constructs a checker. alerter may be nil — Check still
// runs and returns breaches, it just doesn't deliver them anywhere (useful
// for tests, or running with monitoring-but-no-webhook-configured).
func NewBudgetChecker(cfg Config, tracker *Tracker, alerter Alerter) *BudgetChecker {
	return &BudgetChecker{cfg: cfg, tracker: tracker, alerter: alerter}
}

// Check evaluates every configured budget against today's usage and
// delivers (deduplicated) alerts for any that crossed warning or critical.
// Returns every breach found this call, regardless of whether its alert was
// deduplicated — callers that just want today's status (e.g. an admin
// endpoint) can use the return value directly without waiting on an alert.
func (c *BudgetChecker) Check(ctx context.Context) ([]Breach, error) {
	var breaches []Breach
	for _, budget := range c.cfg.Budgets {
		usage, err := c.tracker.DailyUsage(ctx, budget.Category, budget.Provider)
		if err != nil {
			return breaches, fmt.Errorf("costmonitor: usage lookup for %s/%s: %w", budget.Category, budget.Provider, err)
		}

		severity := severityFor(usage, budget.DailyLimit, c.cfg.WarnThresholdPct)
		if severity == "" {
			continue
		}

		breach := Breach{Budget: budget, Usage: usage, Severity: severity}
		breaches = append(breaches, breach)

		firstTime, err := c.tracker.markAlerted(ctx, budget.Category, budget.Provider, severity)
		if err != nil || !firstTime {
			continue
		}
		if c.alerter != nil {
			_ = c.alerter.Alert(ctx, breach)
		}
	}
	return breaches, nil
}

func severityFor(usage, limit int64, warnThresholdPct float64) string {
	if limit <= 0 {
		return ""
	}
	if usage >= limit {
		return SeverityCritical
	}
	pct := float64(usage) / float64(limit) * 100
	if pct >= warnThresholdPct {
		return SeverityWarning
	}
	return ""
}
