package ui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestSurfaceApprovalMigration_Scenario1_DynamicSurfaceQueue(t *testing.T) {
	m := New(Deps{Theme: debugTheme()})
	m.sessionID = "session-1"
	m.phase = phaseRunning
	m = applyAll(m,
		client.PermissionAskMsg{AskID: "session-1:1:a", Tool: "Write"},
		client.PermissionAskMsg{AskID: "session-1:1:a", Tool: "Write"},
		client.PermissionAskMsg{AskID: "child-1:1:b", Tool: "Bash"},
	)
	s := approvalSurfaceOf(t, m)
	if s.ask.AskID != "session-1:1:a" || len(s.queue) != 1 || s.queue[0].AskID != "child-1:1:b" {
		t.Fatalf("head/queue = %+v/%+v, want first ask with one FIFO successor", s.ask, s.queue)
	}
	if m.phase != phaseAwaitingApproval {
		t.Fatalf("phase = %v, want phaseAwaitingApproval", m.phase)
	}
}

func TestSurfaceApprovalMigration_Scenario1_QueueDrainRetractAndCloseLifecycle(t *testing.T) {
	m, _ := queuedAskModel(t)
	first := m.modal
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.modal != first || m.phase != phaseAwaitingApproval || approvalSurfaceOf(t, m).ask.AskID != askB {
		t.Fatal("resolving a visible ask with a successor must retain the surface and awaiting phase")
	}
	if containsSpinnerTick(cmd) {
		t.Fatal("successor must not re-arm spinner")
	}
	m = applyAll(m, client.PermissionRetractMsg{AskID: askB})
	if m.modal != nil || m.phase != phaseRunning {
		t.Fatal("retracting final ask must close surface and resume running")
	}
	if len(m.hits.frame) != 0 {
		t.Fatal("closing final ask must clear render-frame hit cache")
	}
}

func TestSurfaceApprovalMigration_Scenario1_RunAndSessionTeardown(t *testing.T) {
	for _, teardown := range []struct {
		name  string
		apply func(Model) Model
	}{
		{"terminal run", func(m Model) Model { return applyAll(m, client.ResultMsg{Stop: "cancelled"}) }},
		{"session reset", func(m Model) Model { return m.resetSession() }},
	} {
		t.Run(teardown.name, func(t *testing.T) {
			m, _ := queuedAskModel(t)
			_ = m.View() // populate a frame cache that teardown must discard
			m = teardown.apply(m)
			if m.modal != nil || len(m.hits.frame) != 0 {
				t.Fatal("teardown retained an approval surface or frame cache")
			}
		})
	}
}

func TestApprovalSurfaceCloseIsNoop(t *testing.T) {
	s := &approvalSurface{
		ask:  pendingAsk{AskID: "ask"},
		hits: map[HitID]client.Verdict{1: client.VerdictAllowOnce},
	}

	s.Close()

	if s.ask.AskID != "ask" || len(s.hits) != 1 {
		t.Fatal("approval surface Close must leave ephemeral state for parent closeModal to discard")
	}
}

func TestCloseModalSynchronizesSurfaceTokensWithoutLifecycleEffects(t *testing.T) {
	m := New(Deps{Theme: debugTheme()})
	m.phase = phaseRunning
	m.statusMsg = "unchanged"
	m.modelCatalogRequestToken = 3
	m.modal = &modelsState{requestToken: 7}
	m.hits.frame = []renderedHitRegion{{}}
	*m.metrics = renderedSurfaceMetrics{outerBounds: cellRect{x1: 1, y1: 1}, contentBounds: cellRect{x1: 1, y1: 1}}

	m.closeModal()

	if m.modal != nil || len(m.hits.frame) != 0 || m.metrics.outerBounds != (cellRect{}) || m.metrics.contentBounds != (cellRect{}) || m.metrics.contentOrigin != (cellPoint{}) {
		t.Fatal("closeModal must release the modal, its current hit frame, and its metrics")
	}
	if m.modelCatalogRequestToken != 7 {
		t.Fatalf("model catalog token = %d, want 7", m.modelCatalogRequestToken)
	}
	if m.phase != phaseRunning || m.statusMsg != "unchanged" {
		t.Fatalf("closeModal changed lifecycle state: phase=%v status=%q", m.phase, m.statusMsg)
	}
}

func TestApprovalSurfaceRoutesKeysBeforePhase(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "ask", Tool: "Bash", offerAlways: true})
	m.phase = phaseRunning // stale chrome must not bypass the open modal.

	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})

	if got := lastNotice(m); got != "permission allowed" {
		t.Fatalf("modal-first key routing notice = %q, want permission allowed", got)
	}
}

func TestEffectiveModelSetterUpdatesApprovalIdentityOnly(t *testing.T) {
	m := planAskModel(t, true)
	s := approvalSurfaceOf(t, m)
	(&m).setEffectiveModel(client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5", ContextWindow: 128000})
	if s.modelID != "gpt-5" {
		t.Fatalf("approval model id = %q, want gpt-5", s.modelID)
	}
	(&m).setEffectiveModel(client.ResolvedModel{ModelID: "gpt-5", ContextWindow: 64000})
	if got := m.effectiveModel.ContextWindow; got != 128000 {
		t.Fatalf("context window = %d, want raise-only 128000", got)
	}
	if s.modelID != "gpt-5" {
		t.Fatalf("context-only update changed approval model id to %q", s.modelID)
	}
}

func TestApprovalExpandIntentCarriesSurfaceValue(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "diff", Tool: "Edit", Args: `{"path":"a","old_string":"a","new_string":"b"}`, offerAlways: true})
	s := approvalSurfaceOf(t, m)
	s.expandTools = true
	m.expandTools = false // the surface remains the source of this interaction's desired value.

	m, _ = pressKey(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})

	if s.expandTools || m.expandTools {
		t.Fatalf("expand values = surface:%v model:%v, want false:false", s.expandTools, m.expandTools)
	}
}

func TestApprovalSurfaceStateIsNotModelOwned(t *testing.T) {
	st := reflect.TypeOf(Model{})
	for i := 0; i < st.NumField(); i++ {
		if st.Field(i).Type.Name() == "approvalState" || st.Field(i).Name == "approval" {
			t.Fatalf("Model owns approval state through %q; it must live only in the dynamic surface", st.Field(i).Name)
		}
	}
}

func TestApprovalStateLookupCannotOpenSurface(t *testing.T) {
	m := New(Deps{Theme: debugTheme()})
	if m.modal != nil {
		t.Fatal("a new model must not have an approval surface")
	}
}

func TestSurfaceApprovalMigration_Scenario2_VerdictTransportAndChildPolicy(t *testing.T) {
	m, send := queuedAskModel(t)
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	runBatchLeaves(cmd)
	if got := resumeApprovalAskIDs(send); len(got) != 1 || got[0] != askA {
		t.Fatalf("sent verdict IDs = %v, want exact visible ask ID %q", got, askA)
	}
	if approvalSurfaceOf(t, m).ask.offerAlways {
		t.Fatal("child ask must withhold Allow Always")
	}

	debug := New(Deps{Theme: debugTheme()})
	debug.sessionID = "debug"
	debug.phase = phaseIdle
	debug = applyAll(debug, client.PermissionAskMsg{AskID: "debug:1:a", Tool: "Bash"})
	debug.stream = nil
	debug, cmd = pressKey(debug, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd != nil {
		_ = cmd()
	}
	if got := lastNotice(debug); got != "permission allowed" {
		t.Fatalf("nil-stream verdict notice = %q, want permission allowed", got)
	}
}

func approvalSurfaceOf(t *testing.T, m Model) *approvalSurface {
	t.Helper()
	s, ok := m.modal.(*approvalSurface)
	if !ok || s == nil {
		t.Fatalf("modal = %T, want *approvalSurface", m.modal)
	}
	return s
}

func TestSurfaceApprovalMigration_Scenario2_PlanReviewLayoutAndOffset(t *testing.T) {
	m := planAskModel(t, true)
	setPlanArgs(t, &m, `{"plan":"one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten"}`)
	_ = m.View()
	s := approvalSurfaceOf(t, m)
	if !strings.Contains(stripANSIstr(m.View().Content), "Plan ready for review") {
		t.Fatal("surface plan render must fill the body with the review")
	}
	s.planVP.SetYOffset(2)
	before := s.planVP.YOffset()
	_ = m.View()
	if got := s.planVP.YOffset(); got != before {
		t.Fatalf("same ask/queue/model/geometry must be a no-op: offset %d, want %d", got, before)
	}
	s.queue = append(s.queue, pendingAsk{AskID: "queued", Tool: "Bash"})
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "Plan ready for review (1 of 2)") {
		t.Fatalf("queue-count change must invalidate plan cache: %q", got)
	}
	(&m).setEffectiveModel(client.ResolvedModel{ModelID: "review-model"})
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "plan model: review-model") {
		t.Fatalf("model change must invalidate plan cache: %q", got)
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 60, Height: 30})
	_ = m.View()
	if got := approvalSurfaceOf(t, m).planVPWidth; got != 60 {
		t.Fatalf("geometry change must rewrap plan at width 60, got %d", got)
	}
}

func TestSurfaceApprovalMigration_Scenario2_ArgsAndDiffModes(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, longBashArgs))
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "Ask args: Bash") {
		t.Fatalf("surface args render = %q, want args view", got)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if !approvalSurfaceOf(t, m).argsViewRaw {
		t.Fatal("raw key must toggle the surface-owned args mode")
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if m.phase != phaseRunning || lastNotice(m) != "permission denied" {
		t.Fatalf("args verdict key = phase %v, notice %q", m.phase, lastNotice(m))
	}

	diff := approvalModel(t, pendingAsk{AskID: "diff", Tool: "Edit", Args: `{"path":"a","old_string":"a","new_string":"b"}`, offerAlways: true})
	before := diff.expandTools
	diff, _ = pressKey(diff, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if diff.expandTools == before || approvalSurfaceOf(t, diff).argsViewOpen {
		t.Fatal("diff mode must retain the in-modal expand keyboard behavior")
	}
}

func TestSurfaceApprovalMigration_Scenario2_PlanContinuationOrdering(t *testing.T) {
	m, send := queuedAskModel(t)
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	runBatchLeaves(cmd)
	if got := resumeApprovalAskIDs(send); len(got) != 1 || got[0] != askA {
		t.Fatalf("sent verdict IDs = %v, want first ask %q", got, askA)
	}
	if approvalSurfaceOf(t, m).ask.AskID != askB {
		t.Fatalf("successor = %q, want %q", approvalSurfaceOf(t, m).ask.AskID, askB)
	}
}

func TestSurfaceApprovalMigration_Scenario3_ApprovalMouseParity(t *testing.T) {
	m := driveTo(t, debugTheme())
	m.deps.NoAltScreen = false
	_ = m.View() // allocate the current-frame surface hit IDs
	if len(m.hits.frame) == 0 {
		t.Fatal("approval render must register current-frame button hits")
	}
	hit := m.hits.frame[0]
	x, y := m.metrics.localToGlobal(hit.rect.x0, hit.rect.y0)
	// A verdict from HandleMsg must refresh immediately, just as the legacy
	// Model.resolveAsk mouse path did; it cannot wait for a later stream event.
	m.viewDirty = true
	m = leftClick(t, m, x, y)
	if m.viewDirty {
		t.Fatal("mouse verdict must immediately refresh the viewport")
	}
	if got := stripANSIstr(m.vp.View()); !strings.Contains(got, "permission allowed") {
		t.Fatalf("mouse verdict viewport = %q, want immediate permission notice", got)
	}
	if m.phase != phaseRunning || lastNotice(m) != "permission allowed" {
		t.Fatalf("surface hit verdict = phase %v, notice %q", m.phase, lastNotice(m))
	}
}

func TestSurfaceApprovalMigration_Scenario3_WheelCapture(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, tallBashArgs))
	before := approvalSurfaceOf(t, m).argsVP.YOffset()
	mm, _ := m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1})
	m = mm.(Model)
	if approvalSurfaceOf(t, m).argsVP.YOffset() <= before {
		t.Fatal("args surface must capture wheel and scroll its viewport")
	}
}

func TestApprovalFillViewsCaptureWheelBeforeViewportMaterialization(t *testing.T) {
	tests := []struct {
		name  string
		model func(*testing.T) Model
		ready func(*approvalSurface) bool
	}{
		{
			name: "args",
			model: func(t *testing.T) Model {
				m := bashAskModel(t, tallBashArgs)
				m, _ = pressKey(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
				return m
			},
			ready: func(s *approvalSurface) bool { return s.argsVPReady },
		},
		{
			name:  "plan",
			model: func(t *testing.T) Model { return planAskModel(t, true) },
			ready: func(s *approvalSurface) bool { return s.planVPReady },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.model(t)
			m.conv.appendAssistant(strings.Repeat("scrollable conversation\n", 120))
			m.refreshView()
			m.vp.GotoBottom()
			before := m.vp.YOffset()
			if before == 0 {
				t.Fatal("precondition: conversation viewport must be scrollable")
			}
			if tc.ready(approvalSurfaceOf(t, m)) {
				t.Fatal("precondition: approval viewport must not materialize before View")
			}

			mm, _ := m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
			m = mm.(Model)
			if got := m.vp.YOffset(); got != before {
				t.Fatalf("wheel before viewport materialization reached conversation: %d → %d", before, got)
			}
		})
	}
}
