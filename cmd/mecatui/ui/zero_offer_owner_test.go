package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestZeroOfferModelsKeyBeforeFirstView(t *testing.T) {
	store := &fakeStore{}
	m := newModelsModel(t, sampleModels(), store, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	m = resize(m, 0, 0)
	s := modelsSurface(t, m)
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: 'g', Mod: tea.ModCtrl}, {Code: 'x', Text: "x"}, {Code: tea.KeyDown}} {
		mm, cmd = m.Update(k)
		m = mm.(Model)
		if cmd != nil || m.modal != s || s.filter.Value() != "" || s.intent != nil || s.rowBudget != 0 || store.saves != 0 || store.globalSaves != 0 || m.modelCatalog.globalDefault != (client.ModelSelection{}) {
			t.Fatalf("hidden picker changed on %v", k)
		}
	}
	m = resize(m, 100, 30)
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = mm.(Model)
	if modelsSurface(t, m).filter.Value() != "x" {
		t.Fatal("restored picker did not accept filter")
	}
	m = resize(m, 0, 0)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if mm.(Model).modal != nil {
		t.Fatal("hidden picker did not close")
	}
}

func TestZeroOfferToolcallsKeyBeforeFirstView(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = addToolcallsForTest(t, m, 1)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	m = resize(m, 0, 0)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if cmd != nil || !s.compact || s.detail || s.intent != nil {
		t.Fatal("hidden toolcalls entered detail")
	}
	m = resize(m, 100, 30)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !toolcallsForTest(t, mm.(Model)).detail {
		t.Fatal("restored toolcalls did not enter detail")
	}
}

func TestZeroOfferSessionsTranscriptKeyBeforeFirstView(t *testing.T) {
	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: context.Background(), NoAltScreen: true})
	m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	s := m.newSessionsSurface(false)
	s.view = sessionsTranscript
	s.loadErr = context.Canceled
	s.transcripter = &fakeSessionTranscriptLoader{}
	s.nextCursor = "more"
	m.modal, m.phase = s, phaseReplay
	m = resize(m, 0, 0)
	for _, k := range []tea.KeyPressMsg{{Code: 'r', Text: "r"}, {Code: 'e', Text: "e"}} {
		mm, cmd := m.Update(k)
		m = mm.(Model)
		if cmd != nil || m.modal != s || !s.compact || s.view != sessionsTranscript || s.intent != nil || m.phase != phaseReplay {
			t.Fatalf("hidden transcript changed on %v", k)
		}
	}
	m = resize(m, 80, 24)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd == nil || mm.(Model).modal != s || s.compact {
		t.Fatal("restored transcript retry did not schedule load")
	}
	m = mm.(Model)
	m = resize(m, 0, 0)
	closed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = closed.(Model)
	if m.modal != nil || m.phase != phaseIdle || s.nextCursor != "more" || s.view != sessionsTranscript {
		t.Fatal("hidden transcript close ran transcript navigation or left replay phase")
	}
}

func TestZeroOfferApprovalNeverGrantsBeforeFirstView(t *testing.T) {
	m, send := queuedAskModel(t)
	s := approvalSurfaceOf(t, m)
	m = resize(m, 0, 0)
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: 'a', Text: "a"}, {Code: 'w', Text: "w"}, {Code: tea.KeyRight}, {Code: tea.KeyTab}} {
		mm, cmd := m.Update(k)
		m = mm.(Model)
		if cmd != nil || m.modal != s || s.ask.AskID != askA || len(s.queue) != 1 || s.intent != nil || m.phase != phaseAwaitingApproval || len(resumeApprovalAskIDs(send)) != 0 {
			t.Fatalf("hidden approval changed on %v", k)
		}
	}
	m = resize(m, 100, 30)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if approvalSurfaceOf(t, m).ask.AskID != askB || len(resumeApprovalAskIDs(send)) != 1 {
		t.Fatal("visible approval did not advance same ask")
	}
}

func TestAdmissionKeyPreparesSurfaceWithoutPublishingHits(t *testing.T) {
	m, _ := admissionModel(t, false)
	m.prompt.Rewrite("retry me")
	m = rejectAdmission(t, submitAdmission(t, m))
	s := m.modal.(*admissionRecoveryState)
	m.hits.frame = []renderedHitRegion{{}}
	*m.metrics = renderedSurfaceMetrics{outerBounds: cellRect{x1: 1, y1: 1}}
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'z', Text: "z"})
	m = mm.(Model)
	if cmd != nil || m.modal != s || len(m.hits.frame) != 0 || *m.metrics != (renderedSurfaceMetrics{}) {
		t.Fatal("admission key published prepared frame")
	}
	if !m.admissionSubmission.rejected {
		t.Fatal("unrelated admission key lost retained submission")
	}
}

func TestZeroOfferApprovalExplicitDenyPreservesQueue(t *testing.T) {
	m, send := queuedAskModel(t)
	m = resize(m, 0, 0)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if approvalSurfaceOf(t, m).ask.AskID != askB || m.phase != phaseAwaitingApproval || len(resumeApprovalAskIDs(send)) != 1 {
		t.Fatal("explicit deny did not resolve head and retain successor")
	}
}

func TestZeroOfferMCPKeysBeforeFirstView(t *testing.T) {
	t.Run("panel refresh", func(t *testing.T) {
		fake := samplePanelMCP()
		m := newMCPModel(t, aztec(), fake)
		m = openOverlay(t, m, ctrlKey('o'))
		m = resize(m, 0, 0)
		before := fake.sourcesCalls
		mm, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
		m = mm.(Model)
		if cmd != nil || fake.sourcesCalls != before || !mcpActive(m).zeroOffer {
			t.Fatal("hidden panel refresh scheduled an RPC")
		}
		m = resize(m, 100, 30)
		if _, cmd = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"}); cmd == nil || mcpActive(m).zeroOffer {
			t.Fatal("restored panel did not refresh")
		}
	})

	t.Run("panel connect", func(t *testing.T) {
		m, control := setupPanelModel(t)
		m = openOverlay(t, m, ctrlKey('o'))
		m = resize(m, 0, 0)
		mm, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
		m = mm.(Model)
		if cmd != nil || control.connectCalls != 0 {
			t.Fatal("hidden panel queued enrollment")
		}
		m = resize(m, 100, 30)
		if _, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"}); cmd == nil {
			t.Fatal("restored panel did not queue enrollment")
		}
	})

	t.Run("resource preview insertion", func(t *testing.T) {
		m := newMCPModel(t, aztec(), sampleResourceMCP())
		mm, cmd := m.runMCPResources()
		m = feedCmd(t, mm.(Model), cmd)
		m = resize(m, 0, 0)
		mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = mm.(Model)
		if cmd != nil || mcpActive(m).view != mcpResources {
			t.Fatal("hidden resource list started a read")
		}
		m = resize(m, 100, 30)
		mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = feedCmd(t, mm.(Model), cmd)
		if mcpActive(m).view != mcpResourcePrev {
			t.Fatal("restored resource list did not open preview")
		}
		m = resize(m, 0, 0)
		mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = mm.(Model)
		if cmd != nil || m.modal == nil || m.prompt.Value() != "" {
			t.Fatal("hidden resource preview inserted text")
		}
		m = resize(m, 100, 30)
		mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = feedMCPInsertion(t, mm.(Model), cmd)
		if !strings.Contains(m.prompt.Value(), "the resource body") {
			t.Fatal("restored resource preview did not insert text")
		}
	})

	t.Run("prompt enter", func(t *testing.T) {
		m := newMCPModel(t, aztec(), samplePromptMCP())
		mm, cmd := m.runMCPPrompts()
		m = feedCmd(t, mm.(Model), cmd)
		mcpActive(m).prCursor = 1 // summarize has no required arguments.
		m = resize(m, 0, 0)
		mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = mm.(Model)
		if cmd != nil || mcpActive(m).loading {
			t.Fatal("hidden prompt picker started a fetch")
		}
		m = resize(m, 100, 30)
		mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = feedMCPInsertion(t, mm.(Model), cmd)
		if !strings.Contains(m.prompt.Value(), "Please review path X.") {
			t.Fatal("restored prompt picker did not insert prompt")
		}
	})
}

func TestZeroOfferAdmissionRecoveryKeysBeforeFirstView(t *testing.T) {
	newRejected := func(t *testing.T) (Model, *fakeSender) {
		t.Helper()
		m, send := admissionModel(t, false)
		m.prompt.Rewrite("rejected submission")
		return rejectAdmission(t, submitAdmission(t, m)), send
	}

	t.Run("retry and discard", func(t *testing.T) {
		m, send := newRejected(t)
		m = resize(m, 0, 0)
		for _, msg := range []tea.KeyPressMsg{{Code: 'r', Text: "r"}, {Code: 'd', Text: "d"}} {
			mm, cmd := m.Update(msg)
			m = mm.(Model)
			if cmd != nil || m.modal == nil || !retainedAdmission(m) || len(send.frames()) != 1 {
				t.Fatalf("hidden recovery changed on %v", msg)
			}
		}
		m = resize(m, 100, 30)
		mm, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
		m = mm.(Model)
		if cmd == nil || m.modal != nil || len(send.frames()) != 1 {
			t.Fatal("restored recovery did not retry")
		}

		m, _ = newRejected(t)
		m = resize(m, 0, 0)
		m = admissionKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
		m = resize(m, 100, 30)
		m = admissionKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
		if m.modal != nil || retainedAdmission(m) {
			t.Fatal("restored recovery did not discard")
		}
	})

	t.Run("replace is gated but back restores", func(t *testing.T) {
		m, send := newRejected(t)
		m.prompt.Rewrite("newer draft")
		m = resize(m, 0, 0)
		m = admissionKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
		s := m.modal.(*admissionRecoveryState)
		if !s.confirmReplace {
			t.Fatal("hidden Back did not preserve newer draft behind confirmation")
		}
		m = admissionKey(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		if !s.confirmReplace || m.prompt.Value() != "newer draft" || len(send.frames()) != 1 {
			t.Fatal("hidden recovery replaced the newer draft")
		}
		m = admissionKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
		if s.confirmReplace {
			t.Fatal("hidden Back did not cancel confirmation")
		}
		m = resize(m, 100, 30)
		m = admissionKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
		m = admissionKey(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		if m.modal != nil || m.prompt.Value() != "rejected submission" {
			t.Fatal("restored recovery did not replace the newer draft")
		}

		m, send = newRejected(t)
		m = resize(m, 0, 0)
		m = admissionKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
		if m.modal != nil || m.prompt.Value() != "rejected submission" || len(send.frames()) != 1 {
			t.Fatal("hidden Back did not safely restore editing")
		}
	})
}
