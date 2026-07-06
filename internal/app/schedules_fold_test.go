package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// foldOperatorSchedules: a nil resolver / no operator block is a no-op; a YAML
// block is folded onto cfg.DeclaredSchedules as parsed []port.ScheduleSpec.
func TestFoldOperatorSchedulesNoResolverNoOp(t *testing.T) {
	cfg := Config{Model: "m"}
	if got := foldOperatorSchedules(cfg); len(got.DeclaredSchedules) != 0 {
		t.Fatal("with no resolver the fold must be a no-op")
	}
}

// foldOperatorSchedules folds the OPERATOR-TIER YAML onto cfg.DeclaredSchedules,
// parsing the cron/one-shot strings into port.TriggerSpec and the misfire string
// into port.MisfirePolicy. A bad declaration is skipped (fail-soft) — the good one
// survives.
func TestFoldOperatorSchedules(t *testing.T) {
	const yamlCfg = `
schedules:
  - name: nightly-review
    cron: "0 9 * * *"
    timezone: "America/New_York"
    prompt: "Review open PRs"
    provider: openrouter
    model: anthropic/claude-sonnet-4
    mutating: false
    mode: plan
    maxTurns: 20
    maxToolCalls: 100
    singleton: true
    maxFires: 0
  - name: one-shot-flag
    oneShot: "2026-08-01T09:00:00Z"
    prompt: "Remove the flag code"
    mutating: true
    mode: default
    misfire: skip
`
	path := filepath.Join(t.TempDir(), "schedules.yaml")
	if err := os.WriteFile(path, []byte(yamlCfg), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	if res == nil {
		t.Fatal("resolver should be non-nil")
	}

	cfg := foldOperatorSchedules(Config{permResolver: res})
	if len(cfg.DeclaredSchedules) != 2 {
		t.Fatalf("expected 2 declared schedules; got %d", len(cfg.DeclaredSchedules))
	}

	cron := cfg.DeclaredSchedules[0]
	if cron.Name != "nightly-review" {
		t.Fatalf("cron name: %q", cron.Name)
	}
	if cron.Trigger.Kind() != port.TriggerCron || cron.Trigger.Cron != "0 9 * * *" {
		t.Fatalf("cron trigger not parsed: %+v", cron.Trigger)
	}
	if cron.Selector.ProviderID != "openrouter" || cron.Selector.ModelID != "anthropic/claude-sonnet-4" {
		t.Fatalf("selector not parsed: %+v", cron.Selector)
	}
	if cron.Timezone != "America/New_York" {
		t.Fatalf("timezone: %q", cron.Timezone)
	}
	if cron.Limits.MaxTurns != 20 || cron.Limits.MaxToolCalls != 100 {
		t.Fatalf("limits not parsed: %+v", cron.Limits)
	}
	if !cron.Singleton || cron.Mutating {
		t.Fatalf("knobs not parsed: singleton=%v mutating=%v", cron.Singleton, cron.Mutating)
	}
	if cron.Misfire != port.MisfireFireOnceNow {
		t.Fatalf("default misfire: %+v", cron.Misfire)
	}

	one := cfg.DeclaredSchedules[1]
	if one.Trigger.Kind() != port.TriggerOneShot || one.Trigger.OneShot.IsZero() {
		t.Fatalf("one-shot trigger not parsed: %+v", one.Trigger)
	}
	if one.Misfire != port.MisfireSkip {
		t.Fatalf("skip misfire: %+v", one.Misfire)
	}
}

// foldOperatorSchedules skips a bad declaration (an unparseable one-shot time) and
// keeps the good one — fail-soft per declaration.
func TestFoldOperatorSchedulesBadDeclSkipped(t *testing.T) {
	const yamlCfg = `
schedules:
  - name: good
    cron: "0 9 * * *"
    prompt: "ok"
    mode: plan
  - name: bad
    oneShot: "not-a-time"
    prompt: "bad"
    mutating: true
    mode: default
`
	path := filepath.Join(t.TempDir(), "schedules.yaml")
	if err := os.WriteFile(path, []byte(yamlCfg), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	cfg := foldOperatorSchedules(Config{permResolver: res})
	if len(cfg.DeclaredSchedules) != 1 || cfg.DeclaredSchedules[0].Name != "good" {
		t.Fatalf("expected only the good declaration to survive; got %+v", cfg.DeclaredSchedules)
	}
}

// foldOperatorSchedules warns (but does not drop) a singleton:false declaration —
// the create-seam still coerces it to true (M1, opt-out not yet supported), so the
// YAML path must be honest about the override instead of silently accepting it.
func TestFoldOperatorSchedulesSingletonFalseWarns(t *testing.T) {
	const yamlCfg = `
schedules:
  - name: overlap-ok
    cron: "0 9 * * *"
    prompt: "ok"
    mode: plan
    singleton: false
`
	path := filepath.Join(t.TempDir(), "schedules.yaml")
	if err := os.WriteFile(path, []byte(yamlCfg), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	rec := &recordingDiag{}
	cfg := foldOperatorSchedules(Config{permResolver: res, Diagnostics: rec})

	if len(cfg.DeclaredSchedules) != 1 || cfg.DeclaredSchedules[0].Name != "overlap-ok" {
		t.Fatalf("expected the declaration to still fold through; got %+v", cfg.DeclaredSchedules)
	}
	if !rec.has("singleton:false is not yet supported") {
		t.Fatalf("expected a singleton:false WARN; got %v", rec.messages())
	}
}
