package ui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

type scenario8Maintenance struct {
	migrationPlan client.SessionMigrationPlan
	migrationJob  client.SessionMigrationJob
	cleanupPlan   client.CleanupPlan
	cleanupJob    client.CleanupJob
	planErr       error
	calls         []string
}

func (f *scenario8Maintenance) PlanSessionMigration(context.Context) (client.SessionMigrationPlan, error) {
	f.calls = append(f.calls, "plan-migration")
	return f.migrationPlan, f.planErr
}
func (f *scenario8Maintenance) ApplySessionMigration(_ context.Context, id string, _ int32) (client.SessionMigrationJob, error) {
	f.calls = append(f.calls, "apply-migration:"+id)
	return f.migrationJob, nil
}
func (f *scenario8Maintenance) ResumeSessionMigration(_ context.Context, id string, _ int32) (client.SessionMigrationJob, error) {
	f.calls = append(f.calls, "resume-migration:"+id)
	return f.migrationJob, nil
}
func (f *scenario8Maintenance) CancelSessionMigration(_ context.Context, id string) (client.SessionMigrationJob, error) {
	f.calls = append(f.calls, "cancel-migration:"+id)
	job := f.migrationJob
	job.State = "cancelled"
	return job, nil
}
func (f *scenario8Maintenance) GetSessionMigrationJob(_ context.Context, id string) (client.SessionMigrationJob, error) {
	f.calls = append(f.calls, "get-migration:"+id)
	return f.migrationJob, nil
}
func (f *scenario8Maintenance) PlanSessionCleanup(_ context.Context, scope client.CleanupScope) (client.CleanupPlan, error) {
	f.calls = append(f.calls, "plan-cleanup:"+strings.Join(scope.Kinds, ","))
	return f.cleanupPlan, f.planErr
}
func (f *scenario8Maintenance) ApplySessionCleanup(_ context.Context, token string) (client.CleanupJob, error) {
	f.calls = append(f.calls, "apply-cleanup:"+token)
	return f.cleanupJob, nil
}
func (f *scenario8Maintenance) CancelSessionCleanup(_ context.Context, id string) (client.CleanupJob, error) {
	f.calls = append(f.calls, "cancel-cleanup:"+id)
	job := f.cleanupJob
	job.State = "cancelled"
	return job, nil
}
func (f *scenario8Maintenance) GetSessionCleanupJob(_ context.Context, id string) (client.CleanupJob, error) {
	f.calls = append(f.calls, "get-cleanup:"+id)
	return f.cleanupJob, nil
}

func maintenanceScenarioModel(fake *scenario8Maintenance) Model {
	m := newTestModelFromDeps(Deps{
		Migration: fake, Cleanup: fake, Theme: testTheme(), Ctx: context.Background(), NoAltScreen: true,
	})
	m.caps = client.Capabilities{StorageHealth: true, StorageMigration: true, StorageCleanup: true}
	setActiveSessions(&m, newSessionsPanelState())
	ensureActiveSessions(&m).loading = false
	ensureActiveSessions(&m).loadState = sessionsComplete
	ensureActiveSessions(&m).tab = tabStorageHealth
	return m
}

func TestMaintenanceMenuRequiresAdvertisedCapabilitiesThroughRoutes(t *testing.T) {
	fake := &scenario8Maintenance{}
	m := maintenanceScenarioModel(fake)
	ensureActiveSessions(&m).deps.caps.StorageMigration = false
	ensureActiveSessions(&m).deps.caps.StorageCleanup = false

	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	m = mm.(Model)
	if cmd != nil || ensureActiveSessions(&m).actionLoading {
		t.Fatal("Model.Update started migration without the advertised capability")
	}
	mm, cmd, handled := m.onOverlayKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = mm.(Model)
	if !handled || cmd != nil || ensureActiveSessions(&m).actionLoading || len(fake.calls) != 0 {
		t.Fatalf("overlay route started cleanup without capability: handled=%v cmd=%v calls=%v", handled, cmd != nil, fake.calls)
	}
}

func TestSessionStorageContinuity_Scenario8_OptimizeStorageFlow(t *testing.T) {
	fake := &scenario8Maintenance{
		migrationPlan: client.SessionMigrationPlan{ID: "plan-1", Available: true, V1Families: 7, V2Families: 9, InvalidFamilies: 2, SkippedFamilies: 3, CurrentBytes: 8192, ReclaimableBytes: 4096, TemporaryBytes: 2048},
		migrationJob:  client.SessionMigrationJob{ID: "migration-job", State: "paused", V1Families: 7, Processed: 4, Migrated: 2, SkippedFamilies: 1, Failed: 1, Errors: []client.SessionMigrationItemError{{ItemHandle: "item-7", ReasonCode: "write_failed", Message: "sanitized failure"}}},
	}
	m := maintenanceScenarioModel(fake)
	mm, cmd, handled := m.onOverlayKey(tea.KeyPressMsg{Code: 'o', Text: "o"})
	m = mm.(Model)
	if !handled || cmd == nil {
		t.Fatal("optimize affordance did not start a dry-run")
	}
	m = applyAll(m, cmd())
	dryRun := stripANSIstr(m.View().Content)
	for _, want := range []string{"Preview storage optimization", "Sessions are preserved", "Legacy format: 7", "Current format: 9", "Invalid: 2", "Skipped: 3", "Recoverable: 4.1 KB", "Temporary space needed: 2 KB"} {
		if !strings.Contains(dryRun, want) {
			t.Fatalf("optimize dry-run missing %q:\n%s", want, dryRun)
		}
	}
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = applyAll(mm.(Model), cmd())
	progress := stripANSIstr(m.View().Content)
	for _, want := range []string{"migration-job", "paused", "Processed: 4/7", "Updated: 2", "sanitized failure", "r: resume", "c: cancel"} {
		if !strings.Contains(progress, want) {
			t.Fatalf("optimize progress missing %q:\n%s", want, progress)
		}
	}
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	_ = applyAll(mm.(Model), cmd())
	if !slices.Contains(fake.calls, "resume-migration:migration-job") {
		t.Fatalf("resume did not use durable job: %v", fake.calls)
	}

	unavailable := &scenario8Maintenance{migrationPlan: client.SessionMigrationPlan{Available: false, UnavailableReason: "backend_unsupported"}}
	m = maintenanceScenarioModel(unavailable)
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: 'o', Text: "o"})
	m = applyAll(mm.(Model), cmd())
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "unavailable on this backend") || strings.Contains(got, "Reclaimable: 0 B") {
		t.Fatalf("unsupported backend was presented as zero impact:\n%s", got)
	}

	// Plant a raw backend/path/content violation at the client seam. The UI must
	// render only its stable generic failure, never the producer string.
	raw := &scenario8Maintenance{planErr: errors.New("/private/secret/session.jsonl: transcript body")}
	m = maintenanceScenarioModel(raw)
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: 'o', Text: "o"})
	m = applyAll(mm.(Model), cmd())
	if got := stripANSIstr(m.View().Content); strings.Contains(got, "/private/") || strings.Contains(got, "transcript body") || !strings.Contains(got, "maintenance request failed") {
		t.Fatalf("raw maintenance error crossed the UI boundary:\n%s", got)
	}
}

func TestSessionStorageContinuity_Scenario8_CleanupFlow(t *testing.T) {
	fake := &scenario8Maintenance{
		cleanupPlan: client.CleanupPlan{Available: true, ConfirmationToken: "bulk-token", PlannedJobID: "cleanup-job", EstimatedBytes: 3072,
			EligibleCounts: client.CleanupCounts{Total: 6, ByKind: map[string]int{"main": 3, "subagent": 2, "scheduled": 1}},
			Protected:      client.CleanupCounts{Total: 8, ByKind: map[string]int{"unknown": 2}, ByState: map[string]int{"running": 3, "awaiting": 1}, ByReason: map[string]int{"live": 2}}},
		cleanupJob: client.CleanupJob{ID: "cleanup-job", State: "partial", Processed: 6, Deleted: 3, Skipped: 2, Stale: 1, Failed: 1, Errors: []client.CleanupItemError{{ItemHandle: "item-2", ReasonCode: "changed", Message: "sanitized skip"}}},
	}
	m := maintenanceScenarioModel(fake)
	mm, cmd, handled := m.onOverlayKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = mm.(Model)
	if !handled || cmd == nil {
		t.Fatal("cleanup affordance did not start a dry-run")
	}
	m = applyAll(m, cmd())
	review := stripANSIstr(m.View().Content)
	for _, want := range []string{"Preview session cleanup", "DESTRUCTIVE", "Chats: 3", "Child runs: 2", "Scheduled runs: 1", "Unknown: 2", "Live: 2", "Awaiting approval: 1", "Protected: 8", "Type CLEAN UP", "cannot be undone"} {
		if !strings.Contains(review, want) {
			t.Fatalf("cleanup review missing %q:\n%s", want, review)
		}
	}
	if !slices.Contains(fake.calls, "plan-cleanup:main,subagent,parallel_branch,team_member,scheduled") {
		t.Fatalf("unknown entered default cleanup scope: %v", fake.calls)
	}
	// Single-row delete consent (y/enter) must not authorize this bulk operation.
	mm, _, _ = m.onOverlayKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = mm.(Model)
	if slices.ContainsFunc(fake.calls, func(s string) bool { return strings.HasPrefix(s, "apply-cleanup:") }) {
		t.Fatal("single-row delete consent authorized bulk cleanup")
	}
	for _, r := range "CLEAN UP" {
		mm, _, _ = m.onOverlayKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
	}
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = applyAll(mm.(Model), cmd())
	result := stripANSIstr(m.View().Content)
	for _, want := range []string{"partial", "Deleted: 3", "Skipped: 2", "Changed: 1", "Failed: 1", "sanitized skip", "r: new preview"} {
		if !strings.Contains(result, want) {
			t.Fatalf("cleanup result missing %q:\n%s", want, result)
		}
	}
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	_ = applyAll(mm.(Model), cmd())
	count := 0
	for _, call := range fake.calls {
		if call == "plan-cleanup:main,subagent,parallel_branch,team_member,scheduled" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("partial cleanup retry did not create a fresh dry run: %v", fake.calls)
	}
}

func TestSessionStorageContinuity_Scenario8_CancelledCleanupKeepsCompletedDeletions(t *testing.T) {
	job := client.CleanupJob{ID: "cleanup-job", State: teamStopReasonCancelled, Processed: 6, Deleted: 3}
	out := stripANSIstr(renderCleanupJob(testTheme(), job, defaultHelpKeys()))
	for _, want := range []string{"Job: cleanup-job  Status: cancelled", "Deleted: 3", "Cancellation stops remaining items; completed deletions cannot be undone."} {
		if !strings.Contains(out, want) {
			t.Errorf("cancelled cleanup result missing %q:\n%s", want, out)
		}
	}
}

func TestSessionStorageContinuity_Scenario8_MaintenanceProgressReattach(t *testing.T) {
	fake := &scenario8Maintenance{migrationJob: client.SessionMigrationJob{ID: "durable-job", State: "running", V1Families: 5, Processed: 2, Migrated: 2}}
	m := maintenanceScenarioModel(fake)
	m.maintenanceMigrationJobID = "durable-job"
	mm, _, _ := m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	mm, _, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	m.phase = phaseIdle
	m.deps.Sessions = &fakeSessionLister{}
	m.deps.Transcript = &fakeSessionTranscriptLoader{}
	mm, cmd := m.openSessions()
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("reopen did not request durable progress")
	}
	m = feedCmd(t, m, cmd)
	if !slices.Contains(fake.calls, "get-migration:durable-job") {
		t.Fatalf("reopen did not reattach: %v", fake.calls)
	}
	ensureActiveSessions(&m).tab = tabStorageHealth
	view := stripANSIstr(m.View().Content)
	if !strings.Contains(view, "durable-job") || !strings.Contains(view, "Processed: 2/5") {
		t.Fatalf("reattached progress absent:\n%s", view)
	}
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = applyAll(mm.(Model), cmd())
	cancelled := stripANSIstr(m.View().Content)
	if !strings.Contains(cancelled, "Cancellation stops remaining items") || strings.Contains(strings.ToLower(cancelled), "roll back") {
		t.Fatalf("cancellation semantics dishonest:\n%s", cancelled)
	}
}
