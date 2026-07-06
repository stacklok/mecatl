package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// foldOperatorSchedules merges the OPERATOR-TIER `schedules:` YAML subtree (read by
// the permconfig resolver from the user-global + CLI tiers ONLY — never the project
// file) onto cfg as a parsed []port.ScheduleSpec. It maps each ScheduleDecl via
// toScheduleSpec (parsing the cron/one-shot strings the YAML carried into the
// port.TriggerSpec the ScheduleStore expects). A parse failure on any declaration
// is logged and the declaration is skipped (fail-soft per declaration — one bad
// schedule does not drop the rest), mirroring the per-rule fail-soft of
// compileGuardrailRules. It is a no-op when no operator-tier schedules block was
// configured. cfg is taken and returned by value (Build holds a local cfg).
//
// It does NOT reconcile the declared schedules into the store — that is
// reconcileSchedules, called AFTER the scheduler is started (it needs the live
// Service). This fold only parses the YAML into the cfg field the reconcile step
// reads.
func foldOperatorSchedules(cfg Config) Config {
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || res == nil {
		return cfg
	}
	s := res.OperatorSchedules()
	if s == nil || len(s.Items) == 0 {
		return cfg
	}
	specs := make([]port.ScheduleSpec, 0, len(s.Items))
	for _, decl := range s.Items {
		spec, err := toScheduleSpec(decl)
		if err != nil {
			cfg.diag().Log(context.Background(), port.LevelWarn,
				"schedules: invalid declaration skipped",
				"name", decl.Name, "err", err.Error())
			continue
		}
		// singleton:false opt-out is not yet supported end to end (the create-seam
		// forces Singleton=true — a *bool/proto-optional change is deferred). Make
		// the YAML path honest: warn rather than silently overriding the operator's
		// declared intent.
		if !decl.Singleton {
			cfg.diag().Log(context.Background(), port.LevelWarn,
				"schedules: singleton:false is not yet supported and the schedule will run with singleton=true (overlap suppression); track/opt-out is a future change",
				"name", decl.Name)
		}
		specs = append(specs, spec)
	}
	cfg.DeclaredSchedules = specs
	return cfg
}

// toScheduleSpec maps a YAML-friendly ScheduleDecl to a port.ScheduleSpec (the
// value object the ScheduleStore + Service.CreateSchedule expect). It parses the
// cron/one-shot strings, sets the TriggerSpec, maps the selector/profile/mode/limits,
// and interprets the misfire policy string. It does NOT validate the trigger XOR
// or the cron grammar — the Service.CreateSchedule seam does that fail-closed
// (reconcileSchedules calls CreateSchedule/UpdateSchedule, which call
// validateScheduleSpec). It returns an error only for a structural misshape it can
// detect cheaply here (a bad one-shot time format, an unknown misfire policy).
func toScheduleSpec(decl permconfig.ScheduleDecl) (port.ScheduleSpec, error) {
	trigger := port.TriggerSpec{Cron: strings.TrimSpace(decl.Cron)}
	if ot := strings.TrimSpace(decl.OneShot); ot != "" {
		t, err := time.Parse(time.RFC3339, ot)
		if err != nil {
			return port.ScheduleSpec{}, fmt.Errorf("oneShot: invalid RFC3339 time %q: %w", ot, err)
		}
		trigger.OneShot = t
	}
	var misfire port.MisfirePolicy
	switch strings.ToLower(strings.TrimSpace(decl.Misfire)) {
	case "":
		misfire = port.MisfireFireOnceNow // the zero-value default
	case "skip":
		misfire = port.MisfireSkip
	default:
		return port.ScheduleSpec{}, fmt.Errorf("misfire: unknown policy %q (want \"\" / \"skip\")", decl.Misfire)
	}
	mode := session.PermissionMode(strings.TrimSpace(decl.Mode))
	// A read-leaning schedule (Mutating=false) MUST run in plan mode
	// (validateScheduleSpec rejects a write-capable Mode under Mutating=false).
	// When the YAML omits mode: on a non-mutating declaration, default to plan so
	// the generated config example (`mutating: false` with no `mode:`) reconciles
	// instead of failing the create-seam validation.
	if !decl.Mutating && mode == "" {
		mode = session.ModePlan
	}
	return port.ScheduleSpec{
		Name:      strings.TrimSpace(decl.Name),
		Prompt:    decl.Prompt,
		Trigger:   trigger,
		Selector:  port.ScheduleProviderSelector{ProviderID: strings.TrimSpace(decl.Provider), ModelID: strings.TrimSpace(decl.Model)},
		Profile:   strings.TrimSpace(decl.Profile),
		Workspace: strings.TrimSpace(decl.Workspace),
		Mode:      mode,
		Limits: session.Limits{
			MaxTurns:               decl.MaxTurns,
			MaxToolCalls:           decl.MaxToolCalls,
			MaxConsecutiveFailures: 0, // not exposed in YAML v1 — a future knob
		},
		Mutating:  decl.Mutating,
		MaxFires:  decl.MaxFires,
		Misfire:   misfire,
		Singleton: decl.Singleton,
		Timezone:  strings.TrimSpace(decl.Timezone),
	}, nil
}

// schedulesDiffer reports whether the declared spec differs from the stored
// schedule's spec in a way reconcileSchedules should UpdateSchedule for. It
// compares the fields an operator would re-declare; CreatedAt is intentionally
// excluded (it is a store-side timestamp, not operator-authored). Parts is excluded
// (v1 declarations are prompt-only; the YAML carries no multimodal parts).
func schedulesDiffer(decl, stored port.ScheduleSpec) bool {
	switch {
	case decl.Name != stored.Name:
		return true
	case decl.Prompt != stored.Prompt:
		return true
	case decl.Trigger != stored.Trigger:
		return true
	case decl.Selector != stored.Selector:
		return true
	case decl.Profile != stored.Profile:
		return true
	case decl.Workspace != stored.Workspace:
		return true
	case decl.Mode != stored.Mode:
		return true
	case decl.Limits != stored.Limits:
		return true
	case decl.Mutating != stored.Mutating:
		return true
	case decl.MaxFires != stored.MaxFires:
		return true
	case decl.Misfire != stored.Misfire:
		return true
	case decl.Singleton != stored.Singleton:
		return true
	case decl.Timezone != stored.Timezone:
		return true
	}
	return false
}

// reconcileSchedules upserts declared schedules into the durable ScheduleStore
// (create missing, update differing); an unchanged schedule is left alone
// (idempotent — re-running Build does not churn). Schedules removed from the
// YAML are NOT deleted — an operator must delete them explicitly via the
// API/CLI. It is called only when the scheduler is enabled AND there are
// declared schedules, so the default byte-identical path (no scheduler, no
// declarations) never reaches here. Errors are logged (WARN) per schedule,
// never fatal — a broken store at reconcile time does not block startup (the
// scheduler's tick loop will retry on its next tick; a schedule that failed to
// Create simply is not in the store yet).
func reconcileSchedules(ctx context.Context, cfg Config, svc *server.Service) {
	if len(cfg.DeclaredSchedules) == 0 {
		return
	}
	existing, err := svc.ListSchedules(ctx)
	if err != nil {
		cfg.diag().Log(ctx, port.LevelWarn, "schedules: reconcile skipped (ListSchedules failed)",
			"err", err.Error())
		return
	}
	byName := make(map[string]port.ScheduleSpec, len(existing))
	for _, s := range existing {
		byName[s.Spec.Name] = s.Spec
	}
	for _, decl := range cfg.DeclaredSchedules {
		// Normalize the declared spec with the SAME defaults the create-seam
		// (CreateSchedule/UpdateSchedule) applies via applyScheduleDefaults, so
		// the diff comparison is against the store's normalized form. Without
		// this, a declaration with singleton: false would churn on every restart:
		// the stored spec has Singleton=true (the create-seam forces it because
		// scheduleSingletonExplicit is a stub that always returns false), while
		// the declared spec keeps Singleton=false → a spurious Update every run.
		norm := decl
		if !norm.Singleton {
			norm.Singleton = true
		}
		stored, ok := byName[norm.Name]
		switch {
		case !ok:
			if _, err := svc.CreateSchedule(ctx, decl); err != nil {
				cfg.diag().Log(ctx, port.LevelWarn, "schedules: reconcile create failed",
					"name", decl.Name, "err", err.Error())
				continue
			}
			cfg.diag().Log(ctx, port.LevelInfo, "schedules: reconciled (created)",
				"name", decl.Name)
		case schedulesDiffer(norm, stored):
			if _, err := svc.UpdateSchedule(ctx, decl); err != nil {
				cfg.diag().Log(ctx, port.LevelWarn, "schedules: reconcile update failed",
					"name", decl.Name, "err", err.Error())
				continue
			}
			cfg.diag().Log(ctx, port.LevelInfo, "schedules: reconciled (updated)",
				"name", decl.Name)
		default:
			cfg.diag().Log(ctx, port.LevelDebug, "schedules: reconciled (unchanged)",
				"name", decl.Name)
		}
	}
}
