package vaultsnapshot

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// LeaderChecker gates Job's tick to a single replica — the same convention
// every other alert/export-sending scheduler job in this codebase follows
// (see scheduler.APYDeviationJob), so N instances never each export (and,
// for an HTTP exporter, pay for) the same snapshot on the same tick.
// Duck-typed rather than importing the scheduler package, same reasoning as
// costmonitor.LeaderChecker.
type LeaderChecker interface {
	IsLeader() bool
}

// Config controls Job.
type Config struct {
	Enabled  bool
	Interval time.Duration
}

// Job builds a signed Snapshot on a ticker and exports it.
type Job struct {
	cfg        Config
	fetcher    Fetcher
	signingKey []byte
	exporter   Exporter
	logger     *slog.Logger
	leader     LeaderChecker
	clock      func() time.Time
}

// NewJob constructs a Job. logger may be nil (defaults to slog.Default()).
// signingKey must be non-empty for Run to do anything meaningful — see
// main.go's wiring, which disables the job entirely without one configured
// rather than exporting unsigned snapshots.
func NewJob(cfg Config, fetcher Fetcher, signingKey []byte, exporter Exporter, logger *slog.Logger) *Job {
	if logger == nil {
		logger = slog.Default()
	}
	return &Job{
		cfg:        cfg,
		fetcher:    fetcher,
		signingKey: signingKey,
		exporter:   exporter,
		logger:     logger,
		clock:      time.Now,
	}
}

// SetLeaderChecker wires leader election; see LeaderChecker's doc comment.
func (j *Job) SetLeaderChecker(l LeaderChecker) { j.leader = l }

func (j *Job) isLeader() bool {
	return j.leader == nil || j.leader.IsLeader()
}

// Run drives the snapshot loop until ctx is cancelled.
func (j *Job) Run(ctx context.Context) {
	if !j.cfg.Enabled {
		j.logger.Info("vaultsnapshot: disabled; not starting")
		return
	}
	if len(j.signingKey) == 0 {
		j.logger.Error("vaultsnapshot: enabled but no signing key configured; not starting")
		return
	}
	j.logger.Info("vaultsnapshot: starting", "interval", j.cfg.Interval)

	j.tick(ctx)

	ticker := time.NewTicker(j.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			j.logger.Info("vaultsnapshot: stopping")
			return
		case <-ticker.C:
			j.tick(ctx)
		}
	}
}

func (j *Job) tick(ctx context.Context) {
	if !j.isLeader() {
		j.logger.Debug("vaultsnapshot: skipping tick, not leader")
		return
	}

	if err := j.exportOnce(ctx); err != nil {
		j.logger.Error("vaultsnapshot: export failed", "error", err)
		return
	}
}

// exportOnce builds, signs, and exports exactly one snapshot — split out
// from tick so an admin endpoint or a one-off CLI invocation can trigger an
// out-of-band snapshot through the same path the scheduled job uses.
func (j *Job) exportOnce(ctx context.Context) error {
	now := j.clock()

	snapshot, err := Build(ctx, j.fetcher, now)
	if err != nil {
		return err
	}

	envelope, err := Sign(j.signingKey, snapshot)
	if err != nil {
		return err
	}

	data, err := MarshalEnvelope(envelope)
	if err != nil {
		return fmt.Errorf("vaultsnapshot: marshal envelope: %w", err)
	}

	name := fmt.Sprintf("vault-balance-snapshot-%s.json", now.UTC().Format("20060102T150405Z"))
	if err := j.exporter.Export(ctx, name, data); err != nil {
		return err
	}

	j.logger.Info("vaultsnapshot: exported", "name", name, "vault_count", snapshot.VaultCount)
	return nil
}
