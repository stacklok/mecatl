package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// args_view_test.go covers the full-screen ask-args view (issue #488): ctrl+t
// routing by ask type, the pretty/raw toggle, scroll/close/verdict keys inside
// the view, lifecycle (advance/retract/endRun), resize re-wrap, tiny terminals,
// hostile payloads, and the goldens for the pretty/raw/scrolled/modal frames.

// longBashArgs is a long single-line pipeline (~400 chars) whose wrapped
// rendering exercises the args view and the modal mini-viewport. It carries a
// non-zero timeout_ms so the pretty tier's muted timeout annotation renders
// (it must never appear in the verbatim raw tier).
const longBashArgs = `{"command":"find . -name '*.go' -not -path './vendor/*' -print0 | xargs -0 grep -nH 'func Test' | awk -F: '{print $1}' | sort | uniq -c | sort -rn | head -40 | while read -r count file; do printf '%5d  %s\\n' \"$count\" \"$file\"; done | tee /tmp/test-counts.txt | column -t -s' '","timeout_ms":60000}`

// tallBashArgs is a multi-line heredoc-style command with enough real newlines
// (40 source lines) that the full-screen args view has genuine scroll room.
var tallBashArgs = `{"command":"` + strings.Join(tallBashCommandLines(40), `\n`) + `"}`

// bashAskModel builds a connected, awaiting-approval Model with a long-args
// Bash ask (mirroring approvalModel, with the ask populated).
func bashAskModel(t *testing.T, args string) Model {
	t.Helper()
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-test-0001"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.phase = phaseAwaitingApproval
	m.approval.ask = pendingAsk{
		AskID:       "sess-test-0001:1:bash-1",
		Tool:        "Bash",
		Args:        args,
		Reason:      "Bash requires approval",
		offerAlways: true,
	}
	return m
}

// openArgsView drives the ctrl+t keypress that opens the full-screen args view.
func openArgsView(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !m.approval.argsViewOpen {
		t.Fatal("ctrl+t on a non-diff ask must open the full-screen args view")
	}
	if !m.approval.argsVPReady {
		t.Fatal("the args viewport must be populated after ctrl+t")
	}
	return m
}

// TestCtrlTOpensArgsViewForBashAsk pins the ctrl+t routing: a non-diff,
// non-plan ask opens the full-screen args view WITHOUT touching m.expandTools.
func TestCtrlTOpensArgsViewForBashAsk(t *testing.T) {
	m := bashAskModel(t, longBashArgs)
	before := m.expandTools
	m = openArgsView(t, m)
	if m.expandTools != before {
		t.Errorf("ctrl+t on a non-diff ask must NOT toggle expandTools (%v → %v)", before, m.expandTools)
	}
	if m.phase != phaseAwaitingApproval {
		t.Errorf("phase must stay phaseAwaitingApproval with the view open, got %v", m.phase)
	}
	got := stripANSIstr(m.View().Content)
	if !strings.Contains(got, "Ask args: Bash") {
		t.Errorf("the args view must render its title, got %q", got)
	}
	if !strings.Contains(got, "find . -name") {
		t.Errorf("the args view must render the command, got %q", got)
	}
}

// TestCtrlTPlanAskUnchanged is the regression guard: a plan ask's ctrl+t keeps
// toggling expandTools and never opens the args view.
func TestCtrlTPlanAskUnchanged(t *testing.T) {
	m := planAskModel(t, true)
	before := m.expandTools
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if m.approval.argsViewOpen {
		t.Error("ctrl+t on a plan ask must NOT open the args view")
	}
	if m.expandTools == before {
		t.Error("ctrl+t on a plan ask must keep toggling expandTools")
	}
}

// TestCtrlTEditAskKeepsModalExpand pins that an Edit (diff-capable) ask keeps
// the in-modal expand behaviour: ctrl+t toggles expandTools, no args view.
func TestCtrlTEditAskKeepsModalExpand(t *testing.T) {
	m := approvalModel(t, pendingAsk{
		AskID: "sess-test-0001:1:edit-1",
		Tool:  "Edit",
		Args:  `{"path":"main.go","old_string":"a","new_string":"b"}`,
	})
	before := m.expandTools
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if m.approval.argsViewOpen {
		t.Error("ctrl+t on an Edit ask must NOT open the args view")
	}
	if m.expandTools == before {
		t.Error("ctrl+t on an Edit ask must keep toggling expandTools")
	}
}

// TestArgsViewRawToggle pins the RawArgs (r) toggle: inside the view, r flips
// pretty→raw (the VERBATIM wire args string appears) and back. The hint's raw
// clause renders whenever the tiers genuinely differ (a Bash ask, or any ask
// whose pretty tier re-indents the verbatim raw) and hides only when the tiers
// are byte-identical.
func TestArgsViewRawToggle(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, longBashArgs))
	pretty := stripANSIstr(m.View().Content)
	if strings.Contains(pretty, `"command"`) {
		t.Errorf("the pretty tier must not show the JSON envelope, got %q", pretty)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if !m.approval.argsViewRaw {
		t.Fatal("r inside the args view must set argsViewRaw")
	}
	raw := stripANSIstr(m.View().Content)
	if !strings.Contains(raw, `{"command":"find . -name`) {
		t.Errorf("the raw tier must show the verbatim wire args, got %q", raw)
	}
	if strings.Contains(raw, "timeout_ms: ") {
		t.Errorf("the raw tier must never carry the pretty tier's annotation, got %q", raw)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if m.approval.argsViewRaw {
		t.Fatal("a second r must toggle back to pretty")
	}

	// Non-Bash ask with re-indenting JSON args: the tiers DO differ (verbatim
	// single-line raw vs prettyJSON pretty), so the raw clause advertises and r
	// visibly toggles.
	m2 := openArgsView(t, bashAskModel(t, `{"url":"https://example.com"}`))
	m2.approval.ask.Tool = "WebFetch"
	(&m2).openAskArgsView(m2.approval.ask, 0)
	before := stripANSIstr(m2.View().Content)
	if !strings.Contains(before, "raw|pretty") {
		t.Errorf("differing tiers must advertise the raw toggle, got %q", before)
	}
	m2, _ = pressKey(m2, tea.KeyPressMsg{Code: 'r', Text: "r"})
	after := stripANSIstr(m2.View().Content)
	if before == after {
		t.Errorf("r on a non-Bash ask with differing tiers must toggle visibly")
	}
	if !strings.Contains(after, `{"url":"https://example.com"}`) {
		t.Errorf("the non-Bash raw tier must be the verbatim wire args, got %q", after)
	}
}

// TestArgsViewRawTierIsVerbatim pins the raw tier directly on askArgsContent:
// it is the literal wire args string (sanitized, verbatim), never prettyJSON.
func TestArgsViewRawTierIsVerbatim(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	const wire = `{"command":"echo a\n echo b","timeout_ms":1200}`
	_, raw, ok := askArgsContent(th, pendingAsk{Tool: "Bash", Args: wire})
	if !ok {
		t.Fatal("a Bash ask must be args-view capable")
	}
	if raw != wire {
		t.Errorf("raw tier = %q, want the verbatim wire string %q", raw, wire)
	}
	// Control bytes are still neutralized on the raw tier.
	_, raw2, _ := askArgsContent(th, pendingAsk{Tool: "Bash", Args: "{\"c\":\"x\"}\x1b[2J"})
	if strings.Contains(raw2, "\x1b") {
		t.Errorf("the raw tier must neutralize control bytes, got %q", raw2)
	}
}

// TestArgsViewEscReturnsToModal pins that esc closes the view back to the
// modal (phase stays awaitingApproval, the modal renders again).
func TestArgsViewEscReturnsToModal(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, longBashArgs))
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.approval.argsViewOpen {
		t.Error("esc must close the args view")
	}
	if m.phase != phaseAwaitingApproval {
		t.Errorf("esc must return to the modal (phaseAwaitingApproval), got %v", m.phase)
	}
	got := stripANSIstr(m.View().Content)
	if !strings.Contains(got, "Permission required") {
		t.Errorf("after esc the centered modal must render again, got %q", got)
	}
}

// TestArgsViewCtrlTClosesView pins the OTHER close key: ctrl+t inside the args
// view toggles back to the modal (onExpandToolsKey's argsViewOpen branch), with
// the modal rendering again — like esc, and without touching expandTools.
func TestArgsViewCtrlTClosesView(t *testing.T) {
	m := bashAskModel(t, longBashArgs)
	before := m.expandTools
	m = openArgsView(t, m)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if m.approval.argsViewOpen || m.approval.argsVPReady {
		t.Error("ctrl+t inside the args view must close it back to the modal")
	}
	if m.expandTools != before {
		t.Errorf("ctrl+t inside the args view must NOT toggle expandTools (%v → %v)", before, m.expandTools)
	}
	if m.phase != phaseAwaitingApproval {
		t.Errorf("ctrl+t close must return to the modal (phaseAwaitingApproval), got %v", m.phase)
	}
	got := stripANSIstr(m.View().Content)
	if !strings.Contains(got, "Permission required") {
		t.Errorf("after ctrl+t the centered modal must render again, got %q", got)
	}
}

// TestArgsViewHomeEndJump pins argsScroll's GotoTop/GotoBottom arms:
// ScrollTop (home) jumps to the top and ScrollBottom (end) to the bottom of
// tall args (the pinned action bar's hint advertises them).
func TestArgsViewHomeEndJump(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, tallBashArgs))
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if bottom := m.approval.argsVP.YOffset(); bottom == 0 {
		t.Fatal("end inside the args view must jump to the bottom of tall args")
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if m.approval.argsVP.YOffset() != 0 {
		t.Errorf("home inside the args view must jump back to the top, got %d", m.approval.argsVP.YOffset())
	}
}

// TestArgsViewVerdictKeysResolveFromInside pins that the verdict keys resolve
// the ask from inside the view (the operator can read the full args, then act).
func TestArgsViewVerdictKeysResolveFromInside(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, longBashArgs))
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.phase != phaseRunning {
		t.Errorf("allow from inside the view must resolve → phaseRunning, got %v", m.phase)
	}
	if got := lastNotice(m); got != "permission allowed" {
		t.Errorf("allow notice = %q", got)
	}
	if m.approval.argsViewOpen || m.approval.argsVPReady {
		t.Error("resolving must tear down the args view")
	}
}

// TestArgsViewQueuedSuccessorClosesView pins that a queued successor advancing
// while the view is open closes the view and shows the successor's modal.
func TestArgsViewQueuedSuccessorClosesView(t *testing.T) {
	m := bashAskModel(t, longBashArgs)
	m = applyAll(m, client.PermissionAskMsg{AskID: "sess-test-0001:1:write-2", Tool: "Write", Args: `{"path":"n.txt","content":"x"}`})
	m = openArgsView(t, m)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.approval.argsViewOpen {
		t.Error("advancing to a queued successor must close the args view")
	}
	if m.approval.ask.AskID != "sess-test-0001:1:write-2" {
		t.Fatalf("the successor must take the head, got %q", m.approval.ask.AskID)
	}
	if m.phase != phaseAwaitingApproval {
		t.Fatalf("phase must stay awaitingApproval with a successor, got %v", m.phase)
	}
	if m.approval.askVPOffset != 0 {
		t.Errorf("the mini-viewport offset must reset on advance, got %d", m.approval.askVPOffset)
	}
}

// TestArgsViewRetractWhileOpenBehavesLikeResolve pins that retracting the
// visible ask while its args view is open tears the view down symmetrically.
func TestArgsViewRetractWhileOpenBehavesLikeResolve(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, longBashArgs))
	m = applyAll(m, client.PermissionRetractMsg{AskID: "sess-test-0001:1:bash-1"})
	if m.approval.argsViewOpen || m.approval.argsVPReady {
		t.Error("a retract while the view is open must tear it down")
	}
	if m.phase != phaseRunning {
		t.Errorf("retracting the last ask must return to running, got %v", m.phase)
	}
	if m.approval.askVPOffset != 0 {
		t.Errorf("the mini-viewport offset must reset on retract, got %d", m.approval.askVPOffset)
	}
}

// TestArgsViewMouseWheelScrollsArgsVP pins that the mouse wheel routes to the
// args viewport (not the conversation viewport) while the view is open.
func TestArgsViewMouseWheelScrollsArgsVP(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, tallBashArgs))
	if m.approval.argsVP.YOffset() != 0 {
		t.Fatal("precondition: the args view opens at the top")
	}
	vpBefore := m.vp.YOffset()
	mm, _ := m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 10, Y: 10})
	m = mm.(Model)
	if m.approval.argsVP.YOffset() == 0 {
		t.Error("a wheel-down over the open args view must scroll the args viewport")
	}
	if m.vp.YOffset() != vpBefore {
		t.Errorf("the wheel must not scroll the conversation viewport (%d → %d)", vpBefore, m.vp.YOffset())
	}
}

// TestArgsViewResizePreservesYOffset pins that a resize mid-view re-wraps the
// args at the new width while preserving the operator's scroll offset.
func TestArgsViewResizePreservesYOffset(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, tallBashArgs))
	m.approval.argsVP.SetYOffset(2)
	if m.approval.argsVP.YOffset() != 2 {
		t.Fatalf("precondition: the tall content must admit YOffset 2, got %d", m.approval.argsVP.YOffset())
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 60, Height: 30})
	if !m.approval.argsViewOpen || !m.approval.argsVPReady {
		t.Fatal("the args view must survive a resize")
	}
	if m.approval.argsVPWidth != 60 {
		t.Errorf("the args viewport must re-populate at the new width, got %d", m.approval.argsVPWidth)
	}
	if m.approval.argsVP.YOffset() != 2 {
		t.Errorf("a resize must preserve the YOffset, got %d", m.approval.argsVP.YOffset())
	}
}

// TestModalMiniViewportScrollRenders is the regression test for the live bug
// where scrolling the modal's args mini-viewport moved askVPOffset but the
// rendered frame never changed: renderPermissionModal hardcoded argsOffset=0,
// so only the hit-test path saw the offset. The render path must thread the
// live offset so a scroll visibly re-renders the modal.
func TestModalMiniViewportScrollRenders(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m = applyAll(m, tea.WindowSizeMsg{Width: 121, Height: 38})
	m.sessionID = "sess-test-0001"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.phase = phaseAwaitingApproval
	m.approval.ask = pendingAsk{AskID: "sess-test-0001:1:bash-1", Tool: "Bash", Args: tallBashArgs, Reason: "Bash requires approval", offerAlways: true}
	if m.askArgsMiniScrollRange() <= 0 {
		t.Fatal("precondition: tall args must have hidden rows to scroll")
	}
	top := stripANSIstr(m.View().Content)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.approval.askVPOffset == 0 {
		t.Fatal("precondition: pgdn must move the mini-viewport offset")
	}
	scrolled := stripANSIstr(m.View().Content)
	if top == scrolled {
		t.Error("scrolling the modal mini-viewport must visibly re-render the frame (renderPermissionModal must thread askVPOffset)")
	}
}

// TestArgsViewTinyTerminalKeepsCardOnScreen pins the tiny-terminal contract: a
// 24-row terminal's modal grows ONLY by the bounded args mini-viewport (never
// by the full args) — the body stays within the reserve + the region-budgeted
// cap even though centerCard clamps the card to the region origin.
func TestArgsViewTinyTerminalKeepsCardOnScreen(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.sessionID = "sess-test-0001"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.phase = phaseAwaitingApproval
	m.approval.ask = pendingAsk{AskID: "sess-test-0001:1:bash-1", Tool: "Bash", Args: longBashArgs, Reason: "Bash requires approval", offerAlways: true}
	body := m.renderBody()
	// The args region is budgeted to (region - reserve) rows, so the whole body
	// (before the askCard frame) can never exceed reserve + that budget — the
	// modal stays bounded on a tiny terminal instead of growing with the args.
	bound := permissionModalBodyReserve + max(1, m.vp.Height()-permissionModalBodyReserve)
	if got := lipgloss.Height(body) - 4; got > bound { // -4: the askCard frame is outside the body reserve
		t.Errorf("body height %d exceeds the bounded reserve %d on a 24-row terminal", got, bound)
	}
	// And the args mini-viewport itself never renders more rows than its
	// region-budgeted view: the hint line proves rows were hidden.
	if !strings.Contains(stripANSIstr(body), "ctrl+t full args") {
		t.Errorf("a tiny-terminal long-args modal must render the capped hint, got %q", stripANSIstr(body))
	}
}

// TestArgsViewHostilePayload pins that control bytes in the ask args are
// sanitized before they reach the args view (CWE-150, reusing escapePayload).
func TestArgsViewHostilePayload(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, `{"command":"cat `+escapePayload+`"}`))
	raw := []byte(m.View().Content)
	residual := ansiRE.ReplaceAll(raw, nil)
	if strings.Contains(string(residual), "\x1b") {
		t.Fatalf("a server escape survived into the args view:\n%q", residual)
	}
}

// TestArgsViewFocusUnchanged pins that opening/closing the args view does not
// touch the modal's button focus.
func TestArgsViewFocusUnchanged(t *testing.T) {
	m := bashAskModel(t, longBashArgs)
	m.approval.ask.focus = 2
	m = openArgsView(t, m)
	if m.approval.ask.focus != 2 {
		t.Errorf("opening the args view must not move the focus, got %d", m.approval.ask.focus)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.approval.ask.focus != 2 {
		t.Errorf("closing the args view must not move the focus, got %d", m.approval.ask.focus)
	}
}

// TestRawArgsReboundChordToggles pins that a RawArgs key override actually
// rewires the toggle inside the args view: with RawArgs rebound to ctrl+f20,
// the DEFAULT r is a no-op and ONLY the new chord flips argsViewRaw — all
// driven via pressKey through the real Model update path (the KeyOverrides
// wiring lands in New).
func TestRawArgsReboundChordToggles(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
		KeyOverrides: map[string][]string{"RawArgs": {"ctrl+f20"}}})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-test-0001"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.phase = phaseAwaitingApproval
	m.approval.ask = pendingAsk{AskID: "sess-test-0001:1:bash-1", Tool: "Bash", Args: longBashArgs, Reason: "Bash requires approval", offerAlways: true}
	m = openArgsView(t, m)
	// The default chord r is now inert inside the view.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if m.approval.argsViewRaw {
		t.Error("the default r must be inert once RawArgs is rebound")
	}
	// The rebound chord toggles raw on, then off.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyF20, Mod: tea.ModCtrl})
	if !m.approval.argsViewRaw {
		t.Error("the rebound ctrl+f20 must toggle raw on")
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyF20, Mod: tea.ModCtrl})
	if m.approval.argsViewRaw {
		t.Error("a second ctrl+f20 must toggle raw off")
	}
}

// TestBareRInPlainModalDoesNothing pins the shadowing contract: a bare r in
// the plain modal (view closed) does nothing — only the args view consults
// RawArgs.
func TestBareRInPlainModalDoesNothing(t *testing.T) {
	m := bashAskModel(t, longBashArgs)
	before := lastNotice(m)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if m.approval.argsViewOpen || m.approval.argsViewRaw {
		t.Error("a bare r in the plain modal must not open/toggle anything")
	}
	if m.phase != phaseAwaitingApproval {
		t.Errorf("a bare r must not resolve the modal, got %v", m.phase)
	}
	if got := lastNotice(m); got != before {
		t.Errorf("a bare r must record no notice, got %q", got)
	}
}

// TestModalMiniViewportScrollKeys pins that pgdn/up/down scroll the in-card
// args mini-viewport when rows are hidden, and no-op when nothing is hidden.
func TestModalMiniViewportScrollKeys(t *testing.T) {
	var cmdLines []string
	for i := 0; i < 20; i++ {
		cmdLines = append(cmdLines, "echo line"+string(rune('a'+i)))
	}
	m := bashAskModel(t, `{"command":"`+strings.Join(cmdLines, `\n`)+`"}`)
	if maxOff := m.askArgsMiniScrollRange(); maxOff <= 0 {
		t.Fatal("precondition: the args must overflow the mini-viewport")
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.approval.askVPOffset != 1 {
		t.Errorf("down must move the mini-viewport by one row, got %d", m.approval.askVPOffset)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.approval.askVPOffset != 4 {
		t.Errorf("pgdn must move the mini-viewport by three rows, got %d", m.approval.askVPOffset)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.approval.askVPOffset != 1 {
		t.Errorf("pgup must retreat the mini-viewport by three rows, got %d", m.approval.askVPOffset)
	}
	// Scrolling PAST the bound pins at maxOff (no overshoot, no blank rows);
	// scrolling past the bottom then hammering pgup pins back at 0.
	maxOff := m.askArgsMiniScrollRange()
	for i := 0; i < 5; i++ {
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	if m.approval.askVPOffset != maxOff {
		t.Errorf("scrolling past the bound must pin at maxOff %d, got %d", maxOff, m.approval.askVPOffset)
	}
	for i := 0; i < 10; i++ {
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	if m.approval.askVPOffset != 0 {
		t.Errorf("scrolling past the top must pin at 0, got %d", m.approval.askVPOffset)
	}
	// A short-args ask: nothing hidden → the scroll keys no-op AND fall through
	// (they must not be consumed).
	short := bashAskModel(t, `{"command":"ls"}`)
	if maxOff := short.askArgsMiniScrollRange(); maxOff != 0 {
		t.Fatalf("a short ask must have no hidden rows, got max %d", maxOff)
	}
	short, _ = pressKey(short, tea.KeyPressMsg{Code: tea.KeyDown})
	if short.approval.askVPOffset != 0 {
		t.Errorf("a short ask's mini-viewport must not scroll, got %d", short.approval.askVPOffset)
	}
}

// TestModalWheelPrecedence pins the wheel routing with the modal open (args
// view closed): over the card → the in-card mini-viewport scrolls (not the
// conversation); elsewhere → the conversation viewport scrolls.
func TestModalWheelPrecedence(t *testing.T) {
	var cmdLines []string
	for i := 0; i < 20; i++ {
		cmdLines = append(cmdLines, "echo line"+string(rune('a'+i)))
	}
	m := bashAskModel(t, `{"command":"`+strings.Join(cmdLines, `\n`)+`"}`)
	m.conv.addUser("fill the conversation so it can scroll")
	for i := 0; i < 30; i++ {
		m.conv.addUser(strings.Repeat("row ", 10))
	}
	m.refreshView()

	// Find the card rect via the hit-test geometry.
	body, _ := m.permissionModalBody()
	style := m.deps.Theme.Style("askCard")
	card := style.Render(body)
	cardW, cardH := lipgloss.Width(card), lipgloss.Height(card)
	originX, originY := centeredCardOrigin(cardW, cardH, m.width, m.vp.Height())
	top := convTopRow(m)
	cardX, cardY := originX+cardW/2, top+originY+cardH/2

	// Wheel over the card → the mini-viewport moves, the conversation doesn't.
	vpBefore := m.vp.YOffset()
	mm, _ := m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: cardX, Y: cardY})
	m = mm.(Model)
	if m.approval.askVPOffset == 0 {
		t.Error("a wheel-down over the card must scroll the args mini-viewport")
	}
	if m.vp.YOffset() != vpBefore {
		t.Errorf("a wheel over the card must NOT scroll the conversation (%d → %d)", vpBefore, m.vp.YOffset())
	}

	// Wheel outside the card → the conversation viewport moves, mini-viewport stays.
	offBefore := m.approval.askVPOffset
	// The conversation is at-bottom; scroll UP to see movement.
	mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: m.width - 1, Y: m.height - 1})
	m = mm.(Model)
	if m.approval.askVPOffset != offBefore {
		t.Errorf("a wheel off the card must NOT move the mini-viewport (%d → %d)", offBefore, m.approval.askVPOffset)
	}
}

// --- Goldens (same compareGolden seam as help_test.go) ---

// TestAskArgsViewPrettyGolden locks the full-screen args view's pretty frame.
func TestAskArgsViewPrettyGolden(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, longBashArgs))
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "askargs_view_pretty.golden", got)
}

// TestAskArgsViewRawGolden locks the full-screen args view's raw frame.
func TestAskArgsViewRawGolden(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, longBashArgs))
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "askargs_view_raw.golden", got)
}

// TestAskArgsViewScrolledGolden locks the args view after scrolling down. The
// fixture is tall (40 source lines) so a real offset holds — scrolling content
// shorter than the viewport would clamp the offset to 0 and make this golden
// byte-identical to the pretty one (a vacuous test that passes with broken
// scrolling). The first visible line must differ from the pretty golden's.
func TestAskArgsViewScrolledGolden(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, tallBashArgs))
	m.approval.argsVP.SetYOffset(2)
	if m.approval.argsVP.YOffset() != 2 {
		t.Fatalf("the tall fixture must admit YOffset 2, got %d — the golden would be vacuous", m.approval.argsVP.YOffset())
	}
	got := string(stripANSI([]byte(m.View().Content)))
	prettyGolden, err := os.ReadFile(filepath.Join("testdata", "askargs_view_pretty.golden")) //nolint:gosec // test golden
	if err != nil {
		t.Fatalf("read pretty golden: %v", err)
	}
	firstLine := func(s string) string { return strings.SplitN(s, "\n", 4)[2] } // [0] header [1] rule [2] first view row
	if firstLine(got) == firstLine(string(prettyGolden)) {
		t.Errorf("the scrolled golden's first visible line %q must differ from the pretty golden's", firstLine(got))
	}
	compareGolden(t, "askargs_view_scrolled.golden", []byte(got))
}

// TestAskArgsModalCappedHintGolden locks the capped modal WITH its hint line:
// a TALL ask (a heredoc-style command, >6 wrapped args rows) renders exactly
// permissionModalArgsMaxLines args rows + the "… scroll · ctrl+t full args"
// hint (askargs_modal_longbash.golden covers the uncapped 3-row shape). It is
// keyed on renderPermissionModal directly with an EXPLICIT height (as
// ask_queue_test.go does) — the full-View fixture's body region is clamped to
// ~20 rows at 100x30, so the region-budget cap (reserve 16) yields 4 rows, not
// the 6-row cap this golden exists to lock.
func TestAskArgsModalCappedHintGolden(t *testing.T) {
	args := `{"command":"` + strings.Join(tallBashCommandLines(14), `\n`) + `"}`
	m := bashAskModel(t, args) // the live help-key markings ride the renderer
	plain := stripANSIstr(m.rend.renderPermissionModal(m.approval.ask, false, 0, 100, 40, 0))
	if n := strings.Count(plain, "echo step-"); n != permissionModalArgsMaxLines {
		t.Fatalf("the modal must render exactly %d args rows, got %d", permissionModalArgsMaxLines, n)
	}
	if !strings.Contains(plain, "pgup/pgdn scroll · ctrl+t full args") {
		t.Fatalf("the capped modal must render the scroll/full-args hint, got %q", plain)
	}
	compareGolden(t, "askargs_modal_capped_hint.golden", []byte(plain))
}

// tallBashCommandLines builds n heredoc-style command lines (the same shape
// tallBashArgs carries).
func tallBashCommandLines(n int) []string {
	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		lines = append(lines, "echo step-"+strings.Repeat("x", 4)+string(rune('a'+i%26))+"-"+strings.Repeat("y", i%7))
	}
	return lines
}

// TestAskArgsModalLongBashGolden locks the centered modal with a long-args
// Bash ask (the wrapped, capped mini-viewport + hint).
func TestAskArgsModalLongBashGolden(t *testing.T) {
	m := bashAskModel(t, longBashArgs)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "askargs_modal_longbash.golden", got)
}
