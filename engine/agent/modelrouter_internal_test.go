package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
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
	return NewSubagentTool(markerEngine("DEFAULT"),
		WithSubagentEngineFactory(factory),
		WithPinnedAgents([]string{"reviewer"})).(*SubagentTool)
}

// A plain default delegation with a wired routeTask mints the child on the ROUTED model.
func TestRunRouteTaskRoutesPlainDelegation(t *testing.T) {
	tl := routerTool()
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
		return modelRoutingResult{category: "large", model: "big-model", ok: true}
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"deep work"}`)),
		memEnv("/ws"), nil, caps)
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
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
		calls++
		return modelRoutingResult{category: "large", model: "router-model", ok: true}
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x","model":"explicit-model"}`)),
		memEnv("/ws"), nil, caps)
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
		routeDecision: func(context.Context, string) modelRoutingResult {
			calls++
			return modelRoutingResult{category: "large", model: "router-model", ok: true}
		},
	}
	_, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x","fork":true}`)),
		memEnv("/ws"), nil, caps)
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
		WithPinnedAgents([]string{"reviewer"}),
	).(*SubagentTool)
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
		calls++
		return modelRoutingResult{category: "large", model: "router-model", ok: true}
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x","agent":"reviewer"}`)),
		memEnv("/ws"), nil, caps)
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
func hitRoute(calls *int, model string) func(context.Context, string) modelRoutingResult {
	return func(context.Context, string) modelRoutingResult {
		*calls++
		return modelRoutingResult{category: "large", model: model, ok: true}
	}
}

// TestRunRoutableAgentRoutesViaFactory (issue #286): a ROUTABLE (unpinned) `agent`
// delegation IS classified and its SCOPED engine is rebuilt on the routed model via the
// agent+model factory. The classifier is consulted once and the factory engine (not the
// pre-built specialist) runs.
func TestRunRoutableAgentRoutesViaFactory(t *testing.T) {
	tl := routableAgentTool(true, true, session.Limits{})
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: hitRoute(&calls, "router-model")}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"review it","agent":"reviewer"}`)),
		memEnv("/ws"), nil, caps)
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
	eng, limits, _, routed, ok := tl.selectReadOnlyAgentEngine("p1", "reviewer", "router-model")
	if !ok || eng == nil {
		t.Fatalf("selectReadOnlyAgentEngine = (%v, ok=%v), want a non-nil engine", eng, ok)
	}
	if !routed {
		t.Fatal("selectReadOnlyAgentEngine must report that the routed factory accepted the target")
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
	var start *session.SubagentPayload
	emit := func(ev session.Event) {
		if ev.Type == session.EvSubagentStart {
			start = ev.Subagent
		}
	}
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
		calls++
		return modelRoutingResult{reason: RouterMissBadVerdict} // miss
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"review it","agent":"reviewer"}`)),
		memEnv("/ws"), emit, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("a routable agent must consult the classifier once even on a miss; calls=%d", calls)
	}
	if !strings.Contains(res.Content, "SPECIALIST") {
		t.Fatalf("a routable-agent miss must fall back to the pre-built specialist; got %q", res.Content)
	}
	if start == nil || start.RoutingReason != RouterMissBadVerdict {
		t.Fatalf("subagent.start RoutingReason = %q, want %q", startReason(start), RouterMissBadVerdict)
	}
}

// The background path has a distinct synchronous EvSubagentStart emitter. Keep the
// producer-to-event contract pinned there too, rather than relying only on foreground
// coverage of the shared routing decision.
func TestRunBackgroundMissCarriesRoutingReason(t *testing.T) {
	tl := routerTool()
	reg := newChildRunRegistry()
	var start *session.SubagentPayload
	emit := func(ev session.Event) {
		if ev.Type == session.EvSubagentStart {
			start = ev.Subagent
		}
	}
	caps := parentCaps{children: reg, routeDecision: func(context.Context, string) modelRoutingResult {
		return modelRoutingResult{reason: RouterMissCancelled}
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"review it","background":true}`)),
		memEnv("/ws"), emit, caps)
	if err != nil || res.IsError {
		t.Fatalf("background start failed: %v %+v", err, res)
	}
	if start == nil || !start.Background || start.RoutingReason != RouterMissCancelled {
		t.Fatalf("background subagent.start = %+v, want Background and RoutingReason %q",
			start, RouterMissCancelled)
	}
	done, ok := reg.doneChFor("subagent-p1")
	if !ok {
		t.Fatal("background registry entry missing")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background child did not finish")
	}
}

// TestRunRoutableAgentNoFactoryDoesNotSpendClassifier (issue #286): a routable agent with
// NO agent+model factory wired must NOT spend the classifier (the pick could not be
// consumed) — calls==0 — and runs the pre-built specialist.
func TestRunRoutableAgentNoFactoryDoesNotSpendClassifier(t *testing.T) {
	tl := routableAgentTool(true, false /*no factory*/, session.Limits{})
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: hitRoute(&calls, "router-model")}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"review it","agent":"reviewer"}`)),
		memEnv("/ws"), nil, caps)
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
	var start *session.SubagentPayload
	emit := func(ev session.Event) {
		if ev.Type == session.EvSubagentStart {
			start = ev.Subagent
		}
	}
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: hitRoute(&calls, "router-model")}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"review it","agent":"reviewer"}`)),
		memEnv("/ws"), emit, caps)
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
	if start == nil {
		t.Fatal("factory-decline delegation emitted no subagent.start event")
	}
	if start.RoutedCategory != "" || start.RoutedModel != "" || start.RoutingReason != session.RoutingReasonTargetUnavailable {
		t.Fatalf("factory-decline routing metadata = (%q, %q, %q), want empty routed fields + %q",
			start.RoutedCategory, start.RoutedModel, start.RoutingReason, session.RoutingReasonTargetUnavailable)
	}
	if start.Model != "SPECIALIST" {
		t.Fatalf("factory-decline Model = %q, want the actual fallback engine model", start.Model)
	}
}

// TestRunExplicitAgentModelBypassesRouter (issue #286 precedence): an explicit `agent`+`model`
// pins the specialist-on-that-model — the router never fires (per-call model wins), even for a
// routable def.
func TestRunExplicitAgentModelBypassesRouter(t *testing.T) {
	tl := routableAgentTool(true, true, session.Limits{})
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: hitRoute(&calls, "router-model")}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x","agent":"reviewer","model":"fast"}`)),
		memEnv("/ws"), nil, caps)
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
	route := func(context.Context, string) modelRoutingResult {
		calls++
		return modelRoutingResult{category: "large", model: "router-model", ok: true}
	}
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: route}
	args := subagentArgs{Prompt: "x", Resume: "subagent-abc"}
	tl := routerTool()
	cat, model, reason, _ := tl.maybeRouteModel(context.Background(), args, true /*resuming*/, false /*writable*/, caps)
	if calls != 0 {
		t.Fatalf("routeTask must NOT be consulted on a resume; called %d times", calls)
	}
	if cat != "" || model != "" {
		t.Fatalf("a resume must not route; got (%q, %q)", cat, model)
	}
	if reason != session.RoutingReasonResume {
		t.Fatalf("a resume must attribute RoutingReason %q; got %q", session.RoutingReasonResume, reason)
	}
}

// GATE ATTRIBUTION (issue #397): maybeRouteModel must attribute WHY the router did not
// classify — the exact session.RoutingReason* constant per gate, EMPTY on a routed hit,
// and a verbatim pass-through of the classifier's missReason. This is the single source of
// the value that lands on the subagent.start RoutingReason field; a regression here
// silently mislabels the wire. The table covers the five cases the issue enumerates
// (router absent / pinned model / agent-def pinned / inherited default→routed / classifier
// miss) plus the fork gate and a classifier-miss pass-through.
func TestMaybeRouteModelGateAttribution(t *testing.T) {
	routeOK := func(context.Context, string) modelRoutingResult {
		return modelRoutingResult{category: "large", model: "big-model", ok: true}
	}
	routeMiss := func(reason string) func(context.Context, string) modelRoutingResult {
		return func(context.Context, string) modelRoutingResult {
			return modelRoutingResult{reason: reason}
		}
	}
	// agentModelFactory wires the agent+model factory (issue #286) so a ROUTABLE def can be
	// rebuilt on the routed model; routable marks the named def as expressing NO model intent.
	agentModelFactory := func() func(agentName, model string) (*Engine, bool) {
		return func(agentName, model string) (*Engine, bool) {
			return markerEngine("AGENTMODEL:" + agentName + ":" + model), true
		}
	}
	cases := []struct {
		name     string
		args     subagentArgs
		resuming bool
		writable bool
		route    func(context.Context, string) modelRoutingResult // nil = router absent
		tool     func() *SubagentTool                             // nil = routerTool()
		wantCat  string
		wantMod  string
		wantWhy  string
	}{
		{
			name: "router absent", args: subagentArgs{Prompt: "x"}, route: nil,
			wantWhy: session.RoutingReasonRouterDisabled,
		},
		{
			name: "pinned model", args: subagentArgs{Prompt: "x", Model: "fast"}, route: routeOK,
			wantWhy: session.RoutingReasonPinnedModel,
		},
		{
			name: "fork", args: subagentArgs{Prompt: "x", Fork: true}, route: routeOK,
			wantWhy: session.RoutingReasonFork,
		},
		{
			name: "agent-def pinned", args: subagentArgs{Prompt: "x", Agent: "reviewer"}, route: routeOK,
			wantWhy: session.RoutingReasonAgentDefPinned,
		},
		{
			name: "unroutable def without model pin", args: subagentArgs{Prompt: "x", Agent: "switched"}, route: routeOK,
			tool: func() *SubagentTool {
				return NewSubagentTool(markerEngine("DEFAULT")).(*SubagentTool)
			},
			wantWhy: session.RoutingReasonRouterDisabled,
		},
		{
			name: "routed hit", args: subagentArgs{Prompt: "x"}, route: routeOK,
			wantCat: "large", wantMod: "big-model", wantWhy: "",
		},
		{
			name: "classifier miss passes through", args: subagentArgs{Prompt: "x"}, route: routeMiss(RouterMissClassifierError),
			wantWhy: RouterMissClassifierError,
		},
		{
			name: "breaker-open passes through", args: subagentArgs{Prompt: "x"}, route: routeMiss(session.RoutingReasonBreakerOpen),
			wantWhy: session.RoutingReasonBreakerOpen,
		},

		// PRECEDENCE OVERLAPS (issue #397): the explicit CHOICE gates attribute BEFORE the
		// router-absent gate, so a pinned delegation is never mislabeled "router-disabled".
		{
			name: "router absent + pinned model", args: subagentArgs{Prompt: "x", Model: "fast"}, route: nil,
			wantWhy: session.RoutingReasonPinnedModel,
		},
		{
			name: "router absent + agent-def pinned", args: subagentArgs{Prompt: "x", Agent: "reviewer"}, route: nil,
			wantWhy: session.RoutingReasonAgentDefPinned,
		},
		{
			name: "router absent + resume", args: subagentArgs{Prompt: "x", Resume: "subagent-abc"}, resuming: true, route: nil,
			wantWhy: session.RoutingReasonResume,
		},
		{
			name: "router absent + fork", args: subagentArgs{Prompt: "x", Fork: true}, route: nil,
			wantWhy: session.RoutingReasonFork,
		},

		// A ROUTABLE def (NO model intent) that still cannot be routed is NOT "agent-def-pinned":
		// the def never pinned a model. A writable routable specialist, or a routable def whose
		// agent+model factory is unwired, attributes to router-disabled (the pick could not be
		// consumed — as good as no router).
		{
			name: "routable def + writable", args: subagentArgs{Prompt: "x", Agent: "explore"}, writable: true, route: routeOK,
			tool: func() *SubagentTool {
				return NewSubagentTool(markerEngine("DEFAULT"),
					WithAgentModelEngineFactory(agentModelFactory()),
					WithRoutableAgents([]string{"explore"})).(*SubagentTool)
			},
			wantWhy: session.RoutingReasonRouterDisabled,
		},
		{
			name: "routable def + agent+model factory unwired", args: subagentArgs{Prompt: "x", Agent: "explore"}, route: routeOK,
			tool: func() *SubagentTool {
				return NewSubagentTool(markerEngine("DEFAULT"),
					WithRoutableAgents([]string{"explore"})).(*SubagentTool)
			},
			wantWhy: session.RoutingReasonRouterDisabled,
		},
		{
			name: "routable def + read-only + factory wired routes", args: subagentArgs{Prompt: "x", Agent: "explore"}, route: routeOK,
			tool: func() *SubagentTool {
				return NewSubagentTool(markerEngine("DEFAULT"),
					WithAgentModelEngineFactory(agentModelFactory()),
					WithRoutableAgents([]string{"explore"})).(*SubagentTool)
			},
			wantCat: "large", wantMod: "big-model", wantWhy: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tl := routerTool()
			if tc.tool != nil {
				tl = tc.tool()
			}
			caps := parentCaps{children: newChildRunRegistry(), routeDecision: tc.route}
			cat, model, reason, _ := tl.maybeRouteModel(context.Background(), tc.args, tc.resuming, tc.writable, caps)
			if cat != tc.wantCat || model != tc.wantMod {
				t.Fatalf("routed = (%q, %q), want (%q, %q)", cat, model, tc.wantCat, tc.wantMod)
			}
			if reason != tc.wantWhy {
				t.Fatalf("RoutingReason = %q, want %q", reason, tc.wantWhy)
			}
		})
	}
}

// EVENT-SAFE ALLOWLIST (issue #397, Finding 3): routingReasonPayload confines the wire
// RoutingReason to the harness/composition metadata constants. The missReason channel is
// OPEN to external engine compositions (Deps.SubagentModelRouter is exported); one returning
// a provider error body, classifier output, or a task excerpt must see it substituted with
// the generic label on the wire (gauntlet #7), while in-tree detailed composition reasons
// are reduced to static codes.
func TestRoutingReasonPayloadEventSafeAllowlist(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty stays empty (routed hit)", "", ""},
		{"gate constant passes", session.RoutingReasonPinnedModel, session.RoutingReasonPinnedModel},
		{"router-disabled passes", session.RoutingReasonRouterDisabled, session.RoutingReasonRouterDisabled},
		{"target-unavailable passes", session.RoutingReasonTargetUnavailable, session.RoutingReasonTargetUnavailable},
		{"empty routed model passes", routingReasonEmptyModel, routingReasonEmptyModel},
		{"classifier miss constant passes", RouterMissClassifierError, RouterMissClassifierError},
		{"bad-verdict passes", RouterMissBadVerdict, RouterMissBadVerdict},
		{"adapter-specific miss is generic", "jev-error", routingReasonGeneric},
		{"composition selector detail reduces to static code", "category-selector-empty (category=large)", routingReasonCategorySelectorEmpty},
		{"composition target detail reduces to static code", "category-target-unresolvable (category=large selector=fast)", routingReasonCategoryTargetUnresolvable},
		{"whitespace collapsed", "  breaker-open  ", session.RoutingReasonBreakerOpen},
		// The attack surface: an external router leaking dynamic text onto the wire.
		{"provider error body replaced", "provider 500: upstream overloaded at /v1/chat\nstack trace…", routingReasonGeneric},
		{"task excerpt replaced", "the task was: delete the prod database", routingReasonGeneric},
		{"classifier prose replaced", "I think this is a large task because…", routingReasonGeneric},
		{"unknown constant replaced", "some-future-reason", routingReasonGeneric},
		{"trusted prefix without composition shape replaced", "category-selector-empty task excerpt: delete prod", routingReasonGeneric},
		{"trusted selector shape discards hostile suffix", "category-selector-empty (task excerpt: delete prod)", routingReasonCategorySelectorEmpty},
		{"trusted target shape discards hostile suffix", "category-target-unresolvable (provider body: secret)", routingReasonCategoryTargetUnresolvable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routingReasonPayload(tc.in); got != tc.want {
				t.Fatalf("routingReasonPayload(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestADR_0350_Scenario4_CanonicalOutcomeProjection(t *testing.T) {
	canonical := []string{
		RouterMissDegenerateInput,
		RouterMissClassifierError,
		RouterMissCancelled,
		RouterMissTimeout,
		RouterMissBadVerdict,
		RouterMissUnknownCategory,
		RouterMissLowConfidence,
		RouterMissInputOverLimit,
		RouterMissCapacityTimeout,
	}
	for _, reason := range canonical {
		if got := routingReasonPayload(reason); got != reason {
			t.Fatalf("canonical reason %q projected as %q", reason, got)
		}
	}
	for _, unknown := range []string{"jev-error", "provider timeout: secret request body", strings.Repeat("x", maxRoutingReasonPreview+1)} {
		if got := routingReasonPayload(unknown); got != routingReasonGeneric {
			t.Fatalf("unknown external reason projected as %q", got)
		}
	}

	canonicalCaps := func(reason string) (parentCaps, *internalCapturingDiag) {
		d := newInternalCapturingDiag()
		eng := NewEngine(Deps{
			LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "main", Diagnostics: d,
			SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
				return ModelRouteResult{Reason: reason}
			}},
		})
		run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry(), diag: d}
		return eng.parentCaps(run, nil, 0), d
	}
	assertDiag := func(reason string, diag *internalCapturingDiag) {
		t.Helper()
		for _, record := range diag.snapshot() {
			if record.attrs["reason"] == reason {
				return
			}
		}
		t.Fatalf("callback diagnostic omitted canonical reason %q", reason)
	}

	var subagentStart *session.SubagentPayload
	subagentCaps, subagentDiag := canonicalCaps(RouterMissTimeout)
	tl := routerTool()
	_, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("canonical", "Subagent", json.RawMessage(`{"prompt":"x"}`)),
		memEnv("/ws"), func(ev session.Event) {
			if ev.Type == session.EvSubagentStart {
				subagentStart = ev.Subagent
			}
		}, subagentCaps)
	if err != nil || subagentStart == nil || subagentStart.RoutingReason != RouterMissTimeout {
		t.Fatalf("Subagent canonical projection = %+v, err=%v", subagentStart, err)
	}
	assertDiag(RouterMissTimeout, subagentDiag)

	var parallelStart *session.ParallelPayload
	parallelCaps, parallelDiag := canonicalCaps(RouterMissLowConfidence)
	parallel := routerParallelTool(true)
	_, err = parallel.ExecuteWithParent(context.Background(),
		session.NewToolCall("canonical-parallel", "Parallel", parallelArgsJSON("inspect")), memEnv("/ws"),
		func(ev session.Event) {
			if ev.Type == session.EvParallelBranch && ev.Parallel != nil && ev.Parallel.Kind == session.ParallelBranchStart {
				parallelStart = ev.Parallel
			}
		}, parallelCaps)
	if err != nil || parallelStart == nil || parallelStart.RoutingReason != RouterMissLowConfidence {
		t.Fatalf("Parallel canonical projection = %+v, err=%v", parallelStart, err)
	}
	assertDiag(RouterMissLowConfidence, parallelDiag)

	factory := func(tm *team.Team, spec MemberSpec, _ string) MemberBuild {
		cat := tool.NewCatalog()
		for _, memberTool := range MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(memberTool)
		}
		return MemberBuild{Engine: NewEngine(Deps{
			LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: cat, Policy: allowAllInt(), Model: "member",
		})}
	}
	teamTool := NewTeamTool(TeamMemberEngineFactory(factory))
	teamCaps, teamDiag := canonicalCaps(RouterMissInputOverLimit)
	var teamStart *session.TeamPayload
	_, err = teamTool.(childCapableTool).ExecuteWithParent(context.Background(),
		session.NewToolCall("canonical-team", "Team", json.RawMessage(`{"goal":"work","members":[{"name":"lead","role":"coordinate"}]}`)),
		memEnv("/ws"), func(ev session.Event) {
			if ev.Type == session.EvTeamStart {
				teamStart = ev.Team
			}
		}, teamCaps)
	if err != nil || teamStart == nil || len(teamStart.Roster) != 1 || teamStart.Roster[0].RoutingReason != RouterMissInputOverLimit {
		t.Fatalf("Team canonical projection = %+v, err=%v", teamStart, err)
	}
	assertDiag(RouterMissInputOverLimit, teamDiag)
}

// FAIL-SOFT: a routeTask MISS (ok=false) falls through to the DEFAULT explorer engine —
// the delegation still completes, never errors.
func TestRunRouteTaskMissInheritsDefault(t *testing.T) {
	tl := routerTool()
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
		return modelRoutingResult{reason: RouterMissBadVerdict} // miss
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"x"}`)),
		memEnv("/ws"), nil, caps)
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
		memEnv("/ws"), nil, parentCaps{children: newChildRunRegistry()}) // routeTask nil
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
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
		calls++
		return modelRoutingResult{category: "large", model: "big-model", ok: true}
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"implement it","mode":"read-write"}`)),
		memEnv("/ws"), nil, caps)
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
	var start *session.SubagentPayload
	emit := func(ev session.Event) {
		if ev.Type == session.EvSubagentStart {
			start = ev.Subagent
		}
	}
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
		return modelRoutingResult{category: "large", model: "big-model", ok: true}
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"implement it","mode":"read-write"}`)),
		memEnv("/ws"), emit, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("a routed writable factory miss must fall back cleanly, got error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "WRITABLE-DEFAULT") {
		t.Fatalf("a routed writable factory miss must fall back to the DEFAULT writable explorer; got %q", res.Content)
	}
	if start == nil || start.RoutedModel != "" || start.RoutingReason != session.RoutingReasonTargetUnavailable {
		t.Fatalf("writable factory-decline start = %+v, want no routed model and reason %q",
			start, session.RoutingReasonTargetUnavailable)
	}
}

func writableRoutableAgentTool(routeFound, wireRouteFactory bool, limits session.Limits) *SubagentTool {
	opts := []SubagentOption{
		WithAgentEngines(map[string]*Engine{"reviewer": markerEngine("READ-ONLY-SPECIALIST")},
			[]AgentMeta{{Name: "reviewer", Description: "reviews", Limits: limits}}),
		WithRoutableAgents([]string{"reviewer"}),
		WithAgentWritableEngineFactory(func(string) (*Engine, bool) {
			return markerEngine("WRITABLE-SPECIALIST"), true
		}),
	}
	if wireRouteFactory {
		opts = append(opts, WithAgentWritableModelEngineFactory(func(agentName, model string) (*Engine, bool) {
			if !routeFound {
				return nil, false
			}
			return markerEngine("WRITABLE-AGENTMODEL:" + agentName + ":" + model), true
		}))
	}
	return NewSubagentTool(markerEngine("DEFAULT"), opts...).(*SubagentTool)
}

func TestRunWritableRoutableAgentRoutesViaFactory(t *testing.T) {
	tl := writableRoutableAgentTool(true, true, session.Limits{})
	var calls int
	var start *session.SubagentPayload
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"fix it","mode":"read-write","agent":"reviewer"}`)),
		memEnv("/ws"), func(ev session.Event) {
			if ev.Type == session.EvSubagentStart {
				start = ev.Subagent
			}
		}, parentCaps{children: newChildRunRegistry(), routeDecision: hitRoute(&calls, "router-model")})
	if err != nil || res.IsError {
		t.Fatalf("routed writable specialist failed: %v %+v", err, res)
	}
	if calls != 1 {
		t.Fatalf("classifier calls = %d, want 1", calls)
	}
	if !strings.Contains(res.Content, "WRITABLE-AGENTMODEL:reviewer:router-model") {
		t.Fatalf("routed writable specialist did not run: %q", res.Content)
	}
	if start == nil || start.RoutedCategory != "large" || start.RoutedModel != "router-model" || start.RoutingReason != "" || start.Model != "WRITABLE-AGENTMODEL:reviewer:router-model" {
		t.Fatalf("routed writable specialist start metadata = %+v", start)
	}
}

func TestRunWritableRoutableAgentMissUsesOrdinarySpecialist(t *testing.T) {
	tl := writableRoutableAgentTool(true, true, session.Limits{})
	var calls int
	var start *session.SubagentPayload
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"fix it","mode":"read-write","agent":"reviewer"}`)),
		memEnv("/ws"), func(ev session.Event) {
			if ev.Type == session.EvSubagentStart {
				start = ev.Subagent
			}
		}, parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
			calls++
			return modelRoutingResult{reason: RouterMissBadVerdict}
		}})
	if err != nil || res.IsError || !strings.Contains(res.Content, "WRITABLE-SPECIALIST") {
		t.Fatalf("router miss did not use ordinary writable specialist: %v %+v", err, res)
	}
	if calls != 1 || start == nil || start.RoutedModel != "" || start.RoutingReason != RouterMissBadVerdict || start.Model != "WRITABLE-SPECIALIST" {
		t.Fatalf("router-miss calls/start = %d/%+v", calls, start)
	}
}

func TestRunWritableRoutableAgentUnavailableTargetFallsBack(t *testing.T) {
	tl := writableRoutableAgentTool(false, true, session.Limits{})
	var start *session.SubagentPayload
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"fix it","mode":"read-write","agent":"reviewer"}`)),
		memEnv("/ws"), func(ev session.Event) {
			if ev.Type == session.EvSubagentStart {
				start = ev.Subagent
			}
		}, parentCaps{children: newChildRunRegistry(), routeDecision: hitRoute(new(int), "missing-model")})
	if err != nil || res.IsError || !strings.Contains(res.Content, "WRITABLE-SPECIALIST") {
		t.Fatalf("unavailable routed target did not fall back: %v %+v", err, res)
	}
	if start == nil || start.RoutedCategory != "" || start.RoutedModel != "" || start.RoutingReason != session.RoutingReasonTargetUnavailable || start.Model != "WRITABLE-SPECIALIST" {
		t.Fatalf("unavailable-target start metadata = %+v", start)
	}
}

func TestRunWritableRoutableAgentMissingFactoryBypassesClassifier(t *testing.T) {
	tl := writableRoutableAgentTool(true, false, session.Limits{})
	var calls int
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"fix it","mode":"read-write","agent":"reviewer"}`)),
		memEnv("/ws"), nil, parentCaps{children: newChildRunRegistry(), routeDecision: hitRoute(&calls, "router-model")})
	if err != nil || res.IsError || !strings.Contains(res.Content, "WRITABLE-SPECIALIST") {
		t.Fatalf("missing routed factory did not use ordinary writable specialist: %v %+v", err, res)
	}
	if calls != 0 {
		t.Fatalf("classifier called %d times with no writable named-model factory", calls)
	}
}

func TestWritableRoutableAgentRequiresBothFactories(t *testing.T) {
	var calls int
	route := hitRoute(&calls, "router-model")
	base := []SubagentOption{
		WithAgentEngines(map[string]*Engine{"reviewer": markerEngine("SPECIALIST")}, nil),
		WithRoutableAgents([]string{"reviewer"}),
	}
	cases := []struct {
		name string
		opt  SubagentOption
	}{
		{"missing routed factory", WithAgentWritableEngineFactory(func(string) (*Engine, bool) { return markerEngine("WRITABLE"), true })},
		{"missing fallback factory", WithAgentWritableModelEngineFactory(func(string, string) (*Engine, bool) { return markerEngine("ROUTED"), true })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := append(append([]SubagentOption{}, base...), tc.opt)
			tl := NewSubagentTool(markerEngine("DEFAULT"), opts...).(*SubagentTool)
			_, model, reason, _ := tl.maybeRouteModel(context.Background(), subagentArgs{Prompt: "x", Agent: "reviewer"}, false, true, parentCaps{routeDecision: route})
			if model != "" || reason != session.RoutingReasonRouterDisabled {
				t.Fatalf("routing = (model=%q reason=%q), want disabled", model, reason)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("incomplete writable named factory sets consulted classifier %d times", calls)
	}
}

func TestWritableRoutableAgentPreservesLimits(t *testing.T) {
	want := session.Limits{MaxTurns: 3, MaxToolCalls: 5}
	tl := writableRoutableAgentTool(true, true, want)
	eng, got, _, routed, ok := tl.selectWritableSpecialistEngine("p1", "reviewer", "router-model")
	if !ok || !routed || eng == nil || eng.Model() != "WRITABLE-AGENTMODEL:reviewer:router-model" || got != want {
		t.Fatalf("selection = (engine=%v limits=%+v routed=%v ok=%v), want routed engine and limits %+v", eng, got, routed, ok, want)
	}
}

func TestWritableNamedRoutingPrecedenceBypassesClassifier(t *testing.T) {
	var calls int
	route := hitRoute(&calls, "router-model")
	tl := writableRoutableAgentTool(true, true, session.Limits{})
	tl.pinnedAgents = map[string]struct{}{"reviewer": {}}
	cases := []struct {
		name     string
		args     subagentArgs
		resuming bool
		want     string
	}{
		{"pinned def", subagentArgs{Prompt: "x", Agent: "reviewer"}, false, session.RoutingReasonAgentDefPinned},
		{"explicit model", subagentArgs{Prompt: "x", Agent: "reviewer", Model: "explicit"}, false, session.RoutingReasonPinnedModel},
		{"fork", subagentArgs{Prompt: "x", Agent: "reviewer", Fork: true}, false, session.RoutingReasonFork},
		{"resume", subagentArgs{Prompt: "x", Agent: "reviewer", Resume: "subagent-p1"}, true, session.RoutingReasonResume},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, model, reason, _ := tl.maybeRouteModel(context.Background(), tc.args, tc.resuming, true, parentCaps{routeDecision: route})
			if model != "" || reason != tc.want {
				t.Fatalf("routing = (model=%q reason=%q), want empty model and %q", model, reason, tc.want)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("higher-precedence choices consulted classifier %d times", calls)
	}
}

func startReason(p *session.SubagentPayload) string {
	if p == nil {
		return ""
	}
	return p.RoutingReason
}

// TestRunWritableDoesNotSpendClassifierWhenFactoryUnwired (issue #285): a writable
// delegation whose routed pick would be DISCARDED (writable engine factory UNWIRED) must
// NOT spend the classifier at all — calls==0 — and runs the default writable explorer.
func TestRunWritableDoesNotSpendClassifierWhenFactoryUnwired(t *testing.T) {
	// Writable explorer wired, but NO WithWritableEngineFactory.
	tl := NewSubagentTool(markerEngine("READ-ONLY"),
		WithWritableChildEngine(markerEngine("WRITABLE-DEFAULT"))).(*SubagentTool)
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeDecision: func(context.Context, string) modelRoutingResult {
		calls++
		return modelRoutingResult{category: "large", model: "big-model", ok: true}
	}}
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"implement it","mode":"read-write"}`)),
		memEnv("/ws"), nil, caps)
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
	engine, _, _, routed, ok := tl.resolveEngineAndLimits("p1", args, true /*resuming*/, true /*writable*/, "")
	if !ok {
		t.Fatal("a writable resume with a wired store must resolve")
	}
	if engine != writable {
		t.Fatal("a writable resume must run on the WRITABLE explorer engine, not the read-only default explorer")
	}
	if routed {
		t.Fatal("a resumed child must never report a routed factory target")
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
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			mu.Lock()
			callCount++
			mu.Unlock()
			return ModelRouteResult{Reason: RouterMissBadVerdict}
		}},
	})
	// Build a Run carrying the breaker (Engine.Run arms it when the router is wired),
	// then derive the production routeTask via parentCaps.
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, nil, 0)
	if caps.routeDecision == nil {
		t.Fatal("routeTask must be wired when SubagentModelRouter is set")
	}
	// Call past the threshold: the first defaultModelRouterMaxMisses calls consult the
	// underlying router (all miss), the breaker opens, and subsequent calls SKIP it.
	for i := 0; i < defaultModelRouterMaxMisses+3; i++ {
		caps.routeDecision(context.Background(), "task")
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
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			mu.Lock()
			callCount++
			h := hit
			mu.Unlock()
			if h {
				return ModelRouteResult{Category: "large", Model: "big", OK: true}
			}
			return ModelRouteResult{Reason: RouterMissBadVerdict}
		}},
	})
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, nil, 0)
	// Two misses (below the threshold of 3), then a success resets, then more misses must
	// not trip immediately — proving the reset.
	caps.routeDecision(context.Background(), "t")
	caps.routeDecision(context.Background(), "t")
	mu.Lock()
	hit = true
	mu.Unlock()
	if got := caps.routeDecision(context.Background(), "t"); !got.ok {
		t.Fatal("a success must classify")
	}
	mu.Lock()
	hit = false
	mu.Unlock()
	// Three more misses are needed to re-open (the count was reset).
	caps.routeDecision(context.Background(), "t")
	caps.routeDecision(context.Background(), "t")
	caps.routeDecision(context.Background(), "t")
	caps.routeDecision(context.Background(), "t") // this one should be skipped (breaker open again)
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
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			mu.Lock()
			callCount++
			mu.Unlock()
			return ModelRouteResult{Reason: RouterMissBadVerdict} // always miss → the breaker must open after `max`
		}},
	})
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, nil, 0)

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			caps.routeDecision(context.Background(), "concurrent task")
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
// (Subagent / Parallel branches / team members) route through the ONE caps.routeDecision the
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
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			mu.Lock()
			callCount++
			mu.Unlock()
			return ModelRouteResult{Reason: RouterMissBadVerdict} // always miss
		}},
	})
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, nil, 0)

	// Simulate a mixed turn: several "parallel branch" classifications + one "subagent"
	// classification, concurrently, all through the SAME caps.routeDecision. The combined
	// consult count must cap at `max` — proving one breaker spans the families.
	const parallelBranches = 5
	var wg sync.WaitGroup
	for i := 0; i < parallelBranches; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); caps.routeDecision(context.Background(), "branch task") }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); caps.routeDecision(context.Background(), "subagent task") }()
	wg.Wait()
	// A few more after they've all run: the breaker is open, so these are skipped too.
	caps.routeDecision(context.Background(), "later")
	caps.routeDecision(context.Background(), "later")

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
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(ctx context.Context, _ string) ModelRouteResult {
			mu.Lock()
			gotCtx = ctx
			mu.Unlock()
			// Block until the ctx is cancelled, proving the classifier turn observes it.
			<-ctx.Done()
			return ModelRouteResult{Reason: RouterMissCancelled}
		}},
	})
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, nil, 0)
	if caps.routeDecision == nil {
		t.Fatal("routeTask must be wired when SubagentModelRouter is set")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		caps.routeDecision(ctx, "task")
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
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			// Return non-zero usage on EVERY call regardless of hit/miss — tests
			// that both paths fold correctly.
			return ModelRouteResult{Category: "large", Model: "big-model", Usage: fixedUsage, OK: true}
		}},
	})

	// Build a parent session in StateRunning (the state RecordUsage requires).
	parentSess := session.New("parent-fold-test", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := parentSess.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := parentSess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	// Session is now StateRunning; RecordUsage is legal.

	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, parentSess, 0)
	if caps.routeDecision == nil {
		t.Fatal("routeTask must be wired when SubagentModelRouter is set")
	}

	// First call: fold perCall into the parent session.
	caps.routeDecision(context.Background(), "task 1")
	if got := parentSess.Usage.TotalTokens(); got != perCall {
		t.Fatalf("after first routeTask call: sess.Usage.TotalTokens() = %d, want %d (first fold)", got, perCall)
	}

	// Second call: fold another perCall — must ACCUMULATE, not overwrite.
	caps.routeDecision(context.Background(), "task 2")
	if got := parentSess.Usage.TotalTokens(); got != 2*perCall {
		t.Fatalf("after second routeTask call: sess.Usage.TotalTokens() = %d, want %d (cumulative fold)", got, 2*perCall)
	}
}

func TestRouteTaskNewCanonicalMissesShareBreakerAndFoldUsageOnce(t *testing.T) {
	for _, reason := range []string{
		RouterMissTimeout,
		RouterMissLowConfidence,
		RouterMissInputOverLimit,
		RouterMissCapacityTimeout,
	} {
		t.Run(reason, func(t *testing.T) {
			const perCall = 11
			calls := 0
			engine := NewEngine(Deps{
				LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "main",
				SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
					calls++
					return ModelRouteResult{Usage: session.Usage{InputTokens: perCall}, Reason: reason}
				}},
			})
			parent := session.New(session.SessionID("parent-"+reason), session.ModeDefault,
				session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
			if err := parent.RecordUserPrompt("go", nil); err != nil {
				t.Fatal(err)
			}
			if err := parent.BeginTurn(); err != nil {
				t.Fatal(err)
			}
			run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
			route := engine.parentCaps(run, parent, 0).routeDecision
			for i := 0; i < defaultModelRouterMaxMisses; i++ {
				got := route(t.Context(), "task")
				if got.ok || got.reason != reason {
					t.Fatalf("miss %d = reason %q ok=%v, want %q false", i+1, got.reason, got.ok, reason)
				}
				if got := parent.Usage.InputTokens; got != (i+1)*perCall {
					t.Fatalf("after miss %d usage=%d, want exactly %d", i+1, got, (i+1)*perCall)
				}
			}
			got := route(t.Context(), "skipped")
			if got.ok || got.reason != session.RoutingReasonBreakerOpen {
				t.Fatalf("post-threshold route = reason %q ok=%v", got.reason, got.ok)
			}
			if calls != defaultModelRouterMaxMisses || parent.Usage.InputTokens != defaultModelRouterMaxMisses*perCall {
				t.Fatalf("calls=%d usage=%d, want %d calls and exactly-once usage %d", calls, parent.Usage.InputTokens,
					defaultModelRouterMaxMisses, defaultModelRouterMaxMisses*perCall)
			}
		})
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
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			return ModelRouteResult{Usage: missUsage, Reason: RouterMissBadVerdict} // always miss, but still spends tokens
		}},
	})

	parentSess := session.New("parent-miss-fold-test", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := parentSess.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := parentSess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}

	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry()}
	caps := mainEngine.parentCaps(run, parentSess, 0)

	if routed := caps.routeDecision(context.Background(), "classify me"); routed.ok {
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
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			return ModelRouteResult{Category: "large", Model: "big", Usage: spendUsage, OK: true}
		}},
	})

	parentSess := session.New("parent-budget-trip-test", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
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
		caps.routeDecision(context.Background(), "classify")
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
