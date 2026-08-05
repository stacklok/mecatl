package ui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// sessionStateProjection is the comparable snapshot of the Model's
// session-derived fields — the exact set resetSession (model.go) owns. The
// /clear drift guard compares this projection against a freshly-zeroed
// reference, so the comparison bites if resetSession and /clear ever drift from
// the field set. Keep this in sync with resetSession.
type sessionStateProjection struct {
	convEmpty     bool
	stuck         bool
	filesChanged  []string
	filesSeenLen  int
	filesSeenNil  bool
	usage         client.Usage
	contextTokens int64
	activeTool    string
	toolProgress  string
}

// sessionState projects a Model onto its session-derived fields for comparison.
func sessionState(m Model) sessionStateProjection {
	return sessionStateProjection{
		convEmpty:     m.conv.isEmpty(),
		stuck:         m.stuck,
		filesChanged:  m.filesChanged,
		filesSeenLen:  len(m.filesSeen),
		filesSeenNil:  m.filesSeen == nil,
		usage:         m.usage,
		contextTokens: m.contextTokens,
		activeTool:    m.activeTool,
		toolProgress:  m.toolProgress,
	}
}

// runBatchLeaves runs cmd; if it yields a tea.BatchMsg it invokes each leaf
// command (recursively flattening nested batches), so a synchronous side effect
// buried in a batched closure (e.g. the SendPrompt frame) actually fires. Leaf
// result msgs are discarded — the test inspects the side effect, not the msgs.
func runBatchLeaves(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runBatchLeaves(c)
		}
	}
}

// builtinNames extracts the ordered built-in names for a caps + wired-collab combo.
func builtinNames(caps client.Capabilities, w wiredCollaborators) []string {
	bs := builtinCommands(caps, w)
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.name
	}
	return out
}

// TestBuiltinCommandsCapsFilter pins the caps gate AND the fixed order: /clear
// and /help are always present (and lead, in that order); /mcp needs caps.MCP &&
// the MCP collaborator wired; /agents (the def inventory) needs caps.Agents &&
// the agents collaborator wired; /team (the live overlay) needs caps.Teams;
// /skills needs caps.Skills && the skills collaborator wired; /soul needs
// caps.Soul && the soul collaborator wired; /usermodel needs caps.UserModel &&
// the user-model collaborator wired; /models needs caps.ModelSelection && the model
// lister wired; /worktrees needs caps.Worktrees && the worktree lister wired
// (issue #102); /effort is gated identically to /models and follows it (ADR 0055).
// The fixed order is clear, help, mcp, agents, team, skills, soul, usermodel,
// models, effort, worktrees.
func TestBuiltinCommandsCapsFilter(t *testing.T) {
	all := client.Capabilities{MCP: true, Agents: true, Teams: true, Skills: true, Soul: true, UserModel: true, ModelSelection: true, Worktrees: true, Scheduling: true, Posture: "auto"}
	cases := []struct {
		name string
		caps client.Capabilities
		w    wiredCollaborators
		want []string
	}{
		{"bare", client.Capabilities{}, wiredCollaborators{}, []string{"clear", "help"}},
		{"mcp cap but not wired", client.Capabilities{MCP: true}, wiredCollaborators{}, []string{"clear", "help"}},
		{"mcp wired but no cap", client.Capabilities{}, wiredCollaborators{MCP: true}, []string{"clear", "help"}},
		{"mcp cap and wired", client.Capabilities{MCP: true}, wiredCollaborators{MCP: true}, []string{"clear", "help", "mcp"}},
		{"agents cap but not wired", client.Capabilities{Agents: true}, wiredCollaborators{}, []string{"clear", "help"}},
		{"agents wired but no cap", client.Capabilities{}, wiredCollaborators{Agents: true}, []string{"clear", "help"}},
		{"agents cap and wired", client.Capabilities{Agents: true}, wiredCollaborators{Agents: true}, []string{"clear", "help", "agents"}},
		{"teams only", client.Capabilities{Teams: true}, wiredCollaborators{}, []string{"clear", "help", "team"}},
		{"skills cap but not wired", client.Capabilities{Skills: true}, wiredCollaborators{}, []string{"clear", "help"}},
		{"skills wired but no cap", client.Capabilities{}, wiredCollaborators{Skills: true}, []string{"clear", "help"}},
		{"skills cap and wired", client.Capabilities{Skills: true}, wiredCollaborators{Skills: true}, []string{"clear", "help", "skills"}},
		{"soul cap but not wired", client.Capabilities{Soul: true}, wiredCollaborators{}, []string{"clear", "help"}},
		{"soul wired but no cap", client.Capabilities{}, wiredCollaborators{Soul: true}, []string{"clear", "help"}},
		{"soul cap and wired", client.Capabilities{Soul: true}, wiredCollaborators{Soul: true}, []string{"clear", "help", "soul"}},
		{"usermodel cap but not wired", client.Capabilities{UserModel: true}, wiredCollaborators{}, []string{"clear", "help"}},
		{"usermodel wired but no cap", client.Capabilities{}, wiredCollaborators{UserModel: true}, []string{"clear", "help"}},
		{"usermodel cap and wired", client.Capabilities{UserModel: true}, wiredCollaborators{UserModel: true}, []string{"clear", "help", "usermodel"}},
		{"models cap but not wired", client.Capabilities{ModelSelection: true}, wiredCollaborators{}, []string{"clear", "help"}},
		{"models wired but no cap", client.Capabilities{}, wiredCollaborators{Models: true}, []string{"clear", "help"}},
		{"models cap and wired", client.Capabilities{ModelSelection: true}, wiredCollaborators{Models: true}, []string{"clear", "help", "models", "effort"}},
		{"worktrees cap but not wired", client.Capabilities{Worktrees: true}, wiredCollaborators{}, []string{"clear", "help"}},
		{"worktrees wired but no cap", client.Capabilities{}, wiredCollaborators{Worktrees: true}, []string{"clear", "help"}},
		{"worktrees cap and wired", client.Capabilities{Worktrees: true}, wiredCollaborators{Worktrees: true}, []string{"clear", "help", "worktrees"}},
		{"schedule cap but not wired", client.Capabilities{Scheduling: true}, wiredCollaborators{}, []string{"clear", "help"}},
		{"schedule wired but no cap", client.Capabilities{}, wiredCollaborators{Scheduling: true}, []string{"clear", "help"}},
		{"schedule cap and wired", client.Capabilities{Scheduling: true}, wiredCollaborators{Scheduling: true}, []string{"clear", "help", "schedule"}},
		{"sessions wired (no caps bit)", client.Capabilities{}, wiredCollaborators{Sessions: true}, []string{"clear", "help", "sessions"}},
		{"sessions not wired", client.Capabilities{}, wiredCollaborators{}, []string{"clear", "help"}},
		{"posture empty omits the builtin", client.Capabilities{}, wiredCollaborators{}, []string{"clear", "help"}},
		{"posture set adds the builtin", client.Capabilities{Posture: "yolo"}, wiredCollaborators{}, []string{"clear", "help", "posture"}},
		{"posture strict still shows (chrome is reportable)", client.Capabilities{Posture: "strict"}, wiredCollaborators{}, []string{"clear", "help", "posture"}},
		{
			"all",
			all,
			wiredCollaborators{MCP: true, Agents: true, Skills: true, Soul: true, UserModel: true, Models: true, Worktrees: true, Scheduling: true, Sessions: true},
			[]string{"clear", "help", "mcp", "agents", "team", "skills", "soul", "usermodel", "models", "effort", "worktrees", "schedule", "sessions", "posture"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := builtinNames(tc.caps, tc.w)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("builtinCommands order/filter = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBuiltinByName covers the dispatch lookup: a registered built-in is found,
// a gated-off one is not, and an unknown name is not.
func TestBuiltinByName(t *testing.T) {
	// agents cap + teams + skills cap + model_selection + worktrees + scheduling, mcp off.
	caps := client.Capabilities{Agents: true, Teams: true, Skills: true, ModelSelection: true, Worktrees: true, Scheduling: true}
	wired := wiredCollaborators{Agents: true, Skills: true, Models: true, Worktrees: true, Scheduling: true}
	if _, ok := builtinByName(caps, wired, "clear"); !ok {
		t.Error("clear should be found (always registered)")
	}
	if _, ok := builtinByName(caps, wired, "agents"); !ok {
		t.Error("agents should be found (agents cap + wired)")
	}
	if _, ok := builtinByName(caps, wiredCollaborators{Skills: true, Models: true, Worktrees: true}, "agents"); ok {
		t.Error("agents should NOT be found (agents cap on but not wired)")
	}
	if _, ok := builtinByName(caps, wired, "team"); !ok {
		t.Error("team should be found (teams on)")
	}
	if _, ok := builtinByName(caps, wired, "skills"); !ok {
		t.Error("skills should be found (skills cap + wired)")
	}
	if _, ok := builtinByName(caps, wiredCollaborators{Agents: true, Models: true, Worktrees: true}, "skills"); ok {
		t.Error("skills should NOT be found (skills cap on but not wired)")
	}
	if _, ok := builtinByName(caps, wired, "models"); !ok {
		t.Error("models should be found (model_selection cap + wired)")
	}
	if _, ok := builtinByName(caps, wiredCollaborators{Agents: true, Skills: true, Worktrees: true}, "models"); ok {
		t.Error("models should NOT be found (model_selection cap on but not wired)")
	}
	if _, ok := builtinByName(caps, wired, "worktrees"); !ok {
		t.Error("worktrees should be found (worktrees cap + wired)")
	}
	if _, ok := builtinByName(caps, wiredCollaborators{Agents: true, Skills: true, Models: true, Scheduling: true}, "worktrees"); ok {
		t.Error("worktrees should NOT be found (worktrees cap on but not wired)")
	}
	if _, ok := builtinByName(caps, wired, "schedule"); !ok {
		t.Error("schedule should be found (scheduling cap + wired)")
	}
	if _, ok := builtinByName(caps, wiredCollaborators{Agents: true, Skills: true, Models: true, Worktrees: true}, "schedule"); ok {
		t.Error("schedule should NOT be found (scheduling cap on but not wired)")
	}
	if _, ok := builtinByName(caps, wired, "mcp"); ok {
		t.Error("mcp should NOT be found (mcp off / not wired)")
	}
	if _, ok := builtinByName(caps, wired, "nope"); ok {
		t.Error("unknown name should not be found")
	}
}

// TestMergeCommands pins the merge contract: built-ins lead in their order, then
// discovered rows (already name-sorted), de-duplicated, and a built-in WINS a
// name collision (the colliding discovered row is dropped).
func TestMergeCommands(t *testing.T) {
	builtins := []client.Command{
		{Name: "clear", Description: "clear it", Builtin: true},
		{Name: "help", Description: "help", Builtin: true},
	}
	discovered := []client.Command{
		{Name: "clear", Description: "WORKSPACE clear — should be shadowed"}, // collides → dropped
		{Name: "deploy", Description: "deploy"},
		{Name: "deploy", Description: "dup deploy"}, // duplicate discovered → dropped
		{Name: "review", Description: "review"},
	}

	got := mergeCommands(builtins, discovered)

	wantNames := []string{"clear", "help", "deploy", "review"}
	if len(got) != len(wantNames) {
		t.Fatalf("merged len = %d (%v), want %d %v", len(got), got, len(wantNames), wantNames)
	}
	for i, n := range wantNames {
		if got[i].Name != n {
			t.Fatalf("merged[%d] = %q, want %q (order)", i, got[i].Name, n)
		}
	}
	// The "clear" that survived must be the BUILT-IN one (precedence), not the
	// workspace row.
	if !got[0].Builtin || got[0].Description != "clear it" {
		t.Fatalf("collision: want built-in clear to win, got %+v", got[0])
	}
}

// builtinDispatchModel builds a connected, sized, idle model wired to a
// fakeSender (so we can assert no prompt is sent) with the given caps. The MCP
// collaborator is left nil (so /mcp is never registered) unless wireMCP is set.
func builtinDispatchModel(t *testing.T, caps client.Capabilities, wireMCP bool) (Model, *fakeSender) {
	t.Helper()
	recv := &fakeRecver{gate: make(chan struct{})}
	send := &fakeSender{}
	conv := &fakeConv{recv: recv, send: send, caps: caps}
	deps := Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       aztec(),
		Server:      "127.0.0.1:8080",
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         t.Context(),
		NoAltScreen: true,
	}
	if wireMCP {
		deps.MCP = &fakeMCP{}
	}
	m := New(deps)
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: caps},
	)
	return m, send
}

// typeText feeds a string into the idle textarea rune-by-rune, discarding the
// per-keystroke commands (palette fetch / cursor blink). It is the offline way to
// populate the input without driving the program loop.
func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		mm, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
	}
	return m
}

// pressEnter submits via the Enter key, returning the model and the resulting
// command (nil for a built-in that opens no stream).
func pressEnter(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return mm.(Model), cmd
}

// TestClearBuiltinResetsState drives a "/clear"+enter and asserts the
// conversation and all derived session state reset, the status reads "cleared",
// no stream opens, and no prompt is sent.
func TestClearBuiltinResetsState(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)

	// Seed some session state as if a turn had run.
	m.conv.addUser("earlier prompt")
	m.conv.addError("some error")
	m.recordFileChange("note.txt")
	m.usage = client.Usage{InputTokens: 1000, OutputTokens: 200}
	m.contextTokens = 1200
	m.activeTool = "Write"
	m.toolProgress = "writing"
	// Scrolled up (auto-follow off): /clear must re-arm it, since an empty
	// conversation is at-bottom and the next run must tail its streaming deltas.
	m.stuck = false
	m.refreshView()
	if m.conv.isEmpty() {
		t.Fatal("precondition: conversation should be non-empty before /clear")
	}

	m = typeText(t, m, "/clear")
	m, cmd := pressEnter(t, m)

	// Drift guard: every session-derived field must match a freshly-zeroed
	// reference (resetSession of a brand-new model). This compares the WHOLE
	// session-derived projection, so a future field added to resetSession that
	// /clear forgets — or a field /clear zeroes but resetSession does not — fails
	// here without this test re-listing the fields by hand. (sessionState is
	// defined below; keep it in sync with resetSession in model.go.)
	zero := New(m.deps).resetSession()
	if !reflect.DeepEqual(sessionState(m), sessionState(zero)) {
		t.Errorf("/clear must zero ALL session-derived fields (drift?):\n got  %+v\n want %+v",
			sessionState(m), sessionState(zero))
	}
	// Belt-and-braces explicit checks (kept readable; the DeepEqual above is the
	// drift-proof one). Each line mirrors a field resetSession (model.go) owns.
	if !m.conv.isEmpty() {
		t.Error("/clear should empty the conversation")
	}
	if !m.stuck {
		t.Error("/clear should re-arm auto-follow (stuck) — an empty conversation is at-bottom")
	}
	if m.filesChanged != nil || m.filesSeen != nil {
		t.Errorf("/clear should reset changed-files: filesChanged=%v filesSeen=%v", m.filesChanged, m.filesSeen)
	}
	if m.usage != (client.Usage{}) {
		t.Errorf("/clear should reset usage, got %+v", m.usage)
	}
	if m.contextTokens != 0 {
		t.Errorf("/clear should reset contextTokens, got %d", m.contextTokens)
	}
	if m.activeTool != "" || m.toolProgress != "" {
		t.Errorf("/clear should reset activeTool/toolProgress, got %q/%q", m.activeTool, m.toolProgress)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "cleared") {
		t.Errorf("status should read 'cleared', got %q", stripANSIstr(m.statusMsg))
	}
	if m.ta.Value() != "" {
		t.Errorf("input should be reset after /clear, got %q", m.ta.Value())
	}
	if cmd != nil {
		t.Error("/clear should open no stream / issue no command")
	}
	if m.phase != phaseIdle {
		t.Errorf("phase = %v, want idle after /clear", m.phase)
	}
	if len(send.frames()) != 0 {
		t.Errorf("/clear must send no frames, got %d", len(send.frames()))
	}
	// The zero-state welcome card reappears.
	if !strings.Contains(stripANSIstr(m.View().Content), "Welcome to mecatui") {
		t.Error("zero-state welcome card should reappear after /clear")
	}
}

// TestClearBuiltinNoOpWhileRunning asserts /clear is rejected with a status while
// a run streams, leaving the conversation intact.
func TestClearBuiltinNoOpWhileRunning(t *testing.T) {
	m, _ := builtinDispatchModel(t, client.Capabilities{}, false)
	m.conv.addUser("a prompt")
	m.phase = phaseRunning

	mm, cmd := m.runClear()
	m = mm.(Model)

	if m.conv.isEmpty() {
		t.Error("/clear must NOT clear the conversation while running")
	}
	if cmd != nil {
		t.Error("/clear no-op should issue no command")
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "cannot clear while running") {
		t.Errorf("want 'cannot clear while running' status, got %q", stripANSIstr(m.statusMsg))
	}
}

// TestHelpBuiltinOpensOverlay drives "/help"+enter and asserts the help overlay
// opens, the textarea is blurred, no stream opens, and no prompt is sent.
func TestHelpBuiltinOpensOverlay(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)

	m = typeText(t, m, "/help")
	m, cmd := pressEnter(t, m)

	if !m.showHelp {
		t.Error("/help should open the help overlay (showHelp=true)")
	}
	if m.ta.Focused() {
		t.Error("/help should blur the textarea")
	}
	if cmd != nil {
		t.Error("/help should open no stream / issue no command")
	}
	if len(send.frames()) != 0 {
		t.Errorf("/help must send no frames, got %d", len(send.frames()))
	}
}

// TestPaletteEnterRunsBuiltinDirectly asserts that pressing enter on a selected
// BUILT-IN palette row runs it immediately (clears the conversation) rather than
// text-completing it into the input.
func TestPaletteEnterRunsBuiltinDirectly(t *testing.T) {
	m, _ := builtinDispatchModel(t, client.Capabilities{}, false)
	m.conv.addUser("prior turn")
	m.refreshView()

	// Open the palette on "/", with the first row (clear) selected.
	m = typeText(t, m, "/")
	if !m.palette.open {
		t.Fatal("palette should open on '/'")
	}
	if m.palette.filtered[0].Name != "clear" || !m.palette.filtered[0].Builtin {
		t.Fatalf("first row = %+v, want built-in clear", m.palette.filtered[0])
	}

	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)

	if !m.conv.isEmpty() {
		t.Error("enter on built-in /clear row should RUN it (clear the conversation), not text-complete")
	}
	// Text-completion would have left "/clear " in the input; running clears it.
	if strings.HasPrefix(m.ta.Value(), "/clear") {
		t.Errorf("built-in row should not be text-completed into the input, got %q", m.ta.Value())
	}
	if m.palette.open {
		t.Error("palette should close after running a built-in")
	}
}

// TestBuiltinSubmitNeverSends pins that a bare built-in line never reaches the
// model: across /clear and /help, the fakeSender records zero frames AND the
// submit returns a nil command — so no async stream-open cmd (which a bare
// Update never executes in this test) can slip through unobserved.
func TestBuiltinSubmitNeverSends(t *testing.T) {
	for _, name := range []string{"/clear", "/help"} {
		t.Run(name, func(t *testing.T) {
			m, send := builtinDispatchModel(t, client.Capabilities{}, false)
			m = typeText(t, m, name)
			_, cmd := pressEnter(t, m)
			if len(send.frames()) != 0 {
				t.Errorf("%s must not send any frame, got %d", name, len(send.frames()))
			}
			// A real prompt submit returns batch(send, waitCmd, sp.Tick); a built-in
			// returns nil (it opens no stream). Asserting nil closes the gap where an
			// async stream-open cmd could be returned but never executed in-test.
			if cmd != nil {
				t.Errorf("%s submit should return a nil command (no stream open), got non-nil", name)
			}
		})
	}
}

// TestBuiltinNameWithArgsFallsThrough guards the intercept against matching the
// FIRST token regardless of trailing args: a built-in NAME followed by a space +
// args ("/clear now") has a space, so commandPrefix returns false, the
// submitPrompt built-in intercept does NOT fire, and the line is sent to the
// model as a normal prompt — NOT swallowed, and the conversation is NOT cleared.
func TestBuiltinNameWithArgsFallsThrough(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)

	// Seed a turn so we can prove /clear's reset did NOT happen.
	m.conv.addUser("earlier prompt")
	m.refreshView()

	m = typeText(t, m, "/clear now")
	m, cmd := pressEnter(t, m)

	// It must have entered the normal send path: a stream opened (non-nil batch
	// command) and the prompt frame carries the full literal line.
	if cmd == nil {
		t.Fatal("'/clear now' should fall through to the normal send (non-nil command), not be intercepted")
	}
	// Flush the batch's leaf commands so the synchronous SendPrompt closure runs
	// (the submit returns tea.Batch(send, waitCmd, sp.Tick); running the batch
	// yields a BatchMsg of leaves, each of which must be invoked).
	runBatchLeaves(cmd)
	frames := send.frames()
	if len(frames) == 0 {
		t.Fatal("'/clear now' should record a SendPrompt frame (sent, not swallowed)")
	}
	var sentText string
	for _, fr := range frames {
		if p := fr.GetPrompt(); p != nil {
			sentText = p.GetText()
		}
	}
	if sentText != "/clear now" {
		t.Errorf("sent prompt = %q, want the literal %q", sentText, "/clear now")
	}
	// The conversation must NOT have been cleared — the user line is still there,
	// plus the new "/clear now" user line the normal send appended.
	if m.conv.isEmpty() {
		t.Error("'/clear now' must NOT clear the conversation (it is not the bare /clear built-in)")
	}
	if m.phase != phaseRunning {
		t.Errorf("phase = %v, want running after a normal send", m.phase)
	}
}

// TestIsKnownBuiltinName asserts the static built-in name set covers every name
// from the builtinCommands table AND that an unknown name is false.
func TestIsKnownBuiltinName(t *testing.T) {
	known := []string{
		"clear", "help", "mcp", "agents", "team", "skills", "soul", "usermodel",
		"models", "effort", "worktrees", "schedule", "sessions", "posture",
	}
	for _, name := range known {
		if !isKnownBuiltinName(name) {
			t.Errorf("isKnownBuiltinName(%q) = false, want true (known builtin)", name)
		}
	}
	if isKnownBuiltinName("foo") {
		t.Error("isKnownBuiltinName(\"foo\") = true, want false (unknown)")
	}
	if isKnownBuiltinName("") {
		t.Error("isKnownBuiltinName(\"\") = true, want false (empty)")
	}
	// Drift-proof: the hardcoded list must equal the derived set exactly.
	// Adding a builtin to allBuiltins without updating this test must fail.
	knownSet := make(map[string]bool, len(known))
	for _, n := range known {
		knownSet[n] = true
	}
	if len(known) != len(knownBuiltinNames) {
		t.Errorf("hardcoded known list has %d names, knownBuiltinNames has %d", len(known), len(knownBuiltinNames))
	}
	for n := range knownBuiltinNames {
		if !knownSet[n] {
			t.Errorf("name %q is in knownBuiltinNames but MISSING from the hardcoded known list", n)
		}
	}
}

// TestSlashNoMatchBlocksKnownBuiltins asserts that typing a bare known-but-gated
// builtin ("/mcp" when MCP is not available) does NOT send it to the model: the
// textarea is NOT cleared, a warning status is set, and nothing enters the
// conversation. Unknown names ("/foo") still fall through to the normal send path.
func TestSlashNoMatchBlocksKnownBuiltins(t *testing.T) {
	// Model with MINIMAL caps (mcp off, teams off, etc.)
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)

	// Type "/mcp" (a known builtin, but gated off) and press Enter.
	m = typeText(t, m, "/mcp")
	m, cmd := pressEnter(t, m)

	// Must NOT send to the model.
	if cmd != nil {
		t.Error("gated-off /mcp submit should return nil command (blocked, not sent)")
	}
	if len(send.frames()) != 0 {
		t.Errorf("gated-off /mcp must send no frames, got %d", len(send.frames()))
	}
	// The textarea must NOT be cleared (user keeps their input for editing).
	if m.ta.Value() == "" {
		t.Error("/mcp blocked: textarea should KEEP the input (not clear it)")
	}
	// A warning status must be set.
	got := stripANSIstr(m.statusMsg)
	if !strings.Contains(got, "not available") || !strings.Contains(got, "/mcp") {
		t.Errorf("statusMsg = %q, want 'not available' warning naming '/mcp'", got)
	}
}

// TestSlashNoMatchCaseInsensitive asserts that a known builtin with mixed case
// ("/MODELS") is still blocked when gated off, not sent to the model.
// interceptSlashCommand normalises to lower-case so the guard works.
func TestSlashNoMatchCaseInsensitive(t *testing.T) {
	// Zero caps → /models is gated off.
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)

	m = typeText(t, m, "/MODELS")
	m, cmd := pressEnter(t, m)

	// Must NOT send to the model — blocked by the case-insensitive guard.
	if cmd != nil {
		t.Error("/MODELS (gated off) should return nil command (blocked), not be sent")
	}
	if len(send.frames()) != 0 {
		t.Errorf("/MODELS must send no frames, got %d", len(send.frames()))
	}
	if m.ta.Value() == "" {
		t.Error("/MODELS blocked: textarea should KEEP the input")
	}
	got := stripANSIstr(m.statusMsg)
	if !strings.Contains(got, "not available") {
		t.Errorf("statusMsg = %q, want 'not available' warning", got)
	}
}

// TestSlashNoMatchUnknownSends asserts that an unknown slash name ("/foo") still
// falls through to the normal send path (workspace/custom command).
func TestSlashNoMatchUnknownSends(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)

	m = typeText(t, m, "/foo")
	m, cmd := pressEnter(t, m)

	// Must send to the model (normal fall-through).
	if cmd == nil {
		t.Fatal("/foo should fall through to the normal send (non-nil cmd), not be blocked")
	}
	// The textarea should be cleared by the normal send path.
	if m.ta.Value() != "" {
		t.Errorf("/foo: textarea should be cleared by the normal send, got %q", m.ta.Value())
	}
	// Flush the batch so the send frame fires.
	runBatchLeaves(cmd)
	frames := send.frames()
	if len(frames) == 0 {
		t.Fatal("/foo should send a frame to the model")
	}
}

// TestSlashGatedByCapsStillExecutes asserts that a builtin name that IS currently
// registered (gated ON) still EXECUTES the builtin — the "known but gated" block
// fires only when builtinByName misses.
func TestSlashGatedByCapsStillExecutes(t *testing.T) {
	// Model with MCP on + MCP collaborator wired, so /mcp IS registered.
	caps := client.Capabilities{MCP: true}
	m, send := builtinDispatchModel(t, caps, true)

	m = typeText(t, m, "/mcp")
	m, cmd := pressEnter(t, m)

	// The builtin executed (opened the MCP panel). It may return a command to
	// load data, but it must NOT send any frames to the model.
	if len(send.frames()) != 0 {
		t.Errorf("/mcp (registered) must send no frames, got %d", len(send.frames()))
	}
	// The textarea should be cleared (the builtin resets it).
	if strings.HasPrefix(m.ta.Value(), "/mcp") {
		t.Errorf("/mcp (registered): textarea should be cleared after the builtin runs, got %q", m.ta.Value())
	}
	_ = cmd // may be non-nil (to load MCP inventory); that's fine
}
