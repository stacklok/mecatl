package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// approvalModel builds a connected, awaiting-approval Model with the given ask. The
// stream is a no-op fake so resolveAsk's send command is harmless.
func approvalModel(t *testing.T, ask pendingAsk) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-test-0001"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.phase = phaseRunning
	return applyAll(m, client.PermissionAskMsg{AskID: ask.AskID, Tool: ask.Tool, Args: ask.Args, Reason: ask.Reason})
}

// lastNotice returns the raw text of the last notice block, or "".
func lastNotice(m Model) string {
	for i := len(m.conv.blocks) - 1; i >= 0; i-- {
		if m.conv.blocks[i].kind == blockNotice {
			return m.conv.blocks[i].raw
		}
	}
	return ""
}

// TestIsChildAsk is the table for the client-side child detection. The askID
// namespace is "<sessionID>:<n>:<callID>": a main-agent ask is prefixed with the
// live session id; a child ask is prefixed with the CHILD session id; a colon-free
// fixture id is fail-safe classified as the main agent.
func TestIsChildAsk(t *testing.T) {
	const sess = "sess-abc"
	tests := []struct {
		name      string
		askID     string
		sessionID string
		want      bool
	}{
		{"parent-prefixed is main", "sess-abc:1:call-9", sess, false},
		{"child-namespaced is child", "subagent-call-9:1:k1", sess, true},
		{"colon-free fixture is main (fail-safe)", "ask-write-1", sess, false},
		{"empty session classifies colon id as child (fail-safe withhold)", "x:1:y", "", true},
		{"empty session colon-free is main", "askid", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isChildAsk(tc.askID, tc.sessionID); got != tc.want {
				t.Errorf("isChildAsk(%q, %q) = %v, want %v", tc.askID, tc.sessionID, got, tc.want)
			}
		})
	}
}

// TestPermissionAskMsgSetsOfferAlways: a main-agent ask (session-prefixed askID)
// offers always-allow; a surfaced child ask does not.
func TestPermissionAskMsgSetsOfferAlways(t *testing.T) {
	m := newTestModelFromDeps(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-abc"

	m1 := applyAll(m, client.PermissionAskMsg{AskID: "sess-abc:1:c1", Tool: "Shell"})
	if !approvalSurfaceOf(t, m1).ask.offerAlways {
		t.Error("a main-agent ask should offer always-allow")
	}
	m2 := applyAll(m, client.PermissionAskMsg{AskID: "subagent-c1:1:k1", Tool: "Shell"})
	if approvalSurfaceOf(t, m2).ask.offerAlways {
		t.Error("a surfaced child ask must NOT offer always-allow")
	}
}

// TestResolveAskResetsArgsViewState pins the lifecycle contract (issue #488):
// resolving an ask whose args view/mini-viewport were used resets BOTH the
// full-screen view state and the modal's mini-viewport offset, so the next ask
// never inherits a stale scroll position or an open view.
func TestResolveAskResetsArgsViewState(t *testing.T) {
	m := openArgsView(t, shellAskModel(t, longShellArgs))
	approvalSurfaceOf(t, m).argsViewRaw = true
	approvalSurfaceOf(t, m).askVPOffset = 3
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.modal != nil {
		t.Error("resolving the final ask must tear down the args surface and its state")
	}
}

// TestApprovalWResolvesAlways: with always-allow offered, 'w' resolves the modal as
// always-allow and records the always notice.
func TestApprovalWResolvesAlways(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "sess-test-0001:1:c1", Tool: "Shell", offerAlways: true})
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'w', Text: "w"})
	if m.phase != phaseRunning {
		t.Fatalf("resolving must return to phaseRunning, got %v", m.phase)
	}
	if got := lastNotice(m); got != "permission allowed (always, this session)" {
		t.Errorf("always-allow notice = %q", got)
	}
}

// TestApprovalWIgnoredOnChildAsk: 'w' is a no-op on a child ask (no always offered) —
// the modal stays open and no notice is recorded.
func TestApprovalWIgnoredOnChildAsk(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "subagent-c1:1:k1", Tool: "Shell", offerAlways: false})
	before := len(m.conv.blocks)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'w', Text: "w"})
	if m.phase != phaseAwaitingApproval {
		t.Errorf("w on a child ask must NOT resolve; phase = %v", m.phase)
	}
	if len(m.conv.blocks) != before {
		t.Errorf("w on a child ask must record no notice; got %q", lastNotice(m))
	}
}

// TestApprovalAllowAndDenyNotices: 'a' allows once, 'd' denies, with the exact notices.
func TestApprovalAllowAndDenyNotices(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "sess-test-0001:1:c1", Tool: "Shell", offerAlways: true})
	ma, _ := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if got := lastNotice(ma); got != "permission allowed" {
		t.Errorf("allow notice = %q", got)
	}
	md, _ := pressKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if got := lastNotice(md); got != "permission denied" {
		t.Errorf("deny notice = %q", got)
	}
}

// TestApprovalCycleThreeButtons: tab/right cycles focus over the visible verdict
// set; left retreats; enter resolves the focused verdict.
func TestApprovalCycleThreeButtons(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "sess-test-0001:1:c1", Tool: "Shell", offerAlways: true})
	if got := approvalSurfaceOf(t, m).ask.focusedVerdict; got != client.VerdictAllowOnce {
		t.Fatalf("initial focused verdict = %v, want allow once", got)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := approvalSurfaceOf(t, m).ask.focusedVerdict; got != client.VerdictAllowAlways {
		t.Fatalf("after tab focused verdict = %v, want allow always", got)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyRight})
	if got := approvalSurfaceOf(t, m).ask.focusedVerdict; got != client.VerdictDeny {
		t.Fatalf("after right focused verdict = %v, want deny", got)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyRight})
	if got := approvalSurfaceOf(t, m).ask.focusedVerdict; got != client.VerdictAllowOnce {
		t.Fatalf("after wrap focused verdict = %v, want allow once", got)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if got := approvalSurfaceOf(t, m).ask.focusedVerdict; got != client.VerdictDeny {
		t.Fatalf("after left-wrap focused verdict = %v, want deny", got)
	}
	// Enter on deny resolves as deny.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := lastNotice(m); got != "permission denied" {
		t.Errorf("enter on deny notice = %q", got)
	}
}

// TestApprovalCycleTwoButtons: with no always offered, tab skips Allow Always.
func TestApprovalCycleTwoButtons(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "subagent-c1:1:k1", Tool: "Shell", offerAlways: false})
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := approvalSurfaceOf(t, m).ask.focusedVerdict; got != client.VerdictDeny {
		t.Fatalf("after tab focused verdict = %v, want deny — always is skipped", got)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := approvalSurfaceOf(t, m).ask.focusedVerdict; got != client.VerdictAllowOnce {
		t.Fatalf("after wrap focused verdict = %v, want allow once", got)
	}
	// enter on allow resolves allow-once.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := lastNotice(m); got != "permission allowed" {
		t.Errorf("enter on allow notice = %q (want allow-once)", got)
	}
	if !strings.HasPrefix(lastNotice(m), "permission allowed") {
		t.Errorf("two-button enter must not be always-allow")
	}
}

func TestControlRefusedRestoresByExactAskIDAcrossRunMismatch(t *testing.T) {
	ask := pendingAsk{AskID: "ask-current", Tool: "Shell", expectedRunID: "run-old"}
	m := approvalModel(t, ask)
	intent := &approvalResolvedIntent{ask: ask, askID: ask.AskID, expectedRunID: ask.expectedRunID, resume: phaseRunning}
	m.pendingApproval = intent
	approvalSurfaceOf(t, m).ask = pendingAsk{}

	if m.restoreControlRefused(client.ControlRefusedMsg{AskID: "ask-other", RunID: "run-new", Text: "stale"}) {
		t.Fatal("unrelated refusal restored the pending approval")
	}
	if !m.restoreControlRefused(client.ControlRefusedMsg{AskID: ask.AskID, RunID: "run-new", Text: "stale"}) {
		t.Fatal("matching refusal was dropped because the active run changed")
	}
	if got := approvalSurfaceOf(t, m).ask.AskID; got != ask.AskID {
		t.Fatalf("restored ask = %q, want %q", got, ask.AskID)
	}
}

func TestApprovalClickResolvesMappedVerdict(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "sess-test-0001:1:c1", Tool: "Shell", offerAlways: true})
	s := approvalSurfaceOf(t, m)
	s.ask.focusedVerdict = client.VerdictDeny
	_, _ = s.Render(100, 30)

	var allowHit HitID
	for id, verdict := range s.hits {
		if verdict == client.VerdictAllowOnce {
			allowHit = id
			break
		}
	}
	if allowHit == 0 {
		t.Fatal("render did not map an Allow Once button hit")
	}
	if _, handled, _ := s.HandleMsg(surfaceHitMsg{ID: allowHit}); !handled {
		t.Fatal("allow button hit was not handled")
	}
	intent, ok := s.takeSurfaceIntent().(approvalResolvedIntent)
	if !ok || intent.verdict != client.VerdictAllowOnce {
		t.Fatalf("click intent = %#v, want Allow Once resolution", intent)
	}
}
