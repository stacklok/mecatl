package ui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// modalPlain renders the permission modal for an ask (expand is the ctrl+t
// details toggle) and strips ANSI so we can assert on substrings, matching the
// existing diff_test.go style.
func modalPlain(ask pendingAsk, expand bool) string {
	return stripANSIstr(renderApprovalModal(ask, expand, 80, 24))
}

func renderApprovalModal(ask pendingAsk, expand bool, width, height int) string {
	r := newTestRenderer()
	s := approvalSurfaceForRender(r, ask, expand, 0, 0)
	return s.renderPermissionModal(width, height)
}

func renderApprovalModalWithRenderer(r *renderer, ask pendingAsk, expand bool, width, height int) string {
	s := approvalSurfaceForRender(r, ask, expand, 0, 0)
	return s.renderPermissionModal(width, height)
}

func approvalSurfaceForRender(r *renderer, ask pendingAsk, expand bool, queued, argsOffset int) approvalSurface {
	return approvalSurface{
		ask:         ask,
		queue:       make([]pendingAsk, queued),
		askVPOffset: argsOffset,
		expandTools: expand,
		deps:        surfaceDeps{theme: r.th, marks: r.marks},
		render:      newApprovalRender(r),
	}
}

// TestPermissionModalEditDiff: an Edit ask shows the -/+ diff, not raw JSON.
func TestPermissionModalEditDiff(t *testing.T) {
	plain := modalPlain(pendingAsk{
		AskID:  "ask-1",
		Tool:   "Edit",
		Args:   `{"path":"main.go","old_string":"fmt.Println(\"hi\")","new_string":"fmt.Println(\"hello\")"}`,
		Reason: "Edit requires approval",
	}, false)
	if !strings.Contains(plain, "main.go  -1 +1") {
		t.Errorf("expected diff header, got %q", plain)
	}
	if !strings.Contains(plain, `- fmt.Println("hi")`) || !strings.Contains(plain, `+ fmt.Println("hello")`) {
		t.Errorf("expected -/+ diff lines, got %q", plain)
	}
	if strings.Contains(plain, "old_string") {
		t.Errorf("modal should show diff, not raw JSON args, got %q", plain)
	}
	// Title, reason and buttons must survive.
	if !strings.Contains(plain, "Permission required") || !strings.Contains(plain, "Edit requires approval") {
		t.Errorf("expected title + reason, got %q", plain)
	}
	if !strings.Contains(plain, "[A]llow") || !strings.Contains(plain, "[D]eny") {
		t.Errorf("expected allow/deny buttons, got %q", plain)
	}
}

// TestPermissionModalWriteDiff: a Write ask shows an added/green diff.
func TestPermissionModalWriteDiff(t *testing.T) {
	plain := modalPlain(pendingAsk{
		Tool: "Write",
		Args: `{"path":"note.txt","content":"reviewed"}`,
	}, false)
	if !strings.Contains(plain, "note.txt · 1 line (overwrites if it exists)") {
		t.Errorf("expected non-asserting write header, got %q", plain)
	}
	if strings.Contains(plain, "new file") {
		t.Errorf("write header must not claim 'new file', got %q", plain)
	}
	if !strings.Contains(plain, "+ reviewed") {
		t.Errorf("expected added line, got %q", plain)
	}
	if strings.Contains(plain, `"content"`) {
		t.Errorf("modal should show diff, not raw JSON args, got %q", plain)
	}
}

// TestPermissionModalNonDiffToolFallback: a Bash ask renders the decoded command
// TEXT (the pretty tier), not the raw JSON envelope — issue #488.
func TestPermissionModalNonDiffToolFallback(t *testing.T) {
	plain := modalPlain(pendingAsk{
		Tool: "Bash",
		Args: `{"command":"rm -rf /tmp/x"}`,
	}, false)
	// The command may soft-wrap across the accent-barred lines at the narrow test
	// width; assert it survives once the bar/whitespace is normalized out.
	norm := strings.Join(strings.Fields(plain), " ")
	if !strings.Contains(norm, "rm -rf /tmp/x") {
		t.Errorf("expected the decoded command text for Bash, got %q", plain)
	}
	if strings.Contains(plain, `"command"`) {
		t.Errorf("a Bash ask must not render the raw JSON envelope, got %q", plain)
	}
}

// TestPermissionModalNonBashKeepsJSON: a non-diff, non-Bash ask renders the
// pretty-printed JSON args (the pretty tier only decodes Bash commands).
func TestPermissionModalNonBashKeepsJSON(t *testing.T) {
	plain := modalPlain(pendingAsk{
		Tool: "WebFetch",
		Args: `{"url":"https://example.com/x"}`,
	}, false)
	if !strings.Contains(plain, "url") || !strings.Contains(plain, "https://example.com/x") {
		t.Errorf("expected pretty JSON args for a non-Bash tool, got %q", plain)
	}
}

// TestPermissionModalMalformedEditFallback: malformed Edit args fall back to JSON.
func TestPermissionModalMalformedEditFallback(t *testing.T) {
	// Valid JSON but missing the required path/strings, so renderToolDiff returns
	// ok=false and the caller falls back to pretty JSON.
	plain := modalPlain(pendingAsk{
		Tool: "Edit",
		Args: `{"foo":"bar"}`,
	}, false)
	if !strings.Contains(plain, "foo") || !strings.Contains(plain, "bar") {
		t.Errorf("expected JSON fallback for malformed Edit, got %q", plain)
	}
}

// TestPermissionModalExpandRevealsFullDiff: the cross-confirmed CWE-451 fix.
// Collapsed, a long Write is line-capped and shows a truthful "ctrl+t expand"
// marker; with expand on (ctrl+t at the gate) the full content is revealed, so
// the operator can see every line being authorized before deciding.
func TestPermissionModalExpandRevealsFullDiff(t *testing.T) {
	// Build content with more lines than the diff cap so it must collapse.
	var lines []string
	for i := 0; i < maxDiffLines+8; i++ {
		lines = append(lines, "line"+strconv.Itoa(i))
	}
	args := `{"path":"big.txt","content":"` + strings.Join(lines, `\n`) + `"}`
	ask := pendingAsk{Tool: "Write", Args: args}

	collapsed := modalPlain(ask, false)
	if !strings.Contains(collapsed, "ctrl+t expand") {
		t.Errorf("collapsed modal should show the expand marker, got %q", collapsed)
	}
	// The last line is past the cap, so it must be hidden when collapsed.
	if strings.Contains(collapsed, "+ line"+strconv.Itoa(maxDiffLines+7)) {
		t.Errorf("collapsed modal should hide content past the cap, got %q", collapsed)
	}

	expanded := modalPlain(ask, true)
	if strings.Contains(expanded, "ctrl+t expand") {
		t.Errorf("expanded modal should not show the collapse marker, got %q", expanded)
	}
	if !strings.Contains(expanded, "+ line"+strconv.Itoa(maxDiffLines+7)) {
		t.Errorf("expanded modal should reveal the full diff (every line), got %q", expanded)
	}
	if countLines(expanded) <= countLines(collapsed) {
		t.Errorf("expanded modal should be taller than collapsed (more lines visible)")
	}
}

// TestPermissionModalOffersAlways: a main-agent ask (offerAlways) shows three
// buttons — [A]llow / Al[w]ays / [D]eny — plus the muted always-allow caption.
func TestPermissionModalOffersAlways(t *testing.T) {
	plain := modalPlain(pendingAsk{
		Tool:        "Bash",
		Args:        `{"command":"ls"}`,
		offerAlways: true,
	}, false)
	if !strings.Contains(plain, "[A]llow") || !strings.Contains(plain, "Al[w]ays") || !strings.Contains(plain, "[D]eny") {
		t.Errorf("expected three buttons (allow / always / deny), got %q", plain)
	}
	if !strings.Contains(plain, "al[w]ays allows this exact command for the rest of this session") {
		t.Errorf("expected the always-allow caption, got %q", plain)
	}
}

// TestPermissionModalNoAlwaysForChild: a surfaced subagent ask (offerAlways=false)
// shows only the two buttons and no always-allow caption.
func TestPermissionModalNoAlwaysForChild(t *testing.T) {
	plain := modalPlain(pendingAsk{
		Tool:        "Bash",
		Args:        `{"command":"ls"}`,
		offerAlways: false,
	}, false)
	if !strings.Contains(plain, "[A]llow") || !strings.Contains(plain, "[D]eny") {
		t.Errorf("expected allow/deny buttons, got %q", plain)
	}
	if strings.Contains(plain, "Al[w]ays") {
		t.Errorf("a child ask must NOT offer the always button, got %q", plain)
	}
	if strings.Contains(plain, "al[w]ays allows this exact command") {
		t.Errorf("a child ask must NOT show the always-allow caption, got %q", plain)
	}
}

func TestPermissionModalChildReasonsWrapToWidth(t *testing.T) {
	reason := strings.Repeat("child approval reason needs a readable wrapped line ", 8)
	for _, ask := range []pendingAsk{
		{Tool: "Bash", Args: `{"command":"printf child"}`, Reason: reason},
		{Tool: "WebFetch", Args: `{"url":"https://example.com/child"}`, Reason: reason},
	} {
		rendered := renderApprovalModal(ask, false, 52, 24)
		for _, line := range strings.Split(stripANSIstr(rendered), "\n") {
			if w := lipgloss.Width(line); w > 52 {
				t.Fatalf("%s child reason overflows (%d > 52): %q", ask.Tool, w, line)
			}
		}
	}
}

func TestApprovalReasonWrapsInFullArgsAndPlanViews(t *testing.T) {
	reason := strings.Repeat("childreason ", 30)

	args := approvalModel(t, pendingAsk{AskID: "child:1:a", Tool: "Bash", Args: `{"command":"printf child"}`, Reason: reason})
	args = applyAll(args, tea.WindowSizeMsg{Width: 52, Height: 30})
	approvalSurfaceOf(t, args).argsViewOpen = true
	assertApprovalViewFits(t, args, 52)

	plan := planAskModel(t, false)
	plan = applyAll(plan, tea.WindowSizeMsg{Width: 52, Height: 30})
	approvalSurfaceOf(t, plan).ask.Reason = reason
	approvalSurfaceOf(t, plan).ask.Args = ""
	assertApprovalViewFits(t, plan, 52)
}

func assertApprovalViewFits(t *testing.T, m Model, width int) {
	t.Helper()
	for _, line := range strings.Split(stripANSIstr(m.View().Content), "\n") {
		if strings.Contains(line, "childreason") && lipgloss.Width(line) > width {
			t.Fatalf("approval reason overflows (%d > %d): %q", lipgloss.Width(line), width, line)
		}
	}
}

// countLines counts newline-separated lines for the taller-than assertion.
func countLines(s string) int { return strings.Count(s, "\n") + 1 }

// TestPermissionModalArgsWrapLongBash pins the issue #488 fix: a long Bash
// command wraps INSIDE the card — no content line exceeds the wrap budget (the
// same no-runoff assertion shape as TestPlanAskWidthWrapsNoRunoff).
func TestPermissionModalArgsWrapLongBash(t *testing.T) {
	longCmd := "find . -name '*.go' -not -path './vendor/*' -print0 | xargs -0 grep -nH 'func Test' | awk -F: '{print $1}' | sort | uniq -c | sort -rn | head -40"
	args := `{"command":"` + longCmd + `"}`
	ask := pendingAsk{Tool: "Bash", Args: args, Reason: "Bash requires approval"}
	rendered := renderApprovalModal(ask, false, 80, 24)
	// The card renders inside an 80-col region; every visible line must fit.
	for _, line := range strings.Split(stripANSIstr(rendered), "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("modal line exceeds the region width (%d > 80): %q", w, line)
		}
	}
	// And the command must actually be present (wrapped, not dropped).
	plain := stripANSIstr(rendered)
	if !strings.Contains(plain, "find . -name") || !strings.Contains(plain, "uniq -c") {
		t.Errorf("wrapped modal must still carry the command, got %q", plain)
	}
}

// TestAskArgsCardContentWidthUsesOfferedContentWidth pins the approval-surface
// geometry contract: Render receives askCard CONTENT width, so the askCard frame
// is subtracted only once by the parent. The outer-card cap converts once to a
// content cap before limiting the supplied width.
func TestAskArgsCardContentWidthUsesOfferedContentWidth(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	frame := th.Style("askCard").GetHorizontalFrameSize()
	maxContentWidth := permissionModalMaxWidth - frame

	if got := askArgsCardContentWidth(th, 80); got != 80 {
		t.Errorf("content width = %d, want full offered 80 (must not subtract frame %d again)", got, frame)
	}
	if got := askArgsCardContentWidth(th, maxContentWidth+20); got != maxContentWidth {
		t.Errorf("capped content width = %d, want outer cap's content width %d", got, maxContentWidth)
	}
}

// TestAskArgsMiniViewportUsesFullOfferedContentArea verifies that card args wrap
// across the full content area offered by the parent while the resulting outer
// card still cannot exceed permissionModalMaxWidth.
func TestAskArgsMiniViewportUsesFullOfferedContentArea(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	argsFrame := th.Style("askArgs").GetHorizontalFrameSize()
	cardFrame := th.Style("askCard").GetHorizontalFrameSize()

	for _, contentWidth := range []int{80, permissionModalMaxWidth} {
		t.Run(strconv.Itoa(contentWidth), func(t *testing.T) {
			region := askArgsMiniViewport(th, strings.Repeat("x", contentWidth*2), contentWidth, 24)
			want := min(contentWidth, permissionModalMaxWidth-cardFrame) - argsFrame
			if region.blockWidth != want {
				t.Errorf("args block width = %d, want %d", region.blockWidth, want)
			}
		})
	}
}

// TestPermissionModalArgsMiniViewportCapsHeight pins the mini-viewport cap: an
// args block taller than permissionModalArgsMaxLines renders at most the cap
// rows plus the hint line, and the buttons row lands exactly where the hit-test
// reads it (render/hit-test lockstep).
func TestPermissionModalArgsMiniViewportCapsHeight(t *testing.T) {
	// A heredoc-style command with many real newlines → many wrapped lines.
	var cmdLines []string
	for i := 0; i < 20; i++ {
		cmdLines = append(cmdLines, "echo line"+strconv.Itoa(i))
	}
	args := `{"command":"` + strings.Join(cmdLines, `\n`) + `"}`
	ask := pendingAsk{Tool: "Bash", Args: args, offerAlways: true}
	r := newTestRenderer()
	s := approvalSurfaceForRender(r, ask, false, 0, 0)
	body, buttonsRow := s.permissionModalBodyParts(80, 24)
	plain := stripANSIstr(body)
	lines := strings.Split(plain, "\n")

	// The args region renders at most the region-budget rows of accent-barred
	// content: at height 24 the budget is 24-16=8 rows (below the 10-row cap),
	// and the ↩ wrap marker adds no rows of its own. Count the args rows
	// directly (the accent-barred "echo …" lines before the hint).
	argsRows := 0
	for _, ln := range lines {
		if strings.Contains(ln, "echo ") {
			argsRows++
		}
	}
	if want := 24 - permissionModalBodyReserve; argsRows != want {
		t.Errorf("args region rendered %d rows, want exactly %d (the height-24 budget)", argsRows, want)
	}
	// Hidden rows exist → the scroll/full-args hint renders.
	if !strings.Contains(plain, "full args") {
		t.Errorf("a capped args region must advertise the full-args hint, got %q", plain)
	}
	// buttonsRow is the exact line index of the button box top within the body —
	// the box's top-border row (the [A]llow label sits one row below it).
	if !strings.Contains(lines[buttonsRow], "╭──") || !strings.Contains(lines[buttonsRow+1], "[A]llow") {
		t.Errorf("buttonsRow %d does not land on the button box: %q / %q", buttonsRow, lines[buttonsRow], lines[buttonsRow+1])
	}
	// Scrolling the mini-viewport reveals later content without moving buttonsRow.
	s.askVPOffset = 4
	body2, buttonsRow2 := s.permissionModalBodyParts(80, 24)
	if buttonsRow2 != buttonsRow {
		t.Errorf("scrolling the mini-viewport must not move buttonsRow (%d → %d)", buttonsRow, buttonsRow2)
	}
	if stripANSIstr(body) == stripANSIstr(body2) {
		t.Errorf("scrolling to offset 4 must change the visible args content")
	}
}

// TestPermissionModalBashPrettyUnescapesCommand pins the pretty tier: the Bash
// args JSON decodes into the command text with REAL newlines (not \n escapes).
// The raw tier is the VERBATIM wire args string (the literal ask.Args text).
func TestPermissionModalBashPrettyUnescapesCommand(t *testing.T) {
	const wireArgs = `{"command":"printf 'a\nb\n' | sort","timeout_ms":60000}`
	th := theme.New("aztec", theme.AztecPalette())
	pretty, raw, ok := askArgsContent(th, pendingAsk{
		Tool: "Bash",
		Args: wireArgs,
	})
	if !ok {
		t.Fatal("a Bash ask must be args-view capable")
	}
	if !strings.Contains(pretty, "printf 'a\nb\n' | sort") {
		t.Errorf("pretty tier must decode the command with real newlines, got %q", pretty)
	}
	if strings.Contains(pretty, `"command"`) {
		t.Errorf("pretty tier must not carry the JSON envelope, got %q", pretty)
	}
	// A non-zero timeout_ms appends a muted annotation line so the pretty tier
	// loses nothing the envelope carries.
	if !strings.Contains(stripANSIstr(pretty), "timeout_ms: 60000") {
		t.Errorf("pretty tier must annotate a non-zero timeout_ms, got %q", stripANSIstr(pretty))
	}
	// Raw is the VERBATIM wire string — no prettyJSON re-indent.
	if raw != wireArgs {
		t.Errorf("raw tier must be the verbatim wire args %q, got %q", wireArgs, raw)
	}
	if pretty == raw {
		t.Error("a Bash ask's tiers must differ (the raw toggle is honest)")
	}
}

// TestAskArgsContentFallbacks pins the pretty-tier fallbacks (fix-round 4e): a
// Bash ask whose args do not decode into a command (empty command, or invalid
// JSON) falls back to the prettyJSON pretty tier, while the raw tier stays the
// verbatim wire string.
func TestAskArgsContentFallbacks(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())

	// Empty command string: pretty falls back to prettyJSON of the envelope.
	pretty, raw, ok := askArgsContent(th, pendingAsk{Tool: "Bash", Args: `{"command":""}`})
	if !ok {
		t.Fatal("a Bash ask must be args-view capable")
	}
	if pretty != "{\n  \"command\": \"\"\n}" {
		t.Errorf("empty-command pretty must be the prettyJSON envelope, got %q", pretty)
	}
	if raw != `{"command":""}` {
		t.Errorf("raw tier must be the verbatim wire args, got %q", raw)
	}

	// Invalid JSON: both tiers are the sanitizeTerminal passthrough (prettyJSON
	// of malformed JSON returns the sanitized input as-is), so the tiers agree
	// and the toggle honestly hides.
	pretty, raw, ok = askArgsContent(th, pendingAsk{Tool: "Bash", Args: `not json`})
	if !ok {
		t.Fatal("a Bash ask must be args-view capable")
	}
	if pretty != "not json" || raw != "not json" {
		t.Errorf("invalid-JSON tiers must be the passthrough, got pretty=%q raw=%q", pretty, raw)
	}
	if askArgsTiersDiffer(th, pendingAsk{Tool: "Bash", Args: `not json`}) {
		t.Error("identical tiers must hide the raw/pretty toggle hint")
	}
}

// TestAskArgsContentGates pins the ok=false gates: plan asks and diff-capable
// tools have no args-view content.
func TestAskArgsContentGates(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, tool := range []string{"PresentPlan", "Edit", "Write"} {
		if _, _, ok := askArgsContent(th, pendingAsk{Tool: tool, Args: `{"x":"y"}`}); ok {
			t.Errorf("askArgsContent(%s) ok = true, want false", tool)
		}
	}
	// A malformed-Edit-args ask is still diff-FLAVOURED (no args view).
	if _, _, ok := askArgsContent(th, pendingAsk{Tool: "Edit", Args: `{"foo":"bar"}`}); ok {
		t.Error("a malformed Edit ask must stay diff-flavoured (ok=false)")
	}
}

// TestPermissionModalHintHonesty pins the hint text per ask type: a non-diff
// ask with hidden rows advertises the ctrl+t full-args view; a diff ask keeps
// the in-modal expand affordance and never advertises the full-args view.
func TestPermissionModalHintHonesty(t *testing.T) {
	// Non-diff ask with hidden rows → full-args hint.
	var cmdLines []string
	for i := 0; i < 20; i++ {
		cmdLines = append(cmdLines, "echo line"+strconv.Itoa(i))
	}
	longArgs := `{"command":"` + strings.Join(cmdLines, `\n`) + `"}`
	nonDiff := modalPlain(pendingAsk{Tool: "Bash", Args: longArgs}, false)
	if !strings.Contains(nonDiff, "ctrl+t full args") {
		t.Errorf("a long non-diff ask must advertise ctrl+t full args, got %q", nonDiff)
	}
	// Short non-diff ask → the hint is UNCONDITIONAL (ctrl+t opens the full view
	// regardless of length; it just carries no scroll clause when nothing is hidden).
	short := modalPlain(pendingAsk{Tool: "Bash", Args: `{"command":"ls"}`}, false)
	if !strings.Contains(short, "ctrl+t full args") {
		t.Errorf("a short ask must still advertise ctrl+t full args (no scroll clause), got %q", short)
	}
	if strings.Contains(short, "scroll") {
		t.Errorf("a short ask's hint must NOT advertise scroll (nothing hidden), got %q", short)
	}
	// Diff ask → the in-modal expand affordance, never the full-args hint.
	var writeLines []string
	for i := 0; i < maxDiffLines+4; i++ {
		writeLines = append(writeLines, "line"+strconv.Itoa(i))
	}
	diff := modalPlain(pendingAsk{
		Tool: "Write",
		Args: `{"path":"big.txt","content":"` + strings.Join(writeLines, `\n`) + `"}`,
	}, false)
	if !strings.Contains(diff, "ctrl+t expand") {
		t.Errorf("a long diff ask must keep the in-modal expand affordance, got %q", diff)
	}
	if strings.Contains(diff, "full args") {
		t.Errorf("a diff ask must not advertise the full-args view, got %q", diff)
	}
}
