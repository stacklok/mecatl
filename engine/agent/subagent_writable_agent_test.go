package agent_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// writableSpecialistEngineForTest assembles a WRITABLE specialist child engine that
// runs a "Write" fakeTool (recording the workspace root it saw) then emits a marker
// summary. It mirrors writableChildWriting but is named for the writable-specialist
// context (mode:"read-write"+agent).
func writableSpecialistEngineForTest(t *testing.T, summary string, recordedRoot *atomic.Pointer[string]) *agent.Engine {
	t.Helper()
	return writableChildWriting(t, summary, recordedRoot)
}

// TestSubagentWritableAgentRoutesToFactoryEngine is the E2E: a parent delegates
// agent:"reviewer", mode:"read-write", and the child engine that ACTUALLY RAN is the
// writable specialist engine the agentWritableFactory minted (distinguished by a marker
// summary + a Write tool that ran against the REAL parent workspace /ws), NOT the
// pre-built agentEngines["reviewer"] engine (which must not run — no map reuse) and NOT
// the generic writable explorer. No fork happens (failingForker) — direct-write, ADR 0041.
func TestSubagentWritableAgentRoutesToFactoryEngine(t *testing.T) {
	var recordedRoot atomic.Pointer[string]
	writableSpec := writableSpecialistEngineForTest(t, "WRITABLE SPECIALIST RAN", &recordedRoot)

	prebuiltReviewerLLM := mockllm.New(mockllm.TextTurn("PREBUILT-REVIEWER"))
	prebuiltReviewer := childEngineWith(prebuiltReviewerLLM, catalogWith(t))

	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))

	task := agent.NewSubagentTool(defaultEngine,
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": prebuiltReviewer},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}}),
		agent.WithAgentWritableEngineFactory(func(agentName string) (*agent.Engine, bool) {
			if agentName == "reviewer" {
				return writableSpec, true
			}
			return nil, false
		}),
		agent.WithChildForker(&failingForker{t}),
		// Also wire a generic writable explorer engine (must NOT run — a writable specialist
		// keeps its factory engine, never the generic explorer).
		agent.WithWritableChildEngine(childEngineWith(mockllm.New(mockllm.TextTurn("WRITABLE EXPLORER RAN")), catalogWith(t))),
	)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"implement it","mode":"read-write","agent":"reviewer"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("read-write+agent must succeed, got %+v", results[0])
	}
	// The writable specialist ran (its marker summary), not the pre-built read-only reviewer.
	if !strings.Contains(results[0].Content, "WRITABLE SPECIALIST RAN") {
		t.Fatalf("read-write+agent must route to the writable-specialist factory engine, got %q", results[0].Content)
	}
	if strings.Contains(results[0].Content, "PREBUILT-REVIEWER") {
		t.Fatalf("the pre-built read-only reviewer engine must NOT run (no map reuse), got %q", results[0].Content)
	}
	if strings.Contains(results[0].Content, "WRITABLE EXPLORER RAN") {
		t.Fatalf("the generic writable explorer must NOT run (a writable specialist keeps its factory engine), got %q", results[0].Content)
	}
	// The specialist's Write tool ran against the REAL parent workspace (/ws), NOT a fork.
	if rr := recordedRoot.Load(); rr == nil || *rr != "/ws" {
		t.Fatalf("specialist Write ran against root %v, want the real parent workspace /ws (direct-write)", rr)
	}
	// The result carries the direct-write note.
	if !strings.Contains(results[0].Content, "had direct write access to your workspace") {
		t.Fatalf("read-write+agent result must carry the direct-write note, got:\n%s", results[0].Content)
	}
	// The pre-built reviewer engine never ran (no map mutation / reuse on the writable path).
	if prebuiltReviewerLLM.Calls() != 0 {
		t.Fatalf("pre-built reviewer engine must not be driven on read-write+agent, made %d calls", prebuiltReviewerLLM.Calls())
	}
}

// TestSubagentWritableAgentPerDefLimitsBind proves a def's maxTurns pins the writable-
// specialist child: a def with maxTurns=1 and a specialist that tries two turns is
// bounded to one (the per-def limits from agentLimits bind the writable specialist, NOT
// the Subagent default).
func TestSubagentWritableAgentPerDefLimitsBind(t *testing.T) {
	specLLM := mockllm.New(
		mockllm.TextTurn("first turn"),
		mockllm.TextTurn("second turn — must not run"),
	)
	writableSpec := childEngineWith(specLLM, catalogWith(t))
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))

	task := agent.NewSubagentTool(defaultEngine,
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": childEngineWith(mockllm.New(mockllm.TextTurn("r")), catalogWith(t))},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews", Limits: session.Limits{MaxTurns: 1}}}),
		agent.WithAgentWritableEngineFactory(func(string) (*agent.Engine, bool) { return writableSpec, true }),
	)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go","mode":"read-write","agent":"reviewer"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("read-write+agent with per-def limits must succeed, got %+v", results[0])
	}
	// maxTurns=1 ⇒ the specialist ran exactly ONE model call.
	if got := specLLM.Calls(); got != 1 {
		t.Fatalf("per-def maxTurns=1 must bound the writable specialist to 1 call, got %d", got)
	}
}

// TestSubagentWritableAgentMutatesParentTrue pins the MutatesParent OR gate for the
// writable specialist (ADR 0058): it returns true for mode:"read-write",agent:"reviewer"
// when WithAgentWritableEngineFactory is wired (even if WithWritableChildEngine is also
// wired — the OR gate); false for plain mode:"read-write" (no agent) when NEITHER is
// wired; false for mode:"read-only".
func TestSubagentWritableAgentMutatesParentTrue(t *testing.T) {
	writableSpec := childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t))
	prebuiltReviewer := childEngineWith(mockllm.New(mockllm.TextTurn("r")), catalogWith(t))
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t))

	// Factory wired (plus a generic writable explorer wired too — OR gate).
	wired := agent.NewSubagentTool(defaultEngine,
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": prebuiltReviewer},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}}),
		agent.WithAgentWritableEngineFactory(func(string) (*agent.Engine, bool) { return writableSpec, true }),
		agent.WithWritableChildEngine(childEngineWith(mockllm.New(mockllm.TextTurn("we")), catalogWith(t))),
	).(interface{ MutatesParent(session.ToolCall) bool })

	rwAgent := `{"prompt":"go","mode":"read-write","agent":"reviewer"}`
	if !wired.MutatesParent(toolCall("p1", "Subagent", rwAgent)) {
		t.Fatalf("MutatesParent(read-write+agent) with factory wired must be true (the writable specialist mutates the real tree — ADR 0058)")
	}

	// read-only agent does NOT mutate.
	if wired.MutatesParent(toolCall("p1", "Subagent", `{"prompt":"go","mode":"read-only","agent":"reviewer"}`)) {
		t.Fatal("MutatesParent(read-only+agent) must be false")
	}

	// A factory-only-wired tool (NO generic writable explorer) still reports true for
	// read-write+agent (the OR gate's factory arm).
	factoryOnly := agent.NewSubagentTool(defaultEngine,
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": prebuiltReviewer},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}}),
		agent.WithAgentWritableEngineFactory(func(string) (*agent.Engine, bool) { return writableSpec, true }),
	).(interface{ MutatesParent(session.ToolCall) bool })
	if !factoryOnly.MutatesParent(toolCall("p1", "Subagent", rwAgent)) {
		t.Fatalf("MutatesParent(read-write+agent) with ONLY the factory wired must be true (OR gate's factory arm)")
	}
	// plain mode:"read-write" (no agent) on the factory-only tool: the OR gate is
	// satisfied (agentWritableFactory wired), so MutatesParent is true — the dispatcher
	// keeps it serial. (The call would later fail validateMode as "not supported" since
	// no writable explorer engine is wired, but MutatesParent is the coarse OR gate that
	// errs toward serial for any read-write call when either writable path is wired.)
	if !factoryOnly.MutatesParent(toolCall("p1", "Subagent", `{"prompt":"go","mode":"read-write"}`)) {
		t.Fatal("MutatesParent(read-write no agent) with the factory wired must be true (coarse OR gate errs serial)")
	}

	// A tool with NEITHER wired never mutates (the plan's "false when NEITHER wired" case).
	unwired := agent.NewSubagentTool(defaultEngine).(interface{ MutatesParent(session.ToolCall) bool })
	if unwired.MutatesParent(toolCall("p1", "Subagent", rwAgent)) {
		t.Fatal("MutatesParent(read-write+agent) with no writable wiring must be false")
	}
	if unwired.MutatesParent(toolCall("p1", "Subagent", `{"prompt":"go","mode":"read-write"}`)) {
		t.Fatal("MutatesParent(read-write no agent) with no writable wiring must be false")
	}
}

// TestSubagentWritableAgentUnknownAgentErrors proves the name-truth check fires BEFORE
// the factory: agent:"nonexistent", mode:"read-write" returns a model-addressable error
// listing the valid names (the factory is never called for an unknown name).
func TestSubagentWritableAgentUnknownAgentErrors(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))
	var factoryCalled bool
	task := agent.NewSubagentTool(defaultEngine,
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": childEngineWith(mockllm.New(mockllm.TextTurn("r")), catalogWith(t))},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}}),
		agent.WithAgentWritableEngineFactory(func(string) (*agent.Engine, bool) {
			factoryCalled = true
			return childEngineWith(mockllm.New(mockllm.TextTurn("spec")), catalogWith(t)), true
		}),
	)

	res := runOneSubagent(t, task, "p1", `{"prompt":"go","mode":"read-write","agent":"nonexistent"}`)
	if !res.IsError || !strings.Contains(res.Content, "unknown agent") {
		t.Fatalf("unknown agent must be a model-addressable error listing valid names, got %+v", res)
	}
	if !strings.Contains(res.Content, "reviewer") {
		t.Fatalf("the error must list the valid agent names, got %q", res.Content)
	}
	if factoryCalled {
		t.Fatal("the factory must NOT be called for an unknown agent (name-truth check fires first)")
	}
}

// TestSubagentWritableAgentFactoryNilIsUnsupported proves read-write+agent with no
// WithAgentWritableEngineFactory is "not supported in this deployment" (the factory-nil
// arm), even when a generic writable explorer engine IS wired.
func TestSubagentWritableAgentFactoryNilIsUnsupported(t *testing.T) {
	var rr atomic.Pointer[string]
	readOnly := childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t))
	task := agent.NewSubagentTool(readOnly,
		agent.WithWritableChildEngine(writableChildWriting(t, "explorer", &rr)),
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": childEngineWith(mockllm.New(mockllm.TextTurn("r")), catalogWith(t))},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}}),
	)
	res := runOneSubagent(t, task, "p1", `{"prompt":"go","mode":"read-write","agent":"reviewer"}`)
	if !res.IsError || !strings.Contains(res.Content, "not supported in this deployment") {
		t.Fatalf("read-write+agent with no writable-specialist factory must be 'not supported in this deployment', got %+v", res)
	}
}

// TestSubagentWritableAgentNotIsolatedSkipsA2 mirrors TestSubagentWritableNotIsolatedSkipsA2
// for the writable SPECIALIST (mode:"read-write"+agent): the child's isolation-approvable
// Shell substitution under a HEADLESS parent must AUTO-DENY — because a writable specialist
// is isolated:false (ADR 0041/0058 — it mutates the real tree, no fork), so the A2
// isolation auto-approve (which only fires when isolated) does NOT apply. The read-only
// childForker is a failingForker to also prove no fork happens.
func TestSubagentWritableAgentNotIsolatedSkipsA2(t *testing.T) {
	bash := &fakeShell{}
	specLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Shell", `{"command":"go test $(echo ./...)"}`)),
		mockllm.TextTurn("child: adapted after the denied command"),
	)
	writableSpec := shellChildEngine(specLLM, bash)
	task := agent.NewSubagentTool(
		childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t)),
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": childEngineWith(mockllm.New(mockllm.TextTurn("r")), catalogWith(t))},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}}),
		agent.WithAgentWritableEngineFactory(func(string) (*agent.Engine, bool) { return writableSpec, true }),
		agent.WithChildForker(&failingForker{t}),
	)

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"implement","mode":"read-write","agent":"reviewer"}`)),
		mockllm.TextTurn("parent: done"),
	)
	// newEngine ⇒ Interactive=false (headless): an unresolved ask auto-denies.
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	drainWithTimeout(t, r)

	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("a NON-isolated (direct-write) writable specialist's isolation-approvable Shell must NOT be "+
			"A2 auto-approved under a headless parent — it must auto-deny; but the command ran: %v", got)
	}
}
