package costmonitor

import (
	"context"
	"log/slog"
	"time"
)

// LeaderChecker gates Job's tick to a single replica, matching how every
// other alert-sending scheduler job in this codebase (e.g.
// scheduler.APYDeviationJob) avoids every replica sending the same alert on
// the same tick. Duck-typed against scheduler.Leadership's IsLeader method
// rather than importing the scheduler package, consistent with how other
// small per-consumer interfaces are defined in this codebase.
type LeaderChecker interface {
	IsLeader() bool
}

// Job runs BudgetChecker.Check on a ticker until its context is cancelled.
type Job struct {
	cfg     Config
	checker *BudgetChecker
	logger  *slog.Logger
	leader  LeaderChecker
}

// NewJob constructs a Job. logger may be nil (defaults to slog.Default()).
func NewJob(cfg Config, checker *BudgetChecker, logger *slog.Logger) *Job {
	if logger == nil {
		logger = slog.Default()
	}
	return &Job{cfg: cfg, checker: checker, logger: logger}
}

// SetLeaderChecker wires leader election; see LeaderChecker's doc comment.
func (j *Job) SetLeaderChecker(l LeaderChecker) { j.leader = l }

func (j *Job) isLeader() bool {
	return j.leader == nil || j.leader.IsLeader()
}

// Run drives the check loop until ctx is cancelled.
func (j *Job) Run(ctx context.Context) {
	if !j.cfg.Enabled {
		j.logger.Info("costmonitor: disabled; not starting")
		return
	}
	j.logger.Info("costmonitor: starting",
		"interval", j.cfg.Interval,
		"budgets", len(j.cfg.Budgets),
		"warn_threshold_pct", j.cfg.WarnThresholdPct,
	)

	j.tick(ctx)

	ticker := time.NewTicker(j.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			j.logger.Info("costmonitor: stopping")
			return
		case <-ticker.C:
			j.tick(ctx)
		}
	}
}

func (j *Job) tick(ctx context.Context) {
	if !j.isLeader() {
		j.logger.Debug("costmonitor: skipping tick, not leader")
		return
	}

	breaches, err := j.checker.Check(ctx)
	if err != nil {
		j.logger.Error("costmonitor: budget check failed", "error", err)
		return
	}
	for _, b := range breaches {
		j.logger.Warn("costmonitor: budget threshold crossed",
			"category", b.Budget.Category,
			"provider", b.Budget.Provider,
			"usage", b.Usage,
			"limit", b.Budget.DailyLimit,
			"severity", b.Severity,
		)
	}
}
