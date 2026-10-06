package ui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

type scenario8Maintenance struct {
	cleanupPlan client.CleanupPlan
	cleanupJob  client.CleanupJob
	planErr     error
	calls       []string
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
		Cleanup: fake, Theme: testTheme(), Ctx: context.Background(), NoAltScreen: true,
	})
	m.caps = client.Capabilities{StorageHealth: true, StorageCleanup: true}
	m.width, m.height = 100, 40
	setActiveSessions(&m, newSessionsPanelState())
	ensureActiveSessions(&m).loading = false
	ensureActiveSessions(&m).loadState = sessionsComplete
	ensureActiveSessions(&m).tab = tabStorageHealth
	return m
}

func TestMaintenanceMenuRequiresAdvertisedCapabilitiesThroughRoutes(t *testing.T) {
	fake := &scenario8Maintenance{}
	m := maintenanceScenarioModel(fake)
	ensureActiveSessions(&m).deps.caps.StorageCleanup = false

	mm, cmd, handled := m.onOverlayKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = mm.(Model)
	if !handled || cmd != nil || ensureActiveSessions(&m).actionLoading || len(fake.calls) != 0 {
		t.Fatalf("overlay route started cleanup without capability: handled=%v cmd=%v calls=%v", handled, cmd != nil, fake.calls)
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
