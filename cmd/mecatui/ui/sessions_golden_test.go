package ui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

var errGolden = errors.New("could not reach session store")

func newSessionsGoldenModel(t *testing.T, sessions []client.SessionListItem) Model {
	t.Helper()
	conv := newSessionsConv()
	loader := &fakeSessionTranscriptLoader{}
	m := New(Deps{
		Session: conv, Conv: conv, Sessions: &fakeSessionLister{sessions: sessions}, Transcript: loader,
		Theme: theme.New("aztec", theme.AztecPalette()), Workspace: "/workspace",
		Mode: "default", Model: "mock-model", Ctx: context.Background(), NoAltScreen: true,
	})
	return applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 40}, client.SessionReadyMsg{SessionID: "sess-test-0001"})
}

func openAndLoad(t *testing.T, m Model, sessions []client.SessionListItem) Model {
	t.Helper()
	mm, _ := m.runSessions()
	return applyAll(mm.(Model), client.SessionsListedMsg{Sessions: sessions})
}

func TestSessionsPickerGolden(t *testing.T) {
	rows := sampleSessions()
	m := openAndLoad(t, newSessionsGoldenModel(t, rows), rows)
	compareGolden(t, "sessions_picker.golden", stripANSI([]byte(m.View().Content)))
}

func TestSessionsPickerEmptyGolden(t *testing.T) {
	m := openAndLoad(t, newSessionsGoldenModel(t, nil), nil)
	compareGolden(t, "sessions_picker_empty.golden", stripANSI([]byte(m.View().Content)))
}

func TestSessionsPickerErrorGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, nil)
	mm, _ := m.runSessions()
	m = applyAll(mm.(Model), client.SessionsListedMsg{Err: errGolden})
	compareGolden(t, "sessions_picker_error.golden", stripANSI([]byte(m.View().Content)))
}

func TestSessionsAdoptionGoldens(t *testing.T) {
	legacy := client.SessionListItem{ID: "legacy-source", Title: "Old investigation", Kind: client.SessionKindUnknown, Capabilities: client.SessionInventoryCapabilities{Inspect: true}}
	adopter := &scenario8Adopter{preflight: client.AdoptionPreflight{Eligible: true, Bindings: client.AdoptionBindings{Workspace: "/workspace", EnvironmentKind: "local", EnvironmentID: "/workspace", ProviderID: "openai", ModelID: "gpt-5"}}}
	m := newSessionsGoldenModel(t, []client.SessionListItem{legacy})
	m.deps.Adoption = adopter
	m.activeWorkspace = "/workspace"
	m.effectiveModel = client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"}
	m = openAndLoad(t, m, []client.SessionListItem{legacy})
	ensureActiveSessions(&m).tab = tabOtherRuns
	ensureActiveSessions(&m).syncFilter()
	m = applyAll(m, client.SessionAdoptionPreflightMsg{SourceID: legacy.ID, Preflight: adopter.preflight})
	compareGolden(t, "sessions_legacy_adoption.golden", stripANSI([]byte(m.View().Content)))

	mm, _, _ := m.onOverlayKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = mm.(Model)
	compareGolden(t, "sessions_adoption_review.golden", stripANSI([]byte(m.View().Content)))
}

func TestSessionsMaintenanceGoldens(t *testing.T) {
	fake := &scenario8Maintenance{
		migrationPlan: client.SessionMigrationPlan{ID: "plan-1", Available: true, V1Families: 12, V2Families: 40, InvalidFamilies: 2, SkippedFamilies: 3, CurrentBytes: 12 << 20, ReclaimableBytes: 8 << 20, TemporaryBytes: 2 << 20},
		migrationJob:  client.SessionMigrationJob{ID: "job-optimize", State: "paused", V1Families: 12, Processed: 7, Migrated: 5, SkippedFamilies: 1, Failed: 1, Errors: []client.SessionMigrationItemError{{ItemHandle: "item-a4", ReasonCode: "changed", Message: "session changed during optimization"}}},
		cleanupPlan:   client.CleanupPlan{Available: true, ConfirmationToken: "token", EstimatedBytes: 6 << 20, EligibleCounts: client.CleanupCounts{Total: 9, ByKind: map[string]int{"main": 5, "subagent": 2, "scheduled": 2}}, Protected: client.CleanupCounts{Total: 7, ByKind: map[string]int{"unknown": 3}, ByState: map[string]int{"awaiting": 1}, ByReason: map[string]int{"live": 2}}},
	}
	m := maintenanceScenarioModel(fake)
	m.width, m.height = 100, 40

	m = applyAll(m, migrationPlanMsg{plan: fake.migrationPlan})
	compareGolden(t, "sessions_optimize_review.golden", stripANSI([]byte(m.View().Content)))
	m = applyAll(m, migrationJobMsg{job: fake.migrationJob})
	compareGolden(t, "sessions_optimize_progress.golden", stripANSI([]byte(m.View().Content)))
	m = applyAll(m, cleanupPlanMsg{plan: fake.cleanupPlan})
	compareGolden(t, "sessions_cleanup_review.golden", stripANSI([]byte(m.View().Content)))
}

func TestSessionsTranscriptLoadingGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, nil)
	setActiveSessions(&m, sessionsState{view: sessionsTranscript, loading: true, selected: client.SessionListItem{ID: "sched-1", Title: "Nightly checks"}, inspect: true})
	m.phase = phaseReplay
	compareGolden(t, "sessions_transcript_loading.golden", stripANSI([]byte(m.View().Content)))
}

func TestSessionsTranscriptErrorGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, nil)
	setActiveSessions(&m, sessionsState{view: sessionsTranscript, selected: client.SessionListItem{ID: "sched-1", Title: "Nightly checks"}, inspect: true, loadErr: errGolden})
	m.phase = phaseReplay
	compareGolden(t, "sessions_transcript_error.golden", stripANSI([]byte(m.View().Content)))
}

func TestSessionsTranscriptRenderedGolden(t *testing.T) {
	m := newSessionsGoldenModel(t, nil)
	setActiveSessions(&m, sessionsState{
		view: sessionsTranscript, selected: client.SessionListItem{ID: "sched-1", Title: "Nightly checks"}, inspect: true,
		transcript: conversationFromTranscript([]client.ConversationMessage{{Role: "user", Text: "run checks"}, {Role: "assistant", Text: "All checks passed."}}),
	})
	m.phase = phaseReplay
	m.refreshView()
	compareGolden(t, "sessions_transcript_rendered.golden", stripANSI([]byte(m.View().Content)))
}
