package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestResumableSessionStatusMetrics_Scenario3_StartupResumeRestoresStatus(t *testing.T) {
	resume := &client.ResumeSelection{
		Row: client.SessionListItem{ID: "resumed-main", Title: "Restored chat"},
		Snapshot: client.SessionSnapshot{
			ResolvedModel:    client.ResolvedModel{ContextWindow: 200_000},
			Usage:            client.Usage{InputTokens: 120_000, OutputTokens: 4_000, CacheReadTokens: 90_000},
			ContextOccupancy: &client.ContextOccupancy{InputTokens: 40_000, Estimated: true},
		},
	}
	m := New(Deps{Resume: resume, Theme: theme.New("aztec", theme.AztecPalette()), NoAltScreen: true})

	if m.usage != resume.Snapshot.Usage || m.contextTokens != 40_000 || !m.contextEstimated {
		t.Fatalf("startup resume did not adopt snapshot status: usage=%+v context=%d estimated=%t", m.usage, m.contextTokens, m.contextEstimated)
	}
	footer := stripANSIstr(m.fitFooter("connected", 160))
	for _, want := range []string{"~40K/200K", "20%", "↑120K", "↓4K", "cache 75%"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("footer = %q, want %q before a prompt", footer, want)
		}
	}

	m = applyAll(m, client.TurnEndMsg{Usage: client.Usage{InputTokens: 50_000}, Estimated: true})
	if footer := stripANSIstr(m.fitFooter("connected", 160)); !strings.Contains(footer, "~50K/200K") {
		t.Fatalf("live estimated footer = %q, want estimated marker", footer)
	}
}

func TestResumableSessionStatusMetrics_Scenario3_StartupResumeSubmitsSnapshotStatus(t *testing.T) {
	resume := &client.ResumeSelection{
		Row: client.SessionListItem{ID: "resumed-main", Title: "Restored chat"},
		Snapshot: client.SessionSnapshot{
			ResolvedModel:    client.ResolvedModel{ContextWindow: 200_000},
			Usage:            client.Usage{InputTokens: 120_000, OutputTokens: 4_000, CacheReadTokens: 90_000},
			ContextOccupancy: &client.ContextOccupancy{InputTokens: 40_000, Estimated: true},
		},
	}
	source := &statusSourceFake{changed: make(chan struct{})}
	m := New(Deps{Resume: resume, StatusSource: source, Theme: theme.New("aztec", theme.AztecPalette()), NoAltScreen: true})
	if source.inputCount() != 0 {
		t.Fatal("startup submitted status before adoption")
	}

	updated, _ := m.Update(startupResumeReadyMsg{})
	m = updated.(Model)
	if got := source.inputCount(); got != 1 {
		t.Fatalf("startup status submissions = %d, want 1", got)
	}
	input, ok := source.lastInput()
	if !ok || input.Usage.Input.Raw != 120_000 || input.Usage.Output.Raw != 4_000 || input.Usage.CacheRead.Raw != 90_000 ||
		input.Context.Used.Raw != 40_000 || !input.Context.Known || !input.Context.Estimated || input.Context.Window.Raw != 200_000 {
		t.Fatalf("startup status input = %#v, want restored snapshot occupancy and usage", input)
	}
	if m.phase != phaseIdle || !m.prompt.Focused() {
		t.Fatalf("status submitted before resumed chat became interactive: phase=%v focused=%t", m.phase, m.prompt.Focused())
	}
}

func TestResumableSessionStatusMetrics_Scenario3_SessionSwitchRestoresStatus(t *testing.T) {
	adopted := client.SessionSnapshot{
		ResolvedModel:    client.ResolvedModel{ProviderID: "provider-b", ModelID: "model-b", ContextWindow: 200_000},
		Usage:            client.Usage{InputTokens: 120_000, OutputTokens: 4_000, CacheReadTokens: 90_000},
		ContextOccupancy: &client.ContextOccupancy{InputTokens: 40_000, Estimated: true},
	}
	conv := newSessionsConv()
	conv.getSessionSnapshots = []client.SessionSnapshot{adopted}
	loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{SessionID: "next-main", Complete: true}}
	m := newSessionsModel(t, conv, &fakeSessionLister{}, loader)
	m.usage = client.Usage{InputTokens: 9}
	m.contextTokens = 8

	row := client.SessionListItem{ID: "next-main", Title: "next", Kind: client.SessionKindMain}
	mm, cmd, ok := m.loadSessionTranscript(row, false)
	if !ok || cmd == nil {
		t.Fatal("session continuation did not start an authoritative load")
	}
	m = mm.(Model)
	m = applyAll(m, cmd())
	if m.sessionID != row.ID || m.usage != adopted.Usage || m.contextTokens != 40_000 || !m.contextEstimated || m.resolvedSessionModel != adopted.ResolvedModel {
		t.Fatalf("adopted status = id=%q usage=%+v context=%d estimated=%t model=%+v", m.sessionID, m.usage, m.contextTokens, m.contextEstimated, m.resolvedSessionModel)
	}

	m = applyAll(m, client.TurnEndMsg{Usage: client.Usage{InputTokens: 55_000}})
	liveUsage, liveContext := m.usage, m.contextTokens
	m = applyAll(m,
		client.ResolvedModelMsg{SessionID: "other-main", Resolved: client.ResolvedModel{ContextWindow: 8_000}},
		client.ResolvedModelMsg{SessionID: row.ID, Resolved: adopted.ResolvedModel},
	)
	if m.usage != liveUsage || m.contextTokens != liveContext {
		t.Fatalf("ordinary refetch replaced newer live metrics: usage=%+v context=%d", m.usage, m.contextTokens)
	}

	// A superseded response for the same selected session generation must not
	// produce an adoption intent carrying its older snapshot.
	state := m.newSessionsSurface(false)
	state.selected, state.view = row, sessionsTranscript
	state.transcriptRequestToken = 2
	state.transcriptSurfaceRequestToken = 7
	state.handleTranscriptLoaded(sessionTranscriptLoadedMsg{
		sessionID: row.ID, requestToken: 1, surfaceRequestToken: 7,
		transcript: client.SessionTranscript{SessionID: row.ID, Complete: true},
		snapshot:   client.SessionSnapshot{Usage: client.Usage{InputTokens: 1}, ContextOccupancy: &client.ContextOccupancy{InputTokens: 1}},
	})
	if state.takeSurfaceIntent() != nil {
		t.Fatal("superseded same-session response produced an adoption intent")
	}
}

func TestResumableSessionStatusMetrics_Scenario3_InspectionIsNonDestructive(t *testing.T) {
	kinds := []client.SessionKind{
		client.SessionKindSubagent,
		client.SessionKindScheduled,
		client.SessionKindParallelBranch,
		client.SessionKindTeamMember,
		client.SessionKind("debug"),
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			inspected := client.SessionSnapshot{
				ResolvedModel:    client.ResolvedModel{ProviderID: "child-provider", ModelID: "child-model", ContextWindow: 8_000},
				Usage:            client.Usage{InputTokens: 700},
				ContextOccupancy: &client.ContextOccupancy{InputTokens: 600, Estimated: true},
			}
			conv := newSessionsConv()
			conv.getSessionSnapshots = []client.SessionSnapshot{inspected}
			row := client.SessionListItem{ID: "inspected-" + string(kind), Kind: kind}
			loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{SessionID: row.ID, Complete: true, Kind: kind}}
			m := newSessionsModel(t, conv, &fakeSessionLister{}, loader)
			m.resolvedSessionModel = client.ResolvedModel{ProviderID: "root-provider", ModelID: "root-model", ContextWindow: 200_000}
			m.usage = client.Usage{InputTokens: 120_000, OutputTokens: 4_000}
			m.contextTokens = 40_000
			rootID, rootModel, rootUsage, rootContext := m.sessionID, m.resolvedSessionModel, m.usage, m.contextTokens

			mm, cmd, ok := m.loadSessionTranscript(row, true)
			if !ok || cmd == nil {
				t.Fatal("inspection did not start an authoritative load")
			}
			m = mm.(Model)
			m = applyAll(m, cmd())
			if m.sessionID != rootID || m.resolvedSessionModel != rootModel || m.usage != rootUsage || m.contextTokens != rootContext {
				t.Fatalf("inspection rebound root status: id=%q model=%+v usage=%+v context=%d", m.sessionID, m.resolvedSessionModel, m.usage, m.contextTokens)
			}
			state := ensureActiveSessions(&m)
			if state.snapshot.ContextOccupancy == nil || *state.snapshot.ContextOccupancy != *inspected.ContextOccupancy {
				t.Fatalf("inspection did not retain snapshot occupancy: %+v", state.snapshot.ContextOccupancy)
			}
		})
	}
}

func TestResumableSessionStatusMetrics_Scenario3_UnknownAndProvisionalStatus(t *testing.T) {
	resume := &client.ResumeSelection{
		Row: client.SessionListItem{ID: "legacy-main"},
		Snapshot: client.SessionSnapshot{
			ResolvedModel: client.ResolvedModel{ContextWindow: 0},
			Usage:         client.Usage{InputTokens: 120_000},
		},
	}
	m := New(Deps{Resume: resume, Theme: theme.New("aztec", theme.AztecPalette()), NoAltScreen: true})
	if !m.contextUnknown {
		t.Fatal("legacy snapshot without occupancy must remain unknown")
	}
	if footer := stripANSIstr(m.fitFooter("connected", 160)); !strings.Contains(footer, "ctx ?") || strings.Contains(footer, "120K/") {
		t.Fatalf("legacy footer = %q, want an unknown context numerator, never cumulative input", footer)
	}

	m = applyAll(m, client.ResolvedModelMsg{SessionID: "legacy-main", Resolved: client.ResolvedModel{ContextWindow: 200_000}})
	if m.resolvedSessionModel.ContextWindow != 200_000 {
		t.Fatalf("resolved-model refresh did not heal provisional window: %d", m.resolvedSessionModel.ContextWindow)
	}
	if m.usage.InputTokens != 120_000 || !m.contextUnknown {
		t.Fatalf("resolved-model refresh applied stale metrics: usage=%+v contextUnknown=%t", m.usage, m.contextUnknown)
	}
	if footer := stripANSIstr(m.fitFooter("connected", 160)); !strings.Contains(footer, "?/200K") || strings.Contains(footer, "120K/200K") {
		t.Fatalf("healed legacy footer = %q, want unknown numerator with healed denominator", footer)
	}
}
