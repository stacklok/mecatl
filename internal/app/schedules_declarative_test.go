package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
)

// TestSchedulesDeclarativeReconcile is the Phase 2b end-to-end gate (issue #233):
// the DECLARATIVE → STORE path through the real app.Build. An operator-tier
// settings.yaml `schedules:` block is loaded by the permconfig resolver,
// foldOperatorSchedules parses it into cfg.DeclaredSchedules, and
// reconcileSchedules (called by Build after the scheduler starts) upserts the
// schedule into the durable ScheduleStore. The test then verifies via
// ListSchedules that the schedule exists with the correct spec — proving the
// whole declarative arc end-to-end, not just unit tests of the individual folds.
//
// Fully offline: mockllm + jsonlstore (via StoreDir), no network, no API key.
// The cron expression "0 9 1 1 *" (next Jan 1 09:00) is never due during the
// test, so the tick loop does not fire the schedule — this test exercises only
// the reconcile path, not the fire path (covered by scheduler_fire_test.go).
func TestSchedulesDeclarativeReconcile(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	// An operator-tier (CLI explicit) settings.yaml declaring a cron schedule.
	// Written to a temp file the resolver reads via os.ReadFile (OSEnv).
	var schedulesYAML = `
schedules:
  - name: declarative-nightly
    cron: "0 9 1 1 *"
    timezone: "UTC"
    prompt: "Summarize today's commits."
    workspace: "` + workspace + `"
    mode: plan
    maxTurns: 15
    maxToolCalls: 30
    singleton: true
`
	settingsPath := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settingsPath, []byte(schedulesYAML), 0o600); err != nil {
		t.Fatalf("write settings.yaml: %v", err)
	}

	cfg := Config{
		Workspace:             workspace,
		NoSoul:                true,
		NoUserModel:           true,
		StoreDir:              storeDir,
		SchedulerEnabled:      true,
		SchedulerTickInterval: 50 * time.Millisecond,
		// Operator-tier explicit settings file (NOT conventional project discovery,
		// so no project file is read — the operator-tier-only discipline).
		PermissionConfigs:   []string{settingsPath},
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("unused — the schedule is not due"))
		},
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	if !built.Service.HasScheduler() {
		t.Fatal("Service has no scheduler after Build with SchedulerEnabled")
	}

	// (1) The declared schedule was reconciled into the store.
	schedules, err := built.Service.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	var got *port.Schedule
	for i := range schedules {
		if schedules[i].Spec.Name == "declarative-nightly" {
			got = &schedules[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("declared schedule not reconciled into store; schedules=%+v", schedules)
	}

	// (2) The spec matches the declaration.
	if got.Spec.Prompt != "Summarize today's commits." {
		t.Errorf("Prompt = %q, want the declared prompt", got.Spec.Prompt)
	}
	if got.Spec.Workspace != workspace {
		t.Errorf("Workspace = %q, want %q", got.Spec.Workspace, workspace)
	}
	if got.Spec.Mode != "plan" {
		t.Errorf("Mode = %q, want plan", got.Spec.Mode)
	}
	if got.Spec.Trigger.Cron != "0 9 1 1 *" {
		t.Errorf("Trigger.Cron = %q, want \"0 9 1 1 *\"", got.Spec.Trigger.Cron)
	}
	if got.Spec.Timezone != "UTC" {
		t.Errorf("Timezone = %q, want UTC", got.Spec.Timezone)
	}
	if got.Spec.Limits.MaxTurns != 15 || got.Spec.Limits.MaxToolCalls != 30 {
		t.Errorf("Limits = %+v, want MaxTurns=15 MaxToolCalls=30", got.Spec.Limits)
	}
	// The create-seam defaults Singleton to true; the declaration set it true.
	if !got.Spec.Singleton {
		t.Error("Singleton = false, want true")
	}
	// (3) The schedule is enabled with a computed NextFireAt.
	if !got.State.Enabled {
		t.Error("Enabled = false, want true (reconciled schedules start enabled)")
	}
	if got.State.NextFireAt.IsZero() {
		t.Error("NextFireAt = zero, want a computed next fire instant")
	}
}

// TestSchedulesDeclarativeIdempotentReconcile proves the no-delete + idempotent
// upsert contract: re-running reconcileSchedules against a store that already
// holds the schedule is a no-op (no churn), and a changed declaration updates
// the spec. It exercises reconcileSchedules directly against a real *server.Service
// (built once) plus foldOperatorSchedules — the declarative→store path without
// the full Build, since the idempotency is a reconcile-level invariant.
func TestSchedulesDeclarativeIdempotentReconcile(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	// First Build seeds the store with the declared schedule.
	var v1YAML = `
schedules:
  - name: idempotent-review
    cron: "0 9 1 1 *"
    prompt: "v1 prompt"
    workspace: "` + workspace + `"
    mode: plan
`
	v1Path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(v1Path, []byte(v1YAML), 0o600); err != nil {
		t.Fatalf("write v1 settings: %v", err)
	}
	cfg := Config{
		Workspace:             workspace,
		NoSoul:                true,
		NoUserModel:           true,
		StoreDir:              storeDir,
		SchedulerEnabled:      true,
		SchedulerTickInterval: 50 * time.Millisecond,
		PermissionConfigs:     []string{v1Path},
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("unused"))
		},
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build v1: %v", err)
	}
	defer built.Close()

	// Reconcile the SAME declarations again (simulate a restart). The schedule
	// is unchanged → no churn. reconcileSchedules reads cfg.DeclaredSchedules
	// (already folded by Build); re-fold to exercise the path explicitly.
	cfg2 := foldOperatorSchedules(cfg)
	reconcileSchedules(ctx, cfg2, built.Service)

	schedules, err := built.Service.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules after re-reconcile: %v", err)
	}
	var got *port.Schedule
	for i := range schedules {
		if schedules[i].Spec.Name == "idempotent-review" {
			got = &schedules[i]
			break
		}
	}
	if got == nil {
		t.Fatal("schedule missing after idempotent re-reconcile")
	}
	if got.Spec.Prompt != "v1 prompt" {
		t.Errorf("Prompt = %q, want unchanged \"v1 prompt\"", got.Spec.Prompt)
	}
	// Exactly one schedule (no duplicate created by the re-reconcile).
	if len(schedules) != 1 {
		t.Errorf("expected exactly 1 schedule after re-reconcile; got %d", len(schedules))
	}

	// Reconcile a CHANGED declaration (prompt updated) → UpdateSchedule fires.
	var v2YAML = `
schedules:
  - name: idempotent-review
    cron: "0 9 1 1 *"
    prompt: "v2 prompt"
    workspace: "` + workspace + `"
    mode: plan
`
	v2Path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(v2Path, []byte(v2YAML), 0o600); err != nil {
		t.Fatalf("write v2 settings: %v", err)
	}
	cfg3 := Config{
		Workspace:             workspace,
		NoSoul:                true,
		NoUserModel:           true,
		StoreDir:              storeDir,
		SchedulerEnabled:      true,
		SchedulerTickInterval: 50 * time.Millisecond,
		PermissionConfigs:     []string{v2Path},
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("unused"))
		},
	}
	// Re-fold with the new resolver (permconfig.New reads the v2 file).
	cfg3.permResolver = buildPermResolver(cfg3)
	cfg3 = foldOperatorSchedules(cfg3)
	reconcileSchedules(ctx, cfg3, built.Service)

	schedules, err = built.Service.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules after update reconcile: %v", err)
	}
	for i := range schedules {
		if schedules[i].Spec.Name == "idempotent-review" {
			got = &schedules[i]
			break
		}
	}
	if got == nil {
		t.Fatal("schedule missing after update reconcile")
	}
	if got.Spec.Prompt != "v2 prompt" {
		t.Errorf("Prompt = %q, want updated \"v2 prompt\"", got.Spec.Prompt)
	}
	// Still exactly one schedule (update, not create).
	if len(schedules) != 1 {
		t.Errorf("expected exactly 1 schedule after update; got %d", len(schedules))
	}
}

// TestSchedulesProjectTierIgnored proves the operator-tier-only discipline at the
// composition level: a Build with conventional discovery ON over a workspace whose
// PROJECT-tier `.mecatl/settings.yaml` carries a `schedules:` block does NOT
// reconcile any schedule from it (the project block never reaches
// OperatorSchedules, so foldOperatorSchedules produces no DeclaredSchedules and
// reconcileSchedules is a no-op). The WARN + nil-OperatorSchedules invariant is
// pinned in internal/adapter/permconfig/schedules_test.go; this test pins the
// composition-level consequence — no schedule is created from a project tier.
func TestSchedulesProjectTierIgnored(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	// Seed a PROJECT-tier .mecatl/settings.yaml with a schedules: block.
	const projectSchedulesYAML = `
schedules:
  - name: project-must-not-register
    cron: "0 9 * * *"
    prompt: "a project repo must not be able to register this"
`
	mecatlDir := filepath.Join(workspace, ".mecatl")
	if err := os.MkdirAll(mecatlDir, 0o755); err != nil {
		t.Fatalf("mkdir .mecatl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mecatlDir, "settings.yaml"), []byte(projectSchedulesYAML), 0o600); err != nil {
		t.Fatalf("write project settings.yaml: %v", err)
	}

	// Point XDG/HOME at empty temp dirs so no user-global settings.yaml interferes.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	cfg := Config{
		Workspace:             workspace,
		NoSoul:                true,
		NoUserModel:           true,
		StoreDir:              storeDir,
		SchedulerEnabled:      true,
		SchedulerTickInterval: 50 * time.Millisecond,
		// Conventional discovery ON + TrustProject so the project file WOULD be
		// read on a tool eval — but NO operator-tier explicit file, so
		// OperatorSchedules() is nil regardless.
		PermissionsConventional: true,
		TrustProject:            true,
		envDetector:             fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient:     offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("unused"))
		},
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	schedules, err := built.Service.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	for _, s := range schedules {
		if s.Spec.Name == "project-must-not-register" {
			t.Fatalf("a PROJECT-tier schedule must NOT be reconciled; found %+v", s)
		}
	}
}
