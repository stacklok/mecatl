package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// newMCPModel builds an idle, sized Model wired to the given fakeMCP, ready to
// open a surface. It reuses the conversation fake transport (unused here) so the
// Model is identical to production save the injected MCP.
func newMCPModel(t *testing.T, th theme.Theme, mcp client.MCP) Model {
	t.Helper()
	recv := &fakeRecver{script: nil, gate: make(chan struct{})}
	send := &fakeSender{}
	conv := &fakeConv{recv: recv, send: send}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		MCP:         mcp,
		Theme:       th,
		Server:      "127.0.0.1:8080",
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001"},
	)
	return m
}

// mcpActive returns the open MCP modal's state (or nil) off the Model, so a test
// can read the migrated surface state without holding an mcpState field. It
// asserts the modal IS a *mcpState, which pins the open path too.
func mcpActive(m Model) *mcpState {
	if m.modal == nil {
		return nil
	}
	s, ok := m.modal.(*mcpState)
	if !ok {
		return nil
	}
	return s
}

// runCmd executes a tea.Cmd to completion and returns its msg (nil-safe). MCP
// commands are single-shot RPC closures, so this is deterministic.
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// feedMCPInsertion runs the insertion marker/business command once, reduces that
// explicitly expected message, and deliberately discards the follow-up widget blink.
// Focus mutates synchronously; waiting for its timer adds no state relevant here.
func feedMCPInsertion(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	msg := runCmd(cmd)
	switch msg.(type) {
	case client.MCPPromptGotMsg, mcpInsertResourceMsg:
	default:
		t.Fatalf("insertion command returned %T, want MCP prompt/resource insertion message", msg)
	}
	mm, followup := m.Update(msg)
	if followup == nil {
		t.Fatal("insertion update returned no textarea focus/blink command")
	}
	return mm.(Model)
}

// feedCmd runs a tea.Cmd and feeds its result back into the Model, flattening a
// tea.BatchMsg into its constituent cmds (the panel batches sources + groups). It
// recurses so nested batches/sequences are drained deterministically.
func feedCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	msg := runCmd(cmd)
	if msg == nil {
		return m
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = feedCmd(t, m, c)
		}
		return m
	}
	mm, next := m.Update(msg)
	m = mm.(Model)
	if next != nil {
		m = feedCmd(t, m, next)
	}
	return m
}

// openOverlay opens the given surface, runs its initial RPC cmd(s), and feeds the
// result(s) back — leaving the Model in the loaded surface state for a golden.
func openOverlay(t *testing.T, m Model, key tea.KeyPressMsg) Model {
	t.Helper()
	mm, cmd := m.Update(key)
	m = mm.(Model)
	return feedCmd(t, m, cmd)
}

// keyPress builds a printable-rune ctrl key press.
func ctrlKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

func samplePanelMCP() *fakeMCP {
	return &fakeMCP{
		sources: []client.MCPSource{
			{
				Name: "toolhive", Kind: "toolhive", Enabled: true, Group: "dev",
				Servers: []client.MCPServerInfo{
					{Name: "fetch", URL: "http://127.0.0.1:9001", Transport: "streamable-http", Group: "dev"},
					{Name: "github", URL: "http://127.0.0.1:9002", Transport: "streamable-http", Group: "dev"},
				},
				Diagnostics: []string{"skipped sse server 'legacy' (unsupported transport)"},
			},
			{Name: "static", Kind: "static", Enabled: false},
		},
	}
}

// samplePanelWithGroupsMCP is the panel inventory plus a non-empty ToolHive
// groups list, for the groups-line golden.
func samplePanelWithGroupsMCP() *fakeMCP {
	f := samplePanelMCP()
	f.groups = []string{"dev", "prod"}
	return f
}

func sampleResourceMCP() *fakeMCP {
	return &fakeMCP{
		resources: []client.MCPResource{
			{Server: "fetch", URI: "https://example.com/a", Name: "page-a", MimeType: "text/html"},
			{Server: "fetch", URI: "https://example.com/b", Name: "page-b", MimeType: "text/html"},
		},
		contents: []client.MCPResourceContents{
			{URI: "https://example.com/a", MimeType: "text/plain", Text: "the resource body\nsecond line"},
		},
	}
}

func samplePromptMCP() *fakeMCP {
	return &fakeMCP{
		prompts: []client.MCPPrompt{
			{Server: "review", Name: "code-review", Description: "review a file", Arguments: []client.MCPPromptArgument{
				{Name: "path", Description: "file to review", Required: true},
			}},
			{Server: "review", Name: "summarize", Description: "no-arg prompt"},
		},
		promptDsc: "rendered",
		promptMsg: []client.MCPPromptMessage{{Role: "user", Text: "Please review path X."}},
	}
}

func aztec() theme.Theme { return theme.New("aztec", theme.AztecPalette()) }

// --- surface routing ----------------------------------------------------------

// TestRunMCPOpensPanelOnModal asserts runMCP validates, blurs the textarea,
// installs an *mcpState on m.modal at mcpPanel, and fires the inventory+groups
// RPC — and that the surface renders through m.modal.Render (the parent's
// centerCard owns placement; Render returns the UNCENTERED body).
func TestRunMCPOpensPanelOnModal(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePanelMCP())

	mm, cmd := m.runMCP()
	m = mm.(Model)
	st := mcpActive(m)
	if st == nil || st.view != mcpPanel {
		t.Fatalf("modal surface = %v, want a *mcpState at mcpPanel", m.modal)
	}
	if !st.loading {
		t.Error("panel should be loading until the RPC result lands")
	}
	if m.ta.Focused() {
		t.Error("opening the panel should blur the textarea")
	}
	if cmd == nil {
		t.Fatal("runMCP should fire the inventory+groups RPC batch")
	}
	// Render returns the uncentered body (no leading blank row from lipgloss.Place);
	// the parent applies centerCard.
	body, regions := st.Render(100, 24)
	if regions != nil {
		t.Error("MCP surface is keyboard-only; regions should be nil")
	}
	if !strings.HasPrefix(stripANSIstr(body), "MCP inventory") {
		t.Errorf("Render body should start at the title (uncentered), got:\n%q", stripANSIstr(body))
	}
}

// TestMCPOpenGuards asserts the surface only opens while idle with an MCP
// collaborator.
func TestMCPOpenGuards(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePanelMCP())
	m.phase = phaseRunning
	mm, _ := m.runMCP()
	if mm.(Model).modal != nil {
		t.Error("surface opened while running")
	}

	m2 := newMCPModel(t, aztec(), nil)
	m2.deps.MCP = nil
	mm2, _ := m2.runMCP()
	if mm2.(Model).modal != nil {
		t.Error("surface opened with nil MCP dep")
	}
}

// TestMCPEscClosesSurface asserts esc in the panel reports closed and the
// dispatchSurfaceKey closed path nils m.modal, restoring idle input.
func TestMCPEscClosesSurface(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePanelMCP())
	m = openOverlay(t, m, ctrlKey('o'))
	if mcpActive(m) == nil {
		t.Fatal("surface should be open")
	}
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	if m.modal != nil {
		t.Fatalf("esc did not close the surface: modal=%v", m.modal)
	}
}

// TestMCPPromptGotClosesAndInserts asserts a MCPPromptGotMsg while the surface is
// open: the surface's HandleMsg returns closed=true + handled=false (nothing to
// mutate — it is closing), dispatchSurfaceMsg tears the surface down, and the
// Model-side updateMCPMsg remnant loads the rendered prompt into the input.
func TestMCPPromptGotClosesAndInserts(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePromptMCP())
	m = openOverlay(t, m, ctrlKey('p'))
	st := mcpActive(m)
	if st == nil {
		t.Fatal("prompt surface should be open")
	}
	got := client.MCPPromptGotMsg{Name: "summarize", Messages: samplePromptMCP().promptMsg}
	cmd, handled, closed := st.HandleMsg(got)
	if !closed || handled {
		t.Fatalf("HandleMsg(PromptGot) = handled=%v closed=%v, want handled=false closed=true", handled, closed)
	}
	if cmd != nil {
		t.Error("PromptGot carries no surface cmd")
	}
	// Through the full dispatcher: the surface closes AND the Model remnant inserts.
	mm, _ := m.Update(got)
	m = mm.(Model)
	if m.modal != nil {
		t.Fatalf("PromptGot should close the surface: modal=%v", m.modal)
	}
	if !strings.Contains(m.ta.Value(), "Please review path X.") {
		t.Fatalf("Model remnant did not load the prompt: %q", m.ta.Value())
	}
}

// --- panel ------------------------------------------------------------------

// TestMCPListsRenderEnabledEmptyStates covers the successful empty state of each
// MCP list through the public Model renderer. Each caller supplies distinct copy
// when MCP is enabled, rather than collapsing an empty response into the disabled
// capability message.
func TestMCPListsRenderEnabledEmptyStates(t *testing.T) {
	tests := []struct {
		name      string
		key       tea.KeyPressMsg
		emptyNote string
	}{
		{"inventory", ctrlKey('o'), "No MCP sources configured on this server."},
		{"resources", ctrlKey('r'), "No resources advertised by the connected MCP servers."},
		{"prompts", ctrlKey('p'), "No prompts advertised by the connected MCP servers."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMCPModel(t, aztec(), &fakeMCP{})
			m.caps.MCP = true
			m = openOverlay(t, m, tt.key)
			st := mcpActive(m)
			if st == nil || st.loading || st.errMsg != "" {
				t.Fatalf("state = %#v, want loaded without error", st)
			}
			got := string(stripANSI([]byte(m.View().Content)))
			if !strings.Contains(got, tt.emptyNote) {
				t.Errorf("View() missing empty state %q:\n%s", tt.emptyNote, got)
			}
		})
	}
}

func TestMCPPanelGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePanelMCP())
	m = openOverlay(t, m, ctrlKey('o'))
	st := mcpActive(m)
	if st == nil || st.view != mcpPanel {
		t.Fatalf("view = %v, want mcpPanel", m.modal)
	}
	if !st.groupsDone || len(st.groups) != 0 {
		t.Fatalf("groups state = %#v, want done+empty", st)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "mcp_panel.golden", got)
}

// TestMCPPanelGroupsGolden locks the panel WITH ToolHive groups rendered.
func TestMCPPanelGroupsGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePanelWithGroupsMCP())
	m = openOverlay(t, m, ctrlKey('o'))
	st := mcpActive(m)
	if st == nil || len(st.groups) != 2 {
		t.Fatalf("groups = %#v, want 2", st)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "mcp_panel_groups.golden", got)
}

// TestMCPPanelGroupsDegradeQuietly asserts a failed groups fetch does NOT break
// the panel: sources still render and the shared error seam stays clear.
func TestMCPPanelGroupsDegradeQuietly(t *testing.T) {
	// sources succeed; groups error — model the split by feeding the msgs directly.
	m := newMCPModel(t, aztec(), samplePanelMCP())
	mm, _ := m.runMCP()
	m = mm.(Model)
	m = applyAll(m,
		client.MCPSourcesMsg{Sources: samplePanelMCP().sources},
		client.MCPErrMsg{Op: "list groups", Class: client.MCPErrServer, Err: status.Error(codes.Internal, "boom")},
	)
	st := mcpActive(m)
	if st == nil {
		t.Fatal("surface should be open")
	}
	if st.errMsg != "" {
		t.Errorf("shared error seam set by groups failure: %q", st.errMsg)
	}
	if !st.groupsErr || len(st.sources) == 0 {
		t.Errorf("panel state = %#v, want groupsErr + sources intact", st)
	}
}

// TestMCPPanelRefreshPicksUpLiveStatus asserts the panel's r-refresh re-issues
// the inventory fetch and renders the CURRENT server status — a server that
// reconnected (a diagnostic cleared) since the panel first opened — rather than
// the stale first snapshot. The footer flips to the "updated" wording.
func TestMCPPanelRefreshPicksUpLiveStatus(t *testing.T) {
	fm := samplePanelMCP()
	// After the first fetch the toolhive source had a skipped server; on the live
	// re-probe it has reconnected (no diagnostics) and a new server appeared.
	fm.nextSources = []client.MCPSource{
		{
			Name: "toolhive", Kind: "toolhive", Enabled: true, Group: "dev",
			Servers: []client.MCPServerInfo{
				{Name: "fetch", URL: "http://127.0.0.1:9001", Transport: "streamable-http", Group: "dev"},
				{Name: "github", URL: "http://127.0.0.1:9002", Transport: "streamable-http", Group: "dev"},
				{Name: "legacy", URL: "http://127.0.0.1:9003", Transport: "streamable-http", Group: "dev"},
			},
		},
		{Name: "static", Kind: "static", Enabled: false},
	}
	m := newMCPModel(t, aztec(), fm)
	m = openOverlay(t, m, ctrlKey('o'))
	st := mcpActive(m)
	if st == nil || st.view != mcpPanel {
		t.Fatalf("view = %v, want mcpPanel", m.modal)
	}
	// Initial snapshot: the toolhive source carries its skip diagnostic.
	if len(st.sources) == 0 || len(st.sources[0].Diagnostics) != 1 {
		t.Fatalf("initial sources lack the diagnostic: %#v", st.sources)
	}
	if st.refreshed {
		t.Fatalf("panel marked refreshed before any refresh")
	}

	// Press r → re-issues the fetch; feed the batched cmds back.
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = mm.(Model)
	if cmd == nil {
		t.Fatalf("refresh did not issue a fetch cmd")
	}
	m = feedCmd(t, m, cmd)

	if fm.sourcesCalls != 2 {
		t.Fatalf("ListMCPSources called %d times, want 2 (open + refresh)", fm.sourcesCalls)
	}
	st = mcpActive(m)
	// The panel now shows the LIVE status: 3 servers, diagnostic cleared.
	if got := len(st.sources[0].Servers); got != 3 {
		t.Fatalf("after refresh servers = %d, want 3 (live re-probe)", got)
	}
	if len(st.sources[0].Diagnostics) != 0 {
		t.Fatalf("after refresh diagnostics = %#v, want cleared", st.sources[0].Diagnostics)
	}
	if st.refreshing {
		t.Fatalf("refreshing flag stuck on after result landed")
	}
	if !st.refreshed {
		t.Fatalf("panel not marked refreshed after a successful re-probe")
	}
	// The footer advertises the updated state and the refresh key.
	view := string(stripANSI([]byte(m.View().Content)))
	if !strings.Contains(view, "updated") || !strings.Contains(view, "r refresh") {
		t.Fatalf("footer missing updated/refresh hint:\n%s", view)
	}
	if !strings.Contains(view, "legacy") {
		t.Fatalf("refreshed server 'legacy' not rendered:\n%s", view)
	}
}

// TestMCPPanelRefreshIndicatorWhileInFlight asserts that while a refresh is in
// flight the footer reads "refreshing…" and a second r is a no-op (no duplicate
// fetch), without clearing the already-shown sources.
func TestMCPPanelRefreshIndicatorWhileInFlight(t *testing.T) {
	fm := samplePanelMCP()
	m := newMCPModel(t, aztec(), fm)
	m = openOverlay(t, m, ctrlKey('o'))

	// Press r but DON'T feed the result yet: the panel is mid-refresh.
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = mm.(Model)
	st := mcpActive(m)
	if !st.refreshing {
		t.Fatalf("refreshing flag not set after r")
	}
	if cmd == nil {
		t.Fatalf("refresh issued no cmd")
	}
	if len(st.sources) == 0 {
		t.Fatalf("sources cleared during refresh (should stay visible)")
	}
	view := string(stripANSI([]byte(m.View().Content)))
	if !strings.Contains(view, "refreshing") {
		t.Fatalf("footer missing refreshing indicator:\n%s", view)
	}
	// A second r while in flight is a no-op.
	mm, cmd2 := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = mm.(Model)
	if cmd2 != nil {
		t.Fatalf("second r issued a duplicate fetch")
	}
	_ = m
}

// --- resource picker --------------------------------------------------------

func TestMCPResourceListGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), sampleResourceMCP())
	m = openOverlay(t, m, ctrlKey('r'))
	st := mcpActive(m)
	if st == nil || st.view != mcpResources || len(st.resources) != 2 {
		t.Fatalf("resources not loaded: %#v", st)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "mcp_resources.golden", got)
}

func TestMCPResourcePreviewGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), sampleResourceMCP())
	m = openOverlay(t, m, ctrlKey('r'))
	// Select the first resource → triggers ReadMcpResource → preview.
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if msg := runCmd(cmd); msg != nil {
		mm, _ = m.Update(msg)
		m = mm.(Model)
	}
	st := mcpActive(m)
	if st == nil || st.view != mcpResourcePrev {
		t.Fatalf("view = %v, want mcpResourcePrev", m.modal)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "mcp_resource_preview.golden", got)
}

// TestMCPResourceInsertIntoInput asserts that pressing enter in the resource
// preview inserts the resource text into the prompt input and closes the surface
// (same mechanism as the prompt path), so it can be sent as a normal turn.
func TestMCPResourceInsertIntoInput(t *testing.T) {
	m := newMCPModel(t, aztec(), sampleResourceMCP())
	m = openOverlay(t, m, ctrlKey('r'))
	// Select the first resource → read → preview.
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if msg := runCmd(cmd); msg != nil {
		mm, _ = m.Update(msg)
		m = mm.(Model)
	}
	st := mcpActive(m)
	if st == nil || st.view != mcpResourcePrev {
		t.Fatalf("view = %v, want mcpResourcePrev", m.modal)
	}
	// enter in the preview: run the insertion marker once. Its Update follow-up is
	// only the textarea widget's delayed blink; focus already changed synchronously.
	mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	m = feedMCPInsertion(t, m, cmd)
	if m.modal != nil {
		t.Fatalf("surface still open after insert: %v", m.modal)
	}
	if !strings.Contains(m.ta.Value(), "the resource body") {
		t.Fatalf("resource text not in input: %q", m.ta.Value())
	}
}

// TestMCPResourcePreviewCollapse is the focused liveness assertion for the
// resource-preview collapse path (issue #457 QA SHOULD-ADD): when a read
// resource's body exceeds the line cap (maxToolResultLines), renderResourcePreview
// (reached via Render → renderResourcePreview) must cap the body at the limit and
// emit the "+N more lines · <expand> expand" collapse marker carrying the LIVE
// ExpandTools chord. It is narrow and deterministic — it drives the free-function
// path directly, so it covers the cap + collapse marker the function-primitive
// golden does NOT (the golden's fixture body is two lines, under the cap).
func TestMCPResourcePreviewCollapse(t *testing.T) {
	th := aztec()
	hk := defaultHelpKeys()
	expandMark := hk.expandTools
	// A body of maxToolResultLines+5 lines trips the cap; the marker names the
	// 5 dropped lines and the live expand chord.
	var sb strings.Builder
	for i := 0; i < maxToolResultLines+5; i++ {
		sb.WriteString("line\n")
	}
	st := mcpState{view: mcpResourcePrev, preview: sb.String()}
	got := stripANSIstr(renderResourcePreview(th, st, hk))
	if !strings.Contains(got, "line") {
		t.Fatalf("preview body missing: %q", got)
	}
	if !strings.Contains(got, "+5 more lines · "+expandMark+" expand") {
		t.Errorf("preview should carry the collapse marker +5 more lines · %s expand: %q", expandMark, got)
	}
	// The kept body must be capped: exactly maxToolResultLines body lines
	// precede the marker (the title/footer chrome is not body). The toolArgs
	// style pads each line, so count "line" occurrences (one per body line).
	marker := "+5 more lines"
	idx := strings.Index(got, marker)
	if idx < 0 {
		t.Fatalf("collapse marker missing: %q", got)
	}
	body := got[:idx]
	// Subtract the title's "preview" (contains "view" not "line"), so the count
	// is the body-line count.
	if n := strings.Count(body, "line"); n != maxToolResultLines {
		t.Errorf("preview body should keep exactly %d body lines, got %d: %q", maxToolResultLines, n, body)
	}

	// Under the cap there is no collapse marker.
	var short strings.Builder
	for i := 0; i < maxToolResultLines; i++ {
		short.WriteString("line\n")
	}
	shortSt := mcpState{view: mcpResourcePrev, preview: short.String()}
	shortGot := stripANSIstr(renderResourcePreview(th, shortSt, hk))
	if strings.Contains(shortGot, "more lines") {
		t.Errorf("preview under the cap should not carry a collapse marker: %q", shortGot)
	}
}

// --- prompt picker ----------------------------------------------------------

func TestMCPPromptListGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePromptMCP())
	m = openOverlay(t, m, ctrlKey('p'))
	st := mcpActive(m)
	if st == nil || st.view != mcpPrompts || len(st.prompts) != 2 {
		t.Fatalf("prompts not loaded: %#v", st)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "mcp_prompts.golden", got)
}

func TestMCPPromptArgsGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePromptMCP())
	m = openOverlay(t, m, ctrlKey('p'))
	// First prompt has a required arg → enter enters the arg-entry sub-state.
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	st := mcpActive(m)
	if st == nil || st.view != mcpPromptArgs || len(st.argFields) != 1 {
		t.Fatalf("arg entry not entered: %#v", st)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "mcp_prompt_args.golden", got)
}

// TestMCPPromptSendIntoInput asserts that getting a prompt drops the rendered
// text into the prompt input and closes the surface, so the existing Converse
// flow sends it on enter.
func TestMCPPromptSendIntoInput(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePromptMCP())
	m = openOverlay(t, m, ctrlKey('p'))
	// Move to the no-arg prompt and select it → GetMcpPrompt → input buffer.
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	m = feedMCPInsertion(t, m, cmd)
	if m.modal != nil {
		t.Fatalf("surface still open: %v", m.modal)
	}
	if !strings.Contains(m.ta.Value(), "Please review path X.") {
		t.Fatalf("prompt text not in input: %q", m.ta.Value())
	}
}

func TestFeedMCPInsertionRunsBusinessMessageWithoutBlink(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePromptMCP())
	m.modal = &mcpState{view: mcpPrompts}
	m = feedMCPInsertion(t, m, func() tea.Msg {
		return client.MCPPromptGotMsg{Name: "review", Messages: samplePromptMCP().promptMsg}
	})
	if m.modal != nil || !strings.Contains(m.ta.Value(), "Please review") {
		t.Fatalf("business message was not reduced: modal=%v input=%q", m.modal, m.ta.Value())
	}
	// feedMCPInsertion never invokes the returned focus command. Cursor lifecycle
	// rendering remains owned by TestRenderInputInvalidatedByCursorMove.
}

// TestMCPArgsSubmitCollectsValues asserts typing into the required-arg field and
// submitting fires GetMcpPrompt with the entered value, landing in the input.
func TestMCPArgsSubmitCollectsValues(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePromptMCP())
	m = openOverlay(t, m, ctrlKey('p'))
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // enter arg entry
	m = mm.(Model)
	// Type a value into the focused field.
	for _, r := range "main.go" {
		mm, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
	}
	st := mcpActive(m)
	if got := st.argFields[0].input.Value(); got != "main.go" {
		t.Fatalf("arg value = %q, want main.go", got)
	}
	// Enter on the only (last) field submits; the result closes + inserts.
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	m = feedMCPInsertion(t, m, cmd)
	if m.modal != nil || !strings.Contains(m.ta.Value(), "Please review") {
		t.Fatalf("prompt not sent into input: modal=%v ta=%q", m.modal, m.ta.Value())
	}
}

// TestMCPArgsBlankRequiredKeepsFormOpen asserts that submitting a required-arg
// form with a blank field does NOT call GetMcpPrompt: it keeps the form open,
// surfaces the input-class error, and focuses the empty field.
func TestMCPArgsBlankRequiredKeepsFormOpen(t *testing.T) {
	fm := samplePromptMCP()
	m := newMCPModel(t, aztec(), fm)
	m = openOverlay(t, m, ctrlKey('p'))
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // enter arg entry
	m = mm.(Model)
	st := mcpActive(m)
	if st == nil || st.view != mcpPromptArgs {
		t.Fatalf("view = %v, want mcpPromptArgs", m.modal)
	}
	// Submit immediately with the field left blank.
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if cmd != nil {
		t.Fatalf("submit returned a cmd %v, want nil (no RPC)", runCmd(cmd))
	}
	if fm.getPromptCalls != 0 {
		t.Errorf("GetMCPPrompt called %d times, want 0", fm.getPromptCalls)
	}
	st = mcpActive(m)
	if st == nil || st.view != mcpPromptArgs {
		t.Errorf("form closed; view = %v, want mcpPromptArgs", m.modal)
	}
	if st.errCls != client.MCPErrInput || st.errMsg == "" {
		t.Errorf("error not surfaced: cls=%v msg=%q", st.errCls, st.errMsg)
	}
	if st.argCursor != 0 {
		t.Errorf("focus = %d, want first empty field (0)", st.argCursor)
	}
}

// --- error classes ----------------------------------------------------------

func TestMCPErrInputGolden(t *testing.T) {
	mcp := &fakeMCP{err: status.Error(codes.InvalidArgument, "unknown server \"nope\"")}
	m := newMCPModel(t, aztec(), mcp)
	m = openOverlay(t, m, ctrlKey('r'))
	st := mcpActive(m)
	if st == nil || st.errCls != client.MCPErrInput {
		t.Fatalf("errCls = %v, want input", st)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "mcp_err_input.golden", got)
}

func TestMCPErrServerGolden(t *testing.T) {
	mcp := &fakeMCP{err: status.Error(codes.Internal, "downstream blew up")}
	m := newMCPModel(t, aztec(), mcp)
	m = openOverlay(t, m, ctrlKey('p'))
	st := mcpActive(m)
	if st == nil || st.errCls != client.MCPErrServer {
		t.Fatalf("errCls = %v, want server", st)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "mcp_err_server.golden", got)
}

func TestMCPErrNotConfiguredGolden(t *testing.T) {
	mcp := &fakeMCP{err: status.Error(codes.FailedPrecondition, "no MCP provider configured")}
	m := newMCPModel(t, aztec(), mcp)
	m = openOverlay(t, m, ctrlKey('o'))
	st := mcpActive(m)
	if st == nil || st.errCls != client.MCPErrNotConfigured {
		t.Fatalf("errCls = %v, want not-configured", st)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "mcp_err_not_configured.golden", got)
}
