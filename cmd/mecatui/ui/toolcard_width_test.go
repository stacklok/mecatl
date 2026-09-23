package ui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestMecatuiCardLayout_Scenario1_ResultRowsWrapBeforeStyle(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(toolCardMaxWidth + 2 + defaultBlockIndent)
	_, cardWidth, bodyWidth := r.toolCardLayout()

	long := "long-row-" + strings.Repeat("x", bodyWidth+9)
	b := &block{
		kind:       blockTool,
		toolID:     "result-row-order",
		toolName:   "Shell",
		toolArgs:   `{"command":"printf output"}`,
		resolved:   true,
		resultBody: long + "\nshort\x1b[2J-row\n   \nfinal-row",
	}

	prepared := stripANSIstr(r.renderToolResult(b, true, bodyWidth))
	for i, row := range strings.Split(prepared, "\n") {
		if got := maxLineWidth(row); got > bodyWidth {
			t.Errorf("result source row %d must be wrapped before styling/frame application (got %d, want ≤ %d): %q", i, got, bodyWidth, row)
		}
	}

	raw := r.renderTool(b, false)
	if strings.Contains(raw, "\x1b[2J") {
		t.Fatal("tool result leaked a dynamic terminal clear-screen escape")
	}
	plain := stripANSIstr(raw)
	rows := strings.Split(plain, "\n")
	longAt, shortAt, finalAt := -1, -1, -1
	for i, row := range rows {
		switch {
		case strings.Contains(row, "long-row-") && longAt < 0:
			longAt = i
		case strings.Contains(row, "short[2J-row"):
			shortAt = i
		case strings.Contains(row, "final-row"):
			finalAt = i
		}
		if got := maxLineWidth(row); got > cardWidth {
			t.Errorf("card row %d exceeds body frame width %d (got %d): %q", i, cardWidth, got, row)
		}
	}
	if longAt < 0 || shortAt < 0 || finalAt < 0 {
		t.Fatalf("collapsed result omitted source content:\n%s", plain)
	}
	if got, want := finalAt-shortAt, 2; got != want {
		t.Errorf("source blank paragraph must occupy exactly one row between short and final rows; got %d rows:\n%s", got, plain)
	}
	for _, row := range rows[longAt:shortAt] {
		if strings.TrimSpace(strings.Trim(row, "│╭╮╰╯─ ")) == "" {
			t.Errorf("long source row produced a padding-only display row before short source row: %q\n%s", row, plain)
		}
	}
}

func TestDelegationToolArgsWrapBeforeStyle(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(42)
	_, cardWidth, bodyWidth := r.toolCardLayout()
	long := "long-delegation-" + strings.Repeat("value-", 12)

	for _, tc := range []struct {
		name      string
		wantShort string
		block     *block
	}{
		{"subagent", "short-subagent", &block{kind: blockTool, toolName: "Subagent", subagent: true, subGoal: long + "\nshort-subagent"}},
		{"team", "", &block{kind: blockTool, toolName: "Team", team: true, teamLanes: []teamLane{{name: "member", current: long + "\nshort-team"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepared := stripANSIstr(r.renderToolArgs(tc.block, false, bodyWidth))
			for row, line := range strings.Split(prepared, "\n") {
				if got := lipgloss.Width(line); got > bodyWidth {
					t.Errorf("prepared row %d width = %d, want ≤ body width %d: %q", row, got, bodyWidth, line)
				}
			}

			out := stripANSIstr(r.renderTool(tc.block, false))
			rows := strings.Split(out, "\n")
			for row, line := range rows {
				if got := maxLineWidth(line); got > cardWidth {
					t.Errorf("final card row %d width = %d, want ≤ %d: %q", row, got, cardWidth, line)
				}
			}
			if tc.wantShort != "" && !strings.Contains(out, tc.wantShort) {
				t.Fatalf("styled delegation args lost short source row %q:\n%s", tc.wantShort, out)
			}
			for _, line := range rows {
				if strings.TrimSpace(strings.Trim(line, "│╭╮╰╯─ ")) == "" && !strings.Contains(line, "╭") && !strings.Contains(line, "╰") {
					t.Errorf("styled delegation args produced a padding-derived blank row: %q\n%s", line, out)
				}
			}
		})
	}
}

func TestMecatuiCardLayout_Scenario1_CollapsedResultRows(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(toolCardMaxWidth + 2 + defaultBlockIndent)

	rows := make([]string, maxToolResultLines+1)
	for i := range rows {
		rows[i] = "result-row-" + strconv.Itoa(i)
	}
	for _, name := range []string{"Shell", "Grep", "Subagent"} {
		t.Run(name, func(t *testing.T) {
			b := &block{kind: blockTool, toolID: name, toolName: name, resolved: true, resultBody: strings.Join(rows, "\n")}
			collapsed := stripANSIstr(r.renderTool(b, false))
			if !strings.Contains(collapsed, "+1 more line · ctrl+t expand") {
				t.Fatalf("collapsed %s result must reserve its shared row budget for source rows:\n%s", name, collapsed)
			}
			for i := range maxToolResultLines {
				if !strings.Contains(collapsed, "result-row-"+strconv.Itoa(i)) {
					t.Errorf("collapsed %s result omitted retained row %d:\n%s", name, i, collapsed)
				}
			}
			expanded := stripANSIstr(r.renderTool(b, true))
			if strings.Contains(expanded, "ctrl+t expand") || !strings.Contains(expanded, "result-row-12") {
				t.Errorf("expanded %s result must retain complete source rows without a collapse marker:\n%s", name, expanded)
			}
		})
	}

	t.Run("large JSON", func(t *testing.T) {
		result := mustJSON(t, map[string]string{
			"html_url": strings.Repeat("https://example.test/item/", 8),
			"url":      strings.Repeat("https://example.test/api/", 8),
			"id":       strings.Repeat("identifier-", 12),
			"number":   strings.Repeat("number-", 16),
			"sha":      strings.Repeat("sha-", 30),
			"status":   strings.Repeat("status-", 20),
			"state":    strings.Repeat("state-", 20),
			"omitted":  "hidden",
		})
		b := &block{kind: blockTool, toolID: "json", toolName: "WebFetch", resolved: true, resultBody: result}
		collapsed := stripANSIstr(r.renderTool(b, false))
		if !strings.Contains(collapsed, "ctrl+t expand") || strings.Contains(collapsed, "omitted") {
			t.Errorf("collapsed JSON must budget summary rows and hide omitted source fields:\n%s", collapsed)
		}
		expanded := stripANSIstr(r.renderTool(b, true))
		if strings.Contains(expanded, "ctrl+t expand") || !strings.Contains(expanded, "omitted") || !strings.Contains(expanded, "hidden") {
			t.Errorf("expanded JSON must retain complete source content:\n%s", expanded)
		}
	})

	t.Run("typed artifacts", func(t *testing.T) {
		blocks := make([]client.ContentBlock, maxToolResultLines+1)
		for i := range blocks {
			blocks[i] = client.ContentBlock{Kind: client.ContentBlockResourceLink, Name: "artifact-" + strconv.Itoa(i), URL: "https://example.test/artifact/" + strconv.Itoa(i)}
		}
		b := &block{kind: blockTool, toolID: "artifacts", toolName: "WebFetch", resolved: true, resultBody: "source\n\nparagraph", resultBlocks: blocks}
		collapsed := stripANSIstr(r.renderTool(b, false))
		if !strings.Contains(collapsed, "ctrl+t expand") || strings.Contains(collapsed, "artifact-12") {
			t.Errorf("collapsed artifact result must share the source row budget:\n%s", collapsed)
		}
		expanded := stripANSIstr(r.renderTool(b, true))
		sourceAt, paragraphAt := -1, -1
		expandedRows := strings.Split(expanded, "\n")
		for i, row := range expandedRows {
			if strings.Contains(row, "source") {
				sourceAt = i
			}
			if strings.Contains(row, "paragraph") {
				paragraphAt = i
			}
		}
		if strings.Contains(expanded, "ctrl+t expand") || !strings.Contains(expanded, "artifact-12") || sourceAt < 0 || paragraphAt != sourceAt+2 || strings.TrimSpace(strings.Trim(expandedRows[sourceAt+1], "│")) != "" {
			t.Errorf("expanded artifact result must retain all artifacts and intentional blank paragraph:\n%s", expanded)
		}
	})
}

// toolCardMaxWidth columns on a wide terminal, but on a narrow terminal the
// contentWidth-2 inset wins (the card never exceeds the viewport content). The card is
// laid out against contentWidth() = r.width - the left-margin indent, so the cap binds at
// width ≥ toolCardMaxWidth + 2 + indent and the narrow card is (width - indent - 2). The
// card's rendered width is measured per-line via lipgloss.Width on the widest line
// (renderToolBlock calls renderTool directly, so the per-block indent prefix is NOT
// applied here — this measures the raw card).
func TestToolCardWidthCap(t *testing.T) {
	const capBindsAt = toolCardMaxWidth + 2 + defaultBlockIndent
	cases := []struct {
		name     string
		width    int
		wantMax  int // the card's rendered width must be ≤ this
		wantWide bool
	}{
		{"wide terminal caps at the max", 200, toolCardMaxWidth, true},
		{"exactly the cap-binding width caps at the max", capBindsAt, toolCardMaxWidth, true},
		{"narrow terminal uses contentWidth-2", 60, 60 - defaultBlockIndent - 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRenderer()
			r.setWidth(tc.width)
			out := r.renderToolBlock("Read", `{"path":"greeting.txt"}`, false)
			got := maxLineWidth(out)
			if got > tc.wantMax {
				t.Errorf("width %d: card rendered %d cols, want ≤ %d", tc.width, got, tc.wantMax)
			}
			// A wide card should actually REACH the cap (border fills the card width), so
			// the cap is load-bearing, not vacuously satisfied by a short body.
			if tc.wantWide && got != toolCardMaxWidth {
				t.Errorf("width %d: capped card should render exactly %d cols, got %d", tc.width, toolCardMaxWidth, got)
			}
		})
	}
}

// TestToolCardWidthHardWrapsKnownRenderer covers the normal known-width card path:
// a collapsed Shell result's unbreakable divider must not escape the capped card.
func TestToolCardWidthHardWrapsKnownRenderer(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(185)
	resultLines := make([]string, 0, maxToolResultLines+27)
	resultLines = append(resultLines, strings.Repeat("-", 220))
	for range maxToolResultLines - 1 + 27 {
		resultLines = append(resultLines, "completed result line")
	}
	b := &block{
		kind:       blockTool,
		toolID:     "bash-1",
		toolName:   "Shell",
		toolArgs:   mustJSON(t, map[string]string{"command": strings.Repeat("x", toolCardMaxWidth+1)}),
		resolved:   true,
		resultBody: strings.Join(resultLines, "\n"),
	}

	out := r.renderTool(b, false)
	plain := stripANSIstr(out)
	if !strings.Contains(plain, "+29 more lines · ctrl+t expand") {
		t.Fatalf("collapsed Shell card lost its expansion marker:\n%s", plain)
	}
	if got := strings.Count(plain, "-"); got != 220 {
		t.Errorf("divider lost content while wrapping: got %d dashes, want 220", got)
	}
	maxWidth := 0
	for i, line := range strings.Split(out, "\n") {
		got := maxLineWidth(line)
		if got > maxWidth {
			maxWidth = got
		}
		if got > toolCardMaxWidth {
			t.Errorf("line %d exceeds card width %d (got %d): %q", i, toolCardMaxWidth, got, stripANSIstr(line))
		}
	}
	if maxWidth != toolCardMaxWidth {
		t.Errorf("card should be bounded at width %d, got %d", toolCardMaxWidth, maxWidth)
	}
}

func TestToolCardTabIndentedResultDoesNotReflowAtFrame(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(toolCardMaxWidth + 2 + defaultBlockIndent)
	_, cardWidth, _ := r.toolCardLayout()
	b := &block{
		kind:       blockTool,
		toolID:     "tabbed-read",
		toolName:   "Read",
		resolved:   true,
		resultBody: "cmd/mecatui/ui/render.go:789:\t\trows = functionalCardProvenanceRows(prepared, b.id, b.kind, r.indent, r.width)",
	}

	rows := strings.Split(stripANSIstr(r.renderTool(b, true)), "\n")
	containsStableContinuation := false
	for i, row := range rows {
		if got := maxLineWidth(row); got > cardWidth {
			t.Errorf("card row %d width = %d, want ≤ %d: %q", i, got, cardWidth, row)
		}
		if strings.ContainsRune(row, '\t') {
			t.Errorf("card row %d retained a literal tab: %q", i, row)
		}
		if strings.Contains(row, "r.indent, r.w") {
			containsStableContinuation = true
		}
		if strings.Contains(row, "r.inden") && i+1 < len(rows) && strings.Contains(rows[i+1], "t, r.width)") {
			t.Errorf("tab-indented source continuation reflowed at the frame:\n%s", strings.Join(rows, "\n"))
		}
	}
	if !containsStableContinuation {
		t.Errorf("tab-indented source was split again by the frame:\n%s", strings.Join(rows, "\n"))
	}
}

// TestCollapsedShellResultCapsVisualRows wraps multiline, indented Shell output
// before applying the inline cap. Capping logical source lines instead would let
// the final card wrap turn the retained rows into a taller collapsed card.
func TestCollapsedShellResultCapsVisualRows(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(toolCardMaxWidth + 2 + defaultBlockIndent)

	resultLines := make([]string, 7)
	for i := range resultLines {
		resultLines[i] = "    output-" + strconv.Itoa(i) + " " + strings.Repeat("near-width ", 12)
	}
	b := &block{
		kind:       blockTool,
		toolID:     "bash-visual-rows",
		toolName:   "Shell",
		toolArgs:   `{"command":"printf output"}`,
		resolved:   true,
		resultBody: strings.Join(resultLines, "\n"),
	}

	_, _, bodyWidth := r.toolCardLayout()
	expectedOverflow := len(strings.Split(ansi.Hardwrap(strings.Join(resultLines, "\n"), bodyWidth, true), "\n")) - maxToolResultLines
	if expectedOverflow <= 0 {
		t.Fatalf("precondition: Shell result must overflow the visual-row cap, got %d rows", expectedOverflow+maxToolResultLines)
	}
	collapsed := r.renderTool(b, false)
	plain := stripANSIstr(collapsed)
	marker := "+" + strconv.Itoa(expectedOverflow) + " more lines · ctrl+t expand"
	markerRow := -1
	firstResultRow := -1
	for i, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "output-0") && firstResultRow < 0 {
			firstResultRow = i
		}
		if strings.Contains(line, marker) {
			markerRow = i
		}
	}
	if firstResultRow < 0 || markerRow < 0 {
		t.Fatalf("collapsed Shell card must retain result output and an expansion affordance:\n%s", plain)
	}
	if rows := markerRow - firstResultRow; rows != maxToolResultLines {
		t.Errorf("collapsed result has %d visual rows before its affordance, want %d:\n%s", rows, maxToolResultLines, plain)
	}
	for i, line := range strings.Split(collapsed, "\n") {
		if got := maxLineWidth(line); got > toolCardMaxWidth {
			t.Errorf("collapsed line %d exceeds card width %d (got %d): %q", i, toolCardMaxWidth, got, stripANSIstr(line))
		}
	}

	expanded := stripANSIstr(r.renderTool(b, true))
	if strings.Contains(expanded, marker) {
		t.Errorf("expanded card retained collapsed expansion affordance:\n%s", expanded)
	}
	for _, line := range resultLines {
		if !strings.Contains(expanded, "output-"+strings.TrimPrefix(strings.Fields(line)[0], "output-")) {
			t.Errorf("expanded card omitted result line %q:\n%s", line, expanded)
		}
	}
	for i, line := range strings.Split(expanded, "\n") {
		if got := maxLineWidth(line); got > toolCardMaxWidth {
			t.Errorf("expanded line %d exceeds card width %d (got %d): %q", i, toolCardMaxWidth, got, line)
		}
	}
}

// TestCollapsedShellResultSkipsIndentOnlyWrapRows verifies oversized indentation
// cannot consume the collapsed result budget or appear as blank vertical gaps.
func TestCollapsedShellResultSkipsIndentOnlyWrapRows(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(toolCardMaxWidth + 2 + defaultBlockIndent)
	_, _, bodyWidth := r.toolCardLayout()
	resultLines := make([]string, maxToolResultLines+1)
	for i := range resultLines {
		resultLines[i] = strings.Repeat(" ", bodyWidth+1) + "result-" + strconv.Itoa(i)
	}
	b := &block{
		kind:       blockTool,
		toolID:     "bash-indent-rows",
		toolName:   "Shell",
		toolArgs:   `{"command":"printf output"}`,
		resolved:   true,
		resultBody: strings.Join(resultLines, "\n"),
	}

	collapsed := stripANSIstr(r.renderTool(b, false))
	const marker = "+1 more line · ctrl+t expand"
	firstResultRow, markerRow := -1, -1
	rows := strings.Split(collapsed, "\n")
	for i, line := range rows {
		if strings.Contains(line, "result-0") {
			firstResultRow = i
		}
		if strings.Contains(line, marker) {
			markerRow = i
		}
	}
	if firstResultRow < 0 || markerRow < 0 {
		t.Fatalf("collapsed result must retain result-0 and the one-row overflow marker:\n%s", collapsed)
	}
	if got := markerRow - firstResultRow; got != maxToolResultLines {
		t.Errorf("collapsed result has %d rows before its affordance, want %d:\n%s", got, maxToolResultLines, collapsed)
	}
	for i := 0; i < maxToolResultLines; i++ {
		if !strings.Contains(collapsed, "result-"+strconv.Itoa(i)) {
			t.Errorf("collapsed result omitted retained result-%d:\n%s", i, collapsed)
		}
	}
	if strings.Contains(collapsed, "result-12") {
		t.Errorf("collapsed result retained overflow result-12:\n%s", collapsed)
	}
	for _, line := range rows[firstResultRow:markerRow] {
		if !strings.Contains(line, "result-") {
			t.Errorf("collapsed result has whitespace-only interior row %q:\n%s", line, collapsed)
		}
	}

	expanded := stripANSIstr(r.renderTool(b, true))
	if strings.Contains(expanded, "ctrl+t expand") {
		t.Errorf("expanded result retained collapsed expansion affordance:\n%s", expanded)
	}
	for i := range resultLines {
		if !strings.Contains(expanded, "result-"+strconv.Itoa(i)) {
			t.Errorf("expanded result omitted result-%d:\n%s", i, expanded)
		}
	}
}

// TestCollapsedLargeJSONResultCapsVisualRows verifies that the summarized-result
// path shares the text-result display-row cap at narrow card widths.
func TestCollapsedLargeJSONResultCapsVisualRows(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(defaultBlockIndent + 20)

	result := mustJSON(t, map[string]string{
		"html_url": strings.Repeat("https://example.test/path/", 8),
		"url":      strings.Repeat("https://example.test/url/", 8),
		"id":       strings.Repeat("identifier-", 12),
		"number":   strings.Repeat("number-", 16),
		"sha":      strings.Repeat("sha-", 30),
		"status":   strings.Repeat("status-", 20),
		"state":    strings.Repeat("state-", 20),
		"unlisted": "hidden",
	})
	b := &block{
		kind:       blockTool,
		toolID:     "json-visual-rows",
		toolName:   "WebFetch",
		toolArgs:   `{"url":"https://example.test"}`,
		resolved:   true,
		resultBody: result,
	}

	_, cardWidth, bodyWidth := r.toolCardLayout()
	summary, hiddenFields, ok := r.summarizeResolvedResultDetail(b, false)
	if !ok || hiddenFields != 1 {
		t.Fatalf("precondition: expected one omitted non-prominent JSON field, got ok=%v hiddenFields=%d", ok, hiddenFields)
	}
	expectedOverflow := len(strings.Split(ansi.Hardwrap(summary, bodyWidth, true), "\n")) - maxToolResultLines
	if expectedOverflow <= 0 {
		t.Fatalf("precondition: JSON summary must overflow the visual-row cap, got %d rows", expectedOverflow+maxToolResultLines)
	}
	collapsed := r.renderTool(b, false)
	plain := stripANSIstr(collapsed)
	markerPrefix := "  … +" + strconv.Itoa(expectedOverflow) + " more "
	firstSummaryRow, markerRow := -1, -1
	for i, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "html_url:") && firstSummaryRow < 0 {
			firstSummaryRow = i
		}
		if strings.Contains(line, markerPrefix) {
			markerRow = i
		}
	}
	if firstSummaryRow < 0 || markerRow < 0 || !strings.Contains(plain, "ctrl+t") {
		t.Fatalf("collapsed JSON summary must retain its first row and expansion affordance %q:\n%s", markerPrefix, plain)
	}
	if rows := markerRow - firstSummaryRow; rows != maxToolResultLines {
		t.Errorf("collapsed JSON summary has %d visual rows before its affordance, want %d:\n%s", rows, maxToolResultLines, plain)
	}
	if strings.Contains(plain, "unlisted") || strings.Contains(plain, "hidden") {
		t.Errorf("collapsed JSON summary exposed a non-prominent field:\n%s", plain)
	}
	for i, line := range strings.Split(collapsed, "\n") {
		if got := maxLineWidth(line); got > cardWidth {
			t.Errorf("collapsed line %d exceeds card width %d (got %d): %q", i, cardWidth, got, stripANSIstr(line))
		}
	}

	expanded := stripANSIstr(r.renderTool(b, true))
	if strings.Contains(expanded, "ctrl+t expand") {
		t.Errorf("expanded JSON result retained collapsed expansion affordance:\n%s", expanded)
	}
	if !strings.Contains(expanded, "unlisted") || !strings.Contains(expanded, "hidden") {
		t.Errorf("expanded JSON result omitted non-prominent field/value:\n%s", expanded)
	}
	for _, key := range []string{"html_url", "url", "id", "number", "sha", "status", "state"} {
		if !strings.Contains(expanded, key) {
			t.Errorf("expanded JSON result omitted key %q:\n%s", key, expanded)
		}
	}
}

// TestCollapsedLargeJSONSummaryAdvertisesOmittedFields verifies a summary that fits
// the display-row cap still advertises fields hidden by the summary itself.
func TestCollapsedLargeJSONSummaryAdvertisesOmittedFields(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(toolCardMaxWidth + 2 + defaultBlockIndent)
	result := mustJSON(t, map[string]string{
		"html_url": "https://example.test/item",
		"url":      "https://example.test/api/item",
		"id":       "42",
		"number":   "24",
		"sha":      "deadbeef",
		"status":   "open",
		"state":    "active",
		"omitted":  strings.Repeat("x", resultSummaryByteThreshold),
	})
	b := &block{kind: blockTool, toolID: "json-omitted", toolName: "WebFetch", resolved: true, resultBody: result}
	summary, hiddenFields, ok := r.summarizeResolvedResultDetail(b, false)
	if !ok || hiddenFields != 1 {
		t.Fatalf("precondition: expected one hidden summary field, got ok=%v hiddenFields=%d", ok, hiddenFields)
	}
	_, _, bodyWidth := r.toolCardLayout()
	if rows := len(strings.Split(ansi.Hardwrap(summary, bodyWidth, true), "\n")); rows > maxToolResultLines {
		t.Fatalf("precondition: summary has %d display rows, want ≤ %d", rows, maxToolResultLines)
	}

	collapsed := stripANSIstr(r.renderTool(b, false))
	const marker = "  … +1 more key · ctrl+t expand"
	if !strings.Contains(collapsed, marker) {
		t.Fatalf("collapsed JSON summary must advertise its omitted field:\n%s", collapsed)
	}
	if got := strings.Count(collapsed, "ctrl+t expand"); got != 1 {
		t.Errorf("collapsed JSON summary has %d expansion affordances, want 1:\n%s", got, collapsed)
	}

	expanded := stripANSIstr(r.renderTool(b, true))
	if strings.Contains(expanded, "ctrl+t expand") {
		t.Errorf("expanded JSON result retained collapsed expansion affordance:\n%s", expanded)
	}
	if !strings.Contains(expanded, "omitted") {
		t.Errorf("expanded JSON result omitted the hidden field:\n%s", expanded)
	}
}

// TestCollapsedArraySummaryAdvertisesExpansion verifies abbreviated array summaries
// advertise expansion without inventing an object-field count.
func TestCollapsedArraySummaryAdvertisesExpansion(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(toolCardMaxWidth + 2 + defaultBlockIndent)
	elems := make([]string, 20)
	for i := range elems {
		elems[i] = "entry-" + strconv.Itoa(i) + "-" + strings.Repeat("x", 40)
	}
	result := mustJSON(t, elems)
	b := &block{kind: blockTool, toolID: "array-omitted", toolName: "WebFetch", resolved: true, resultBody: result}

	collapsed := stripANSIstr(r.renderTool(b, false))
	const marker = "  … ctrl+t expand"
	if !strings.Contains(collapsed, marker) {
		t.Fatalf("collapsed array summary must advertise expansion:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "more key") || strings.Contains(collapsed, "entry-19") {
		t.Errorf("collapsed array summary must not claim hidden fields or expose items:\n%s", collapsed)
	}
	if got := strings.Count(collapsed, "ctrl+t expand"); got != 1 {
		t.Errorf("collapsed array summary has %d expansion affordances, want 1:\n%s", got, collapsed)
	}

	expanded := stripANSIstr(r.renderTool(b, true))
	if strings.Contains(expanded, "ctrl+t expand") {
		t.Errorf("expanded array result retained collapsed expansion affordance:\n%s", expanded)
	}
	for _, item := range []string{"entry-0", "entry-19"} {
		if !strings.Contains(expanded, item) {
			t.Errorf("expanded array result omitted %q:\n%s", item, expanded)
		}
	}
}

// TestCollapsedToolResultCapsArtifacts verifies that typed artifacts share the
// collapsed text-result budget instead of extending the card below its affordance.
func TestCollapsedToolResultCapsArtifacts(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(toolCardMaxWidth + 2 + defaultBlockIndent)
	blocks := make([]client.ContentBlock, 13)
	for i := range blocks {
		if i == len(blocks)-1 {
			blocks[i] = client.ContentBlock{Kind: client.ContentBlockImage, MimeType: "image/png"}
			continue
		}
		blocks[i] = client.ContentBlock{
			Kind: client.ContentBlockResourceLink,
			Name: "artifact-" + strconv.Itoa(i),
			URL:  "https://example.test/r/" + strconv.Itoa(i),
		}
	}
	b := &block{
		kind:         blockTool,
		toolID:       "artifact-rows",
		toolName:     "WebFetch",
		resolved:     true,
		resultBody:   "body-0\nbody-1",
		resultBlocks: blocks,
	}

	collapsed := r.renderTool(b, false)
	plain := stripANSIstr(collapsed)
	const marker = "  … +3 more lines · ctrl+t expand"
	firstResultRow, markerRow := -1, -1
	for i, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "body-0") && firstResultRow < 0 {
			firstResultRow = i
		}
		if strings.Contains(line, marker) {
			markerRow = i
		}
	}
	if firstResultRow < 0 || markerRow < 0 {
		t.Fatalf("collapsed result must retain text and one truthful artifact overflow marker:\n%s", plain)
	}
	if rows := markerRow - firstResultRow; rows != maxToolResultLines {
		t.Errorf("collapsed result has %d visual rows before its affordance, want %d:\n%s", rows, maxToolResultLines, plain)
	}
	if got := strings.Count(plain, "ctrl+t expand"); got != 1 {
		t.Errorf("collapsed result has %d expansion affordances, want 1:\n%s", got, plain)
	}
	for i := range 10 {
		if !strings.Contains(plain, "artifact-"+strconv.Itoa(i)) {
			t.Errorf("collapsed result omitted retained artifact-%d:\n%s", i, plain)
		}
	}
	for _, name := range []string{"artifact-10", "artifact-11", "[image: image/png]"} {
		if strings.Contains(plain, name) {
			t.Errorf("collapsed result retained overflow artifact %q:\n%s", name, plain)
		}
	}
	for i, line := range strings.Split(collapsed, "\n") {
		if got := maxLineWidth(line); got > toolCardMaxWidth {
			t.Errorf("collapsed line %d exceeds card width %d (got %d): %q", i, toolCardMaxWidth, got, stripANSIstr(line))
		}
	}

	expanded := stripANSIstr(r.renderTool(b, true))
	if strings.Contains(expanded, "ctrl+t expand") {
		t.Errorf("expanded result retained collapsed expansion affordance:\n%s", expanded)
	}
	for i := range blocks[:len(blocks)-1] {
		if !strings.Contains(expanded, "artifact-"+strconv.Itoa(i)) {
			t.Errorf("expanded result omitted artifact-%d:\n%s", i, expanded)
		}
	}
	if !strings.Contains(expanded, "[image: image/png]") {
		t.Errorf("expanded result omitted image artifact:\n%s", expanded)
	}
}

// TestResolvedShellToolCardFitsViewport renders the normal transcript path, including
// the conversation indent, for a resolved Shell call whose command and result have no
// natural break points. Both views must remain within a narrow terminal.
func TestResolvedShellToolCardFitsViewport(t *testing.T) {
	const viewportWidth = 6
	command := strings.Repeat("x", 200)
	result := strings.Repeat("y", 200)

	for _, expand := range []bool{false, true} {
		t.Run(map[bool]string{false: "collapsed", true: "expanded"}[expand], func(t *testing.T) {
			r := newTestRenderer()
			r.setWidth(viewportWidth)
			c := &conversation{}
			c.addTool("bash-1", "Shell", mustJSON(t, map[string]string{"command": command}))
			if !c.resolveTool("bash-1", result, false) {
				t.Fatal("resolve Shell tool")
			}

			out := r.renderConversation(c, expand)
			for i, line := range strings.Split(out, "\n") {
				if got := maxLineWidth(line); got > viewportWidth {
					t.Errorf("line %d exceeds viewport width %d (got %d): %q", i, viewportWidth, got, stripANSIstr(line))
				}
			}
		})
	}
}

// TestTranscriptReflowsOnWidthOnlyResize covers the line-slice SetContentLines
// handoff used by the normal transcript. A narrow resize with unchanged body height
// must replace, rather than retain, a wide Shell card render.
func TestTranscriptReflowsOnWidthOnlyResize(t *testing.T) {
	const (
		wideWidth   = 160
		narrowWidth = 100
		height      = 30
	)
	command := "task docs && task site:build && git status --short --branch && git diff --check && git diff --stat && git diff --name-only --cached"
	m := newMCPModel(t, aztec(), nil)
	m = applyAll(m,
		tea.WindowSizeMsg{Width: wideWidth, Height: height},
		client.ToolCallMsg{ID: "bash-1", Name: "Shell", Args: mustJSON(t, map[string]string{"command": command})},
		client.ToolResultMsg{CallID: "bash-1", Content: "docs and site passed\n M cmd/mecatui/ui/update.go"},
	)
	if m.sel.active || m.expandTools {
		t.Fatal("precondition: normal transcript must use SetContentLines")
	}
	bodyHeight := m.vp.Height()

	m = applyAll(m, tea.WindowSizeMsg{Width: narrowWidth, Height: height})
	if m.vp.Height() != bodyHeight {
		t.Fatalf("precondition: width-only resize changed body height from %d to %d", bodyHeight, m.vp.Height())
	}
	for i, line := range strings.Split(m.vp.GetContent(), "\n") {
		if width := maxLineWidth(line); width > narrowWidth {
			t.Errorf("viewport line %d exceeds width %d (got %d): %q", i, narrowWidth, width, stripANSIstr(line))
		}
	}
}

// TestTranscriptWidthOnlyResizeResticksAndFollowsTranscript covers the scroll-state
// half of the normal transcript width-only refresh. Reflow can reduce the transcript
// until an initially unstuck viewport is now at its bottom; the resize must derive
// stuck from that resulting position before the next transcript event arrives.
func TestTranscriptWidthOnlyResizeResticksAndFollowsTranscript(t *testing.T) {
	const (
		narrowWidth = 100
		wideWidth   = 200
		height      = 30
	)
	m := newMCPModel(t, aztec(), nil)
	m = applyAll(m,
		tea.WindowSizeMsg{Width: narrowWidth, Height: height},
		client.TurnStartMsg{Turn: 1},
		client.AssistantDeltaMsg{Turn: 1, Text: strings.Repeat("reflowed transcript text ", 100)},
		renderTickMsg{},
	)

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.conversationView.mode == followTail || m.vp.AtBottom() {
		t.Fatalf("precondition: pgup should leave the narrow viewport unstuck (stuck=%v atBottom=%v)", m.conversationView.mode == followTail, m.vp.AtBottom())
	}

	m = applyAll(m, tea.WindowSizeMsg{Width: wideWidth, Height: height})
	if m.conversationView.mode == followTail != m.vp.AtBottom() {
		t.Fatalf("width-only resize must synchronize stuck: stuck=%v atBottom=%v", m.conversationView.mode == followTail, m.vp.AtBottom())
	}
	if m.conversationView.mode != followTail {
		t.Fatal("precondition: wider reflow should leave the viewport at bottom")
	}

	m = applyAll(m, client.ToolCallMsg{ID: "follow-1", Name: "Read", Args: `{"path":"README.md"}`})
	if !m.vp.AtBottom() {
		t.Fatal("a transcript event after the re-synchronized resize should follow the bottom")
	}
}

// TestResolvedShellToolCardFitsOneColumnViewport ensures the transcript indent is
// suppressed when it would otherwise make a frameless, one-column card overflow.
func TestResolvedShellToolCardFitsOneColumnViewport(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(1)
	c := &conversation{}
	c.addTool("bash-1", "Shell", mustJSON(t, map[string]string{"command": strings.Repeat("x", 200)}))
	if !c.resolveTool("bash-1", strings.Repeat("y", 200), false) {
		t.Fatal("resolve Shell tool")
	}

	for i, line := range strings.Split(r.renderConversation(c, false), "\n") {
		if got := maxLineWidth(line); got > r.width {
			t.Errorf("line %d exceeds viewport width %d (got %d): %q", i, r.width, got, stripANSIstr(line))
		}
	}
}

// TestResolvedShellToolCardFitsFrameTransition verifies the first width that can
// render the normal card frame while retaining the existing 2-cell right inset.
func TestResolvedShellToolCardFitsFrameTransition(t *testing.T) {
	command := strings.Repeat("x", 200)
	result := strings.Repeat("y", 200)

	for _, expand := range []bool{false, true} {
		t.Run(map[bool]string{false: "collapsed", true: "expanded"}[expand], func(t *testing.T) {
			r := newTestRenderer()
			r.setWidth(defaultBlockIndent + r.th.Style("toolCard").GetHorizontalFrameSize() + 2)
			c := &conversation{}
			c.addTool("bash-1", "Shell", mustJSON(t, map[string]string{"command": command}))
			if !c.resolveTool("bash-1", result, false) {
				t.Fatal("resolve Shell tool")
			}

			for i, line := range strings.Split(r.renderConversation(c, expand), "\n") {
				if got := maxLineWidth(line); got > r.width {
					t.Errorf("line %d exceeds viewport width %d (got %d): %q", i, r.width, got, stripANSIstr(line))
				}
			}
		})
	}
}

// TestToolCardWidthUnchangedAtCap proves the cap is a no-op exactly at the cap
// boundary: at the cap-binding width the card is identical to a card at a far wider
// width — i.e. the cap only ever clamps, it never changes a card that already fits.
func TestToolCardWidthUnchangedAtCap(t *testing.T) {
	const capBindsAt = toolCardMaxWidth + 2 + defaultBlockIndent

	r1 := newTestRenderer()
	r1.setWidth(capBindsAt) // contentWidth-2 == cap, min(cap, cap) == cap
	a := r1.renderToolBlock("Read", `{"path":"x"}`, false)

	r2 := newTestRenderer()
	r2.setWidth(400) // far past the cap → min clamps to the cap
	b := r2.renderToolBlock("Read", `{"path":"x"}`, false)

	if a != b {
		t.Errorf("card at the cap-binding width and at 400 must be byte-identical (both clamp to the cap):\nlen(a)=%d len(b)=%d", lipgloss.Width(a), lipgloss.Width(b))
	}
}
