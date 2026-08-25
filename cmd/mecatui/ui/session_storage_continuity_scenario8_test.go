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

type scenario8Adopter struct {
	preflight client.AdoptionPreflight
	preErr    error
	adopt     client.AdoptionResult
	adoptErr  error
	calls     []string
}

func (f *scenario8Adopter) PreflightSessionAdoption(_ context.Context, id string, bindings client.AdoptionBindings) (client.AdoptionPreflight, error) {
	f.calls = append(f.calls, "preflight:"+id+":"+bindings.ProviderID+":"+bindings.ModelID)
	return f.preflight, f.preErr
}

func (f *scenario8Adopter) AdoptSession(_ context.Context, id, _ string, bindings client.AdoptionBindings) (client.AdoptionResult, error) {
	f.calls = append(f.calls, "adopt:"+id+":"+bindings.ProviderID+":"+bindings.ModelID)
	return f.adopt, f.adoptErr
}

func scenario8Model(adopter client.SessionAdopter, row client.SessionListItem) Model {
	m := New(Deps{
		Adoption: adopter, Transcript: &fakeSessionTranscriptLoader{}, Theme: testTheme(),
		Workspace: "/target", Ctx: context.Background(), NoAltScreen: true,
	})
	m.activeWorkspace = "/target"
	m.effectiveModel = client.ResolvedModel{ProviderID: "provider-b", ModelID: "model-b"}
	setActiveSessions(&m, sessionsState{view: sessionsPanel, tab: tabOtherRuns, loadState: sessionsComplete, sessions: []client.SessionListItem{row}})
	ensureActiveSessions(&m).syncFilter()
	return m
}

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
	m := New(Deps{
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
	for _, want := range []string{"Optimize storage", "Sessions are preserved", "v1: 7", "v2: 9", "Invalid: 2", "Skipped: 3", "Reclaimable: 4.1 KB", "Temporary space required: 2 KB"} {
		if !strings.Contains(dryRun, want) {
			t.Fatalf("optimize dry-run missing %q:\n%s", want, dryRun)
		}
	}
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = applyAll(mm.(Model), cmd())
	progress := stripANSIstr(m.View().Content)
	for _, want := range []string{"migration-job", "paused", "Processed: 4/7", "Migrated: 2", "sanitized failure", "r: resume", "c: cancel"} {
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
	for _, want := range []string{"Clean up sessions", "DESTRUCTIVE", "Main: 3", "Child: 2", "Scheduled: 1", "Unknown: 2 protected", "Live: 2", "Awaiting: 1", "Protected: 8", "type CLEAN UP"} {
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
	for _, want := range []string{"partial", "Deleted: 3", "Skipped: 2", "Stale: 1", "Failed: 1", "sanitized skip", "r: new dry run"} {
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
	if !strings.Contains(cancelled, "Cancellation stops future items") || strings.Contains(strings.ToLower(cancelled), "roll back") {
		t.Fatalf("cancellation semantics dishonest:\n%s", cancelled)
	}
}

func TestSessionStorageContinuity_Scenario8_AdoptAffordanceTruth(t *testing.T) {
	legacy := client.SessionListItem{ID: "ordinary-opaque-id", Title: "Old investigation", Kind: client.SessionKindUnknown, Capabilities: client.SessionInventoryCapabilities{Inspect: true}}
	m := scenario8Model(&scenario8Adopter{}, legacy)

	before := stripANSIstr(m.View().Content)
	if !strings.Contains(before, "Legacy session — inspect only") {
		t.Fatalf("legacy label missing:\n%s", before)
	}
	if strings.Contains(before, "a: adopt as chat") {
		t.Fatalf("adopt action shown before server preflight:\n%s", before)
	}
	unresolved := scenario8Model(&scenario8Adopter{}, legacy)
	unresolved.effectiveModel = client.ResolvedModel{}
	unresolved.deps.InitialModel = client.ModelSelection{}
	if unresolved.adoptionPreflightCmd(legacy) != nil || !strings.Contains(stripANSIstr(unresolved.View().Content), "select an explicit workspace, environment, provider, and model") {
		t.Fatal("unresolved target silently fell back instead of requiring explicit selection")
	}

	ineligible := client.AdoptionPreflight{Eligible: false, Reason: client.CapabilityReasonProtectedProvenance}
	m = applyAll(m, client.SessionAdoptionPreflightMsg{SourceID: legacy.ID, Preflight: ineligible})
	disabled := stripANSIstr(m.View().Content)
	if strings.Contains(disabled, "a: adopt as chat") || !strings.Contains(disabled, "reserved legacy provenance cannot be adopted") {
		t.Fatalf("disabled affordance is not server-authoritative:\n%s", disabled)
	}

	eligible := client.AdoptionPreflight{Eligible: true, Bindings: client.AdoptionBindings{Workspace: "/target", EnvironmentKind: "local", EnvironmentID: "/target", ProviderID: "provider-b", ModelID: "model-b"}}
	m = applyAll(m, client.SessionAdoptionPreflightMsg{SourceID: legacy.ID, Preflight: eligible})
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "a: adopt as chat") {
		t.Fatalf("eligible affordance missing:\n%s", got)
	}

	// Opaque ID spelling never grants eligibility: changing selection invalidates
	// the correlated preflight even when the new ID resembles a main chat.
	ensureActiveSessions(&m).sessions = append(ensureActiveSessions(&m).sessions, client.SessionListItem{ID: "session-main-looking", Kind: client.SessionKindUnknown})
	ensureActiveSessions(&m).syncFilter()
	ensureActiveSessions(&m).cursor = 1
	ensureActiveSessions(&m).requestAdoptionPreflight()
	if got := stripANSIstr(m.View().Content); strings.Contains(got, "a: adopt as chat") {
		t.Fatalf("stale preflight leaked across rows:\n%s", got)
	}
}

func TestSessionStorageContinuity_Scenario8_AdoptionTUIFlow(t *testing.T) {
	legacy := client.SessionListItem{ID: "legacy-source", Title: "Old investigation", Kind: client.SessionKindUnknown, Capabilities: client.SessionInventoryCapabilities{Inspect: true}}
	binding := client.AdoptionBindings{Workspace: "/target", EnvironmentKind: "local", EnvironmentID: "/target", ProviderID: "provider-b", ModelID: "model-b"}
	adopter := &scenario8Adopter{preflight: client.AdoptionPreflight{Eligible: true, Bindings: binding}}
	m := scenario8Model(adopter, legacy)
	preflightCmd := m.adoptionPreflightCmd(legacy)
	if preflightCmd == nil {
		t.Fatal("complete explicit binding did not request server preflight")
	}
	m = applyAll(m, preflightCmd())

	mm, _, handled := m.onOverlayKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = mm.(Model)
	if !handled {
		t.Fatal("eligible adoption key was not handled")
	}
	review := stripANSIstr(m.View().Content)
	for _, want := range []string{"Adopt as new chat", "legacy-source", "Old investigation", "/target", "local", "provider-b", "model-b", "Future tool writes affect the target workspace", "source remains inspect-only"} {
		if !strings.Contains(review, want) {
			t.Fatalf("review missing %q:\n%s", want, review)
		}
	}

	// Cancel returns to the same progressive inventory and preserves selection.
	mm, _, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if ensureActiveSessions(&m).view != sessionsPanel || ensureActiveSessions(&m).cursor != 0 || len(ensureActiveSessions(&m).sessions) != 1 {
		t.Fatalf("cancel destabilized panel: %+v", ensureActiveSessions(&m))
	}

	// A stale preflight is revalidated by AdoptSession. Its error leaves the
	// review stable and never mutates/deletes the source row.
	preflightCmd = m.adoptionPreflightCmd(legacy)
	m = applyAll(m, preflightCmd())
	mm, _, _ = m.onOverlayKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = mm.(Model)
	adopter.adoptErr = errors.New("failed precondition: source changed")
	mm, adoptCmd, _ := m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if adoptCmd == nil {
		t.Fatal("confirmed review did not call adoption seam")
	}
	m = applyAll(m, adoptCmd())
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "source changed") || !strings.Contains(got, "Adopt as new chat") {
		t.Fatalf("stale-preflight error did not leave stable review:\n%s", got)
	}
	if len(ensureActiveSessions(&m).sessions) != 1 || ensureActiveSessions(&m).sessions[0].ID != legacy.ID {
		t.Fatalf("error changed source inventory: %+v", ensureActiveSessions(&m).sessions)
	}

	// Success trusts neither the mutation response nor local transcript: it opens
	// only after authoritative target snapshot + transcript refetch.
	adopted := client.SessionListItem{ID: "new-main", Title: "Old investigation", Kind: client.SessionKindMain, Workspace: "/target", Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Inspect: true}}
	transcript := client.SessionTranscript{SessionID: adopted.ID, Complete: true, Kind: client.SessionKindMain, Messages: []client.ConversationMessage{{Role: "user", Text: "original question"}}}
	adopter.adoptErr = nil
	adopter.adopt = client.AdoptionResult{SessionID: adopted.ID, SourceSessionID: legacy.ID}
	conv := newSessionsConv()
	conv.getSessionTitle = adopted.Title
	conv.resolvedModel = client.ResolvedModel{ProviderID: "provider-b", ModelID: "model-b"}
	m.deps.Session = conv
	m.deps.Transcript = &fakeSessionTranscriptLoader{transcript: transcript}
	ensureActiveSessions(&m).forker = conv
	ensureActiveSessions(&m).transcripter = m.deps.Transcript
	mm, adoptCmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	m = applyAll(m, adoptCmd())
	if m.sessionID != adopted.ID || m.phase != phaseIdle || !m.ta.Focused() || m.modal != nil {
		t.Fatalf("success did not open writable authoritative target: id=%q phase=%v focused=%v modal=%v", m.sessionID, m.phase, m.ta.Focused(), m.modal)
	}
	if m.sessionID == legacy.ID {
		t.Fatalf("success rebound the inspect-only source instead of the new target")
	}
}
