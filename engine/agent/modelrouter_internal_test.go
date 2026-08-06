package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// modelrouter_internal_test.go drives the Subagent run() router HOOK directly (the
// parentCaps.routeTask seam) to assert PRECEDENCE, FAIL-SOFT, and the NO-NESTING guard —
// the seams the composition end-to-end test cannot reach in isolation.

// markerEngine builds a default explorer-style child engine whose single turn returns a
// MARKER text, so a test can tell WHICH engine (default vs routed) actually drove.
func markerEngine(marker string) *Engine {
	return NewEngine(Deps{
		LLM:     mockllm.New(mockllm.TextTurn(marker)),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   marker,
	})
}

// routerTool builds a Subagent tool whose default engine returns "DEFAULT" and whose
// per-call factory returns an engine returning "ROUTED:<model>" for any model — so the
// result text reveals whether the router's model override took effect.
func routerTool() *SubagentTool {
	factory := func(model string) (*Engine, bool) {
		return markerEngine("ROUTED:" + model), true
	}
	return NewSubagentTool(markerEngine("DEFAULT"), WithSubagentEngineFactory(factory)).(*SubagentTool)
}

// A plain default delegation with a wired routeTask mints the child on the ROUTED model.
func TestRunRouteTaskRoutesPlainDelegation(t *testing.T) {
	tl := routerTool()
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, string, bool) {
		return "large", "big-model", "", true
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"deep work"}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !strings.Contains(res.Content, "ROUTED:big-model") {
		t.Fatalf("plain delegation must run on the routed model; got %q", res.Content)
	}
}

// PRECEDENCE: an explicit `model` arg PINS the engine — the router must NOT fire (the
// run() hook gates routing on args.Model==""). The routeTask here would route to a
// DIFFERENT model; the result must reflect the explicit one, and routeTask must be
// untouched (call count 0).
func TestRunExplicitModelBeatsRouter(t *testing.T) {
	tl := routerTool()
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, string, bool) {
		calls++
		return "large", "router-model", "", true
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x","model":"explicit-model"}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !strings.Contains(res.Content, "ROUTED:explicit-model") {
		t.Fatalf("explicit model must win; got %q", res.Content)
	}
	if calls != 0 {
		t.Fatalf("routeTask must NOT be consulted when an explicit model is set; called %d times", calls)
	}
}

// PRECEDENCE: fork does NOT route (a fork inherits the parent engine). The router would
// route otherwise; routeTask must be untouched. (fork also requires forkHistory; we
// supply a trivial one so the fork precondition passes and the run reaches the gate.)
func TestRunForkDoesNotRoute(t *testing.T) {
	tl := routerTool()
	var calls int
	caps := parentCaps{
		children:    newChildRunRegistry(),
		forkHistory: func() []session.Message { return nil }, // turn-0 fork → empty snapshot (benign)
		routeTask: func(context.Context, string) (string, string, string, bool) {
			calls++
			return "large", "router-model", "", true
		},
	}
	_, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x","fork":true}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("routeTask must NOT be consulted on a fork; called %d times", calls)
	}
}

// PRECEDENCE: a named `agent` PINS its specialist engine — the router must NOT fire (the
// run() hook gates routing on args.Agent==""). A regression dropping `args.Agent != ""`
// from the gate would route a specialist's child through the classifier and silently
// override its def-pinned model. The routeTask here would route elsewhere; the result
// must be the SPECIALIST engine's output, and routeTask must be untouched (calls==0).
func TestRunNamedAgentBeatsRouter(t *testing.T) {
	specialist := markerEngine("SPECIALIST")
	tl := NewSubagentTool(markerEngine("DEFAULT"),
		WithSubagentEngineFactory(func(model string) (*Engine, bool) { return markerEngine("ROUTED:" + model), true }),
		WithAgentEngines(map[string]*Engine{"reviewer": specialist},
			[]AgentMeta{{Name: "reviewer", Description: "a specialist"}}),
	).(*SubagentTool)
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, string, bool) {
		calls++
		return "large", "router-model", "", true
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x","agent":"reviewer"}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("routeTask must NOT be consulted when a named agent is set; called %d times", calls)
	}
	if !strings.Contains(res.Content, "SPECIALIST") {
		t.Fatalf("a named agent must run the specialist engine, not a routed one; got %q", res.Content)
	}
}

// routableAgentTool builds a Subagent with a "reviewer" specialist declared ROUTABLE
// (WithRoutableAgents), a pre-built specialist engine emitting "SPECIALIST", and an
// agent+model factory minting "AGENTMODEL:<agent>:<model>". factoryOK=false makes the
// factory decline (the inline-MCP-decline shape). defLimits binds the def's per-call limits
// so a test can prove they survive a routed engine swap. wireFactory=false omits the
// agent+model factory entirely (the "pick can't be consumed" gate).
func routableAgentTool(factoryOK, wireFactory bool, defLimits session.Limits) *SubagentTool {
	opts := []SubagentOption{
		WithAgentEngines(map[string]*Engine{"reviewer": markerEngine("SPECIALIST")},
			[]AgentMeta{{Name: "reviewer", Description: "a specialist", Limits: defLimits}}),
		WithRoutableAgents([]string{"reviewer"}),
	}
	if wireFactory {
		opts = append(opts, WithAgentModelEngineFactory(func(agentName, model string) (*Engine, bool) {
			if !factoryOK {
				return nil, false
			}
			return markerEngine("AGENTMODEL:" + agentName + ":" + model), true
		}))
	}
	return NewSubagentTool(markerEngine("DEFAULT"), opts...).(*SubagentTool)
}

// hitRoute returns a routeTask that always classifies to model, counting consultations.
func hitRoute(calls *int, model string) func(context.Context, string) (string, string, string, bool) {
	return func(context.Context, string) (string, string, string, bool) {
		*calls++
		return "large", model, "", true
	}
}

// TestRunRoutableAgentRoutesViaFactory (issue #286): a ROUTABLE (unpinned) `agent`
// delegation IS classified and its SCOPED engine is rebuilt on the routed model via the
// agent+model factory. The classifier is consulted once and the factory engine (not the
// pre-built specialist) runs.
func TestRunRoutableAgentRoutesViaFactory(t *testing.T) {
	tl := routableAgentTool(true, true, session.Limits{})
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeTask: hitRoute(&calls, "router-model")}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"review it","agent":"reviewer"}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("a routable agent must consult the classifier exactly once; calls=%d", calls)
	}
	if !strings.Contains(res.Content, "AGENTMODEL:reviewer:router-model") {
		t.Fatalf("a routed routable agent must run the factory engine on the routed model; got %q", res.Content)
	}
	if strings.Contains(res.Content, "SPECIALIST") {
		t.Fatalf("a routed routable agent must NOT run the pre-built specialist engine; got %q", res.Content)
	}
}

// TestSelectReadOnlyAgentEnginePreservesDefLimitsOnRoutedSwap pins that the routed engine
// swap keeps the def's PER-CALL limits (only the engine changes) — the unit-level guard for
// the "per-def limits untouched" contract.
func TestSelectReadOnlyAgentEnginePreservesDefLimitsOnRoutedSwap(t *testing.T) {
	defLimits := session.Limits{MaxTurns: 7, MaxToolCalls: 13}
	tl := routableAgentTool(true, true, defLimits)
	eng, limits, _, ok := tl.selectReadOnlyAgentEngine("p1", "reviewer", "router-model")
	if !ok || eng == nil {
		t.Fatalf("selectReadOnlyAgentEngine = (%v, ok=%v), want a non-nil engine", eng, ok)
	}
	if eng.Model() != "AGENTMODEL:reviewer:router-model" {
		t.Fatalf("routed swap must run the factory engine; Model()=%q", eng.Model())
	}
	if limits != defLimits {
		t.Fatalf("per-def limits must survive the routed swap; got %+v want %+v", limits, defLimits)
	}
}

// TestRunRoutableAgentMissUsesPrebuilt (issue #286): a routable agent whose classification
// MISSES falls back to the pre-built specialist engine (fail-soft); the classifier is still
// consulted once (the miss is a real classification attempt, not a skip).
func TestRunRoutableAgentMissUsesPrebuilt(t *testing.T) {
	tl := routableAgentTool(true, true, session.Limits{})
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, string, bool) {
		calls++
		return "", "", "", false // miss
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"review it","agent":"reviewer"}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("a routable agent must consult the classifier once even on a miss; calls=%d", calls)
	}
	if !strings.Contains(res.Content, "SPECIALIST") {
		t.Fatalf("a routable-agent miss must fall back to the pre-built specialist; got %q", res.Content)
	}
}

// TestRunRoutableAgentNoFactoryDoesNotSpendClassifier (issue #286): a routable agent with
// NO agent+model factory wired must NOT spend the classifier (the pick could not be
// consumed) — calls==0 — and runs the pre-built specialist.
func TestRunRoutableAgentNoFactoryDoesNotSpendClassifier(t *testing.T) {
	tl := routableAgentTool(true, false /*no factory*/, session.Limits{})
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeTask: hitRoute(&calls, "router-model")}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"review it","agent":"reviewer"}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("a routable agent with no agent+model factory must NOT spend the classifier; calls=%d", calls)
	}
	if !strings.Contains(res.Content, "SPECIALIST") {
		t.Fatalf("it must run the pre-built specialist; got %q", res.Content)
	}
}

// TestRunRoutableAgentFactoryDeclineUsesPrebuilt (issue #286, ADVERSARIAL — the inline-MCP
// decline shape): the classifier hits but the agent+model factory DECLINES (returns false);
// the call falls back to the pre-built specialist, no error.
func TestRunRoutableAgentFactoryDeclineUsesPrebuilt(t *testing.T) {
	tl := routableAgentTool(false /*factory declines*/, true, session.Limits{})
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeTask: hitRoute(&calls, "router-model")}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"review it","agent":"reviewer"}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("a factory decline must fall back cleanly, got error: %q", res.Content)
	}
	if calls != 1 {
		t.Fatalf("the classifier is consulted once; calls=%d", calls)
	}
	if !strings.Contains(res.Content, "SPECIALIST") {
		t.Fatalf("a factory decline must fall back to the pre-built specialist; got %q", res.Content)
	}
}

// TestRunExplicitAgentModelBypassesRouter (issue #286 precedence): an explicit `agent`+`model`
// pins the specialist-on-that-model — the router never fires (per-call model wins), even for a
// routable def.
func TestRunExplicitAgentModelBypassesRouter(t *testing.T) {
	tl := routableAgentTool(true, true, session.Limits{})
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeTask: hitRoute(&calls, "router-model")}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x","agent":"reviewer","model":"fast"}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("an explicit agent+model must NOT consult the router; calls=%d", calls)
	}
	if !strings.Contains(res.Content, "AGENTMODEL:reviewer:fast") {
		t.Fatalf("agent+model must pin the specialist on the explicit model; got %q", res.Content)
	}
}

// PRECEDENCE: a `resume` call continues a persisted child on the default explorer engine
// (the v1 resume invariant) — the router must NOT fire (the run() hook gates routing on
// !resuming). A regression dropping the `resuming` guard would re-classify a resumed
// child, undetected. Asserted at the gate (maybeRouteModel), the single source of the
// precedence decision, so the assertion is deterministic and needs no store fixture.
func TestRunResumeDoesNotRoute(t *testing.T) {
	var calls int
	route := func(context.Context, string) (string, string, string, bool) {
		calls++
		return "large", "router-model", "", true
	}
	caps := parentCaps{children: newChildRunRegistry(), routeTask: route}
	args := subagentArgs{Prompt: "x", Resume: "subagent-abc"}
	tl := routerTool()
	cat, model, _ := tl.maybeRouteModel(context.Background(), args, true /*resuming*/, false /*writable*/, caps)
	if calls != 0 {
		t.Fatalf("routeTask must NOT be consulted on a resume; called %d times", calls)
	}
	if cat != "" || model != "" {
		t.Fatalf("a resume must not route; got (%q, %q)", cat, model)
	}
}

// TestMaybeRouteModelGateReasons (issue #367) pins the CLOSED wire reason each routing
// GATE yields: a delegation that never reaches the classifier must still say WHY on the
// wire (pinned model / def pin / resume / fork / router disabled / writable-unroutable),
// and a classifier/breaker miss passes the routeTask-supplied reason through. These are
// the labels a UI branches on to tell "router off / pinned / inherited default" apart
// from a classifier failure or breaker-open fallback — the cases routed_*="" alone
// collapses. Asserted at maybeRouteModel, the single source of the gate decision.
func TestMaybeRouteModelGateReasons(t *testing.T) {
	hitCaps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, string, bool) {
		return "large", "big-model", "", true
	}}
	missCaps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, string, bool) {
		return "", "", RouterMissBadVerdict, false
	}}
	nilCaps := parentCaps{children: newChildRunRegistry()} // routeTask nil

	cases := []struct {
		name       string
		args       subagentArgs
		resuming   bool
		writable   bool
		caps       parentCaps
		wantReason string
	}{
		{"resume pins", subagentArgs{Prompt: "x", Resume: "subagent-abc"}, true, false, hitCaps, RoutingReasonResume},
		{"fork pins", subagentArgs{Prompt: "x", Fork: true}, false, false, hitCaps, RoutingReasonFork},
		{"explicit model pins", subagentArgs{Prompt: "x", Model: "fast"}, false, false, hitCaps, RoutingReasonPinnedModel},
		{"router disabled (nil routeTask)", subagentArgs{Prompt: "x"}, false, false, nilCaps, RoutingReasonRouterDisabled},
		{"writable unroutable (no writable factory)", subagentArgs{Prompt: "x"}, false, true, hitCaps, RoutingReasonWritableUnroutable},
		// A WRITABLE delegation that also names an agent has the SAME cause as the plain
		// writable one above (the pick can't be consumed on the writable arm), so it must
		// carry the SAME label — the reason names the cause, not the call shape. It used to
		// collapse to the generic not-routed floor, which is the very ambiguity #367 removes.
		{"writable named agent is writable-unroutable, not the floor",
			subagentArgs{Prompt: "x", Agent: "reviewer"}, false, true, hitCaps, RoutingReasonWritableUnroutable},
		{"classifier miss passes reason through", subagentArgs{Prompt: "x"}, false, false, missCaps, RouterMissBadVerdict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tl := routerTool() // read-only default explorer; no writable factory
			_, _, reason := tl.maybeRouteModel(context.Background(), tc.args, tc.resuming, tc.writable, tc.caps)
			if reason != tc.wantReason {
				t.Fatalf("maybeRouteModel reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}

	// A routed HIT yields an empty reason (routed_category/routed_model carry the
	// classification instead) — the success sentinel.
	tl := routerTool()
	cat, model, reason := tl.maybeRouteModel(context.Background(), subagentArgs{Prompt: "x"}, false, false, hitCaps)
	if cat != "large" || model != "big-model" || reason != "" {
		t.Fatalf("a routed hit must yield (large, big-model, \"\"); got (%q, %q, %q)", cat, model, reason)
	}
}

// FAIL-SOFT: a routeTask MISS (ok=false) falls through to the DEFAULT explorer engine —
// the delegation still completes, never errors.
func TestRunRouteTaskMissInheritsDefault(t *testing.T) {
	tl := routerTool()
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, string, bool) {
		return "", "", "", false // miss
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x"}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("a router miss must still complete on the default engine, got error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "DEFAULT") {
		t.Fatalf("a router miss must run on the DEFAULT explorer; got %q", res.Content)
	}
}

// NO-NESTING: a nil routeTask (the child posture — a child has no parentCaps.routeTask)
// runs the default engine with no routing. A child structurally cannot route.
func TestRunNilRouteTaskNoRouting(t *testing.T) {
	tl := routerTool()
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x"}`)),
		memfs.NewWorkspace("/ws"), nil, parentCaps{children: newChildRunRegistry()}) // routeTask nil
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !strings.Contains(res.Content, "DEFAULT") {
		t.Fatalf("a nil routeTask must run the default engine; got %q", res.Content)
	}
}

// writableRouterTool builds a Subagent whose DEFAULT writable explorer emits
// "WRITABLE-DEFAULT" and whose writable engine factory mints "WRITABLE-ROUTED:<model>"
// (found=true) — so a test can tell whether a routed writable pick took effect or the call
// fell back to the default writable explorer. found=false makes every factory call a miss.
func writableRouterTool(found bool) *SubagentTool {
	wf := func(model string) (*Engine, bool) {
		if !found {
			return nil, false
		}
		return markerEngine("WRITABLE-ROUTED:" + model), true
	}
	return NewSubagentTool(markerEngine("READ-ONLY"),
		WithWritableChildEngine(markerEngine("WRITABLE-DEFAULT")),
		WithWritableEngineFactory(wf)).(*SubagentTool)
}

// TestRunWritableRoutesWhenFactoryWired (issue #285): a PLAIN writable delegation
// (mode:"read-write", no model/agent) with the writable engine factory wired consults the
// router (calls==1) and mints the child on the routed pick via the WRITABLE factory.
func TestRunWritableRoutesWhenFactoryWired(t *testing.T) {
	tl := writableRouterTool(true)
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, string, bool) {
		calls++
		return "large", "big-model", "", true
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"implement it","mode":"read-write"}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("a routed writable delegation must complete, got error: %q", res.Content)
	}
	if calls != 1 {
		t.Fatalf("the router must be consulted exactly once for a plain writable delegation with a wired factory; calls=%d", calls)
	}
	if !strings.Contains(res.Content, "WRITABLE-ROUTED:big-model") {
		t.Fatalf("a routed writable pick must mint the WRITABLE factory engine on the routed model; got %q", res.Content)
	}
}

// TestRunWritableRoutedFactoryMissFailSoft: when the router returns a pick but the writable
// factory misses it (nil,false), the call FAILS SOFT to the default writable explorer —
// never an error (the router is never load-bearing).
func TestRunWritableRoutedFactoryMissFailSoft(t *testing.T) {
	tl := writableRouterTool(false) // factory always misses
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, string, bool) {
		return "large", "big-model", "", true
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"implement it","mode":"read-write"}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("a routed writable factory miss must fall back cleanly, got error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "WRITABLE-DEFAULT") {
		t.Fatalf("a routed writable factory miss must fall back to the DEFAULT writable explorer; got %q", res.Content)
	}
}

// TestRunWritableDoesNotSpendClassifierWhenFactoryUnwired (issue #285): a writable
// delegation whose routed pick would be DISCARDED (writable engine factory UNWIRED) must
// NOT spend the classifier at all — calls==0 — and runs the default writable explorer.
func TestRunWritableDoesNotSpendClassifierWhenFactoryUnwired(t *testing.T) {
	// Writable explorer wired, but NO WithWritableEngineFactory.
	tl := NewSubagentTool(markerEngine("READ-ONLY"),
		WithWritableChildEngine(markerEngine("WRITABLE-DEFAULT"))).(*SubagentTool)
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, string, bool) {
		calls++
		return "large", "big-model", "", true
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"implement it","mode":"read-write"}`)),
		memfs.NewWorkspace("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("a writable delegation with no writable factory must NOT spend the classifier; calls=%d", calls)
	}
	if res.IsError || !strings.Contains(res.Content, "WRITABLE-DEFAULT") {
		t.Fatalf("it must run the default writable explorer; got error=%v content=%q", res.IsError, res.Content)
	}
}

// TestWritableResumeUsesWritableChildEngine (issue #285): a writable RESUME continues on
// the WRITABLE explorer engine (not the read-only default explorer validateResume returns),
// so its Edit/Write survive. Asserted at resolveEngineAndLimits — the single seam that
// forces the swap — with a store wired so validateResume passes.
func TestWritableResumeUsesWritableChildEngine(t *testing.T) {
	writable := markerEngine("WRITABLE-DEFAULT")
	tl := NewSubagentTool(markerEngine("READ-ONLY"),
		WithWritableChildEngine(writable),
		WithSubagentStore(memstore.New())).(*SubagentTool)

	args := subagentArgs{Prompt: "continue", Resume: "subagent-abc"}
	engine, _, _, ok := tl.resolveEngineAndLimits("p1", args, true /*resuming*/, true /*writable*/, "")
	if !ok {
		t.Fatal("a writable resume with a wired store must resolve")
	}
	if engine != writable {
		t.Fatal("a writable resume must run on the WRITABLE explorer engine, not the read-only default explorer")
	}
}

// The per-run router breaker opens after defaultModelRouterMaxMisses CONSECUTIVE misses
// and then SKIPS the classifier for the rest of the run. This drives the breaker through
// the Engine.parentCaps closure (the production binding), so the threshold + skip are
// exercised end-to-end, not just the bare helper.
func TestRouterBreakerOpensAfterConsecutiveMisses(t *testing.T) {
	var (
		mu        sync.Mutex
		callCount int
	)
	// A router closure that ALWAYS misses (ok=false), counting how often it is consulted.
	mainEngine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "main",
		SubagentModelRouter: func(context.Context, string) (string, string, session.Usage, string, bool) {
			mu.Lock()
			callCount++
			mu.Unlock()
			return "", "", session.Usage{}, RouterMissBadVerdict, false
		},
	})
	// Build a Run carrying the breaker (RunContentWith arms it when the router is wired),
	// then derive the production routeTask via parentCaps.
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, nil, 0)
	if caps.routeTask == nil {
		t.Fatal("routeTask must be wired when SubagentModelRouter is set")
	}
	// Call past the threshold: the first defaultModelRouterMaxMisses calls consult the
	// underlying router (all miss), the breaker opens, and subsequent calls SKIP it.
	for i := 0; i < defaultModelRouterMaxMisses+3; i++ {
		caps.routeTask(context.Background(), "task")
	}
	mu.Lock()
	defer mu.Unlock()
	if callCount != defaultModelRouterMaxMisses {
		t.Fatalf("underlying router consulted %d times, want exactly %d (breaker opens then skips)", callCount, defaultModelRouterMaxMisses)
	}
}

// A successful classification RESETS the breaker's consecutive-miss count.
func TestRouterBreakerResetsOnSuccess(t *testing.T) {
	var (
		mu        sync.Mutex
		callCount int
		hit       bool
	)
	mainEngine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "main",
		SubagentModelRouter: func(context.Context, string) (string, string, session.Usage, string, bool) {
			mu.Lock()
			callCount++
			h := hit
			mu.Unlock()
			if h {
				return "large", "big", session.Usage{}, "", true
			}
			return "", "", session.Usage{}, RouterMissBadVerdict, false
		},
	})
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, nil, 0)
	// Two misses (below the threshold of 3), then a success resets, then more misses must
	// not trip immediately — proving the reset.
	caps.routeTask(context.Background(), "t")
	caps.routeTask(context.Background(), "t")
	mu.Lock()
	hit = true
	mu.Unlock()
	if _, _, _, ok := caps.routeTask(context.Background(), "t"); !ok {
		t.Fatal("a success must classify")
	}
	mu.Lock()
	hit = false
	mu.Unlock()
	// Three more misses are needed to re-open (the count was reset).
	caps.routeTask(context.Background(), "t")
	caps.routeTask(context.Background(), "t")
	caps.routeTask(context.Background(), "t")
	caps.routeTask(context.Background(), "t") // this one should be skipped (breaker open again)
	mu.Lock()
	defer mu.Unlock()
	// 2 (initial misses) + 1 (success) + 3 (re-trip) = 6 underlying consultations; the 7th
	// is skipped. Had the success not reset, the breaker would have opened at the 3rd call.
	if callCount != 6 {
		t.Fatalf("underlying router consulted %d times, want 6 (success reset the consecutive count)", callCount)
	}
}

// TestRouterBreakerSerializesConcurrentCalls hardens the "a Subagent fan-out cannot
// multiply classifier spend in parallel" claim: the breaker mutex is held across the
// WHOLE routeTask call, so concurrent calls are serialised and the consecutive-miss
// count stays deterministic. With an always-miss router and N concurrent calls past the
// threshold, EXACTLY `max` underlying consultations happen (the breaker opens once and
// the rest are skipped) — never more, however the goroutines interleave. Run under -race
// it also proves the closure + breaker are data-race-clean.
func TestRouterBreakerSerializesConcurrentCalls(t *testing.T) {
	var (
		mu        sync.Mutex
		callCount int
	)
	mainEngine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "main",
		SubagentModelRouter: func(context.Context, string) (string, string, session.Usage, string, bool) {
			mu.Lock()
			callCount++
			mu.Unlock()
			return "", "", session.Usage{}, RouterMissBadVerdict, false // always miss → the breaker must open after `max`
		},
	})
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, nil, 0)

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			caps.routeTask(context.Background(), "concurrent task")
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	// The breaker serialises: the first `max` calls consult the underlying router (each a
	// miss), the breaker opens, and every remaining concurrent call is skipped. So the
	// underlying router is consulted EXACTLY `max` times regardless of interleaving — a
	// non-serialised breaker would let several goroutines read consecutiveMiss < max
	// before any incremented it, over-consulting (and racing the field under -race).
	if callCount != defaultModelRouterMaxMisses {
		t.Fatalf("underlying router consulted %d times under %d concurrent calls, want exactly %d (serialised breaker)",
			callCount, goroutines, defaultModelRouterMaxMisses)
	}
}

// TestRouterBreakerSharedAcrossFamilies (ADR 0034): all three delegation families
// (Subagent / Parallel branches / team members) route through the ONE caps.routeTask the
// dispatcher binds per run, so a mixed turn shares a SINGLE breaker + miss counter. Here a
// Parallel fan-out of N branches and one Subagent-shaped call all consult the same
// always-miss router; the breaker must open ONCE after exactly `max` underlying
// consultations across the families combined — never `max` per family. Run under -race it
// also proves the cross-family concurrent classifications are data-race-clean on the one
// breaker. (We drive routeTask directly here, the shape every family uses, so the test is
// family-agnostic — exactly the point: one closure, one breaker.)
func TestRouterBreakerSharedAcrossFamilies(t *testing.T) {
	var (
		mu        sync.Mutex
		callCount int
	)
	mainEngine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "main",
		SubagentModelRouter: func(context.Context, string) (string, string, session.Usage, string, bool) {
			mu.Lock()
			callCount++
			mu.Unlock()
			return "", "", session.Usage{}, RouterMissBadVerdict, false // always miss
		},
	})
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, nil, 0)

	// Simulate a mixed turn: several "parallel branch" classifications + one "subagent"
	// classification, concurrently, all through the SAME caps.routeTask. The combined
	// consult count must cap at `max` — proving one breaker spans the families.
	const parallelBranches = 5
	var wg sync.WaitGroup
	for i := 0; i < parallelBranches; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); caps.routeTask(context.Background(), "branch task") }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); caps.routeTask(context.Background(), "subagent task") }()
	wg.Wait()
	// A few more after they've all run: the breaker is open, so these are skipped too.
	caps.routeTask(context.Background(), "later")
	caps.routeTask(context.Background(), "later")

	mu.Lock()
	defer mu.Unlock()
	if callCount != defaultModelRouterMaxMisses {
		t.Fatalf("underlying router consulted %d times across families, want exactly %d (ONE shared breaker, not per-family)",
			callCount, defaultModelRouterMaxMisses)
	}
}

// TestRouteTaskPropagatesRunCtx (issue #94): the run's ctx — NOT context.Background() —
// is threaded into SubagentModelRouter, so a Run.Cancel between the breaker's hardAbort
// check and the classifier call propagates into the classifier turn and it dies with the
// run instead of running out its 30s clock. The routeTask closure built by parentCaps
// must forward the ctx it receives. Fail-soft holds: a cancelled ctx yields ok=false.
func TestRouteTaskPropagatesRunCtx(t *testing.T) {
	var (
		gotCtx context.Context
		mu     sync.Mutex
	)
	mainEngine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "main",
		SubagentModelRouter: func(ctx context.Context, _ string) (string, string, session.Usage, string, bool) {
			mu.Lock()
			gotCtx = ctx
			mu.Unlock()
			// Block until the ctx is cancelled, proving the classifier turn observes it.
			<-ctx.Done()
			return "", "", session.Usage{}, RouterMissCancelled, false
		},
	})
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, nil, 0)
	if caps.routeTask == nil {
		t.Fatal("routeTask must be wired when SubagentModelRouter is set")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		caps.routeTask(ctx, "task")
		close(done)
	}()
	// Cancel the run ctx; the router closure must unblock and return (fail-soft) rather
	// than hang for the 30s modelRouterTimeout.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("routeTask did not return after its ctx was cancelled — the run ctx is not propagated into the classifier turn (issue #94)")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotCtx == nil {
		t.Fatal("SubagentModelRouter was never invoked")
	}
	if gotCtx != ctx {
		t.Fatal("SubagentModelRouter received a ctx that is not the one passed to routeTask — the run ctx must propagate (issue #94)")
	}
}

// TestRouteTaskFoldsClassifierUsageIntoParentSession (#92, CWE-770): the dispatch-path
// routeTask closure must fold the classifier's session.Usage into the parent session's
// cumulative sess.Usage UNCONDITIONALLY (on both miss and hit paths) so the single
// budget-brake authority (budgetExhausted reads sess.Usage.TotalTokens()) covers
// classifier spend. This is the headline correctness proof: calling routeTask TWICE must
// accumulate the spend additively, and a miss must still fold (not silently discard).
func TestRouteTaskFoldsClassifierUsageIntoParentSession(t *testing.T) {
	const perCall = 500                                              // tokens per classification call (hit or miss)
	fixedUsage := session.Usage{InputTokens: 300, OutputTokens: 200} // TotalTokens() = perCall

	mainEngine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "main",
		SubagentModelRouter: func(context.Context, string) (string, string, session.Usage, string, bool) {
			// Return non-zero usage on EVERY call regardless of hit/miss — tests
			// that both paths fold correctly.
			return "large", "big-model", fixedUsage, "", true
		},
	})

	// Build a parent session in StateRunning (the state RecordUsage requires).
	parentSess := session.New("parent-fold-test", session.ModeDefault, "/", session.Limits{}, time.Now())
	if err := parentSess.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := parentSess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	// Session is now StateRunning; RecordUsage is legal.

	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, parentSess, 0)
	if caps.routeTask == nil {
		t.Fatal("routeTask must be wired when SubagentModelRouter is set")
	}

	// First call: fold perCall into the parent session.
	caps.routeTask(context.Background(), "task 1")
	if got := parentSess.Usage.TotalTokens(); got != perCall {
		t.Fatalf("after first routeTask call: sess.Usage.TotalTokens() = %d, want %d (first fold)", got, perCall)
	}

	// Second call: fold another perCall — must ACCUMULATE, not overwrite.
	caps.routeTask(context.Background(), "task 2")
	if got := parentSess.Usage.TotalTokens(); got != 2*perCall {
		t.Fatalf("after second routeTask call: sess.Usage.TotalTokens() = %d, want %d (cumulative fold)", got, 2*perCall)
	}
}

// TestRouteTaskFoldsClassifierUsageOnMissPath (#92): a MISS (ok=false from the underlying
// router) must still fold its classifier usage into the parent session — the spend was
// real even though the classification failed. A regression returning session.Usage{} on
// the miss path would silently drop spend and allow CWE-770 unbounded accumulation.
func TestRouteTaskFoldsClassifierUsageOnMissPath(t *testing.T) {
	const perCall = 300                                             // tokens the classifier spends even on a miss
	missUsage := session.Usage{InputTokens: 200, OutputTokens: 100} // TotalTokens() = perCall

	mainEngine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "main",
		SubagentModelRouter: func(context.Context, string) (string, string, session.Usage, string, bool) {
			return "", "", missUsage, RouterMissBadVerdict, false // always miss, but still spends tokens
		},
	})

	parentSess := session.New("parent-miss-fold-test", session.ModeDefault, "/", session.Limits{}, time.Now())
	if err := parentSess.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := parentSess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}

	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, parentSess, 0)

	_, _, _, routeOK := caps.routeTask(context.Background(), "classify me")
	if routeOK {
		t.Fatal("the underlying router misses (ok=false), routeTask must pass through as miss")
	}
	if got := parentSess.Usage.TotalTokens(); got != perCall {
		t.Fatalf("miss path: sess.Usage.TotalTokens() = %d, want %d (miss must fold spend)", got, perCall)
	}
}

// TestClassifierSpendTripsMaxRunTokens (#92, CWE-770 proof): classifier spend folded
// via routeTask into the parent sess.Usage is visible to budgetExhausted, which reads
// sess.Usage.TotalTokens() as the single budget-brake authority. This test proves the
// end-to-end correctness of the fold: calling routeTask repeatedly accumulates spend
// until budgetExhausted returns true, with ZERO main-turn model calls.
func TestClassifierSpendTripsMaxRunTokens(t *testing.T) {
	const perCall = 400 // tokens per classifier call
	const budget = 1000 // budget threshold (trips after 3 calls: 3*400=1200 ≥ 1000)
	const wantTrip = 3  // number of routeTask calls that should trip the budget
	const wantExhausted = true

	spendUsage := session.Usage{InputTokens: 250, OutputTokens: 150} // TotalTokens() = perCall

	mainEngine := NewEngine(Deps{
		LLM:          mockllm.New(),
		Catalog:      tool.NewCatalog(),
		Policy:       allowAllInt(),
		Model:        "main",
		MaxRunTokens: budget,
		SubagentModelRouter: func(context.Context, string) (string, string, session.Usage, string, bool) {
			return "large", "big", spendUsage, "", true
		},
	})

	parentSess := session.New("parent-budget-trip-test", session.ModeDefault, "/", session.Limits{}, time.Now())
	if err := parentSess.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := parentSess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}

	// The miss-breaker is irrelevant here: the router stub always returns ok=true (a hit),
	// which RESETS the consecutive-miss count on every call, so the breaker never advances
	// regardless of its threshold — the fold runs on every (hit) call. We pass a non-default
	// max only to make that independence explicit.
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses + 100}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, parentSess, 0)

	tripped := false
	for i := 0; i < 10; i++ {
		caps.routeTask(context.Background(), "classify")
		if mainEngine.budgetExhausted(run, parentSess.Usage) {
			tripped = true
			// Assert it tripped at exactly the expected call (wantTrip-th call crosses budget).
			if i+1 < wantTrip {
				t.Fatalf("budget tripped after %d routeTask calls, want at least %d", i+1, wantTrip)
			}
			break
		}
	}
	if tripped != wantExhausted {
		t.Fatalf("budgetExhausted = %v after %d routeTask calls with perCall=%d and budget=%d — want %v (classifier spend must fold into the budget brake)",
			tripped, 10, perCall, budget, wantExhausted)
	}
}
