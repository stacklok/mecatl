package agent_test

import (
	"context"
	"iter"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// runawayProvider is an ADVERSARIAL, uncooperative port.LLMProvider: every turn it
// emits ONE tool call (so the loop keeps going) plus a fixed per-turn usage and a
// benign StopEndTurn — and it NEVER stops on its own. A cooperative scripted mock
// eventually runs out of turns; this one does not, so the ONLY thing that can
// terminate a run driven by it is a loop-level brake (the token budget / max-turns /
// a deadline). It is the regression guard that the budget is what stops a runaway.
type runawayProvider struct {
	perTurn session.Usage
	calls   atomic.Int64
}

func (*runawayProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

func (p *runawayProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	n := p.calls.Add(1)
	id := session.ToolCallID("c" + string(rune('A'+(n%26))))
	usage := p.perTurn
	return func(yield func(port.Chunk, error) bool) {
		if ctx.Err() != nil {
			return
		}
		tc := session.NewToolCall(id, "Loop", []byte(`{}`))
		if !yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &tc}, nil) {
			return
		}
		if !yield(port.Chunk{Kind: port.ChunkUsage, Usage: &usage}, nil) {
			return
		}
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

// loopTool is a trivial read-only tool the runawayProvider keeps calling so the loop
// never falls into the no-tool-call branch.
func loopTool() *fakeTool {
	return &fakeTool{name: "Loop", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
}

// TestBudgetTerminatesRunawayCleanly is the MODEL-FACING e2e + ADVERSARIAL test: a
// provider that emits a tool call forever is stopped by the token budget alone. It
// asserts the run ends with StopBudget, COMPLETED (Reopen-recoverable, NOT failed), and
// that the per-turn usage accumulated past the ceiling.
func TestBudgetTerminatesRunawayCleanly(t *testing.T) {
	const perTurn = 100
	const budget = 350 // crossed after 4 turns (4*100=400 >= 350)

	llm := &runawayProvider{perTurn: session.Usage{InputTokens: 60, OutputTokens: 40}}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, loopTool()), MaxRunTokens: budget})
	sess := newSession(t, session.Limits{}) // no turn/tool limits: the budget is the only brake
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "run forever"}))

	res := lastResult(t, evs)
	if res.Stop != session.StopBudget {
		t.Fatalf("terminal stop = %q, want %q (the token budget must stop the runaway)", res.Stop, session.StopBudget)
	}
	// COMPLETED + Reopen-recoverable: a budget stop is a clean terminal, never failed.
	if sess.State != session.StateCompleted {
		t.Fatalf("session state = %q, want completed (StopBudget is a clean terminal)", sess.State)
	}
	if err := sess.Reopen(); err != nil {
		t.Fatalf("Reopen after StopBudget: %v (a budget-stopped session must stay recoverable)", err)
	}
	// Cumulative usage on the result must have crossed the ceiling.
	if got := res.Usage.TotalTokens(); got < budget {
		t.Fatalf("cumulative usage = %d, want >= budget %d", got, budget)
	}
	_ = perTurn
}

// TestBudgetDisabledByZero asserts MaxRunTokens==0 disables the budget entirely: a
// finite cooperative script runs to its real end (StopEndTurn) with no early budget
// stop, byte-identical to the pre-budget behaviour.
func TestBudgetDisabledByZero(t *testing.T) {
	llm := mockllm.NewWith(nil,
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("c1", "Loop", `{}`)),
			mockllm.UsageChunk(session.Usage{InputTokens: 10000, OutputTokens: 10000}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, loopTool()), MaxRunTokens: 0})
	sess := newSession(t, session.Limits{})
	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	res := lastResult(t, evs)
	if res.Stop != session.StopEndTurn || res.Text != "done" {
		t.Fatalf("result = {stop:%q text:%q}, want {end_turn done} (MaxRunTokens==0 disables the budget)", res.Stop, res.Text)
	}
}

// TestBudgetBoundaryCheckCompletesInFlightTurn is the ADVERSARIAL boundary case: a
// SINGLE turn whose usage massively OVERSHOOTS the ceiling must still complete that
// turn (the check is at the turn boundary, never a mid-stream abort — preserving
// no-replay-after-first-chunk). The overshoot turn runs to completion; the budget then
// stops the run BEFORE the next turn.
func TestBudgetBoundaryCheckCompletesInFlightTurn(t *testing.T) {
	const budget = 50
	llm := mockllm.NewWith(nil,
		// Turn 1: a tool call (loop continues) + a huge usage overshoot.
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("c1", "Loop", `{}`)),
			mockllm.UsageChunk(session.Usage{InputTokens: 5000, OutputTokens: 5000}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		// Turn 2 must NEVER run: the budget trips at the boundary before it.
		mockllm.TextTurn("should-never-run"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, loopTool()), MaxRunTokens: budget})
	sess := newSession(t, session.Limits{})
	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	// Exactly ONE model call: the in-flight turn completed, the budget stopped before turn 2.
	if got := llm.Calls(); got != 1 {
		t.Fatalf("model calls = %d, want 1 (overshoot turn completes; budget stops before the next turn)", got)
	}
	res := lastResult(t, evs)
	if res.Stop != session.StopBudget {
		t.Fatalf("terminal stop = %q, want %q", res.Stop, session.StopBudget)
	}
	if res.Text == "should-never-run" {
		t.Fatal("the second turn ran; the budget must trip at the boundary, not after the next turn")
	}
}

// TestSubagentChildInheritsBudgetAndReturnsCleanResult is the CHILD-PATH behavioral guard
// (not a field-copy assertion): a Subagent child whose engine carries MaxRunTokens and is
// driven by a never-stopping runawayProvider actually hits StopBudget and the Subagent tool
// returns a CLEAN tool result (not an error) — StopBudget is a non-error terminal, so it
// folds back as the child's summary, never a tool error like StopError would.
func TestSubagentChildInheritsBudgetAndReturnsCleanResult(t *testing.T) {
	const budget = 250
	childLLM := &runawayProvider{perTurn: session.Usage{InputTokens: 60, OutputTokens: 40}}
	childEngine := agent.NewEngine(agent.Deps{
		LLM:          childLLM,
		Catalog:      catalogWith(t, loopTool()),
		Policy:       allowAll(),
		Model:        "child-model",
		MaxRunTokens: budget,
	})
	task := agent.NewSubagentTool(childEngine)

	res, err := task.Execute(context.Background(),
		session.NewToolCall("p1", "Subagent", []byte(`{"prompt":"run forever"}`)),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("Subagent.Execute returned a transport error: %v", err)
	}
	// A budget-stopped child is a CLEAN terminal → a normal tool result, NOT an error.
	if res.IsError {
		t.Fatalf("budget-stopped child must yield a clean tool result, got error: %q", res.Content)
	}
	// The child must have actually run multiple turns before the budget tripped (proving
	// the child engine inherited and enforced the ceiling, not stopped at turn 1).
	if got := childLLM.calls.Load(); got < 2 {
		t.Fatalf("child made %d model calls, want >= 2 (the inherited budget should bound a runaway child)", got)
	}
}

// subagentSpammerProvider is an ADVERSARIAL parent provider: every turn it emits ONE
// Subagent tool call (so the loop keeps delegating) carrying ZERO parent-turn usage and a
// benign StopEndTurn, and it NEVER stops on its own. Zero parent usage is deliberate: it
// isolates the CLASSIFIER's folded spend as the ONLY thing that can move the parent
// budget, so a run that trips StopBudget proves the router-fold (#92) is the cause.
type subagentSpammerProvider struct{ calls atomic.Int64 }

func (*subagentSpammerProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *subagentSpammerProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	n := p.calls.Add(1)
	id := session.ToolCallID("sub" + string(rune('A'+(n%26))))
	return func(yield func(port.Chunk, error) bool) {
		if ctx.Err() != nil {
			return
		}
		tc := session.NewToolCall(id, "Subagent", []byte(`{"prompt":"explore something"}`))
		if !yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &tc}, nil) {
			return
		}
		// ZERO parent-turn usage: only the classifier fold may move the parent budget.
		zero := session.Usage{}
		if !yield(port.Chunk{Kind: port.ChunkUsage, Usage: &zero}, nil) {
			return
		}
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

// TestClassifierSpendTripsStopBudgetE2E is the REAL-LOOP companion to the unit-level
// TestClassifierSpendTripsMaxRunTokens (#92, CWE-770): it drives an actual e.Run to a
// terminal StopBudget asserted off the event stream, mirroring TestBudgetTerminatesRunawayCleanly.
// The parent provider delegates a plain Subagent every turn with ZERO parent usage; a wired
// SubagentModelRouter (the Deps closure the composition builds) classifies each delegation,
// returning a fixed non-zero classifier usage that the dispatch-path routeTask folds into the
// parent sess.Usage. With no other parent spend, the run must terminate StopBudget purely from
// accumulated classifier cost — COMPLETED + Reopen-recoverable, the clean-terminal contract.
//
// A regression that dropped the fold (routeTask not folding, or RunModelRouter/
// buildModelRouterTask returning zero usage) would leave the parent budget at zero forever and
// this run would never terminate — the test would hang then fail the harness timeout, the
// honest signal that classifier spend escaped the budget.
func TestClassifierSpendTripsStopBudgetE2E(t *testing.T) {
	const classifierPerCall = 120                                       // tokens the router reports per classification
	const budget = 350                                                  // crossed after 3 classifications (3*120=360 >= 350)
	classifierUsage := session.Usage{InputTokens: 80, OutputTokens: 40} // TotalTokens()==classifierPerCall

	// The routed child completes immediately (a one-turn summary), so each delegation
	// returns cleanly and the parent loop takes another turn → another classification.
	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("child summary")), tool.NewCatalog())
	subagentTool := agent.NewSubagentTool(childEngine)

	parentLLM := &subagentSpammerProvider{}
	var routeCalls atomic.Int64
	e := newEngine(agent.Deps{
		LLM:          parentLLM,
		Catalog:      catalogWith(t, subagentTool),
		MaxRunTokens: budget,
		// The composition-built router closure stand-in: classify (hit) and report spend.
		SubagentModelRouter: &agent.SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) agent.ModelRouteResult {
			routeCalls.Add(1)
			return agent.ModelRouteResult{Category: "large", Usage: classifierUsage, OK: true} // empty model → inherit default child; the FOLD is what matters
		}},
	})
	sess := newSession(t, session.Limits{}) // no turn/tool limits: the budget is the only brake
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "delegate forever"}))

	res := lastResult(t, evs)
	if res.Stop != session.StopBudget {
		t.Fatalf("terminal stop = %q, want %q (folded classifier spend must trip the budget)", res.Stop, session.StopBudget)
	}
	// Clean terminal: COMPLETED + Reopen-recoverable (StopBudget is never failed).
	if sess.State != session.StateCompleted {
		t.Fatalf("session state = %q, want completed (StopBudget is a clean terminal)", sess.State)
	}
	if err := sess.Reopen(); err != nil {
		t.Fatalf("Reopen after StopBudget: %v (a budget-stopped session must stay recoverable)", err)
	}
	// The router must have actually classified more than once (proving repeated folds, not a
	// single overshoot) — and the cumulative parent usage must have crossed the ceiling from
	// classifier spend alone (parent turns contributed zero).
	if got := routeCalls.Load(); got < 3 {
		t.Fatalf("router classified %d time(s), want >= 3 (the budget should trip after repeated folds)", got)
	}
	if got := sess.Usage.TotalTokens(); got < budget {
		t.Fatalf("cumulative parent usage = %d, want >= budget %d (folded classifier spend only)", got, budget)
	}
	_ = classifierPerCall
}

// countingProvider records how many Stream calls it received, then delegates to a
// scripted mockllm. It proves whether the loop reached a model call at all.
type countingProvider struct {
	calls atomic.Int64
	inner port.LLMProvider
}

func (p *countingProvider) Capabilities() port.ProviderCapabilities { return p.inner.Capabilities() }

func (p *countingProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.calls.Add(1)
	return p.inner.Stream(ctx, req)
}

// TestOrdinaryRunUsesZeroBudgetBaseline is the restart-budget guard (cloud-native
// Phase 1): an ordinary Engine.Run always has a zero baseline, so a session loaded
// carrying cumulative Usage ALREADY past the MaxRunTokens ceiling trips StopBudget
// at the FIRST turn boundary — before any model call — instead of re-granting a
// fresh budget. Internal cleanup/synthesis baselines must never leak into this
// public run entry point.
// The budget brake is evaluated against the AGGREGATE's cumulative Usage
// (sess.Usage), NOT a fresh-from-zero per-run total; there is no loop seed. Mutation:
// changing the budget check from `budgetExhausted(r, sess.Usage)` to
// `budgetExhausted(r, total)` makes the run proceed and the model gets called.
func TestOrdinaryRunUsesZeroBudgetBaseline(t *testing.T) {
	const budget = 350

	inner := mockllm.New(mockllm.TextTurn("should-never-run"))
	llm := &countingProvider{inner: inner}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, loopTool()), MaxRunTokens: budget})

	// A session reloaded mid-conversation with prior spend already over the ceiling.
	sess := newSession(t, session.Limits{})
	sess.Usage = session.Usage{InputTokens: 300, OutputTokens: 100} // 400 >= 350

	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "continue"}))

	res := lastResult(t, evs)
	if res.Stop != session.StopBudget {
		t.Fatalf("terminal stop = %q, want %q (the cumulative budget must trip at the first boundary)", res.Stop, session.StopBudget)
	}
	if got := llm.calls.Load(); got != 0 {
		t.Fatalf("model was called %d time(s); want 0 (the budget tripped at the boundary before any turn)", got)
	}
	// The aggregate's cumulative usage still reflects the loaded prior spend (the
	// per-run EvResult.Usage is zero here — no turn ran this run — which is correct).
	if got := sess.Usage.TotalTokens(); got < budget {
		t.Fatalf("aggregate usage = %d, want >= prior spend %d", got, budget)
	}
	// Clean terminal, Reopen-recoverable.
	if sess.State != session.StateCompleted {
		t.Fatalf("session state = %q, want completed", sess.State)
	}
}
