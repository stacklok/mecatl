package scenarios_test

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// scenarioEpoch is the fixed session start time for every scenario session: a
// deterministic clock seed so nothing in the measured path reads the wall clock
// or randomises (perf-tracking.md "must stay deterministic"). time.Unix(0,0).
var scenarioEpoch = time.Unix(0, 0)

// scenarioWorkspaceRoot is the in-memory workspace root the scenarios mount.
const scenarioWorkspaceRoot = "/ws"

// scenarioModel is the opaque model id stamped into requests (the offline mock
// ignores it; a model id is still required by the engine).
const scenarioModel = "perf-mock-model"

// noopHooks is a present-but-inert port.HookRunner: the hook-dispatch path runs
// but no hook is configured (the engine tree is self-contained, so we cannot
// import the internal hookexec adapter — this mirrors hookexec.New(nil)).
type noopHooks struct{}

func (noopHooks) Run(context.Context, governance.HookEvent) (governance.HookOutcome, error) {
	return governance.HookOutcome{}, nil
}

// allowAll returns a policy that allows every tool call (the AllowAllFloorRules
// floor with no store). Scenarios are offline and exercise the loop, not the
// permission fold (that has its own Phase 1 microbenchmarks).
func allowAll() *permpolicy.Policy {
	return permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
}

// scenarioCatalog builds a tool.Catalog from the given tools.
func scenarioCatalog(tools ...tool.Tool) *tool.Catalog {
	c := tool.NewCatalog()
	for _, tl := range tools {
		c.MustRegister(tl)
	}
	return c
}

// scenarioSession builds a fresh default-mode session at the fixed epoch. A
// session is a one-shot state machine, so the caller builds one per iteration.
func scenarioSession(id string, limits session.Limits) *session.Session {
	return session.New(session.SessionID(id), session.ModeDefault, scenarioWorkspaceRoot, limits, scenarioEpoch)
}

// scenarioWorkspace returns a fresh shell-less in-memory Environment mounted at
// the scenario root.
func scenarioWorkspace() tool.Environment {
	ws := memfs.NewWorkspace(scenarioWorkspaceRoot)
	return tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: scenarioWorkspaceRoot}, ws, nil)
}

// readTool is a deterministic read-only tool returning fixed content — the
// stand-in for a file read in the long-session scenario (the actual file body is
// irrelevant to the perf shape).
type readTool struct{}

func (readTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Read", Description: "read a file (perf scenario stand-in)", Schema: objectSchema}
}
func (readTool) ReadOnly() bool { return true }
func (readTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(in.ID, "package main\n\nfunc main() {}\n"), nil
}

// objectSchema is the trivial JSON-schema shared by the scenario stand-in tools.
var objectSchema = []byte(`{"type":"object"}`)

// drain consumes a run's events to completion, returning the count (assigned to
// a sink so the drain is not elided). It does not retain the events — long
// scenarios stream thousands and retaining them would dominate the allocation
// budget with harness garbage rather than loop garbage.
func drain(r *agent.Run) int {
	n := 0
	for range r.Events() {
		n++
	}
	return n
}

// buildEngine assembles a scenario engine with sane offline defaults.
func buildEngine(d agent.Deps) *agent.Engine {
	if d.Policy == nil {
		d.Policy = allowAll()
	}
	if d.Hooks == nil {
		d.Hooks = noopHooks{}
	}
	if d.Model == "" {
		d.Model = scenarioModel
	}
	return agent.NewEngine(d)
}

// ensure the port import is used (port.LLMProvider is the Deps.LLM type the
// scenarios construct via mockllm; this keeps the import honest if a scenario
// file is edited to drop its direct port use).
var _ port.LLMProvider
