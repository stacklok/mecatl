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
)

// planAskModel builds a connected, awaiting-approval Model with a PresentPlan ask.
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
	m.ask = pendingAsk{
		AskID:       "sess-test-0001:1:presentplan-1",
		Tool:        "PresentPlan",
		Reason:      "Plan mode requires approval to execute.",
		offerAlways: offerAlways,
	}
	return m
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
	m.askQueue = append(m.askQueue, pendingAsk{AskID: "sess-test-0001:2:c2", Tool: "Bash"})
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
	m.ask = pendingAsk{AskID: "sess-test-0001:1:c1", Tool: "Bash", Reason: "Bash requires approval"}
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
	m.ask.Args = string(args)
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
	m.ask.Args = string(args)
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

// TestPlanAskLongPlanLineCappedThenExpandable pins the scrollable/expandable
// behaviour: a plan longer than the line cap shows the first maxPlanLines lines
// plus a "+N more lines · ctrl+t expand" affordance (the established reveal
// pattern mirroring renderToolDiff's diffSide / truncateLines), and ctrl+t
// (expand) reveals the full plan.
func TestPlanAskLongPlanLineCappedThenExpandable(t *testing.T) {
	m := planAskModel(t, true)
	// Build a plan with long source LINES that each wrap to multiple display
	// lines. Single-word lines get merged by glamour into one paragraph and never
	// hit the cap; long numbered-list items with blank lines between them become
	// separate paragraphs that each wrap independently, so 8 items with ~200-char
	// lines comfortably exceed the 12-line display cap.
	var lines []string
	repeat := strings.Repeat("analysis ", 20) // ~200 chars per line
	for i := 1; i <= 8; i++ {
		lines = append(lines, fmt.Sprintf("%d. %s", i, repeat))
	}
	// Blank lines between items so glamour treats them as separate paragraphs.
	planText := strings.Join(lines, "\n\n")
	args, err := json.Marshal(map[string]string{"plan": planText})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	m.ask.Args = string(args)

	collapsed := stripANSIstr(m.View().Content)
	// The plan rendered — some content is visible.
	if !strings.Contains(collapsed, "analysis") {
		t.Errorf("collapsed plan modal must show plan content, got: %s", collapsed)
	}
	// The collapse marker is present (the plan wraps past maxPlanLines).
	if !strings.Contains(collapsed, "ctrl+t expand") {
		t.Errorf("collapsed plan modal must show the 'ctrl+t expand' affordance, got: %s", collapsed)
	}
	// The marker reports the hidden line count.
	if !strings.Contains(collapsed, " more lines") {
		t.Errorf("collapsed plan modal must report hidden line count, got: %s", collapsed)
	}

	// Expand (ctrl+t) reveals the full wrapped plan — no collapse marker.
	m.expandTools = true
	expanded := stripANSIstr(m.View().Content)
	if !strings.Contains(expanded, "analysis") {
		t.Errorf("expanded plan modal must show plan content, got: %s", expanded)
	}
	if strings.Contains(expanded, "ctrl+t expand") {
		t.Errorf("expanded plan modal must NOT show the collapse affordance, got: %s", expanded)
	}
}

// TestPlanAskFallbackToNote pins backwards/forwards compat: an older model that
// ignores the `plan` arg schema but passes a `note` degrades gracefully — the
// note is rendered as the plan body (better than the bare reason line).
func TestPlanAskFallbackToNote(t *testing.T) {
	m := planAskModel(t, true)
	m.ask.Args = `{"note":"short aside"}`
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
	m.ask.Args = `{}`
	got := stripANSIstr(m.View().Content)
	// No plan body rendered (the fallback is the reason line).
	if strings.Contains(got, "plan:") {
		t.Errorf("plan modal with empty args must not render a plan: label, got: %s", got)
	}
	// The reason line still shows.
	if !strings.Contains(got, "Plan mode requires approval") {
		t.Errorf("plan modal with empty args must still show the reason line, got: %s", got)
	}
}

// TestPlanAskMalformedArgsDegrades pins that malformed args JSON (not an object,
// or unparseable) degrades to the reason line — never a panic / broken modal.
func TestPlanAskMalformedArgsDegrades(t *testing.T) {
	m := planAskModel(t, true)
	m.ask.Args = `not json at all`
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
	m.ask.Args = string(args)
	got := stripANSIstr(m.View().Content)

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
	m.ask.Args = string(args)
	got := stripANSIstr(m.View().Content)

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
