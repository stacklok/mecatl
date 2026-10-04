package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestMecatuiAuthoritativeReload_Scenario1_TitleRevertsAndLiveOrderingResumes(t *testing.T) {
	m := titleModel(t, &titleRenamer{})
	m.sessionTitle, m.sessionTitleProvenance, m.sessionTitleRevision = "stale", "generated", 12
	m = applyAll(m, client.ResolvedModelMsg{SessionID: "active", TitleMetadataPresent: true, Title: "server", TitleProvenance: "operator", TitleRevision: 3})
	if m.sessionTitle != "server" || m.sessionTitleProvenance != "operator" || m.sessionTitleRevision != 3 {
		t.Fatalf("reload title = %q/%q/%d", m.sessionTitle, m.sessionTitleProvenance, m.sessionTitleRevision)
	}
	m = applyAll(m, client.ResolvedModelMsg{SessionID: "active", TitleMetadataPresent: true, TitleRevision: 1})
	if m.sessionTitle != "" || m.sessionTitleProvenance != "" || m.sessionTitleRevision != 1 {
		t.Fatalf("empty snapshot = %q/%q/%d", m.sessionTitle, m.sessionTitleProvenance, m.sessionTitleRevision)
	}
	m = m.onSessionTitle(client.SessionTitleMsg{Title: "live", Provenance: "generated", Revision: 2})
	m = m.onSessionTitle(client.SessionTitleMsg{Title: "duplicate", Revision: 2})
	m = m.onSessionTitle(client.SessionTitleMsg{Title: "lower", Revision: 1})
	if m.sessionTitle != "live" || m.sessionTitleProvenance != "generated" || m.sessionTitleRevision != 2 {
		t.Fatalf("live title = %q/%q/%d", m.sessionTitle, m.sessionTitleProvenance, m.sessionTitleRevision)
	}
}

func TestMecatuiAuthoritativeReload_Scenario1_ReplacesSuppliedSessionFacts(t *testing.T) {
	m := titleModel(t, &titleRenamer{})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "old", ModelID: "same", ReasoningEffort: "high", ContextWindow: 200000}
	m.usage = client.Usage{InputTokens: 999}
	m.contextTokens = 888
	m.contextUnknown = true
	placement := client.Placement{Kind: "worktree", Label: "server"}
	m = applyAll(m, client.ResolvedModelMsg{SessionID: "active", ResolvedModelPresent: true,
		Resolved:         client.ResolvedModel{ProviderID: "new", ModelID: "same", ReasoningEffort: "low", ContextWindow: 64000},
		MainUsagePresent: true, Usage: client.Usage{InputTokens: 120, OutputTokens: 20},
		ContextOccupancy: &client.ContextOccupancy{InputTokens: 35, Estimated: true},
		Mode:             "ask", State: "idle", Placement: placement})
	if m.resolvedSessionModel != (client.ResolvedModel{ProviderID: "new", ModelID: "same", ReasoningEffort: "low", ContextWindow: 64000}) || m.activeMode != client.ModeString(client.ModeFromString("ask")) || m.sessionState != "idle" || m.activePlacement != placement {
		t.Fatalf("session metadata = %+v %q %q %+v", m.resolvedSessionModel, m.activeMode, m.sessionState, m.activePlacement)
	}
	if m.usage != (client.Usage{InputTokens: 120, OutputTokens: 20}) || m.contextTokens != 35 || m.contextUnknown || !m.contextEstimated {
		t.Fatalf("usage/occupancy = %+v %d unknown=%t estimated=%t", m.usage, m.contextTokens, m.contextUnknown, m.contextEstimated)
	}

	// A legacy snapshot can supply occupancy without a main usage bucket.
	// The former is canonical, while the latter must retain its known value.
	m = applyAll(m, client.ResolvedModelMsg{
		SessionID: "active", Usage: client.Usage{InputTokens: 999},
		ContextOccupancy: &client.ContextOccupancy{InputTokens: 17},
	})
	if m.usage != (client.Usage{InputTokens: 120, OutputTokens: 20}) || m.contextTokens != 17 || m.contextUnknown || m.contextEstimated {
		t.Fatalf("missing usage with present occupancy = usage %+v occupancy %d unknown=%t estimated=%t", m.usage, m.contextTokens, m.contextUnknown, m.contextEstimated)
	}
}

func TestMecatuiAuthoritativeReload_Scenario1_LegacyAndExplicitAbsence(t *testing.T) {
	m := titleModel(t, &titleRenamer{})
	m.sessionTitle, m.sessionTitleRevision = "confirmed title", 3
	m.contextTokens, m.contextEstimated, m.contextUnknown = 99, true, false
	m.liveGen, m.reloadFeedGen, m.reloadSeq = 7, 7, 1
	m.liveCh = make(chan tea.Msg)
	m.reloadPending, m.reloadSession = true, m.sessionID
	m = applyAll(m, snapshotReply{seq: 1, session: m.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: m.sessionID}})
	if !m.contextUnknown || m.contextTokens != 0 || m.contextEstimated {
		t.Fatalf("nil authoritative occupancy = tokens=%d unknown=%t estimated=%t", m.contextTokens, m.contextUnknown, m.contextEstimated)
	}
	if m.sessionTitle != "confirmed title" || m.sessionTitleRevision != 3 {
		t.Fatalf("absent legacy title metadata erased confirmed title: %q/%d", m.sessionTitle, m.sessionTitleRevision)
	}

	m.liveGen, m.reloadFeedGen, m.reloadSeq = 7, 7, 2
	m.reloadPending, m.reloadSession = true, m.sessionID
	m = applyAll(m, snapshotReply{seq: 2, session: m.sessionID, feedGen: 7, msg: client.ResolvedModelMsg{SessionID: m.sessionID, ContextOccupancy: &client.ContextOccupancy{}}})
	if m.contextUnknown || m.contextTokens != 0 || m.contextEstimated {
		t.Fatalf("present zero authoritative occupancy = tokens=%d unknown=%t estimated=%t", m.contextTokens, m.contextUnknown, m.contextEstimated)
	}

	// A legacy snapshot has no resolved-model presence bit. It must preserve
	// known compatible facts, but must not invent a model from a zero value.
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "known", ModelID: "known-model", ReasoningEffort: "high", ContextWindow: 128000}
	m = applyAll(m, client.ResolvedModelMsg{SessionID: "active"})
	if got := m.resolvedSessionModel; got != (client.ResolvedModel{ProviderID: "known", ModelID: "known-model", ReasoningEffort: "high", ContextWindow: 128000}) {
		t.Fatalf("missing legacy resolved model erased confirmed value: %+v", got)
	}
	m.resolvedSessionModel = client.ResolvedModel{}
	m = applyAll(m, client.ResolvedModelMsg{SessionID: m.sessionID})
	if m.resolvedSessionModel != (client.ResolvedModel{}) {
		t.Fatalf("missing legacy resolved model invented an identity: %+v", m.resolvedSessionModel)
	}

	row := client.SessionListItem{ID: "next-main", Kind: client.SessionKindMain}
	global := client.Capabilities{MCP: true, SlashCommands: true}
	for _, tt := range []struct {
		name     string
		snapshot client.SessionSnapshot
		want     client.Capabilities
	}{
		{
			name:     "legacy globals retain confirmed controls with explicit text-only media",
			snapshot: client.SessionSnapshot{Capabilities: client.Capabilities{SessionMediaPresent: true}},
			want:     client.Capabilities{MCP: true, SlashCommands: true, SessionMediaPresent: true},
		},
		{
			name:     "present all-false globals clear controls",
			snapshot: client.SessionSnapshot{ServerCapabilitiesPresent: true},
			want:     client.Capabilities{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := newSessionsModel(t, newSessionsConv(), &fakeSessionLister{}, &fakeSessionTranscriptLoader{})
			model.caps = global
			adopted, _, ok := model.adoptAuthoritativeTranscript(row, conversation{}, tt.snapshot)
			if !ok {
				t.Fatal("adoption was rejected")
			}
			got := adopted.(Model)
			if got.caps != tt.want {
				t.Fatalf("adopted capabilities = %+v, want %+v", got.caps, tt.want)
			}
		})
	}
	legacy := client.SessionSnapshot{TitleMetadataPresent: true, Title: "legacy"}
	legacyRow := client.SessionListItem{ID: "legacy", Kind: client.SessionKindMain}
	t.Run("startup missing ledger is unknown", func(t *testing.T) {
		model := New(Deps{Resume: &client.ResumeSelection{Row: legacyRow, Snapshot: legacy}, Theme: theme.New("aztec", theme.AztecPalette()), NoAltScreen: true})
		if !model.usageUnknown || strings.Contains(stripANSIstr(model.renderFooter()), "↑0") || model.statusLineInput(time.Time{}).Usage.Input.Human != "?" {
			t.Fatalf("startup claimed confirmed zero: unknown=%t footer=%q", model.usageUnknown, stripANSIstr(model.renderFooter()))
		}
	})
	t.Run("interactive same session retains event usage", func(t *testing.T) {
		model := newSessionsModel(t, newSessionsConv(), &fakeSessionLister{}, &fakeSessionTranscriptLoader{})
		model.sessionID = legacyRow.ID
		model.usage = client.Usage{InputTokens: 17}
		adopted, _, ok := model.adoptAuthoritativeTranscript(legacyRow, conversation{}, legacy)
		if !ok {
			t.Fatal("adoption rejected")
		}
		got := adopted.(Model)
		if got.usage.InputTokens != 17 || got.usageUnknown {
			t.Fatalf("same-session legacy usage lost: usage=%+v unknown=%t", got.usage, got.usageUnknown)
		}
		model.usage = client.Usage{}
		adopted, _, ok = model.adoptAuthoritativeTranscript(legacyRow, conversation{}, legacy)
		if !ok || !adopted.(Model).usageUnknown {
			t.Fatal("no event-derived usage must not claim a confirmed zero")
		}
	})
	t.Run("interactive new session missing ledger is unknown", func(t *testing.T) {
		model := newSessionsModel(t, newSessionsConv(), &fakeSessionLister{}, &fakeSessionTranscriptLoader{})
		model.usage = client.Usage{InputTokens: 17}
		adopted, _, ok := model.adoptAuthoritativeTranscript(legacyRow, conversation{}, legacy)
		if !ok {
			t.Fatal("adoption rejected")
		}
		got := adopted.(Model)
		if !got.usageUnknown || got.usage.InputTokens != 0 || strings.Contains(stripANSIstr(got.renderFooter()), "↑0") {
			t.Fatalf("new-session legacy usage claimed zero or carried old session: usage=%+v unknown=%t footer=%q", got.usage, got.usageUnknown, stripANSIstr(got.renderFooter()))
		}
	})
}

func TestMecatuiAuthoritativeReload_Scenario3_AdoptionUsesSnapshot(t *testing.T) {
	row := client.SessionListItem{ID: "next-main", Kind: client.SessionKindMain, Title: "stale", TitleRevision: 99, State: "running", CreatedAt: 10, ModifiedAt: 11, Placement: client.Placement{Label: "stale"}}
	snap := client.SessionSnapshot{TitleMetadataPresent: true, Title: "canonical", TitleProvenance: "operator", TitleRevision: 2, State: "idle", CreatedAt: 20, Placement: client.Placement{Label: "canonical"}, Mode: "ask", ResolvedModelPresent: true, ResolvedModel: client.ResolvedModel{ProviderID: "new", ModelID: "new", ContextWindow: 64000}, MainUsagePresent: true, Usage: client.Usage{InputTokens: 120}, ContextOccupancy: &client.ContextOccupancy{InputTokens: 30}}
	t.Run("startup", func(t *testing.T) {
		m := New(Deps{Resume: &client.ResumeSelection{Row: row, Snapshot: snap}, Theme: theme.New("aztec", theme.AztecPalette()), NoAltScreen: true})
		assertAdoptedSnapshot(t, m, row, snap)
		m = applyAll(m, client.ResolvedModelMsg{SessionID: row.ID, TitleMetadataPresent: true, Title: "refreshed", TitleRevision: 1, ResolvedModelPresent: true, Resolved: client.ResolvedModel{ProviderID: "later", ModelID: "new", ContextWindow: 32000}, MainUsagePresent: true, Usage: client.Usage{InputTokens: 50}, ContextOccupancy: &client.ContextOccupancy{InputTokens: 15}, State: "awaiting", Placement: client.Placement{Label: "later"}})
		if m.sessionTitle != "refreshed" || m.sessionTitleRevision != 1 || m.resolvedSessionModel.ProviderID != "later" || m.resolvedSessionModel.ContextWindow != 32000 || m.usage.InputTokens != 50 || m.contextTokens != 15 || m.sessionState != "awaiting" || m.activePlacement.Label != "later" {
			t.Fatalf("matching refresh = title %q/%d model %+v usage %+v occupancy %d state %q placement %+v", m.sessionTitle, m.sessionTitleRevision, m.resolvedSessionModel, m.usage, m.contextTokens, m.sessionState, m.activePlacement)
		}
	})
	t.Run("interactive", func(t *testing.T) {
		conv := newSessionsConv()
		conv.getSessionSnapshots = []client.SessionSnapshot{snap}
		loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{SessionID: row.ID, Complete: true}}
		m := newSessionsModel(t, conv, &fakeSessionLister{}, loader)
		mm, cmd, ok := m.loadSessionTranscript(row, false)
		if !ok || cmd == nil {
			t.Fatal("adoption did not start")
		}
		m = applyAll(mm.(Model), cmd())
		assertAdoptedSnapshot(t, m, row, snap)
	})
}

func assertAdoptedSnapshot(t *testing.T, m Model, row client.SessionListItem, snap client.SessionSnapshot) {
	t.Helper()
	if m.sessionID != row.ID || m.sessionTitle != snap.Title || m.sessionTitleRevision != snap.TitleRevision || m.sessionTitleProvenance != snap.TitleProvenance || m.sessionState != snap.State || m.sessionCreatedAt != snap.CreatedAt || m.sessionModifiedAt != row.ModifiedAt || m.activePlacement != snap.Placement || m.resolvedSessionModel != snap.ResolvedModel || m.usage != snap.Usage || m.contextTokens != snap.ContextOccupancy.InputTokens {
		t.Fatalf("adopted = id %q title %q/%q/%d state %q created %d modified %d placement %+v model %+v usage %+v occupancy %d", m.sessionID, m.sessionTitle, m.sessionTitleProvenance, m.sessionTitleRevision, m.sessionState, m.sessionCreatedAt, m.sessionModifiedAt, m.activePlacement, m.resolvedSessionModel, m.usage, m.contextTokens)
	}
}

func TestMecatuiAuthoritativeReload_Scenario3_PreservesViewAndRecovery(t *testing.T) {
	m := scrollModel(t)
	m.prompt.Rewrite("unsent draft")
	m.queued = []string{"queued prompt"}
	m.conv.addUser("recorded conversation")
	m.refreshView()
	m.vp.SetYOffset(10)
	m.conversationView.observe(m.vp)
	if m.conversationView.mode != anchored || m.vp.YOffset() == 0 {
		t.Fatalf("scroll setup = mode %v offset %d, want anchored non-zero offset", m.conversationView.mode, m.vp.YOffset())
	}
	beforeOffset := m.vp.YOffset()
	beforeAnchor, ok := m.conversationView.frame.observedAnchorForRow(beforeOffset, towardStart)
	if !ok || beforeAnchor.blockID == 0 {
		t.Fatalf("scroll setup anchor = %#v, ok=%t", beforeAnchor, ok)
	}
	m.sessionDetailsOpen = true
	modal := &placementTestSurface{body: "open modal"}
	m.modal = modal
	m.prompt.Focus()
	m.phase = phaseAwaitingApproval
	pending := &pendingApprovalRecovery{approval: client.PendingApproval{SessionID: m.sessionID, RunID: "run", AskID: "ask"}}
	m.pendingRecovery = pending

	m = applyAll(m, client.ResolvedModelMsg{
		SessionID:            m.sessionID,
		Mode:                 "ask",
		State:                "awaiting",
		TitleMetadataPresent: true,
		Title:                "server title",
		ResolvedModelPresent: true,
		Resolved:             client.ResolvedModel{ProviderID: "server", ModelID: "server-model", ContextWindow: 64000},
	})

	if m.prompt.Value() != "unsent draft" || !m.prompt.Focused() || len(m.queued) != 1 || m.queued[0] != "queued prompt" {
		t.Fatalf("reload discarded draft, focus, or queue: draft=%q focused=%t queued=%v", m.prompt.Value(), m.prompt.Focused(), m.queued)
	}
	if got := m.conv.testBlocks(); len(got) != 3 || testCardText(got[2]) != "recorded conversation" {
		t.Fatalf("reload changed recorded conversation: %+v", got)
	}
	if m.conversationView.mode != anchored || m.vp.YOffset() != beforeOffset {
		t.Fatalf("reload reset reading offset: mode=%v offset=%d, want anchored/%d", m.conversationView.mode, m.vp.YOffset(), beforeOffset)
	}
	afterAnchor, ok := m.conversationView.frame.observedAnchorForRow(m.vp.YOffset(), towardEnd)
	if !ok || afterAnchor != beforeAnchor {
		t.Fatalf("reload changed reading anchor: got %#v ok=%t, want %#v", afterAnchor, ok, beforeAnchor)
	}
	if !m.sessionDetailsOpen || m.modal != modal {
		t.Fatalf("reload reset navigation or modal: details=%t modal=%p want=%p", m.sessionDetailsOpen, m.modal, modal)
	}
	if m.pendingRecovery != pending || m.phase != phaseAwaitingApproval {
		t.Fatalf("reload discarded pending approval recovery: recovery=%p phase=%v", m.pendingRecovery, m.phase)
	}
	if m.activeMode != client.ModeString(client.ModeFromString("ask")) {
		t.Fatalf("confirmed permission mode not visible: %q", m.activeMode)
	}
}
