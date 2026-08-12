package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// These tests cover the workspace-aware-Bash follow-up: a forked/other-workspace
// child — a Fork branch and a Mutating team member — IS now given Bash when a
// runner is configured, because BashTool.Execute passes the child's forked
// Workspace.Root() to the runner as the working directory, so the command runs in
// the fork, not the shared parent base. Edit/Write remain too (a fork child
// implements). The cfg used by every test configures a real shell so
// buildCommandRunner returns a non-nil runner; without one the child runs
// shell-less (like the main session). A read-only (base-sharing) member still must
// NOT get Bash — that assertion is kept below.

// bashThenEdit scripts a child/member turn that first calls Bash, then Edit, then a
// text turn. Whether each tool is in the catalog is observable from the resulting
// tool.result: an unknown tool surfaces as an error result reading `unknown tool ...`.
func bashThenEdit() *mockllm.Provider {
	bash := session.ToolCall{
		ID:   "b1",
		Name: "Bash",
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
	sess := session.New("s", session.ModeDefault, "/ws", session.Limits{MaxTurns: 5}, time.Now())
	run := eng.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	var events []session.Event
	for ev := range run.Events() {
		events = append(events, ev)
	}
	return events
}

// TestForkChildEngineHasBashAndEdit proves buildParallelChildEngine's catalog now
// contains BOTH Edit and Bash when a runner is configured — Bash is workspace-aware
// and runs in the branch's fork, so it is safe to re-enable.
func TestForkChildEngineHasBashAndEdit(t *testing.T) {
	cfg := teamCfg(t) // configures Shell, so a runner exists and Bash is wired
	runner := buildCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil command runner with Shell set")
	}
	eng := buildParallelChildEngine(cfg, nil, bashThenEdit(), "", cfg.Model, runner)

	events := drainEngine(t, eng)

	if unknownToolResult(events, "b1") {
		t.Error("Fork child did NOT have Bash; workspace-aware Bash must be re-enabled for a forked child")
	}
	if !sawDispatchedTool(events, "b1") {
		t.Error("Fork child did not dispatch Bash; it must be present in the catalog")
	}
	if !sawDispatchedTool(events, "e1") {
		t.Error("Fork child did not dispatch Edit; a fork child must keep Edit/Write to implement in its fork")
	}
}

// TestForkChildEngineNoRunnerHasNoBash proves a shell-less deployment (nil runner)
// still yields a Bash-less forked child — the runner gate is honored, exactly like
// the main session.
func TestForkChildEngineNoRunnerHasNoBash(t *testing.T) {
	cfg := teamCfg(t)
	eng := buildParallelChildEngine(cfg, nil, bashThenEdit(), "", cfg.Model, nil)

	events := drainEngine(t, eng)

	if !unknownToolResult(events, "b1") {
		t.Error("Fork child with a nil runner dispatched Bash; without a runner there must be no Bash")
	}
	if !sawDispatchedTool(events, "e1") {
		t.Error("Fork child did not dispatch Edit; Edit must be present regardless of the runner")
	}
}

// TestMutatingMemberHasBashAndEdit proves a default (no-def) Mutating team member's
// engine now contains BOTH Edit and Bash — Bash is workspace-aware and runs in the
// member's fork.
func TestMutatingMemberHasBashAndEdit(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil command runner with Shell set")
	}
	tm := team.New("t")
	factory := memberFactoryForTest(cfg, bashThenEdit(), hookexec.New(nil), agents.NewRegistry(nil), nil, runner, false, nil)
	build := factory(tm, agent.MemberSpec{Name: "writer", Mutating: true}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}

	events := drainEngine(t, build.Engine)

	if unknownToolResult(events, "b1") {
		t.Error("Mutating member did NOT have Bash; workspace-aware Bash must be re-enabled for a Mutating (forked) member")
	}
	if !sawDispatchedTool(events, "b1") {
		t.Error("Mutating member did not dispatch Bash; it must be present in the catalog")
	}
	if !sawDispatchedTool(events, "e1") {
		t.Error("Mutating member did not dispatch Edit; a Mutating member must keep Edit/Write to implement in its fork")
	}
}

// TestReadOnlyMemberHasNoBashOrEdit proves a read-only (base-sharing) member gets
// NEITHER Bash NOR Edit — the read-only-share / mutating-fork guarantee. A
// read-only member shares the parent base, so it must never get a mutating tool.
func TestReadOnlyMemberHasNoBashOrEdit(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildCommandRunner(cfg)
	tm := team.New("t")
	factory := memberFactoryForTest(cfg, bashThenEdit(), hookexec.New(nil), agents.NewRegistry(nil), nil, runner, false, nil)
	build := factory(tm, agent.MemberSpec{Name: "reader", Mutating: false}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}

	events := drainEngine(t, build.Engine)

	if !unknownToolResult(events, "b1") {
		t.Error("read-only member dispatched Bash; a base-sharing member must NOT get Bash")
	}
	if !unknownToolResult(events, "e1") {
		t.Error("read-only member dispatched Edit; a base-sharing member must NOT get Edit")
	}
}

// TestReadOnlyIsolatedMemberHasBashNotEdit proves the new three-tier behaviour for a
// DEFAULT (no-def) read-only member: with a runner AND read-only isolation available,
// it gets Bash (for inspection) but NOT Edit, and the build is flagged
// IsolateReadOnly so the supervisor runs it in a worktree.
func TestReadOnlyIsolatedMemberHasBashNotEdit(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildSandboxedCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil sandboxed runner with Shell set")
	}
	tm := team.New("t")
	factory := memberFactoryForTest(cfg, bashThenEdit(), hookexec.New(nil), agents.NewRegistry(nil), nil, runner, true, nil)
	build := factory(tm, agent.MemberSpec{Name: "reader", Mutating: false}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	if !build.IsolateReadOnly {
		t.Error("read-only member with Bash should set IsolateReadOnly so the supervisor isolates it in a worktree")
	}

	events := drainEngine(t, build.Engine)

	if unknownToolResult(events, "b1") {
		t.Error("read-only-isolated member did NOT get Bash; it must get a shell for inspection in its worktree")
	}
	if !sawDispatchedTool(events, "b1") {
		t.Error("read-only-isolated member did not dispatch Bash; it must be present in the catalog")
	}
	if !unknownToolResult(events, "e1") {
		t.Error("read-only-isolated member dispatched Edit; an inspect-only member must NOT get Edit/Write")
	}
}

// TestReadOnlyMemberNoRunnerNoBashNotIsolated reasserts the base-sharing fallback:
// with no runner the read-only member gets no Bash and is NOT IsolateReadOnly (so the
// supervisor base-shares it), unchanged from before this feature.
func TestReadOnlyMemberNoRunnerNoBashNotIsolated(t *testing.T) {
	cfg := teamCfg(t)
	tm := team.New("t")
	// roIsolationAvailable is moot when the runner is nil — pass true to prove the
	// runner gate (runner != nil) is what actually withholds Bash.
	factory := memberFactoryForTest(cfg, bashThenEdit(), hookexec.New(nil), agents.NewRegistry(nil), nil, nil, true, nil)
	build := factory(tm, agent.MemberSpec{Name: "reader", Mutating: false}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	if build.IsolateReadOnly {
		t.Error("read-only member with no runner must NOT be IsolateReadOnly (no shell => base-share)")
	}

	events := drainEngine(t, build.Engine)
	if !unknownToolResult(events, "b1") {
		t.Error("read-only member with no runner dispatched Bash; without a runner there must be no shell")
	}
}

// TestReadOnlyIsolatedMemberDefKeepsBashDropsEdit proves the DEFINED read-only-member
// path with isolation available keeps Bash (a def allowlisting it) but still drops
// Edit, and flags IsolateReadOnly.
func TestReadOnlyIsolatedMemberDefKeepsBashDropsEdit(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildSandboxedCommandRunner(cfg)
	tm := team.New("t")
	def := agents.AgentDef{Name: "reader", Description: "r", Tools: []string{"Read", "Edit", "Bash"}}
	factory := memberFactoryForTest(cfg, bashThenEdit(), hookexec.New(nil), regOf(def), nil, runner, true, nil)
	build := factory(tm, agent.MemberSpec{Name: "reader", AgentType: "reader", Mutating: false}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	if !build.IsolateReadOnly {
		t.Error("read-only def-member that kept Bash should set IsolateReadOnly")
	}

	events := drainEngine(t, build.Engine)
	if unknownToolResult(events, "b1") {
		t.Error("read-only-isolated def-member allowlisting Bash did NOT get Bash; it must keep it for inspection")
	}
	if !unknownToolResult(events, "e1") {
		t.Error("read-only-isolated def-member got Edit; Edit/Write must still be dropped for an inspect-only member")
	}
}

// TestMutatingMemberDefCanScopeInBash proves the DEFINED-member path now lets a
// Mutating member's agent def scope Bash IN: a def that allowlists Bash gets it
// (workspace-aware, fork-confined), alongside a listed Edit.
func TestMutatingMemberDefCanScopeInBash(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildCommandRunner(cfg)
	tm := team.New("t")
	def := agents.AgentDef{Name: "writer", Description: "w", Tools: []string{"Read", "Edit", "Bash"}}
	factory := memberFactoryForTest(cfg, bashThenEdit(), hookexec.New(nil), regOf(def), nil, runner, false, nil)
	build := factory(tm, agent.MemberSpec{Name: "writer", AgentType: "writer", Mutating: true}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}

	events := drainEngine(t, build.Engine)

	if unknownToolResult(events, "b1") {
		t.Error("Mutating member def allowlisting Bash did NOT get Bash; it must be scopable for forked members now")
	}
	if !sawDispatchedTool(events, "b1") {
		t.Error("Mutating member did not dispatch Bash; a def listing Bash must yield it")
	}
	if !sawDispatchedTool(events, "e1") {
		t.Error("Mutating member def listing Edit did not dispatch Edit; Edit must survive scoping")
	}
}

// TestReadOnlyMemberDefCannotScopeInBash proves the DEFINED read-only-member path
// still DROPS Bash (and Edit): scopedToolNamesMode drops mutating tools for a
// base-sharing member regardless of the def allowlist.
func TestReadOnlyMemberDefCannotScopeInBash(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildCommandRunner(cfg)
	tm := team.New("t")
	def := agents.AgentDef{Name: "reader", Description: "r", Tools: []string{"Read", "Edit", "Bash"}}
	factory := memberFactoryForTest(cfg, bashThenEdit(), hookexec.New(nil), regOf(def), nil, runner, false, nil)
	build := factory(tm, agent.MemberSpec{Name: "reader", AgentType: "reader", Mutating: false}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}

	events := drainEngine(t, build.Engine)

	if !unknownToolResult(events, "b1") {
		t.Error("read-only member def allowlisting Bash got Bash; a base-sharing member must drop mutating tools")
	}
	if !unknownToolResult(events, "e1") {
		t.Error("read-only member def allowlisting Edit got Edit; a base-sharing member must drop mutating tools")
	}
}
