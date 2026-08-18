package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// planAskModel builds a connected, awaiting-approval Model with a PresentPlan ask.
// It mirrors the reducer's PermissionAskMsg path: setting m.approval.ask + m.phase AND
// populating the dedicated scrollable plan-review viewport (openPlanReviewView)
// so the render path reads a populated planVP. Tests that set m.approval.ask directly
// without this helper must also call openPlanReviewView, or the plan-review view
// renders only its pinned action bar.
func planAskModel(t *testing.T, offerAlways bool) Model {
	t.Helper()
	m := New(Deps{
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-test-0001"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.phase = phaseAwaitingApproval
	m.approval.ask = pendingAsk{
		AskID:       "sess-test-0001:1:presentplan-1",
		Tool:        "PresentPlan",
		Reason:      "Plan mode requires approval to execute.",
		offerAlways: offerAlways,
	}
	// Populate the plan-review viewport exactly as the PermissionAskMsg reducer
	// does (the helper sets m.approval.ask directly, bypassing the reducer). A test that
	// later mutates m.approval.ask.Args MUST re-call openPlanReviewView to re-populate.
	(&m).openPlanReviewView(m.approval.ask, 0, m.effectiveModel.ModelID)
	return m
}

// setPlanArgs sets the plan ask's Args JSON and re-populates the plan-review
// viewport so the next View() reflects the new plan content. Tests that drive a
// plan ask through the reducer (PermissionAskMsg) don't need this — the reducer
// calls openPlanReviewView; this is for tests that mutate m.approval.ask.Args directly
// after planAskModel (which bypasses the reducer). It mirrors the reducer's
// Args-carrying population path exactly.
func setPlanArgs(t *testing.T, m *Model, args string) {
	t.Helper()
	m.approval.ask.Args = args
	m.openPlanReviewView(m.approval.ask, len(m.approval.queue), m.effectiveModel.ModelID)
}

func TestIsPlanAsk(t *testing.T) {
	if !isPlanAsk("PresentPlan") {
		t.Error("isPlanAsk(PresentPlan) = false, want true")
	}
	if isPlanAsk("Bash") {
		t.Error("isPlanAsk(Bash) = true, want false")
	}
	if isPlanAsk("Write") {
		t.Error("isPlanAsk(Write) = true, want false")
	}
	if isPlanAsk("") {
		t.Error("isPlanAsk('') = true, want false")
	}
}

func TestPlanAskRendersPlanApprovalTitle(t *testing.T) {
	m := planAskModel(t, true)
	got := stripANSIstr(m.View().Content)
	if strings.Contains(got, "Permission required") {
		t.Errorf("plan ask must NOT render 'Permission required', got %q", got)
	}
	if !strings.Contains(got, "Plan ready for review") {
		t.Errorf("plan ask must render 'Plan ready for review', got %q", got)
	}
}

func TestPlanAskButtonCopy(t *testing.T) {
	m := planAskModel(t, true)
	got := stripANSIstr(m.View().Content)
	for _, want := range []string{
		"[A]pprove & run",
		"[W] auto-accept edits",
		"[D] iterate",
		"auto-accept allows every edit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan-approval modal missing %q in: %s", want, got)
		}
	}
}

func TestPlanAskNoAlwaysButtonCopy(t *testing.T) {
	// When not offered always (child ask, which doesn't happen in plan mode
	// but we test the fallback), the button copy omits the auto-accept button.
	m := planAskModel(t, false)
	got := stripANSIstr(m.View().Content)
	if strings.Contains(got, "[W] auto-accept") {
		t.Errorf("no-always plan ask must NOT render the auto-accept button: %s", got)
	}
	if !strings.Contains(got, "[A]pprove & run") {
		t.Error("no-always plan ask must still render 'Approve & run'")
	}
	if !strings.Contains(got, "[D] iterate") {
		t.Error("no-always plan ask must still render 'iterate'")
	}
}

func TestPlanAskFooterLabel(t *testing.T) {
	m := planAskModel(t, true)
	got := stripANSIstr(m.renderFooter())
	if !strings.Contains(got, "plan review") {
		t.Errorf("plan ask footer must show 'plan review', got %q", got)
	}
	if strings.Contains(got, "awaiting approval") {
		t.Errorf("plan ask footer must NOT show 'awaiting approval', got %q", got)
	}
}

func TestPlanAskFooterHelpLine(t *testing.T) {
	m := planAskModel(t, true)
	got := stripANSIstr(m.renderFooter())
	for _, want := range []string{
		"A approve & run",
		"W auto-accept",
		"D iterate",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan ask footer help missing %q in: %s", want, got)
		}
	}
}

func TestPlanAskApproveResolvesAllowOnce(t *testing.T) {
	m := planAskModel(t, true)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.phase != phaseRunning {
		t.Fatalf("approve must return to phaseRunning, got %v", m.phase)
	}
	if got := lastNotice(m); got != "permission allowed" {
		t.Errorf("allow-once notice = %q, want 'permission allowed'", got)
	}
}

func TestPlanAskAlwaysResolvesAllowAlways(t *testing.T) {
	m := planAskModel(t, true)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'w', Text: "w"})
	if m.phase != phaseRunning {
		t.Fatalf("always must return to phaseRunning, got %v", m.phase)
	}
	if got := lastNotice(m); got != "permission allowed (always, this session)" {
		t.Errorf("always-allow notice = %q", got)
	}
}

func TestPlanAskDenyIterates(t *testing.T) {
	m := planAskModel(t, true)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if m.phase != phaseRunning {
		t.Fatalf("deny must return to phaseRunning, got %v", m.phase)
	}
	if got := lastNotice(m); got != "permission denied" {
		t.Errorf("deny notice = %q", got)
	}
}

// TestPlanAskDenyThenIterateTerminalReturnsToIdle pins issue #206 iterate UX: after
// the operator presses [D] (iterate/deny), the resumed run terminates with
// StopPlanIterate, and the terminal ResultMsg drives endRun → phaseIdle so the input
// box is USABLE (the operator types the revision). This mirrors the approve path's
// StopPlanApproved → phaseIdle handoff but for the iterate pause: the run ENDS so
// the operator's next prompt drives the revision (vs the old behaviour where the
// model kept working in-turn).
func TestPlanAskDenyThenIterateTerminalReturnsToIdle(t *testing.T) {
	m := planAskModel(t, true)
	// Press [D] iterate — the verdict is sent and the run stays phaseRunning (the
	// resumed run is streaming toward its StopPlanIterate terminal).
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if m.phase != phaseRunning {
		t.Fatalf("deny must return to phaseRunning (run still streaming), got %v", m.phase)
	}
	// The resumed run terminates StopPlanIterate — the terminal ResultMsg lands.
	m = applyAll(m, client.ResultMsg{Stop: "plan_iterate"})
	if m.phase != phaseIdle {
		t.Fatalf("after StopPlanIterate the phase must be phaseIdle (input usable), got %v", m.phase)
	}
	// The footer must show the iterate label (muted/transient — awaiting feedback).
	got := stripANSIstr(m.renderFooter())
	if !strings.Contains(got, "plan iterate · awaiting your feedback") {
		t.Errorf("footer after StopPlanIterate = %q, want it to contain 'plan iterate · awaiting your feedback'", got)
	}
	// The textarea is focused (input usable): the cursor-blink state is on.
	if !m.ta.Focused() {
		t.Errorf("textarea must be focused after StopPlanIterate (operator types the revision)")
	}
}

func TestStopPlanApprovedFooterLabel(t *testing.T) {
	text, slot := stopReasonLabel("plan_approved")
	if text != "plan approved · executing" {
		t.Errorf("stopReasonLabel(plan_approved) text = %q, want 'plan approved · executing'", text)
	}
	if slot != "muted" {
		t.Errorf("stopReasonLabel(plan_approved) slot = %q, want 'muted'", slot)
	}
}

func TestStopPlanApprovedReachesFooter(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning
	m = applyAll(m, client.ResultMsg{Stop: "plan_approved"})
	got := stripANSIstr(m.renderFooter())
	if !strings.Contains(got, "plan approved · executing") {
		t.Errorf("ResultMsg{Stop:plan_approved} → footer = %q, want it to contain 'plan approved · executing'", got)
	}
}

func TestPlanAskQueueBadge(t *testing.T) {
	m := planAskModel(t, true)
	// Enqueue a second ask — the plan ask is the head, the queue has one entry.
	m.approval.queue = append(m.approval.queue, pendingAsk{AskID: "sess-test-0001:2:c2", Tool: "Bash"})
	// Re-populate the plan-review viewport so the title badge reflects the queue
	// (planAskModel populated it with queued=0; the badge lives in planVP's
	// header, which View renders from planVP.View()).
	(&m).openPlanReviewView(m.approval.ask, len(m.approval.queue), m.effectiveModel.ModelID)
	got := stripANSIstr(m.View().Content)
	if !strings.Contains(got, "Plan ready for review (1 of 2)") {
		t.Errorf("plan ask with queue must show '(1 of 2)' badge, got %q", got)
	}
	// Footer must also carry the badge.
	footer := stripANSIstr(m.renderFooter())
	if !strings.Contains(footer, "plan review (1 of 2)") {
		t.Errorf("plan ask footer with queue must show 'plan review (1 of 2)', got %q", footer)
	}
}

func TestGenericAskFooterIsUnchanged(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-test-0001"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.phase = phaseAwaitingApproval
	m.approval.ask = pendingAsk{AskID: "sess-test-0001:1:c1", Tool: "Bash", Reason: "Bash requires approval"}
	got := stripANSIstr(m.renderFooter())
	if !strings.Contains(got, "awaiting approval") {
		t.Errorf("generic ask footer must show 'awaiting approval', got %q", got)
	}
	if strings.Contains(got, "plan review") {
		t.Errorf("generic ask footer must NOT show 'plan review', got %q", got)
	}
	// The header help line must not leak plan-approval hints.
	if strings.Contains(got, "A approve & run") {
		t.Errorf("generic ask footer help must not show plan-approval copy: %q", got)
	}
}

func TestPlanAskRendersModelNames(t *testing.T) {
	m := planAskModel(t, true)
	m.effectiveModel = client.ResolvedModel{ModelID: "gpt-5", ProviderID: "openai"}
	// Re-populate the plan-review viewport so the model line reflects the now-set
	// effective model (planAskModel populated it with no model echo).
	(&m).openPlanReviewView(m.approval.ask, len(m.approval.queue), m.effectiveModel.ModelID)
	got := stripANSIstr(m.View().Content)
	if !strings.Contains(got, "plan model: gpt-5") {
		t.Errorf("plan modal should show the plan model, got %q", got)
	}
	if !strings.Contains(got, "execute model: session default model") {
		t.Errorf("plan modal should note the execute model runs on the session default, got %q", got)
	}
}

func TestPlanAskRendersModelNamesWhenUnknown(t *testing.T) {
	m := planAskModel(t, true)
	m.effectiveModel = client.ResolvedModel{} // no echo
	got := stripANSIstr(m.View().Content)
	if strings.Contains(got, "plan model:") {
		t.Errorf("plan modal with no model echo must not show 'plan model:', got %q", got)
	}
	if !strings.Contains(got, "execute model: session default model") {
		t.Errorf("plan modal with no model echo must still name the default model, got %q", got)
	}
}

// TestPlanAskRendersPlanFromArgs pins issue #206 UX fix: the plan content the
// model passed in the PresentPlan `plan` argument rides PendingAsk.Args (raw JSON
// string) into the plan-approval modal, which parses it and renders it so the
// operator can READ what they are approving (not just "plan ready for operator
// approval"). The args JSON is terminal-sanitized (model-authored content).
func TestPlanAskRendersPlanFromArgs(t *testing.T) {
	m := planAskModel(t, true)
	args, err := json.Marshal(map[string]string{
		"plan": "1. read foo\n2. edit bar\n3. run tests",
		"note": "three steps",
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	setPlanArgs(t, &m, string(args))
	got := stripANSIstr(m.View().Content)
	if !strings.Contains(got, "plan:") {
		t.Errorf("plan modal must render the plan: label, got: %s", got)
	}
	for _, want := range []string{"read foo", "edit bar", "run tests"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan modal must render the plan content; missing %q in: %s", want, got)
		}
	}
}

// TestPlanAskPlanArgsSanitized pins that a control-sequence-laden plan arg is
// terminal-sanitized (an attacker who controls the model output can't redraw the
// approval modal via ESC). Mirrors the diff/agents-inventory sanitize guards.
func TestPlanAskPlanArgsSanitized(t *testing.T) {
	m := planAskModel(t, true)
	// JSON-marshall so the ESC (0x1b) is a valid JSON string escape (\u001b).
	args, err := json.Marshal(map[string]string{"plan": "\x1b[31mfake-red\x1b[0m plan line"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	setPlanArgs(t, &m, string(args))
	// Strip the theme/lipgloss chrome (legitimate ESC) so the assertion targets
	// the plan text: sanitizeTerminal must have removed the model-injected ESC
	// (0x1b control byte), leaving the inert "[31m"/"[0m" fragments + the plan
	// text. No raw ESC remains anywhere in a rendered line.
	got := stripANSIstr(m.View().Content)
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "\x1b") {
			t.Errorf("raw ESC leaked into a rendered line (sanitizeTerminal not applied): %q", line)
		}
	}
	// The ESC was stripped; the inert "[31m"/"[0m" fragments + text remain.
	if !strings.Contains(got, "[31mfake-red[0m plan line") {
		t.Errorf("plan modal must render the sanitized plan text (ESC stripped, fragments inert), got: %s", got)
	}
}

// TestPlanAskLongPlanFullNotCollapsed pins the scrollable-view UX (issue #206
// rework): a plan longer than the viewport shows the FULL plan in the
// scrollable plan-review view — NO "+N more lines · ctrl+t expand" collapse
// marker (the collapse-by-default / ctrl+t gate is GONE for the plan path),
// and the plan is NOT rendered in the small centered card. The full plan is
// reachable by scrolling (asserted in TestPlanAskScrollReachesFullPlan).
func TestPlanAskLongPlanFullNotCollapsed(t *testing.T) {
	m := planAskModel(t, true)
	// Build a plan whose wrapped line count far exceeds the viewport height.
	// Each item carries a UNIQUE marker (LAST-ITEM-MARKER on the last) so the
	// full-plan assertion is robust against glamour's list-marker reformatting.
	var lines []string
	repeat := strings.Repeat("analysis ", 20) // ~200 chars per line
	for i := 1; i <= 8; i++ {
		marker := fmt.Sprintf("step-%d", i)
		if i == 8 {
			marker = "LAST-ITEM-MARKER"
		}
		lines = append(lines, fmt.Sprintf("%d. %s %s", i, marker, repeat))
	}
	// Blank lines between items so glamour treats them as separate paragraphs.
	planText := strings.Join(lines, "\n\n")
	args, err := json.Marshal(map[string]string{"plan": planText})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	setPlanArgs(t, &m, string(args))

	got := stripANSIstr(m.View().Content)
	// The plan content rendered.
	if !strings.Contains(got, "analysis") {
		t.Errorf("plan-review view must show plan content, got: %s", got)
	}
	// NO collapse marker — the ctrl+t expand affordance is gone for the plan.
	if strings.Contains(got, "ctrl+t expand") {
		t.Errorf("plan-review view must NOT show the 'ctrl+t expand' collapse affordance, got: %s", got)
	}
	if strings.Contains(got, "more lines") {
		t.Errorf("plan-review view must NOT show a '+N more lines' collapse marker, got: %s", got)
	}
	// The full plan's last item is present in the planVP content (reachable by
	// scrolling — the viewport's GetContent holds the entire plan).
	content := m.approval.planVP.GetContent()
	if !strings.Contains(content, "LAST-ITEM-MARKER") {
		t.Errorf("planVP must hold the FULL plan incl. the last item; GetContent missing the LAST-ITEM-MARKER: %s", content)
	}
}

// TestPlanAskScrollReachesFullPlan pins the scroll behaviour: the plan-review
// viewport starts at the top (the operator reads from the title down), and
// pgdn/wheel-down advance the YOffset so later plan content comes into view;
// pgup/home returns. The full plan is reachable by scrolling, no truncation.
func TestPlanAskScrollReachesFullPlan(t *testing.T) {
	m := planAskModel(t, true)
	// A plan long enough that the viewport (height ~ body - footer) cannot show
	// it all at once. The last item carries a unique marker reachable only by
	// scrolling to the bottom.
	var lines []string
	repeat := strings.Repeat("analysis ", 20)
	for i := 1; i <= 8; i++ {
		marker := fmt.Sprintf("step-%d", i)
		if i == 8 {
			marker = "LAST-ITEM-MARKER"
		}
		lines = append(lines, fmt.Sprintf("%d. %s %s", i, marker, repeat))
	}
	planText := strings.Join(lines, "\n\n")
	args, err := json.Marshal(map[string]string{"plan": planText})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	setPlanArgs(t, &m, string(args))

	// Opens at the top.
	if y := m.approval.planVP.YOffset(); y != 0 {
		t.Fatalf("planVP must open at YOffset 0, got %d", y)
	}

	// pgdn advances the scroll offset.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if y := m.approval.planVP.YOffset(); y <= 0 {
		t.Errorf("pgdn must advance planVP YOffset past 0, got %d", y)
	}
	afterPgdn := m.approval.planVP.YOffset()

	// wheel-down advances further.
	m, _ = pressKey(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if y := m.approval.planVP.YOffset(); y < afterPgdn {
		t.Errorf("wheel-down must not regress planVP YOffset (was %d, now %d)", afterPgdn, y)
	}

	// home returns to the top.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if y := m.approval.planVP.YOffset(); y != 0 {
		t.Errorf("home must return planVP to YOffset 0, got %d", y)
	}

	// The full plan's last item is reachable by scrolling to the bottom (end).
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	content := m.approval.planVP.View()
	if !strings.Contains(stripANSIstr(content), "LAST-ITEM-MARKER") {
		t.Errorf("after scrolling to the bottom the last plan item must be visible, got: %s", content)
	}
}

// TestPlanAskFallbackToNote pins backwards/forwards compat: an older model that
// ignores the `plan` arg schema but passes a `note` degrades gracefully — the
// note is rendered as the plan body (better than the bare reason line).
func TestPlanAskFallbackToNote(t *testing.T) {
	m := planAskModel(t, true)
	setPlanArgs(t, &m, `{"note":"short aside"}`)
	got := stripANSIstr(m.View().Content)
	if !strings.Contains(got, "short aside") {
		t.Errorf("plan modal with no `plan` arg must fall back to `note`, got: %s", got)
	}
}

// TestPlanAskFallbackToReason pins backwards/forwards compat: when args have
// neither `plan` nor `note` (an older model that put the plan only in message
// text), the modal degrades to the current "plan ready for operator approval"
// reason line — it never breaks.
func TestPlanAskFallbackToReason(t *testing.T) {
	m := planAskModel(t, true)
	setPlanArgs(t, &m, `{}`)
	got := stripANSIstr(m.View().Content)
	// No plan body rendered (the fallback is the reason line).
	if strings.Contains(got, "plan:") {
		t.Errorf("plan modal with empty args must not render a plan: label, got: %s", got)
	}
	// The reason line still shows (scrollable in the plan-review view).
	if !strings.Contains(got, "Plan mode requires approval") {
		t.Errorf("plan modal with empty args must still show the reason line, got: %s", got)
	}
}

// TestPlanAskMalformedArgsDegrades pins that malformed args JSON (not an object,
// or unparseable) degrades to the reason line — never a panic / broken modal.
func TestPlanAskMalformedArgsDegrades(t *testing.T) {
	m := planAskModel(t, true)
	setPlanArgs(t, &m, `not json at all`)
	got := stripANSIstr(m.View().Content)
	if !strings.Contains(got, "Plan mode requires approval") {
		t.Errorf("plan modal with malformed args must fall back to the reason line, got: %s", got)
	}
}

// TestPlanAskLongSingleLineWraps pins issue #206 wrapping: a long single-source-line
// plan (e.g. a 300-char bullet point) wraps to multiple display lines within the
// modal card's content width. Before the fix, this line ran off the right edge
// unreadable.
func TestPlanAskLongSingleLineWraps(t *testing.T) {
	m := planAskModel(t, true)
	// A 400-char single line (bullet-point style) — this MUST wrap to multiple
	// display lines. Before the fix, this ran off the right edge.
	longBullet := "- " + strings.Repeat("analysis ", 50) // ~400 chars
	args, err := json.Marshal(map[string]string{"plan": longBullet})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	setPlanArgs(t, &m, string(args))
	// The plan-review viewport holds the FULL wrapped plan; assert wrapping
	// against the full content (the visible window may only show the first rows).
	got := stripANSIstr(m.approval.planVP.GetContent())

	// The content is present and spans multiple display lines (wrapping happened).
	if !strings.Contains(got, "analysis") {
		t.Errorf("plan modal must contain the plan content, got: %s", got)
	}

	// Count display lines containing the plan text — a 400-char line that DOES
	// NOT wrap would appear on exactly 1 line, so >1 proves wrapping.
	contentLines := 0
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "analysis") {
			contentLines++
		}
	}
	if contentLines <= 1 {
		t.Errorf("plan content must wrap to >1 display line, got %d lines (no wrapping)", contentLines)
	}

	// The plan body must NOT contain the raw 400-char line as a single unbroken
	// line (the original bug: a single source line rendering without wrapping).
	// "analysis analysis analysis ..." appearing once with >200 consecutive
	// non-newline chars would be the bug.
	n := 0
	for _, ch := range got {
		if ch == '\n' {
			n = 0
			continue
		}
		n++
		if n > 200 {
			t.Errorf("plan body has a line >200 chars (no wrapping applied): ran to %d chars", n)
			break
		}
	}
}

// TestPlanAskMarkdownRendersWrapped verifies that markdown structure in the plan
// (headings, bullets, code fences) renders through glamour without error and
// wrapped to the modal width.
func TestPlanAskMarkdownRendersWrapped(t *testing.T) {
	m := planAskModel(t, true)
	md := "# Plan: database migration\n\n" +
		"- Add new `email` column to the `users` table\n" +
		"- Create an index on `email` for lookups\n" +
		"- Backfill existing rows with a default value\n\n" +
		"```sql\nALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';\nCREATE INDEX idx_users_email ON users(email);\n```\n\n" +
		"The migration runs in a single transaction to avoid partial state."
	args, err := json.Marshal(map[string]string{"plan": md})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	setPlanArgs(t, &m, string(args))
	// The plan-review viewport holds the FULL wrapped plan (scrollable); the
	// visible window is only the top rows, so assert against the full content.
	got := stripANSIstr(m.approval.planVP.GetContent())

	// Key markdown elements survived glamour rendering.
	for _, want := range []string{
		"Plan: database migration",
		"Add new",
		"email",
		"CREATE INDEX",
		"single transaction",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan modal missing markdown content %q in: %s", want, got)
		}
	}
	// The plan rendered without error (no raw glamour error fallback text leaking).
	if strings.Contains(got, "markdown:") {
		t.Errorf("plan modal must not contain glamour error fallback, got: %s", got)
	}
}

// TestGenericAskStillUsesCenteredModal pins that a NON-plan permission ask
// (e.g. Bash/Write) KEEPS the centered card modal + its diff collapse — the
// full-screen scrollable plan-review view is ONLY for plan asks. This is the
// "generic permission modal unchanged" contract from the design.
func TestGenericAskStillUsesCenteredModal(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-test-0001"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.phase = phaseAwaitingApproval
	m.approval.ask = pendingAsk{
		AskID:       "sess-test-0001:1:c1",
		Tool:        "Bash",
		Args:        `{"command":"echo hi"}`,
		Reason:      "Bash requires approval",
		offerAlways: true,
	}
	got := stripANSIstr(m.View().Content)
	// The generic modal shows "Permission required" (NOT the plan-review title).
	if !strings.Contains(got, "Permission required") {
		t.Errorf("generic ask must render the centered 'Permission required' modal, got: %s", got)
	}
	// The plan-review surface is NOT used for a generic ask.
	if strings.Contains(got, "Plan ready for review") {
		t.Errorf("generic ask must NOT render the plan-review view, got: %s", got)
	}
	// planVP is not populated for a generic ask (the plan-review viewport is
	// plan-ask-only).
	if m.approval.planVPReady {
		t.Errorf("planVP must not be ready for a generic (non-plan) ask")
	}
}

// TestPlanAskActionButtonsResolve pins that the action keys (A/W/D) resolve the
// ask via resolveAsk from the scrollable plan-review view — the operator reads
// (scrolls), then acts. Each verdict transitions phaseRunning + the right notice.
func TestPlanAskActionButtonsResolve(t *testing.T) {
	// Approve (A) → allow-once.
	m := planAskModel(t, true)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.phase != phaseRunning {
		t.Fatalf("approve must return to phaseRunning, got %v", m.phase)
	}
	if got := lastNotice(m); got != "permission allowed" {
		t.Errorf("approve notice = %q, want 'permission allowed'", got)
	}
	if m.approval.planVPReady {
		t.Errorf("planVP must be cleared after resolve, still ready")
	}

	// Always (W) → allow-always.
	m = planAskModel(t, true)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'w', Text: "w"})
	if m.phase != phaseRunning {
		t.Fatalf("always must return to phaseRunning, got %v", m.phase)
	}
	if got := lastNotice(m); got != "permission allowed (always, this session)" {
		t.Errorf("always notice = %q", got)
	}
	if m.approval.planVPReady {
		t.Errorf("planVP must be cleared after resolve, still ready")
	}

	// Deny (D) → deny.
	m = planAskModel(t, true)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if m.phase != phaseRunning {
		t.Fatalf("deny must return to phaseRunning, got %v", m.phase)
	}
	if got := lastNotice(m); got != "permission denied" {
		t.Errorf("deny notice = %q", got)
	}
	if m.approval.planVPReady {
		t.Errorf("planVP must be cleared after resolve, still ready")
	}
}

// TestPlanAskActionBarRendered pins that the pinned action bar (buttons + the
// auto-accept footnote + the scroll hint) renders in the view so the operator
// can act after reading. The bar stays reachable regardless of scroll position.
func TestPlanAskActionBarRendered(t *testing.T) {
	m := planAskModel(t, true)
	got := stripANSIstr(m.View().Content)
	for _, want := range []string{
		"[A]pprove & run",
		"[W] auto-accept edits",
		"[D] iterate",
		"auto-accept allows every edit in the execution phase for the rest of this session",
		"scroll: ↑/↓ · pgup/pgdn · home/end · mouse wheel",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan-review view missing action-bar element %q in: %s", want, got)
		}
	}
}

// TestPlanAskWidthWrapsNoRunoff pins that the plan is glamour-wrapped to the
// view width — no line runs off the right edge (the original bug a centered card
// with a tiny content width could not fix). The plan-review view wraps at the
// FULL view content width, so wrapped lines stay within the terminal.
func TestPlanAskWidthWrapsNoRunoff(t *testing.T) {
	m := planAskModel(t, true)
	// A single very long source line (no internal newlines) that MUST wrap to the
	// view width. If wrapping were absent it would run off the right edge.
	longLine := strings.Repeat("word ", 120)
	args, err := json.Marshal(map[string]string{"plan": longLine})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	setPlanArgs(t, &m, string(args))
	content := m.approval.planVP.GetContent()
	// No display line exceeds the view width (the plan wraps to the content
	// width). Allow a small tolerance for trailing padding; assert strictly
	// against the view width + a margin.
	maxW := m.width + 2
	for _, line := range strings.Split(stripANSIstr(content), "\n") {
		// Count runes (approx display width for ASCII "word " content).
		if w := len([]rune(line)); w > maxW {
			t.Errorf("plan line exceeds view width (%d > %d): %q", w, maxW, line)
		}
	}
}

// TestPlanAskResolveClearsPlanView pins the cleanup contract: after the plan
// ask resolves (any verdict), planVP is cleared (not ready) and the normal
// conversation view is restored (renderBody no longer routes to the plan-review
// view for a non-plan phase).
func TestPlanAskResolveClearsPlanView(t *testing.T) {
	m := planAskModel(t, true)
	args, _ := json.Marshal(map[string]string{"plan": "1. step one\n2. step two"})
	setPlanArgs(t, &m, string(args))
	if !m.approval.planVPReady {
		t.Fatal("precondition: planVP must be ready after a plan ask opens")
	}
	// Approve resolves the ask.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.approval.planVPReady {
		t.Errorf("planVP must be cleared (not ready) after resolve, still ready")
	}
	if m.phase != phaseRunning {
		t.Fatalf("phase must be phaseRunning after resolve, got %v", m.phase)
	}
	// The view no longer routes to the plan-review surface (the phase is running,
	// so renderBody returns the conversation viewport, not the plan-review view).
	got := stripANSIstr(m.View().Content)
	if strings.Contains(got, "Plan ready for review") {
		t.Errorf("view must not show the plan-review surface after resolve, got: %s", got)
	}
	if strings.Contains(got, "[A]pprove & run") {
		t.Errorf("view must not show the plan action bar after resolve, got: %s", got)
	}
}

// ---------------------------------------------------------------------------
// Interactive plan-approval continuation (issue #206): the proceed prompt
// ---------------------------------------------------------------------------

// planProceedModel builds a connected Model parked on a plan ask (mirroring
// planAskModel's direct-state setup) BUT wired through a fakeConv so that
// submitProceedPrompt — which opens a FRESH stream via m.deps.Conv.OpenConverse
// — records its Prompt frame on the returned sender. The continuation stream
// (contRecv) is a clean end_turn so the execution run does not wedge the test.
//
// State is set directly (m.approval.ask + m.phase + a live m.stream) rather than driven
// through a live stream, so the test is deterministic and focuses on the
// resolveAsk → ResultMsg → submitProceedPrompt transition (the issue #206 fix).
// The approval (resolveAsk) sends SendApproval on m.stream; the proceed
// (submitProceedPrompt) opens a fresh stream via the conv and sends SendPrompt.
func planProceedModel(t *testing.T, offerAlways bool) (Model, *fakeConv, *fakeSender) {
	t.Helper()
	contRecv := &fakeRecver{
		script: []*mecatlv1.ConverseResponse{
			ev(&mecatlv1.Event{Type: "session.init", Seq: 1}),
			ev(&mecatlv1.Event{Type: "turn.start", Seq: 2, Turn: 1}),
			ev(&mecatlv1.Event{Type: "result", Seq: 3, Turn: 1, Result: &mecatlv1.Result{
				Stop: "end_turn", Text: "executed", Usage: &mecatlv1.Usage{},
			}}),
		},
	}
	send := &fakeSender{}
	conv := &fakeConv{recv: contRecv, send: send, recvers: []*fakeRecver{contRecv}}
	m := New(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001"},
	)
	// Park on a plan ask with a LIVE stream (the approval run's stream). resolveAsk
	// sends SendApproval on this stream; submitProceedPrompt opens a FRESH stream.
	m.stream = client.NewStream(&fakeRecver{}, send)
	m.streamCh = make(chan tea.Msg, 64)
	m.streamGen++
	m.phase = phaseAwaitingApproval
	m.approval.ask = pendingAsk{
		AskID:       "sess-test-0001:1:presentplan-1",
		Tool:        "PresentPlan",
		Reason:      "Plan mode requires approval to execute.",
		offerAlways: offerAlways,
	}
	(&m).openPlanReviewView(m.approval.ask, 0, m.effectiveModel.ModelID)
	return m, conv, send
}

// approvalFrames returns the ResumeApproval frames recorded by the sender.
func approvalFrames(send *fakeSender) []*mecatlv1.ResumeApproval {
	var out []*mecatlv1.ResumeApproval
	for _, fr := range send.frames() {
		if ra := fr.GetResumeApproval(); ra != nil {
			out = append(out, ra)
		}
	}
	return out
}

// proceedPromptTexts returns the text of every Prompt frame recorded by the sender.
func proceedPromptTexts(send *fakeSender) []string {
	return promptTexts(send)
}

// TestPlanApprovedResultMsgFiresProceedPrompt is the headline ordering + proceed
// test: approving a plan ask with allow-once sends the ResumeApproval frame on
// the approval stream, then — ONLY once the approval run's ResultMsg{Stop:
// "plan_approved"} arrives — the TUI opens a FRESH stream and sends a Prompt
// frame carrying the proceed text, so the server starts the execution run. The
// proceed is NOT sent on the approval frame (the gRPC Converse handler ignores a
// second Prompt frame on the same stream) and NOT before the approval run
// terminates (StartRunContent's run-entry funnel requires a terminal session).
func TestPlanApprovedResultMsgFiresProceedPrompt(t *testing.T) {
	m, _, send := planProceedModel(t, true)

	// Approve (allow-once). resolveAsk sends the ResumeApproval frame on m.stream.
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	runBatchLeaves(cmd)

	// Immediately after resolveAsk, EXACTLY one approval frame must be recorded and
	// NO proceed Prompt frame yet — the proceed fires on ResultMsg, not on approval.
	// (This is the safe-ordering invariant: firing immediately after SendApproval
	// would race the still-live approval run.)
	apps := approvalFrames(send)
	if len(apps) != 1 {
		t.Fatalf("after resolveAsk: expected exactly 1 ResumeApproval frame, got %d", len(apps))
	}
	if apps[0].GetAllow() != true || apps[0].GetVerdict() != mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE {
		t.Fatalf("approval verdict = allow=%v verdict=%v, want allow-once", apps[0].GetAllow(), apps[0].GetVerdict())
	}
	if pp := proceedPromptTexts(send); len(pp) != 0 {
		t.Fatalf("no proceed Prompt must be sent before the plan_approved ResultMsg; got %v", pp)
	}

	// The approval run's terminal ResultMsg{plan_approved} arrives. applyResult →
	// endRun (tears down the approval stream) → submitProceedPrompt (opens a FRESH
	// stream + sends the proceed Prompt). Feed it via the real Update path.
	mm, rcmd := m.Update(client.ResultMsg{Stop: "plan_approved"})
	m = mm.(Model)
	runBatchLeaves(rcmd)

	// The proceed Prompt frame must now be recorded, carrying the proceed text.
	pp := proceedPromptTexts(send)
	var proceed string
	for _, text := range pp {
		if text == planApprovedProceedText {
			proceed = text
		}
	}
	if proceed == "" {
		t.Fatalf("plan_approved ResultMsg must fire a proceed Prompt with the proceed text; prompts sent = %v", pp)
	}
	// The model must be phaseRunning (the execution run started).
	if m.phase != phaseRunning {
		t.Fatalf("after the proceed, phase = %v, want phaseRunning (execution run started)", m.phase)
	}
}

// TestPlanApprovedAllowAlwaysFiresProceedPrompt: an allow-always (auto-accept
// edits) approval also fires the proceed prompt on the plan_approved ResultMsg.
func TestPlanApprovedAllowAlwaysFiresProceedPrompt(t *testing.T) {
	m, _, send := planProceedModel(t, true)

	// Approve with always (the 'w' key = auto-accept edits → allow-always).
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'w', Text: "w"})
	runBatchLeaves(cmd)

	apps := approvalFrames(send)
	if len(apps) != 1 {
		t.Fatalf("after resolveAsk: expected exactly 1 ResumeApproval frame, got %d", len(apps))
	}
	if apps[0].GetVerdict() != mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ALWAYS {
		t.Fatalf("approval verdict = %v, want allow-always", apps[0].GetVerdict())
	}
	if pp := proceedPromptTexts(send); len(pp) != 0 {
		t.Fatalf("no proceed Prompt before the plan_approved ResultMsg; got %v", pp)
	}

	mm, rcmd := m.Update(client.ResultMsg{Stop: "plan_approved"})
	m = mm.(Model)
	runBatchLeaves(rcmd)

	pp := proceedPromptTexts(send)
	var proceed string
	for _, text := range pp {
		if text == planApprovedProceedText {
			proceed = text
		}
	}
	if proceed == "" {
		t.Fatalf("allow-always plan_approved must fire a proceed Prompt; prompts sent = %v", pp)
	}
	if m.phase != phaseRunning {
		t.Fatalf("after the proceed, phase = %v, want phaseRunning", m.phase)
	}
}

// TestPlanIterateResultMsgDoesNotFireProceed: a deny (iterate) verdict does NOT
// fire the proceed prompt. The resumed run terminates StopPlanIterate (not
// plan_approved), so applyResult's plan_approved gate is off — the operator types
// their own feedback as the next prompt.
func TestPlanIterateResultMsgDoesNotFireProceed(t *testing.T) {
	m, _, send := planProceedModel(t, true)

	// Deny (iterate).
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	runBatchLeaves(cmd)

	apps := approvalFrames(send)
	if len(apps) != 1 || apps[0].GetAllow() != false {
		t.Fatalf("deny must send exactly one deny ResumeApproval frame; got %+v", apps)
	}
	// No proceed before the terminal.
	if pp := proceedPromptTexts(send); len(pp) != 0 {
		t.Fatalf("no proceed Prompt before the iterate terminal; got %v", pp)
	}

	// The resumed run terminates StopPlanIterate — NOT plan_approved.
	mm, rcmd := m.Update(client.ResultMsg{Stop: "plan_iterate"})
	m = mm.(Model)
	runBatchLeaves(rcmd)

	// Still NO proceed prompt.
	pp := proceedPromptTexts(send)
	for _, text := range pp {
		if text == planApprovedProceedText {
			t.Fatalf("iterate (deny) must NOT fire a proceed prompt; prompts sent = %v", pp)
		}
	}
	// The model must be idle (the iterate pause leaves the input usable).
	if m.phase != phaseIdle {
		t.Fatalf("after StopPlanIterate, phase = %v, want phaseIdle (operator types feedback)", m.phase)
	}
}

// TestNonPlanAskResultDoesNotFireProceed: a NON-plan ask (e.g. Bash) approved
// does NOT fire the proceed prompt — the gate is the plan_approved stop reason,
// which a regular tool-approval run never emits (it ends end_turn).
func TestNonPlanAskResultDoesNotFireProceed(t *testing.T) {
	m, _, send := planProceedModel(t, true)
	// Replace the plan ask with a BASH ask (non-plan). A fresh continuation stream
	// is still wired so submitProceedPrompt COULD fire if the gate were wrong —
	// proving the gate (the stop reason), not the wiring, suppresses it.
	m.approval.ask = pendingAsk{
		AskID: "sess-test-0001:1:bash-1", Tool: "Bash",
		Args: `{"command":"ls"}`, Reason: "Bash requires approval", offerAlways: true,
	}
	m.phase = phaseAwaitingApproval
	m.refreshView()

	// Approve the Bash ask.
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	runBatchLeaves(cmd)
	if len(approvalFrames(send)) != 1 {
		t.Fatalf("expected one approval frame, got %d", len(approvalFrames(send)))
	}

	// The run ends end_turn (a regular tool-approval run, not plan_approved).
	mm, rcmd := m.Update(client.ResultMsg{Stop: "end_turn"})
	m = mm.(Model)
	runBatchLeaves(rcmd)

	// NO proceed prompt must have fired (the stop was end_turn, not plan_approved).
	pp := proceedPromptTexts(send)
	for _, text := range pp {
		if text == planApprovedProceedText {
			t.Fatalf("a non-plan ask approved must NOT fire a proceed prompt; prompts sent = %v", pp)
		}
	}
	if m.phase != phaseIdle {
		t.Fatalf("after end_turn, phase = %v, want phaseIdle", m.phase)
	}
}

// TestPlanApprovedProceedFiresOnResultMsgNotImmediatelyOnApproval pins the
// ordering decision directly: immediately after resolveAsk (SendApproval), NO
// proceed Prompt frame exists; only after the ResultMsg{plan_approved} does the
// proceed fire. This is the deterministic guard against the unsafe
// "send-approval-then-immediately-send-prompt" variant that would race the
// still-live approval run.
func TestPlanApprovedProceedFiresOnResultMsgNotImmediatelyOnApproval(t *testing.T) {
	m, _, send := planProceedModel(t, true)

	// No prompts recorded yet (the approval stream is live but no Prompt was sent).
	beforeApprove := len(proceedPromptTexts(send))

	// Approve.
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	runBatchLeaves(cmd)

	// Immediately after approval: the prompt count is UNCHANGED (no proceed sent).
	if got := len(proceedPromptTexts(send)); got != beforeApprove {
		t.Fatalf("proceed must NOT fire on approval (only on ResultMsg): prompt count went %d → %d", beforeApprove, got)
	}

	// Now the ResultMsg{plan_approved} arrives — the proceed fires (count +1).
	mm, rcmd := m.Update(client.ResultMsg{Stop: "plan_approved"})
	m = mm.(Model)
	runBatchLeaves(rcmd)
	pp := proceedPromptTexts(send)
	if got := len(pp); got != beforeApprove+1 {
		t.Fatalf("proceed must fire exactly once on plan_approved ResultMsg: prompt count went %d → %d", beforeApprove, got)
	}
	last := pp[len(pp)-1]
	if last != planApprovedProceedText {
		t.Fatalf("the fired proceed prompt = %q, want %q", last, planApprovedProceedText)
	}
	_ = m
}

// ---------------------------------------------------------------------------
// Plan-approval header mode+model refresh (issue #206 follow-up)
// ---------------------------------------------------------------------------

// TestPlanApprovedFiresModeModelRefresh asserts that plan_approved fires a
// RefreshResolvedModelCmd (GetSession refetch) alongside the proceed prompt.
// The refetch carries the server's flipped mode + execute model so the header
// updates from the session snapshot.
func TestPlanApprovedFiresModeModelRefresh(t *testing.T) {
	m, conv, _ := planProceedModel(t, true)

	// Approve.
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	runBatchLeaves(cmd)

	// The plan_approved ResultMsg must fire both the proceed prompt AND the
	// RefreshResolvedModelCmd (GetSession refetch).
	beforeGet := conv.getSessionCalls()
	mm, rcmd := m.Update(client.ResultMsg{Stop: "plan_approved"})
	m = mm.(Model)
	runBatchLeaves(rcmd)

	// The refetch must have fired (GetSession called at least once).
	if got := conv.getSessionCalls(); got <= beforeGet {
		t.Fatalf("plan_approved must fire a GetSession refetch; calls = %d (was %d)", got, beforeGet)
	}

	// The execution run must be running.
	if m.phase != phaseRunning {
		t.Fatalf("after the proceed, phase = %v, want phaseRunning", m.phase)
	}
}

// TestPlanApprovedRefreshUpdatesModeAndModel proves the ResolvedModelMsg
// refetch result updates both m.activeMode and m.effectiveModel when the
// server returns a flipped mode + new execute model. This is the end-to-end
// reducer path: the ResolvedModelMsg lands after plan_approved and the header
// updates in-place while the execution run is in progress.
func TestPlanApprovedRefreshUpdatesModeAndModel(t *testing.T) {
	m, _, _ := planProceedModel(t, true)

	// Approve and let plan_approved fire.
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	runBatchLeaves(cmd)
	mm, rcmd := m.Update(client.ResultMsg{Stop: "plan_approved"})
	m = mm.(Model)
	runBatchLeaves(rcmd)

	// Before the refetch lands: mode is still the create-time default
	// (SessionReadyMsg didn't set a Mode, so activeMode is the deps.Mode).
	// effectiveModel still shows the plan model (from SessionReadyMsg).
	planModel := m.effectiveModel

	// Simulate the refetch result: the server's session snapshot returns a
	// flipped mode AND a new execute model.
	mm, _ = m.Update(client.ResolvedModelMsg{
		SessionID: "sess-test-0001",
		Mode:      "accept-edits",
		Resolved: client.ResolvedModel{
			ProviderID:    "openai",
			ModelID:       "gpt-5-execute",
			ContextWindow: 200000,
		},
	})
	m = mm.(Model)

	// Mode must be flipped to accept-edits (ModeString canonicalises to "accept-edits").
	if m.activeMode != "accept-edits" {
		t.Fatalf("activeMode = %q, want accept-edits after the refetch", m.activeMode)
	}

	// effectiveModel must be the execute model, not the plan model.
	if m.effectiveModel.ModelID == planModel.ModelID && planModel.ModelID != "" {
		t.Fatalf("effectiveModel.ModelID = %q, want the execute model (not the plan model %q)", m.effectiveModel.ModelID, planModel.ModelID)
	}
	if m.effectiveModel.ModelID != "gpt-5-execute" {
		t.Fatalf("effectiveModel.ModelID = %q, want gpt-5-execute", m.effectiveModel.ModelID)
	}
	if m.effectiveModel.ContextWindow != 200000 {
		t.Fatalf("effectiveModel.ContextWindow = %d, want 200000", m.effectiveModel.ContextWindow)
	}
}

// TestPlanIterateDoesNotFireModeModelRefresh asserts that a deny (iterate)
// terminal does NOT fire the GetSession refetch.
func TestPlanIterateDoesNotFireModeModelRefresh(t *testing.T) {
	m, conv, _ := planProceedModel(t, true)

	// Deny (iterate).
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	runBatchLeaves(cmd)

	beforeGet := conv.getSessionCalls()
	mm, rcmd := m.Update(client.ResultMsg{Stop: "plan_iterate"})
	m = mm.(Model)
	runBatchLeaves(rcmd)

	// No refetch must have fired on iterate.
	if got := conv.getSessionCalls(); got != beforeGet {
		t.Fatalf("plan_iterate must NOT fire a GetSession refetch; calls = %d (was %d)", got, beforeGet)
	}

	_ = m
}

// TestNonPlanResultDoesNotFireModeModelRefresh asserts that a non-plan
// ResultMsg (e.g. end_turn) does NOT trigger the GetSession refetch.
func TestNonPlanResultDoesNotFireModeModelRefresh(t *testing.T) {
	m, conv, _ := planProceedModel(t, true)

	// Replace the plan ask with a non-plan Bash ask.
	m.approval.ask = pendingAsk{
		AskID: "sess-test-0001:1:bash-1", Tool: "Bash",
		Args: `{"command":"ls"}`, Reason: "Bash requires approval", offerAlways: true,
	}
	m.phase = phaseAwaitingApproval
	m.refreshView()

	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	runBatchLeaves(cmd)

	beforeGet := conv.getSessionCalls()
	mm, rcmd := m.Update(client.ResultMsg{Stop: "end_turn"})
	m = mm.(Model)
	runBatchLeaves(rcmd)

	// No refetch on end_turn.
	if got := conv.getSessionCalls(); got != beforeGet {
		t.Fatalf("end_turn must NOT fire a GetSession refetch; calls = %d (was %d)", got, beforeGet)
	}

	_ = m
}

// TestResolvedModelMsgModeUpdateBenignOnFooterHeal proves that the footer-heal
// path (ResolvedModelMsg with no Mode set) does NOT touch m.activeMode or the
// model identity — only the ContextWindow is raised, preserving the existing
// RAISE-ONLY contract.
func TestResolvedModelMsgModeUpdateBenignOnFooterHeal(t *testing.T) {
	m, _, _ := planProceedModel(t, true)
	// Set a known mode and model before the heal lands.
	m.activeMode = "plan"
	m.effectiveModel = client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5", ContextWindow: 128000}

	// The footer-heal ResolvedModelMsg: same model identity, a raised window,
	// and NO Mode (the footer-heal path doesn't carry mode).
	mm, _ := m.Update(client.ResolvedModelMsg{
		SessionID: "sess-test-0001",
		Resolved:  client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5", ContextWindow: 256000},
	})
	m = mm.(Model)

	// Mode must be untouched (the heal didn't carry a mode).
	if m.activeMode != "plan" {
		t.Fatalf("activeMode = %q, want plan (untouched by the footer heal)", m.activeMode)
	}

	// Model identity must be untouched.
	if m.effectiveModel.ProviderID != "openai" || m.effectiveModel.ModelID != "gpt-5" {
		t.Fatalf("effectiveModel identity = %+v, want openai/gpt-5 (untouched by the footer heal)", m.effectiveModel)
	}

	// ContextWindow must be RAISED (the heal's only mutation).
	if m.effectiveModel.ContextWindow != 256000 {
		t.Fatalf("effectiveModel.ContextWindow = %d, want 256000 (raised by the heal)", m.effectiveModel.ContextWindow)
	}
}
