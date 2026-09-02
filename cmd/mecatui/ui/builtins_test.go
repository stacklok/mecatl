package ui

import (
	"fmt"
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

// TestBuiltinCommandsCapsFilter pins the caps gate AND the fixed order: /clear,
// /help, and /session are always present (and lead, in that order); /mcp needs
// caps.MCP && the MCP collaborator wired; /agents (the def inventory) needs caps.Agents &&
// the agents collaborator wired; /team (the live overlay) needs caps.Teams;
// /skills needs caps.Skills && the skills collaborator wired; /soul needs
// caps.Soul && the soul collaborator wired; /usermodel needs caps.UserModel &&
// the user-model collaborator wired; /models needs caps.ModelSelection && the model
// lister wired; /worktrees needs caps.Worktrees && the worktree lister wired
// (issue #102); /effort is gated identically to /models and follows it (ADR 0055).
// The fixed order is clear, help, session, mcp, agents, team, skills, soul, usermodel,
// models, effort, worktrees.
func TestBuiltinCommandsCapsFilter(t *testing.T) {
	all := client.Capabilities{MCP: true, Agents: true, Teams: true, Skills: true, Soul: true, UserModel: true, ModelSelection: true, Worktrees: true, Scheduling: true, ManualCompaction: true, Posture: "auto"}
	cases := []struct {
		name string
		caps client.Capabilities
		w    wiredCollaborators
		want []string
	}{
		{"bare", client.Capabilities{}, wiredCollaborators{}, []string{"clear", "help"}},
		{"compact cap but not wired", client.Capabilities{ManualCompaction: true}, wiredCollaborators{}, []string{"clear", "help"}},
		{"compact wired but no cap", client.Capabilities{}, wiredCollaborators{Compactor: true}, []string{"clear", "help"}},
		{"compact cap and wired", client.Capabilities{ManualCompaction: true}, wiredCollaborators{Compactor: true}, []string{"clear", "help", "compact"}},
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
			wiredCollaborators{MCP: true, Agents: true, Skills: true, Soul: true, UserModel: true, Models: true, Worktrees: true, Scheduling: true, Sessions: true, Compactor: true},
			[]string{"clear", "help", "compact", "mcp", "agents", "team", "skills", "soul", "usermodel", "models", "effort", "worktrees", "schedule", "sessions", "posture"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := builtinNames(tc.caps, tc.w)
			want := append([]string{"clear", "help", "session", "retry", "diagnostics"}, tc.want[2:]...)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("builtinCommands order/filter = %v, want %v", got, want)
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

// TestDebugAskBuiltinGatedOnEnv pins the /debug-ask gate (issue #488): without
// Deps.DebugAsk the builtin is absent; with it, it is registered.
func TestDebugAskBuiltinGatedOnEnv(t *testing.T) {
	caps := client.Capabilities{}
	if _, ok := builtinByName(caps, wiredCollaborators{}, "debug-ask"); ok {
		t.Error("/debug-ask must be ABSENT without the DebugAsk gate")
	}
	if _, ok := builtinByName(caps, wiredCollaborators{DebugAsk: true}, "debug-ask"); !ok {
		t.Error("/debug-ask must be registered with the DebugAsk gate on")
	}
}

// TestDebugAskInjectsFakeAsk pins that /debug-ask drives the REAL ask reducer:
// the fake ask opens the modal at phaseIdle (applyPermissionAsk does not gate
// on phase), cycles through three payloads, and resolving it leaves a notice.
func TestDebugAskInjectsFakeAsk(t *testing.T) {
	m, _ := builtinDispatchModel(t, client.Capabilities{}, false)
	m.deps.DebugAsk = true

	// Three invocations cycle through the three canned payloads, each opening a
	// fresh ask (resolve between invocations).
	seenArgs := map[string]bool{}
	for i := 0; i < 3; i++ {
		b, ok := builtinByName(m.caps, m.wiredCollaborators(), "debug-ask")
		if !ok {
			t.Fatal("precondition: /debug-ask must be registered with DebugAsk on")
		}
		mm, _ := b.run(m)
		m = mm.(Model)
		if m.phase != phaseAwaitingApproval {
			t.Fatalf("invocation %d: /debug-ask must open the modal (even at idle), got phase %v", i, m.phase)
		}
		if approvalSurfaceOf(t, m).ask.Tool != "Bash" {
			t.Errorf("invocation %d: the fake ask must be a Bash ask, got %q", i, approvalSurfaceOf(t, m).ask.Tool)
		}
		seenArgs[approvalSurfaceOf(t, m).ask.Args] = true
		// Resolve it (allow once) so the next invocation's ask opens fresh.
		m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
		if got := lastNotice(m); got != "permission allowed" {
			t.Errorf("invocation %d: resolving must record the notice, got %q", i, got)
		}
	}
	if len(seenArgs) != 3 {
		t.Errorf("three invocations must cycle through three DISTINCT payloads, got %d", len(seenArgs))
	}
	if m.modal != nil {
		t.Error("/clear should close the approval surface")
	}
	if m.phase != phaseIdle {
		t.Errorf("a /debug-ask opened at idle must RESUME to idle on resolve, got %v", m.phase)
	}
}

// TestDebugAskIdleResolveDoesNotSpin reproduces issue-#553's "stuck spinner":
// a /debug-ask opened at phaseIdle must NOT resume into phaseRunning on
// resolve (that left the footer spinner running with no run behind it).
func TestDebugAskIdleResolveDoesNotSpin(t *testing.T) {
	m, _ := builtinDispatchModel(t, client.Capabilities{}, false)
	m.deps.DebugAsk = true
	b, _ := builtinByName(m.caps, m.wiredCollaborators(), "debug-ask")
	mm, _ := b.run(m)
	m = mm.(Model)
	if m.phase != phaseAwaitingApproval {
		t.Fatalf("precondition: the debug ask must open the modal, got %v", m.phase)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.phase != phaseIdle {
		t.Fatalf("resolving an idle-opened debug ask must return to idle, got %v", m.phase)
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
	m := newTestModelFromDeps(deps)
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

// pressEnter submits via the Enter key, returning the model and resulting command.
func pressEnter(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return mm.(Model), cmd
}

// firstBatchLeaf runs only the first leaf of a command batch. Both /clear and a
// prompt put their externally-observable RPC command first; avoiding the reader
// and spinner leaves keeps reducer tests synchronous and deterministic.
func firstBatchLeaf(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("command result = %T, want non-empty tea.BatchMsg", msg)
	}
	return batch[0]()
}

// TestClearBuiltinCreatesThenBindsThenCloses proves the create-first handoff:
// /clear preserves the old transcript while creation is pending, binds a new empty
// session through SessionReady, then closes the old session best-effort.
func TestClearBuiltinCreatesThenBindsThenCloses(t *testing.T) {
	m, _ := builtinDispatchModel(t, client.Capabilities{}, false)
	conv := m.deps.Session.(*fakeConv)
	// builtinDispatchModel binds a ready message directly, so model the startup
	// create that would already have happened in the real lifecycle.
	conv.createCount = 1
	conv.closeErr = fmt.Errorf("close unavailable")

	// Seed state that must remain visible until the replacement exists.
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
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "effective-provider", ModelID: "effective-model", ReasoningEffort: "high"}
	m.createModelSelection = client.ModelSelection{ProviderID: "stale-provider", ModelID: "stale-model", ReasoningEffort: "low"}
	m.activePlacement = client.Placement{Kind: "local", Label: "current-worktree"}
	oldID := m.sessionID

	m.pendingMode = "plan"
	mm, clearCmd := m.runClear()
	m = mm.(Model)
	if m.phase != phaseConnecting || m.sessionID != oldID || m.conv.isEmpty() {
		t.Fatalf("pending /clear must retain the old UI/session while blocking input: phase=%v id=%q empty=%v", m.phase, m.sessionID, m.conv.isEmpty())
	}
	if got := conv.closed(); len(got) != 0 {
		t.Fatalf("old session closed before replacement creation: %v", got)
	}
	_, promptCmd := pressEnter(t, m)
	if promptCmd != nil {
		t.Error("pending /clear must not send a prompt to the old session")
	}

	msg := firstBatchLeaf(t, clearCmd)
	ready, ok := msg.(clearSessionReadyMsg)
	if !ok {
		t.Fatalf("clear create message = %T, want clearSessionReadyMsg", msg)
	}
	if ready.placement.Label == "" {
		t.Error("clear successor omitted server-authored placement metadata")
	}
	if got := conv.closed(); len(got) != 0 {
		t.Fatalf("old session closed before successful new-session binding: %v", got)
	}

	mm, closeCmd := m.Update(ready)
	m = mm.(Model)
	zero := newTestModelFromDeps(m.deps).resetSession()
	if !reflect.DeepEqual(sessionState(m), sessionState(zero)) {
		t.Errorf("successful /clear must reset ALL session-derived fields:\n got  %+v\n want %+v", sessionState(m), sessionState(zero))
	}
	if m.sessionID == oldID || m.sessionID == "" || m.phase != phaseIdle {
		t.Errorf("successful /clear must bind a new idle session, got id=%q phase=%v", m.sessionID, m.phase)
	}
	if m.pendingMode != "" {
		t.Errorf("successful /clear must settle pending mode, got %q", m.pendingMode)
	}
	// A delayed mode response for the old session must not alter the replacement.
	mm, _ = m.Update(client.ModeChangedMsg{SessionID: oldID, Mode: "plan"})
	m = mm.(Model)
	if m.activeMode != "default" {
		t.Errorf("stale old-session mode update changed replacement mode to %q", m.activeMode)
	}
	m.prompt.Rewrite("new prompt")
	m, promptCmd = pressEnter(t, m)
	_ = firstBatchLeaf(t, promptCmd)
	frames := conv.send.frames()
	if got := frames[len(frames)-1].GetPrompt().GetSessionId(); got != m.sessionID {
		t.Errorf("post-clear prompt session = %q, want new session %q", got, m.sessionID)
	}
	if got := conv.closed(); len(got) != 0 {
		t.Fatalf("old session closed before reducer bound replacement: %v", got)
	}
	runBatchLeaves(closeCmd)
	if got := conv.closed(); !reflect.DeepEqual(got, []string{oldID}) {
		t.Errorf("closed sessions = %v, want old session only after binding", got)
	}
	if got := conv.ops(); !reflect.DeepEqual(got, []string{"clear", "close"}) {
		t.Errorf("session RPC order = %v, want [create close]", got)
	}
	if m.sessionID == oldID || m.sessionID == "" {
		t.Errorf("best-effort close failure must retain new session: id=%q", m.sessionID)
	}
}

func TestClearBuiltinCreateFailureKeepsOldSession(t *testing.T) {
	m, _ := builtinDispatchModel(t, client.Capabilities{}, false)
	conv := m.deps.Session.(*fakeConv)
	conv.createErr = fmt.Errorf("create unavailable")
	m.conv.addUser("earlier prompt")
	m.recordFileChange("note.txt")
	oldID := m.sessionID

	mm, clearCmd := m.runClear()
	m = mm.(Model)
	msg := firstBatchLeaf(t, clearCmd)
	failed, ok := msg.(clearSessionFailedMsg)
	if !ok {
		t.Fatalf("clear create message = %T, want clearSessionFailedMsg", msg)
	}
	mm, _ = m.Update(failed)
	m = mm.(Model)

	if m.sessionID != oldID || m.conv.isEmpty() || m.phase != phaseIdle {
		t.Errorf("failed /clear must retain old active state: id=%q empty=%v phase=%v", m.sessionID, m.conv.isEmpty(), m.phase)
	}
	if m.filesChanged == nil {
		t.Error("failed /clear must retain old derived UI state")
	}
	if got := conv.closed(); len(got) != 0 {
		t.Errorf("failed /clear must not close old session, got %v", got)
	}
	if strings.Contains(stripANSIstr(m.statusMsg), "cleared") {
		t.Errorf("failed /clear status must not claim cleared: %q", stripANSIstr(m.statusMsg))
	}
	m.prompt.Rewrite("retry old session")
	m, promptCmd := pressEnter(t, m)
	_ = firstBatchLeaf(t, promptCmd)
	frames := conv.send.frames()
	if got := frames[len(frames)-1].GetPrompt().GetSessionId(); got != oldID {
		t.Errorf("post-failure prompt session = %q, want old session %q", got, oldID)
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
	if m.prompt.Focused() {
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
// BUILT-IN palette row starts /clear immediately rather than text-completing it
// into the input; the old conversation remains visible until the replacement is
// created.
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

	if m.phase != phaseConnecting || m.conv.isEmpty() {
		t.Error("enter on built-in /clear row should start a create-first handoff, not text-complete")
	}
	// Text-completion would have left "/clear " in the input; running clears it.
	if strings.HasPrefix(m.prompt.Value(), "/clear") {
		t.Errorf("built-in row should not be text-completed into the input, got %q", m.prompt.Value())
	}
	if m.palette.open {
		t.Error("palette should close after running a built-in")
	}
}

// TestBuiltinPaletteAndTypedDispatchAgree proves that selecting a builtin is the
// same operation as submitting its canonical bare line, including while a run is
// active. /clear's running warning makes both ingress paths observable.
func TestBuiltinPaletteAndTypedDispatchAgree(t *testing.T) {
	for _, phase := range []phase{phaseIdle, phaseRunning} {
		name := "idle"
		if phase == phaseRunning {
			name = "running"
		}
		t.Run(name, func(t *testing.T) {
			typed, _ := builtinDispatchModel(t, client.Capabilities{}, false)
			typed.phase = phase
			typed.conv.addUser("prior turn")
			typed = typeText(t, typed, "/clear")
			typed, typedCmd := pressEnter(t, typed)

			selected, _ := builtinDispatchModel(t, client.Capabilities{}, false)
			selected.phase = phase
			selected.conv.addUser("prior turn")
			selected = typeText(t, selected, "/")
			mm, selectedCmd := selected.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			selected = mm.(Model)

			if (typedCmd == nil) != (selectedCmd == nil) {
				t.Fatalf("command presence differs: typed nil=%t, palette nil=%t", typedCmd == nil, selectedCmd == nil)
			}
			if typed.phase != selected.phase || !reflect.DeepEqual(sessionState(typed), sessionState(selected)) || stripANSIstr(typed.statusMsg) != stripANSIstr(selected.statusMsg) {
				t.Fatalf("typed and palette builtin outcomes differ: typed phase=%v state=%+v status=%q; palette phase=%v state=%+v status=%q", typed.phase, sessionState(typed), stripANSIstr(typed.statusMsg), selected.phase, sessionState(selected), stripANSIstr(selected.statusMsg))
			}
			if typed.palette.open || selected.palette.open || typed.prompt.Value() != "" || selected.prompt.Value() != "" {
				t.Fatalf("both builtin ingress paths must clear input and close the palette: typed=%q/%t palette=%q/%t", typed.prompt.Value(), typed.palette.open, selected.prompt.Value(), selected.palette.open)
			}
		})
	}
}

// TestBuiltinSubmitNeverSends pins that a bare built-in line never reaches the
// model: /clear creates a replacement session, while /help returns nil. Neither
// can open a Converse stream or send a prompt frame.
func TestBuiltinSubmitNeverSends(t *testing.T) {
	for _, name := range []string{"/clear", "/help"} {
		t.Run(name, func(t *testing.T) {
			m, send := builtinDispatchModel(t, client.Capabilities{}, false)
			m = typeText(t, m, name)
			_, cmd := pressEnter(t, m)
			if len(send.frames()) != 0 {
				t.Errorf("%s must not send any frame, got %d", name, len(send.frames()))
			}
			// Only /clear legitimately returns a session-create handoff command;
			// neither built-in may return a Converse stream command.
			if name == "/help" && cmd != nil {
				t.Errorf("%s submit should return nil, got non-nil", name)
			}
			if name == "/clear" && cmd == nil {
				t.Errorf("%s submit should start session creation", name)
			}
		})
	}
}

// TestBuiltinNameWithArgsStaysLocal verifies a recognized no-argument builtin
// keeps its input, warns locally, and never reaches the model.
func TestBuiltinNameWithArgsStaysLocal(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)
	m.conv.addUser("earlier prompt")
	m.refreshView()

	m = typeText(t, m, "/clear now")
	m, cmd := pressEnter(t, m)

	if cmd != nil {
		t.Fatal("/clear with arguments must not open a model run")
	}
	if len(send.frames()) != 0 {
		t.Fatalf("/clear with arguments sent %d frames", len(send.frames()))
	}
	if m.prompt.Value() != "/clear now" {
		t.Errorf("input = %q, want unchanged", m.prompt.Value())
	}
	if got := stripANSIstr(m.statusMsg); !strings.Contains(got, "does not take arguments") || !strings.Contains(got, "/clear") {
		t.Errorf("status = %q, want argument warning", got)
	}
	if m.conv.isEmpty() {
		t.Error("/clear with arguments must not clear the conversation")
	}
}

// TestDispatchBareBuiltinWhitespace defines a bare invocation as exactly a
// builtin name after trimming surrounding Unicode whitespace. Recognized builtins
// with arguments are retained locally for correction; unknown slash commands stay
// model-facing.
func TestDispatchBareBuiltinWhitespace(t *testing.T) {
	for _, tc := range []struct {
		input   string
		handled bool
	}{
		{"/clear", true},
		{" \u2003/clear\u00a0", true},
		{"/clear\n", true},
		{"/clear now", true},
		{"/clear\nnow", true},
		{"/foo", false},
	} {
		t.Run(strings.ReplaceAll(tc.input, "\n", "\\n"), func(t *testing.T) {
			m, _ := builtinDispatchModel(t, client.Capabilities{}, false)
			_, _, handled := m.dispatchBareBuiltin(tc.input)
			if handled != tc.handled {
				t.Fatalf("dispatchBareBuiltin(%q) handled=%t, want %t", tc.input, handled, tc.handled)
			}
		})
	}
}

// TestDispatchBareBuiltinUnicodeWhitespaceThroughTextarea drives Unicode
// whitespace through the actual key-by-key textarea ingress, then Enter. The
// local dispatch must happen before the prompt-opening path.
func TestDispatchBareBuiltinUnicodeWhitespaceThroughTextarea(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)
	m.conv.addUser("prior turn")

	const input = "\u2003/clear\u00a0"
	m = typeText(t, m, input)
	if got := m.prompt.Value(); got != input {
		t.Fatalf("textarea value = %q, want %q", got, input)
	}
	m, cmd := pressEnter(t, m)

	if cmd == nil {
		t.Fatal("Unicode-whitespace /clear must start its local replacement-session handoff")
	}
	if m.phase != phaseConnecting || m.conv.isEmpty() {
		t.Fatalf("pending Unicode-whitespace /clear must retain the old UI while connecting: phase=%v empty=%v", m.phase, m.conv.isEmpty())
	}
	ready := firstBatchLeaf(t, cmd)
	mm, _ := m.Update(ready)
	m = mm.(Model)
	if !m.conv.isEmpty() {
		t.Fatal("Unicode-whitespace /clear must clear the conversation after its local handoff")
	}
	if m.phase != phaseIdle {
		t.Fatalf("Unicode-whitespace /clear phase = %v, want idle", m.phase)
	}
	if m.palette.open || m.prompt.Value() != "" {
		t.Fatalf("Unicode-whitespace /clear must close the palette and clear input, open=%t input=%q", m.palette.open, m.prompt.Value())
	}
	if got := promptTexts(send); len(got) != 0 {
		t.Fatalf("Unicode-whitespace /clear must send no prompt frames, got %v", got)
	}
}

// TestIsKnownBuiltinName asserts the static built-in name set covers every name
// from the builtinCommands table AND that an unknown name is false.
func TestIsKnownBuiltinName(t *testing.T) {
	known := []string{
		"clear", "help", "session", "retry", "diagnostics", "compact", "mcp", "agents", "team", "skills", "soul", "usermodel",
		"models", "effort", "worktrees", "schedule", "sessions", "learning", "learning-sensitivity", "posture",
		"debug-ask",
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
	if m.prompt.Value() == "" {
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
// dispatchBareBuiltin normalises to lower-case so the guard works.
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
	if m.prompt.Value() == "" {
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
	if m.prompt.Value() != "" {
		t.Errorf("/foo: textarea should be cleared by the normal send, got %q", m.prompt.Value())
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
	if strings.HasPrefix(m.prompt.Value(), "/mcp") {
		t.Errorf("/mcp (registered): textarea should be cleared after the builtin runs, got %q", m.prompt.Value())
	}
	_ = cmd // may be non-nil (to load MCP inventory); that's fine
}
