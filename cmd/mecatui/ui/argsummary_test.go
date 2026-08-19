package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// mustJSON marshals v to a compact JSON args string for a tool block.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// renderToolBlock builds a minimal tool block and renders it through renderTool —
// the real card path, so the head/args/result wiring is exercised end to end.
func (r *renderer) renderToolBlock(name, args string, expand bool) string {
	b := &block{kind: blockTool, toolID: "call-1", toolName: name, toolArgs: args}
	return r.renderTool(b, expand)
}

// longBody is a ~4 KB, 72-line string used to exercise the collapsed long-string
// arg/result summary (size + line count + preview, full body hidden).
func longBody() string {
	var b strings.Builder
	b.WriteString("## Context\n")
	for i := 0; i < 71; i++ {
		b.WriteString("this is line content padding to grow the body well past four kilobytes\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func TestSummarizeArgsLongStringCollapsed(t *testing.T) {
	r := newTestRenderer()
	body := longBody()
	if lc := lineCount(body); lc != 72 {
		t.Fatalf("fixture should be 72 lines, got %d", lc)
	}
	if len(body) < 4096 {
		t.Fatalf("fixture should be ≥4 KB, got %d bytes", len(body))
	}
	args := mustJSON(t, map[string]any{
		"method": "create",
		"body":   body,
	})
	out, ok := r.summarizeArgs(args)
	if !ok {
		t.Fatal("object args should summarize")
	}
	plain := stripANSIstr(out)
	// Size signal + line count + a quoted preview, NOT the full body.
	if !strings.Contains(plain, "KB") {
		t.Errorf("expected a byte-size signal (KB), got %q", plain)
	}
	if !strings.Contains(plain, "72 lines") {
		t.Errorf("expected the line count, got %q", plain)
	}
	if !strings.Contains(plain, `"## Context`) {
		t.Errorf("expected a quoted first-line preview, got %q", plain)
	}
	if strings.Contains(plain, "padding to grow") {
		t.Errorf("collapsed summary must NOT contain the full body, got %q", plain)
	}
	// And it is genuinely more compact than dumping the full pretty JSON.
	full := prettyJSON(args)
	if len(stripANSIstr(out)) >= len(stripANSIstr(full)) {
		t.Errorf("summary (%d) should be shorter than prettyJSON (%d)", len(stripANSIstr(out)), len(stripANSIstr(full)))
	}
}

func TestSummarizeArgsAffordanceOnHiddenValue(t *testing.T) {
	r := newTestRenderer()
	// A single long-string arg: the key count (2) is well under the cap, so the
	// KEY-overflow path never trips — but the body value is collapsed, so the
	// ctrl+t affordance MUST still appear (the silent-data trap fix, #1).
	args := mustJSON(t, map[string]any{
		"method": "create",
		"body":   longBody(),
	})
	out, ok := r.summarizeArgs(args)
	if !ok {
		t.Fatal("object args should summarize")
	}
	plain := stripANSIstr(out)
	rows := strings.Split(plain, "\n")
	last := rows[len(rows)-1]
	if !strings.Contains(last, "ctrl+t expand") {
		t.Errorf("a collapsed long-string arg must advertise ctrl+t even with no key overflow, got footer %q\nfull:\n%s", last, plain)
	}
	// It is the per-value form (no key overflow), so it does NOT name a key count.
	if strings.Contains(last, "more key") {
		t.Errorf("no key overflow here, so the footer must not name a key count, got %q", last)
	}
	// And a collapsed BIG ARRAY/OBJECT (no long string, no key overflow) also
	// advertises the affordance.
	twelve := make([]any, 12)
	for i := range twelve {
		twelve[i] = i
	}
	out2, _ := r.summarizeArgs(mustJSON(t, map[string]any{"many": twelve}))
	if !strings.Contains(stripANSIstr(out2), "ctrl+t expand") {
		t.Errorf("a collapsed array must advertise ctrl+t, got %q", stripANSIstr(out2))
	}
}

func TestSummarizeArgsArrayObjectSummarized(t *testing.T) {
	r := newTestRenderer()
	twelve := make([]any, 12)
	for i := range twelve {
		twelve[i] = i
	}
	eightKeys := map[string]any{}
	for i := 0; i < 8; i++ {
		eightKeys[string(rune('a'+i))] = i
	}
	args := mustJSON(t, map[string]any{
		"labels": []any{"enhancement"},
		"many":   twelve,
		"nested": eightKeys,
	})
	out, ok := r.summarizeArgs(args)
	if !ok {
		t.Fatal("object args should summarize")
	}
	plain := stripANSIstr(out)
	if !strings.Contains(plain, "[enhancement]") {
		t.Errorf("a short scalar array should render inline, got %q", plain)
	}
	if !strings.Contains(plain, "12 items") {
		t.Errorf("a 12-element array should collapse to '12 items', got %q", plain)
	}
	if !strings.Contains(plain, "8 keys") {
		t.Errorf("an 8-key object should collapse to '8 keys', got %q", plain)
	}
}

func TestSummarizeArgsBoundedRows(t *testing.T) {
	r := newTestRenderer()
	// 12 keys: a mix of priority and non-priority so order is testable.
	args := mustJSON(t, map[string]any{
		// priority keys (should sort first, in argPriorityKeys order):
		"repo":  "mecatl",
		"owner": "stacklok",
		"title": "t",
		// non-priority (alphabetical after):
		"aaa": 1, "bbb": 2, "ccc": 3, "ddd": 4, "eee": 5,
		"fff": 6, "ggg": 7, "hhh": 8, "iii": 9,
	})
	out, ok := r.summarizeArgs(args)
	if !ok {
		t.Fatal("object args should summarize")
	}
	plain := stripANSIstr(out)
	rows := strings.Split(plain, "\n")
	if len(rows) != maxSummaryRows+1 {
		t.Fatalf("expected %d capped rows + 1 roll-up, got %d:\n%s", maxSummaryRows, len(rows), plain)
	}
	if !strings.Contains(rows[len(rows)-1], "more key") || !strings.Contains(rows[len(rows)-1], "ctrl+t expand") {
		t.Errorf("expected a '+K more keys · ctrl+t expand' roll-up, got %q", rows[len(rows)-1])
	}
	// Deterministic order: priority keys first (owner before repo before title),
	// each on its own row, ahead of any non-priority key.
	if !strings.HasPrefix(rows[0], "owner:") {
		t.Errorf("owner should be the first row, got %q", rows[0])
	}
	if !strings.HasPrefix(rows[1], "repo:") {
		t.Errorf("repo should be the second row, got %q", rows[1])
	}
	if !strings.HasPrefix(rows[2], "title:") {
		t.Errorf("title should be the third row, got %q", rows[2])
	}
	// Determinism: re-running yields byte-identical output (map iteration is random).
	out2, _ := r.summarizeArgs(args)
	if out != out2 {
		t.Error("summarizeArgs must be deterministic across runs")
	}
}

func TestSummarizeArgsScalarInline(t *testing.T) {
	r := newTestRenderer()
	args := mustJSON(t, map[string]any{
		"owner":  "stacklok",
		"repo":   "mecatl",
		"number": 24,
		"draft":  false,
	})
	out, ok := r.summarizeArgs(args)
	if !ok {
		t.Fatal("object args should summarize")
	}
	plain := stripANSIstr(out)
	if !strings.Contains(plain, `owner: "stacklok"`) {
		t.Errorf("short string should be inline-quoted, got %q", plain)
	}
	if !strings.Contains(plain, "number: 24") {
		t.Errorf("number should be inline verbatim, got %q", plain)
	}
	if !strings.Contains(plain, "draft: false") {
		t.Errorf("bool should be inline verbatim, got %q", plain)
	}
	// Nothing hidden → NO affordance/roll-up line.
	if strings.Contains(plain, "ctrl+t expand") || strings.Contains(plain, "more key") {
		t.Errorf("an all-short-scalar card should show no affordance line, got %q", plain)
	}
}

func TestSummarizeArgsFallback(t *testing.T) {
	r := newTestRenderer()
	cases := []string{
		`[1,2,3]`,    // bare array
		`"a string"`, // bare scalar string
		`42`,         // bare number
		`not json`,   // malformed
		``,           // empty
		`{}`,         // empty object
		`   `,        // whitespace
	}
	for _, c := range cases {
		if _, ok := r.summarizeArgs(c); ok {
			t.Errorf("summarizeArgs(%q) should fall back (ok=false)", c)
		}
	}
}

func TestHumanizeBytesSI(t *testing.T) {
	// SI/decimal math (1 KB = 1000 B, 1 MB = 1e6 B) — the number must reconcile
	// with bytes/1000, so a labelled "KB" is honest (not mislabelled KiB).
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{-5, "0 B"},
		{999, "999 B"},
		{1000, "1 KB"},   // exactly 1000 → 1 KB (would be "0.98 KB" under 1024 math)
		{1500, "1.5 KB"}, // 1500/1000
		{5000, "5 KB"},   // trailing .0 trimmed
		{999999, "1000 KB"},
		{1000000, "1 MB"}, // exactly 1e6 → 1 MB
		{2500000, "2.5 MB"},
		{1_000_000_000, "1 GB"},
		{2_500_000_000, "2.5 GB"},
		{1_000_000_000_000, "1 TB"},
	}
	for _, c := range cases {
		if got := humanizeBytes(c.n); got != c.want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestParseMCPName(t *testing.T) {
	server, tool, ok := parseMCPName("mcp__github__issue_write")
	if !ok || server != "github" || tool != "issue_write" {
		t.Fatalf("got (%q, %q, %v), want (github, issue_write, true)", server, tool, ok)
	}
	// The tool half may itself contain "__": split on the FIRST "__" only.
	server, tool, ok = parseMCPName("mcp__foo__bar__baz")
	if !ok || server != "foo" || tool != "bar__baz" {
		t.Fatalf("got (%q, %q, %v), want (foo, bar__baz, true)", server, tool, ok)
	}
	// Non-MCP names.
	for _, name := range []string{"Read", "Bash", "mcp__only", "mcp__", "notmcp__a__b", ""} {
		if _, _, ok := parseMCPName(name); ok {
			t.Errorf("parseMCPName(%q) should be ok=false", name)
		}
	}
}

func TestMCPTitleFallback(t *testing.T) {
	got, ok := mcpTitle("mcp__github__issue_write")
	if !ok || got != "GitHub · Issue write" {
		t.Fatalf("got (%q, %v), want (GitHub · Issue write, true)", got, ok)
	}
	// Unknown server + unknown tool → humanized.
	got, ok = mcpTitle("mcp__foo__bar_baz")
	if !ok || got != "Foo · Bar baz" {
		t.Fatalf("got (%q, %v), want (Foo · Bar baz, true)", got, ok)
	}
	// Non-MCP name.
	if _, ok := mcpTitle("Read"); ok {
		t.Error("mcpTitle(Read) should be ok=false")
	}
}

func TestMCPTitleHyphenatedServerNotMangled(t *testing.T) {
	// A real hyphenated/namespaced server must NOT be title-cased into
	// "Io-github-stacklok-playwright" — the raw token is the readable choice.
	got, ok := mcpTitle("mcp__io-github-stacklok-playwright__browser_click")
	if !ok {
		t.Fatal("MCP name should produce a title")
	}
	server, _, _ := strings.Cut(got, " · ")
	if server != "io-github-stacklok-playwright" {
		t.Errorf("hyphenated server should stay raw, got server part %q (full %q)", server, got)
	}
	// The known-server map stays the high-quality path (not regressed by the fallback).
	if got, _ := mcpTitle("mcp__github__issue_write"); !strings.HasPrefix(got, "GitHub · ") {
		t.Errorf("known server should keep its pretty name, got %q", got)
	}
	// A plain short unknown token is still title-cased (the fallback only fires for
	// hyphenated/long tokens).
	if got := humanizeMCPServer("notion"); got != "Notion" {
		t.Errorf("a plain short token should title-case, got %q", got)
	}
}

func TestSummarizeArgsSanitizes(t *testing.T) {
	r := newTestRenderer()
	// A value carrying raw escapes (ESC [ 2 J = clear screen; ESC [ 31 m = colour)
	// must be stripped by sanitizeTerminal BEFORE styling, so the injected sequences
	// never reach the terminal — neither in the COLLAPSED summary nor the EXPANDED
	// full JSON. lipgloss emits its OWN SGR escapes, so we assert the *injected*
	// sequences are absent rather than "no ESC at all".
	evil := "before\x1b[2Jafter and some more text to push past the inline length budget"
	args := mustJSON(t, map[string]any{"body": evil, "method": "x\x1b[31mred"})

	for _, tc := range []struct {
		name   string
		expand bool
	}{{"collapsed", false}, {"expanded", true}} {
		out := r.renderToolBlock("mcp__github__issue_write", args, tc.expand)
		if strings.Contains(out, "\x1b[2J") {
			t.Errorf("%s render leaked the clear-screen escape", tc.name)
		}
		if strings.Contains(out, "\x1b[31mred") {
			t.Errorf("%s render leaked the injected colour escape", tc.name)
		}
		// Belt-and-braces: NO bare ESC byte from the injected payload survives. After
		// stripping lipgloss's own SGR escapes the output must carry no 0x1b at all.
		if strings.ContainsRune(stripANSIstr(out), 0x1b) {
			t.Errorf("%s render leaked a bare ESC byte after SGR-stripping", tc.name)
		}
	}

	// DIRECT-CALL assertions (the de-vacuuming, #3). The assertions above route
	// through valStyle.Render / the themed render path, and lipgloss .Render strips
	// \x1b itself — so they'd pass even if sanitizeTerminal were deleted from these
	// paths (they measure lipgloss, not the harness). These call mcpTitle DIRECTLY
	// (NOT through any styled render) and assert the returned string is escape-free,
	// so the guard actually exercises sanitizeTerminal.
	//
	// mcpTitle is the security-critical path: it builds its title by '+'
	// concatenation (NO %q / strconv.Quote), over server/tool tokens that are
	// MCP-SERVER-NAMED — attacker-controllable — and that path was previously
	// untested. (The value paths — summarizeStringValue / summarizeValue scalars —
	// are %q/strconv.Quote-rendered, which itself escapes a raw ESC into the literal
	// text "\\x1b"; sanitizeTerminal there is defence-in-depth, not the load-bearing
	// guard, and a raw ESC inside an array element is invalid JSON that never reaches
	// the inline-join. So mcpTitle is where a direct-call guard has real teeth.)
	//
	// MUTATION-VERIFIED: deleting sanitizeTerminal from mcpTitle fails BOTH
	// assertions below (boundary ESC and mid-token ESC) — they bypass lipgloss.
	const esc = "\x1b"
	if got, _ := mcpTitle("mcp__\x1b[2Jevil__\x1b[31mtool"); strings.Contains(got, esc) {
		t.Errorf("mcpTitle leaked an ESC byte from the (server-derived) name: %q", got)
	}
	// An ESC embedded MID-token in the server name (not just at the boundary):
	if got, _ := mcpTitle("mcp__ev\x1bil__browser_click"); strings.Contains(got, esc) {
		t.Errorf("mcpTitle leaked a mid-token ESC byte from the server name: %q", got)
	}
}

func TestRenderToolMCPExpandedShowsRawNameAndJSON(t *testing.T) {
	r := newTestRenderer()
	args := mustJSON(t, map[string]any{"owner": "stacklok", "repo": "mecatl", "method": "create"})
	out := stripANSIstr(r.renderToolBlock("mcp__github__issue_write", args, true))
	if !strings.Contains(out, "GitHub · Issue write") {
		t.Errorf("expanded head should keep the friendly title, got:\n%s", out)
	}
	if !strings.Contains(out, "mcp__github__issue_write") {
		t.Errorf("expanded head should show the raw MCP name, got:\n%s", out)
	}
	// Full pretty JSON present (indented object), not the compact summary.
	if !strings.Contains(out, `"owner": "stacklok"`) {
		t.Errorf("expanded card should show full pretty JSON, got:\n%s", out)
	}
}

func TestSummarizeResultProminentFields(t *testing.T) {
	r := newTestRenderer()
	// A large JSON result with prominent fields surfaces a few of them + a size line.
	result := mustJSON(t, map[string]any{
		"html_url": "https://github.com/stacklok/mecatl/issues/24",
		"number":   24,
		"state":    "open",
		"body":     longBody(),
		"extra1":   1, "extra2": 2, "extra3": 3,
	})
	out, ok := r.summarizeResult(result)
	if !ok {
		t.Fatal("a large JSON object result should summarize")
	}
	plain := stripANSIstr(out)
	if !strings.Contains(plain, "https://github.com/stacklok/mecatl/issues/24") {
		t.Errorf("expected the url surfaced, got %q", plain)
	}
	if !strings.Contains(plain, "number: 24") {
		t.Errorf("expected the number surfaced, got %q", plain)
	}
	if lc := lineCount(plain); lc > maxToolResultLines {
		t.Errorf("result summary must stay within the line cap (%d), got %d", maxToolResultLines, lc)
	}
	// Field ORDER is pinned (resultProminentKeys order): html_url before number
	// before state, regardless of JSON map iteration order.
	iURL := strings.Index(plain, "html_url:")
	iNum := strings.Index(plain, "number:")
	iState := strings.Index(plain, "state:")
	if iURL < 0 || iNum <= iURL || iState <= iNum {
		t.Errorf("result fields must render in resultProminentKeys order (html_url < number < state), got:\n%s", plain)
	}
	// A non-JSON, line-shaped result is NOT summarized (unchanged behaviour).
	if _, ok := r.summarizeResult("line one\nline two\nline three"); ok {
		t.Error("a non-JSON result should not summarize")
	}
	// A small JSON result is not summarized either.
	if _, ok := r.summarizeResult(`{"a":1}`); ok {
		t.Error("a small JSON result should not summarize")
	}
	// A LARGE-but-malformed JSON body (truncated "{") returns ("", false) so the
	// caller falls through to the existing line-cap — never a crash or garbage.
	malformed := "{" + strings.Repeat("\n", maxToolResultLines+2) + `"html_url": "x"`
	if out, ok := r.summarizeResult(malformed); ok || out != "" {
		t.Errorf("a malformed large JSON result must fall through (\"\", false), got (%q, %v)", out, ok)
	}
}

// mcpCardArgs is the canonical issue-card example from the plan's target shape: a
// long body alongside scannable scalar/array fields.
func mcpCardArgs(t *testing.T) string {
	t.Helper()
	return mustJSON(t, map[string]any{
		"method": "create",
		"owner":  "stacklok",
		"repo":   "mecatl",
		"title":  "compact tool cards",
		"labels": []any{"enhancement"},
		"body":   longBody(),
	})
}

// TestMCPCardCollapsedGolden pins the compact collapsed MCP card. Paired with the
// direct assertions in TestSummarizeArgsLongStringCollapsed / ...ArrayObject... /
// ...ScalarInline and TestMCPTitleFallback (friendly title, inline scalars,
// inline array, collapsed long body, no raw name).
func TestMCPCardCollapsedGolden(t *testing.T) {
	r := newTestRenderer()
	got := stripANSIstr(r.renderToolBlock("mcp__github__issue_write", mcpCardArgs(t), false))
	compareGolden(t, "tool_mcp_card_collapsed.golden", []byte(got))
}

// TestMCPCardExpandedGolden pins the expanded MCP card. Paired with the direct
// assertions in TestRenderToolMCPExpandedShowsRawNameAndJSON (friendly title +
// raw mcp__ name + full pretty JSON).
func TestMCPCardExpandedGolden(t *testing.T) {
	r := newTestRenderer()
	got := stripANSIstr(r.renderToolBlock("mcp__github__issue_write", mcpCardArgs(t), true))
	compareGolden(t, "tool_mcp_card_expanded.golden", []byte(got))
}

// renderResolvedToolBlock builds a RESOLVED tool block (with a result body and typed
// content blocks) and renders it through renderTool — the real card path, so the
// result + blocks wiring is exercised end to end. A peer of renderToolBlock (which
// builds an unresolved call-only block for the args goldens).
func (r *renderer) renderResolvedToolBlock(name, args, body string, blocks []client.ContentBlock, expand bool) string {
	b := &block{
		kind:         blockTool,
		toolID:       "call-1",
		toolName:     name,
		toolArgs:     args,
		resolved:     true,
		resultBody:   body,
		resultBlocks: blocks,
	}
	return r.renderTool(b, expand)
}

// TestMCPCardBlocksGolden pins a resolved MCP card whose result carries typed
// content blocks (a resource link + image) alongside the model-facing text body.
// The blocks surface as distinct muted artifact lines (↗ resource-link, [image])
// IN ADDITION to the text body — not buried in/below it. The two existing tool-card
// goldens (collapsed/expanded, no blocks) are untouched: nil resultBlocks leaves the
// text path byte-unchanged.
func TestMCPCardBlocksGolden(t *testing.T) {
	r := newTestRenderer()
	args := mustJSON(t, map[string]any{"owner": "stacklok", "repo": "mecatl"})
	blocks := []client.ContentBlock{
		{Kind: client.ContentBlockResourceLink, Name: "issue-24", URL: "https://github.com/stacklok/mecatl/issues/24"},
		{Kind: client.ContentBlockImage, MimeType: "image/png"},
		{Kind: client.ContentBlockText, Text: "already in the body — must NOT double-render"},
	}
	got := stripANSIstr(r.renderResolvedToolBlock(
		"mcp__github__issue_write", args, "Created issue #24", blocks, false))
	compareGolden(t, "tool_mcp_card_blocks.golden", []byte(got))
	// Direct assertions: the resource-link + image lines surface distinctly; the
	// text block does NOT double-render.
	if !strings.Contains(got, "↗ issue-24 · https://github.com/stacklok/mecatl/issues/24") {
		t.Errorf("expected the resource-link artifact line, got:\n%s", got)
	}
	if !strings.Contains(got, "[image: image/png]") {
		t.Errorf("expected the image artifact line, got:\n%s", got)
	}
	if strings.Contains(got, "already in the body") {
		t.Errorf("a text block must NOT double-render, got:\n%s", got)
	}
	// The model-facing body still renders (not replaced).
	if !strings.Contains(got, "Created issue #24") {
		t.Errorf("expected the model-facing text body, got:\n%s", got)
	}
}

// TestGenericLongJSONCardGolden pins a non-MCP tool whose args are a generic long
// JSON object: the head stays the plain tool name, the args collapse to the
// compact summary. Paired with TestSummarizeArgsLongStringCollapsed /
// TestSummarizeArgsBoundedRows (compaction + row cap + roll-up).
func TestGenericLongJSONCardGolden(t *testing.T) {
	r := newTestRenderer()
	args := mustJSON(t, map[string]any{
		"query":       "func main",
		"path":        "cmd/mecatui",
		"description": longBody(),
		"alpha":       1, "bravo": 2, "charlie": 3, "delta": 4,
		"echo": 5, "foxtrot": 6, "golf": 7, "hotel": 8,
	})
	got := stripANSIstr(r.renderToolBlock("CustomSearch", args, false))
	compareGolden(t, "tool_generic_longjson_collapsed.golden", []byte(got))
}

func TestResultBodyExpandedUnchanged(t *testing.T) {
	r := newTestRenderer()
	result := mustJSON(t, map[string]any{
		"html_url": "https://example.com/x",
		"body":     longBody(),
	})
	// summarizeResolvedResult gates on collapsed-only: expanded → ok=false, so the
	// full sanitized body renders through resultBody.
	b := &block{kind: blockTool, resolved: true, resultBody: result}
	if _, ok := r.summarizeResolvedResult(b, true); ok {
		t.Error("expanded result must not be summarized")
	}
	full := r.resultBody(result, true)
	if !strings.Contains(full, "padding to grow") {
		t.Error("expanded result body must be the full sanitized body")
	}
}
