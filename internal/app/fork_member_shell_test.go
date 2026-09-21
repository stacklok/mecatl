package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// These tests cover the workspace-aware-Shell follow-up: a forked/other-workspace
// child — a Fork branch and a Mutating team member — IS now given Shell when a
// runner is configured, because ShellTool.Execute passes the child's forked
// Workspace.Root() to the runner as the working directory, so the command runs in
// the fork, not the shared parent base. Edit/Write remain too (a fork child
// implements). The cfg used by every test configures a real shell so
// buildCommandRunner returns a non-nil runner; without one the child runs
// shell-less (like the main session). A read-only (base-sharing) member still must
// NOT get Shell — that assertion is kept below.

// shellThenEdit scripts a child/member turn that first calls Shell, then Edit, then a
// text turn. Whether each tool is in the catalog is observable from the resulting
// tool.result: an unknown tool surfaces as an error result reading `unknown tool ...`.
func shellThenEdit() *mockllm.Provider {
	bash := session.ToolCall{
		ID:   "b1",
		Name: "Shell",
		Args: json.RawMessage(`{"command":"echo hi"}`),
	}
	edit := session.ToolCall{
		ID:   "e1",
		Name: "Edit",
		Args: json.RawMessage(`{"file_path":"/ws/f.txt","old_string":"a","new_string":"b"}`),
	}
	return mockllm.New(
		mockllm.ToolCallTurn(bash),
		mockllm.ToolCallTurn(edit),
		mockllm.TextTurn("done"),
	)
}

// unknownToolResult reports whether the event stream contains a tool.result for
// callID that is an error reading "unknown tool" (the loop's signal that the tool
// is NOT in the engine's catalog).
func unknownToolResult(events []session.Event, callID session.ToolCallID) bool {
	for _, ev := range events {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil &&
			ev.ToolResult.CallID == callID && ev.ToolResult.IsError &&
			strings.Contains(ev.ToolResult.Content, "unknown tool") {
			return true
		}
	}
	return false
}

// sawCatalogToolCall reports whether the stream dispatched a (known) tool.call for
// the named tool — i.e. a tool.call event for it that was NOT answered by an
// "unknown tool" error.
func sawDispatchedTool(events []session.Event, callID session.ToolCallID) bool {
	var called bool
	for _, ev := range events {
		if ev.Type == session.EvToolCall && ev.ToolCall != nil && ev.ToolCall.ID == callID {
			called = true
		}
	}
	return called && !unknownToolResult(events, callID)
}

// drainEngine runs an engine over a fresh session and returns the full event stream.
func drainEngine(t *testing.T, eng *agent.Engine) []session.Event {
	t.Helper()
	sess := session.New("s", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 5}, time.Now())
	run := eng.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "go"})
	var events []session.Event
	for ev := range run.Events() {
		events = append(events, ev)
	}
	return events
}

func assertCanonicalShellCatalog(t *testing.T, eng *agent.Engine, enabled bool) {
	t.Helper()
	if got := eng.HasTool("Shell"); got != enabled {
		t.Fatalf("Shell present = %v, want %v", got, enabled)
	}
	if got := eng.HasTool("ShellStatus"); got != enabled {
		t.Fatalf("ShellStatus present = %v, want %v", got, enabled)
	}
	if eng.HasTool("Bash") {
		t.Fatal("catalog must not register legacy Bash")
	}
}

// TestForkChildEngineHasShellAndEdit proves buildParallelChildEngine's catalog now
// contains BOTH Edit and Shell when a runner is configured — Shell is workspace-aware
// and runs in the branch's fork, so it is safe to re-enable.
func TestForkChildEngineHasShellAndEdit(t *testing.T) {
	cfg := teamCfg(t) // configures Shell, so a runner exists and Shell is wired
	runner := buildCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil command runner with Shell set")
	}
	eng := buildParallelChildEngine(cfg, nil, shellThenEdit(), "", cfg.Model, runner)
	assertCanonicalShellCatalog(t, eng, true)

	events := drainEngine(t, eng)

	if unknownToolResult(events, "b1") {
		t.Error("Fork child did NOT have Shell; workspace-aware Shell must be re-enabled for a forked child")
	}
	if !sawDispatchedTool(events, "b1") {
		t.Error("Fork child did not dispatch Shell; it must be present in the catalog")
	}
	if !sawDispatchedTool(events, "e1") {
		t.Error("Fork child did not dispatch Edit; a fork child must keep Edit/Write to implement in its fork")
	}
}

// TestForkChildEngineNoRunnerHasNoShell proves a shell-less deployment (nil runner)
// still yields a Shell-less forked child — the runner gate is honored, exactly like
// the main session.
func TestForkChildEngineNoRunnerHasNoShell(t *testing.T) {
	cfg := teamCfg(t)
	eng := buildParallelChildEngine(cfg, nil, shellThenEdit(), "", cfg.Model, nil)
	assertCanonicalShellCatalog(t, eng, false)

	events := drainEngine(t, eng)

	if !unknownToolResult(events, "b1") {
		t.Error("Fork child with a nil runner dispatched Shell; without a runner there must be no Shell")
	}
	if !sawDispatchedTool(events, "e1") {
		t.Error("Fork child did not dispatch Edit; Edit must be present regardless of the runner")
	}
}

// TestMutatingMemberHasShellAndEdit proves a default (no-def) Mutating team member's
// engine now contains BOTH Edit and Shell — Shell is workspace-aware and runs in the
// member's fork.
func TestMutatingMemberHasShellAndEdit(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil command runner with Shell set")
	}
	tm := team.New("t")
	factory := memberFactoryForTest(cfg, shellThenEdit(), hookexec.New(nil), agents.NewRegistry(nil), nil, runner, false, nil)
	build := factory(tm, agent.MemberSpec{Name: "writer", Mutating: true}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	assertCanonicalShellCatalog(t, build.Engine, true)

	events := drainEngine(t, build.Engine)

	if unknownToolResult(events, "b1") {
		t.Error("Mutating member did NOT have Shell; workspace-aware Shell must be re-enabled for a Mutating (forked) member")
	}
	if !sawDispatchedTool(events, "b1") {
		t.Error("Mutating member did not dispatch Shell; it must be present in the catalog")
	}
	if !sawDispatchedTool(events, "e1") {
		t.Error("Mutating member did not dispatch Edit; a Mutating member must keep Edit/Write to implement in its fork")
	}
}

// TestReadOnlyMemberHasNoShellOrEdit proves a read-only (base-sharing) member gets
// NEITHER Shell NOR Edit — the read-only-share / mutating-fork guarantee. A
// read-only member shares the parent base, so it must never get a mutating tool.
func TestReadOnlyMemberHasNoShellOrEdit(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildCommandRunner(cfg)
	tm := team.New("t")
	factory := memberFactoryForTest(cfg, shellThenEdit(), hookexec.New(nil), agents.NewRegistry(nil), nil, runner, false, nil)
	build := factory(tm, agent.MemberSpec{Name: "reader", Mutating: false}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	assertCanonicalShellCatalog(t, build.Engine, false)

	events := drainEngine(t, build.Engine)

	if !unknownToolResult(events, "b1") {
		t.Error("read-only member dispatched Shell; a base-sharing member must NOT get Shell")
	}
	if !unknownToolResult(events, "e1") {
		t.Error("read-only member dispatched Edit; a base-sharing member must NOT get Edit")
	}
}

// TestReadOnlyIsolatedMemberHasShellNotEdit proves the new three-tier behaviour for a
// DEFAULT (no-def) read-only member: with a runner AND read-only isolation available,
// it gets Shell (for inspection) but NOT Edit, and the build is flagged
// IsolateReadOnly so the supervisor runs it in a worktree.
func TestReadOnlyIsolatedMemberHasShellNotEdit(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildSandboxedCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil sandboxed runner with Shell set")
	}
	tm := team.New("t")
	factory := memberFactoryForTest(cfg, shellThenEdit(), hookexec.New(nil), agents.NewRegistry(nil), nil, runner, true, nil)
	build := factory(tm, agent.MemberSpec{Name: "reader", Mutating: false}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	if !build.IsolateReadOnly {
		t.Error("read-only member with Shell should set IsolateReadOnly so the supervisor isolates it in a worktree")
	}
	assertCanonicalShellCatalog(t, build.Engine, true)

	events := drainEngine(t, build.Engine)

	if unknownToolResult(events, "b1") {
		t.Error("read-only-isolated member did NOT get Shell; it must get a shell for inspection in its worktree")
	}
	if !sawDispatchedTool(events, "b1") {
		t.Error("read-only-isolated member did not dispatch Shell; it must be present in the catalog")
	}
	if !unknownToolResult(events, "e1") {
		t.Error("read-only-isolated member dispatched Edit; an inspect-only member must NOT get Edit/Write")
	}
}

// TestReadOnlyMemberNoRunnerNoShellNotIsolated reasserts the base-sharing fallback:
// with no runner the read-only member gets no Shell and is NOT IsolateReadOnly (so the
// supervisor base-shares it), unchanged from before this feature.
func TestReadOnlyMemberNoRunnerNoShellNotIsolated(t *testing.T) {
	cfg := teamCfg(t)
	tm := team.New("t")
	// roIsolationAvailable is moot when the runner is nil — pass true to prove the
	// runner gate (runner != nil) is what actually withholds Shell.
	factory := memberFactoryForTest(cfg, shellThenEdit(), hookexec.New(nil), agents.NewRegistry(nil), nil, nil, true, nil)
	build := factory(tm, agent.MemberSpec{Name: "reader", Mutating: false}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	if build.IsolateReadOnly {
		t.Error("read-only member with no runner must NOT be IsolateReadOnly (no shell => base-share)")
	}
	assertCanonicalShellCatalog(t, build.Engine, false)

	events := drainEngine(t, build.Engine)
	if !unknownToolResult(events, "b1") {
		t.Error("read-only member with no runner dispatched Shell; without a runner there must be no shell")
	}
}

// TestReadOnlyIsolatedMemberDefKeepsShellDropsEdit proves the DEFINED read-only-member
// path with isolation available keeps Shell (a def allowlisting it) but still drops
// Edit, and flags IsolateReadOnly.
func TestReadOnlyIsolatedMemberDefKeepsShellDropsEdit(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildSandboxedCommandRunner(cfg)
	tm := team.New("t")
	def := agents.AgentDef{Name: "reader", Description: "r", Tools: []string{"Read", "Edit", "Shell"}}
	factory := memberFactoryForTest(cfg, shellThenEdit(), hookexec.New(nil), regOf(def), nil, runner, true, nil)
	build := factory(tm, agent.MemberSpec{Name: "reader", AgentType: "reader", Mutating: false}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	if !build.IsolateReadOnly {
		t.Error("read-only def-member that kept Shell should set IsolateReadOnly")
	}
	assertCanonicalShellCatalog(t, build.Engine, true)

	events := drainEngine(t, build.Engine)
	if unknownToolResult(events, "b1") {
		t.Error("read-only-isolated def-member allowlisting Shell did NOT get Shell; it must keep it for inspection")
	}
	if !unknownToolResult(events, "e1") {
		t.Error("read-only-isolated def-member got Edit; Edit/Write must still be dropped for an inspect-only member")
	}
}

// TestMutatingMemberDefCanScopeInShell proves the DEFINED-member path now lets a
// Mutating member's agent def scope Shell IN: a def that allowlists Shell gets it
// (workspace-aware, fork-confined), alongside a listed Edit.
func TestMutatingMemberDefCanScopeInShell(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildCommandRunner(cfg)
	tm := team.New("t")
	def := agents.AgentDef{Name: "writer", Description: "w", Tools: []string{"Read", "Edit", "Shell"}}
	factory := memberFactoryForTest(cfg, shellThenEdit(), hookexec.New(nil), regOf(def), nil, runner, false, nil)
	build := factory(tm, agent.MemberSpec{Name: "writer", AgentType: "writer", Mutating: true}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	assertCanonicalShellCatalog(t, build.Engine, true)

	events := drainEngine(t, build.Engine)

	if unknownToolResult(events, "b1") {
		t.Error("Mutating member def allowlisting Shell did NOT get Shell; it must be scopable for forked members now")
	}
	if !sawDispatchedTool(events, "b1") {
		t.Error("Mutating member did not dispatch Shell; a def listing Shell must yield it")
	}
	if !sawDispatchedTool(events, "e1") {
		t.Error("Mutating member def listing Edit did not dispatch Edit; Edit must survive scoping")
	}
}

// TestReadOnlyMemberDefCannotScopeInShell proves the DEFINED read-only-member path
// still DROPS Shell (and Edit): scopedToolNamesMode drops mutating tools for a
// base-sharing member regardless of the def allowlist.
func TestReadOnlyMemberDefCannotScopeInShell(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildCommandRunner(cfg)
	tm := team.New("t")
	def := agents.AgentDef{Name: "reader", Description: "r", Tools: []string{"Read", "Edit", "Shell"}}
	factory := memberFactoryForTest(cfg, shellThenEdit(), hookexec.New(nil), regOf(def), nil, runner, false, nil)
	build := factory(tm, agent.MemberSpec{Name: "reader", AgentType: "reader", Mutating: false}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	assertCanonicalShellCatalog(t, build.Engine, false)

	events := drainEngine(t, build.Engine)

	if !unknownToolResult(events, "b1") {
		t.Error("read-only member def allowlisting Shell got Shell; a base-sharing member must drop mutating tools")
	}
	if !unknownToolResult(events, "e1") {
		t.Error("read-only member def allowlisting Edit got Edit; a base-sharing member must drop mutating tools")
	}
}
