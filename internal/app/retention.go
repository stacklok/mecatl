package app

import (
	"context"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// RetentionCLISet records which compatibility flags were explicitly supplied.
type RetentionCLISet struct {
	MainMaxAge, MainMaxCount, ChildMaxAge, ChildMaxCount bool
	ScheduledMaxAge, ScheduledMaxCount, SweepCadence     bool
}

func foldOperatorRetention(cfg Config) (Config, error) {
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok {
		return cfg, validateRetentionScalars(cfg)
	}
	r, err := res.OperatorRetention()
	if err != nil {
		return cfg, fmt.Errorf("invalid operator retention config: %w", err)
	}
	if r == nil {
		return cfg, validateRetentionScalars(cfg)
	}
	if err := applyOperatorRetention(&cfg, r); err != nil {
		return cfg, err
	}
	cfg.AcknowledgeMainRetention = cfg.AcknowledgeMainRetention || r.AcknowledgeMainDeletion
	return cfg, validateRetentionScalars(cfg)
}

func applyOperatorRetention(cfg *Config, r *permconfig.RetentionSection) error {
	var err error
	if !cfg.RetentionCLISet.MainMaxAge && r.Main.MaxAgeSet {
		cfg.MainRetention, err = parseRetentionDuration("retention.main.max_age", r.Main.MaxAge)
		if err != nil {
			return err
		}
	}
	if !cfg.RetentionCLISet.MainMaxCount && r.Main.MaxCountSet {
		cfg.MainRetentionMaxTotal = r.Main.MaxCount
	}
	if !cfg.RetentionCLISet.ChildMaxAge && r.Child.MaxAgeSet {
		cfg.ChildRetention, err = parseRetentionDuration("retention.child.max_age", r.Child.MaxAge)
		if err != nil {
			return err
		}
	}
	if !cfg.RetentionCLISet.ChildMaxCount && r.Child.MaxCountSet {
		cfg.ChildRetentionMaxPerFamily = r.Child.MaxCount
	}
	if !cfg.RetentionCLISet.ScheduledMaxAge && r.Scheduled.MaxAgeSet {
		cfg.ScheduleFireRetention, err = parseRetentionDuration("retention.scheduled.max_age", r.Scheduled.MaxAge)
		if err != nil {
			return err
		}
	}
	if !cfg.RetentionCLISet.ScheduledMaxCount && r.Scheduled.MaxCountSet {
		cfg.ScheduleFireRetentionMaxTotal = r.Scheduled.MaxCount
	}
	if !cfg.RetentionCLISet.SweepCadence && r.SweepCadenceSet {
		cfg.ChildGCInterval, err = parseRetentionDuration("retention.sweep_cadence", r.SweepCadence)
		if err != nil {
			return err
		}
	}
	return nil
}

func parseRetentionDuration(path, raw string) (time.Duration, error) {
	d, err := permconfig.ParseRetentionDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a non-negative duration", path)
	}
	return d, nil
}

func validateRetentionScalars(cfg Config) error {
	if cfg.MainRetention < 0 || cfg.ChildRetention < 0 || cfg.ScheduleFireRetention < 0 || cfg.ChildGCInterval < 0 ||
		cfg.MainRetentionMaxTotal < 0 || cfg.ChildRetentionMaxPerFamily < 0 || cfg.ScheduleFireRetentionMaxTotal < 0 {
		return fmt.Errorf("retention limits and sweep cadence must be non-negative; 0 disables")
	}
	return nil
}

func validateDestructiveMainRetention(ctx context.Context, cfg Config) error {
	if cfg.MainRetention <= 0 && cfg.MainRetentionMaxTotal <= 0 {
		return nil
	}
	d := cfg.Diagnostics
	if d == nil {
		d = port.NopDiagnostics{}
	}
	d.Log(ctx, port.LevelInfo, "retention planner summary: destructive main-session cleanup policy",
		"policy_version", "retention/v1", "main_max_age", cfg.MainRetention,
		"main_max_count", cfg.MainRetentionMaxTotal, "unknown", "protected")
	if !cfg.AcknowledgeMainRetention {
		return fmt.Errorf("destructive main retention requires explicit acknowledgement (--acknowledge-main-retention or retention.acknowledge_main_deletion: true)")
	}
	return nil
}
