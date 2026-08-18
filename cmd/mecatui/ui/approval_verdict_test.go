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
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-test-0001"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.phase = phaseAwaitingApproval
	m.approval.ask = ask
	return m
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
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-abc"

	m1 := applyAll(m, client.PermissionAskMsg{AskID: "sess-abc:1:c1", Tool: "Bash"})
	if !m1.approval.ask.offerAlways {
		t.Error("a main-agent ask should offer always-allow")
	}
	m2 := applyAll(m, client.PermissionAskMsg{AskID: "subagent-c1:1:k1", Tool: "Bash"})
	if m2.approval.ask.offerAlways {
		t.Error("a surfaced child ask must NOT offer always-allow")
	}
}

// TestResolveAskResetsArgsViewState pins the lifecycle contract (issue #488):
// resolving an ask whose args view/mini-viewport were used resets BOTH the
// full-screen view state and the modal's mini-viewport offset, so the next ask
// never inherits a stale scroll position or an open view.
func TestResolveAskResetsArgsViewState(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, longBashArgs))
	m.approval.argsViewRaw = true
	m.approval.askVPOffset = 3
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.approval.argsViewOpen || m.approval.argsVPReady {
		t.Error("resolve must close the full-screen args view")
	}
	if m.approval.argsViewRaw {
		t.Error("resolve must reset the raw toggle")
	}
	if m.approval.askVPOffset != 0 {
		t.Errorf("resolve must reset the mini-viewport offset, got %d", m.approval.askVPOffset)
	}
}

// TestApprovalWResolvesAlways: with always-allow offered, 'w' resolves the modal as
// always-allow and records the always notice.
func TestApprovalWResolvesAlways(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "sess-test-0001:1:c1", Tool: "Bash", offerAlways: true})
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
	m := approvalModel(t, pendingAsk{AskID: "subagent-c1:1:k1", Tool: "Bash", offerAlways: false})
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
	m := approvalModel(t, pendingAsk{AskID: "sess-test-0001:1:c1", Tool: "Bash", offerAlways: true})
	ma, _ := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if got := lastNotice(ma); got != "permission allowed" {
		t.Errorf("allow notice = %q", got)
	}
	md, _ := pressKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if got := lastNotice(md); got != "permission denied" {
		t.Errorf("deny notice = %q", got)
	}
}

// TestApprovalCycleThreeButtons: tab/right cycles focus over {0,1,2}; left retreats;
// enter resolves the focused button.
func TestApprovalCycleThreeButtons(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "sess-test-0001:1:c1", Tool: "Bash", offerAlways: true})
	if m.approval.ask.focus != 0 {
		t.Fatalf("initial focus = %d, want 0", m.approval.ask.focus)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.approval.ask.focus != 1 {
		t.Fatalf("after tab focus = %d, want 1 (always)", m.approval.ask.focus)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.approval.ask.focus != 2 {
		t.Fatalf("after right focus = %d, want 2 (deny)", m.approval.ask.focus)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.approval.ask.focus != 0 {
		t.Fatalf("after wrap focus = %d, want 0 (allow)", m.approval.ask.focus)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.approval.ask.focus != 2 {
		t.Fatalf("after left-wrap focus = %d, want 2 (deny)", m.approval.ask.focus)
	}
	// enter on deny resolves as deny.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := lastNotice(m); got != "permission denied" {
		t.Errorf("enter on deny notice = %q", got)
	}
}

// TestApprovalCycleTwoButtons: with no always offered, the focus ring is {0,2} —
// tab skips the (absent) always button.
func TestApprovalCycleTwoButtons(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "subagent-c1:1:k1", Tool: "Bash", offerAlways: false})
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.approval.ask.focus != 2 {
		t.Fatalf("after tab focus = %d, want 2 (deny) — always is skipped", m.approval.ask.focus)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.approval.ask.focus != 0 {
		t.Fatalf("after wrap focus = %d, want 0 (allow)", m.approval.ask.focus)
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
