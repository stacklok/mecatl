package ui

import (
	"context"
	"errors"
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
	m.sessions = sessionsState{view: sessionsPanel, tab: tabOtherRuns, loadState: sessionsComplete, sessions: []client.SessionListItem{row}}
	m = m.syncSessionsFilter()
	return m
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
	if unresolved.adoptionPreflightCmd() != nil || !strings.Contains(stripANSIstr(unresolved.View().Content), "select an explicit workspace, environment, provider, and model") {
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
	m.sessions.sessions = append(m.sessions.sessions, client.SessionListItem{ID: "session-main-looking", Kind: client.SessionKindUnknown})
	m = m.syncSessionsFilter()
	m.sessions.cursor = 1
	m = m.invalidateAdoptionPreflight()
	if got := stripANSIstr(m.View().Content); strings.Contains(got, "a: adopt as chat") {
		t.Fatalf("stale preflight leaked across rows:\n%s", got)
	}
}

func TestSessionStorageContinuity_Scenario8_AdoptionTUIFlow(t *testing.T) {
	legacy := client.SessionListItem{ID: "legacy-source", Title: "Old investigation", Kind: client.SessionKindUnknown, Capabilities: client.SessionInventoryCapabilities{Inspect: true}}
	binding := client.AdoptionBindings{Workspace: "/target", EnvironmentKind: "local", EnvironmentID: "/target", ProviderID: "provider-b", ModelID: "model-b"}
	adopter := &scenario8Adopter{preflight: client.AdoptionPreflight{Eligible: true, Bindings: binding}}
	m := scenario8Model(adopter, legacy)
	preflightCmd := m.adoptionPreflightCmd()
	if preflightCmd == nil {
		t.Fatal("complete explicit binding did not request server preflight")
	}
	m = applyAll(m, preflightCmd())

	mm, _, handled := m.onSessionsKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
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
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.sessions.view != sessionsPanel || m.sessions.cursor != 0 || len(m.sessions.sessions) != 1 {
		t.Fatalf("cancel destabilized panel: %+v", m.sessions)
	}

	// A stale preflight is revalidated by AdoptSession. Its error leaves the
	// review stable and never mutates/deletes the source row.
	preflightCmd = m.adoptionPreflightCmd()
	m = applyAll(m, preflightCmd())
	mm, _, _ = m.onSessionsKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = mm.(Model)
	adopter.adoptErr = errors.New("failed precondition: source changed")
	mm, adoptCmd, _ := m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if adoptCmd == nil {
		t.Fatal("confirmed review did not call adoption seam")
	}
	m = applyAll(m, adoptCmd())
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "source changed") || !strings.Contains(got, "Adopt as new chat") {
		t.Fatalf("stale-preflight error did not leave stable review:\n%s", got)
	}
	if len(m.sessions.sessions) != 1 || m.sessions.sessions[0].ID != legacy.ID {
		t.Fatalf("error changed source inventory: %+v", m.sessions.sessions)
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
	mm, adoptCmd, _ = m.onSessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	m = applyAll(m, adoptCmd())
	if m.sessionID != adopted.ID || m.phase != phaseIdle || !m.ta.Focused() || m.sessions.view != sessionsNone {
		t.Fatalf("success did not open writable authoritative target: id=%q phase=%v focused=%v view=%v", m.sessionID, m.phase, m.ta.Focused(), m.sessions.view)
	}
	if m.sessionID == legacy.ID {
		t.Fatalf("success rebound the inspect-only source instead of the new target")
	}
}
