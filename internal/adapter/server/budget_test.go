package server_test

import (
	"context"
	"iter"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// runawayProvider is an ADVERSARIAL provider that emits a tool call + per-turn usage
// forever and never stops, so only a loop-level brake (the token budget) can terminate
// a run driven by it. It mirrors the agent-package adversarial mock at the service tier.
type runawayProvider struct {
	usage session.Usage
	calls atomic.Int64
}

func (*runawayProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

func (p *runawayProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.calls.Add(1)
	u := p.usage
	return func(yield func(port.Chunk, error) bool) {
		if ctx.Err() != nil {
			return
		}
		tc := session.NewToolCall("loop", "Loop", []byte(`{}`))
		if !yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &tc}, nil) {
			return
		}
		if !yield(port.Chunk{Kind: port.ChunkUsage, Usage: &u}, nil) {
			return
		}
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

// loopServerTool is a trivial read-only tool the runaway provider keeps calling.
type loopServerTool struct{}

func (loopServerTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Loop", Description: "loop", Schema: []byte(`{"type":"object"}`)}
}
func (loopServerTool) ReadOnly() bool { return true }
func (loopServerTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(in.ID, "ok"), nil
}

// TestServiceBudgetSurfacesAndReopens is the SERVICE-LEVEL e2e for the token budget:
// it drives a runaway run through the real server.Service and asserts the terminal
// result carries StopBudget on the wire, the session ends completed (Reopen-recoverable
// via loadAndReopen), and a subsequent prompt on the SAME session succeeds. It mirrors
// TestServiceNoProgressSurfacesAndReopens for the budget terminal.
func TestServiceBudgetSurfacesAndReopens(t *testing.T) {
	const budget = 300
	llm := &runawayProvider{usage: session.Usage{InputTokens: 60, OutputTokens: 40}}

	cat := tool.NewCatalog()
	cat.MustRegister(loopServerTool{})
	engine := agent.NewEngine(agent.Deps{
		LLM:          llm,
		Catalog:      cat,
		Policy:       permpolicy.NewPolicy(allowRules(), nil),
		Model:        "test-model",
		MaxRunTokens: budget,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:              engine,
		Store:               memstore.New(),
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	run, err := svc.StartRunContent(context.Background(), sess.ID, "run forever", nil)
	if err != nil {
		t.Fatalf("StartRunContent #1: %v", err)
	}
	var stop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run)

	if stop != session.StopBudget {
		t.Fatalf("terminal stop = %q, want %q on the wire", stop, session.StopBudget)
	}
	if got, _ := svc.GetSession(context.Background(), sess.ID); got.State != session.StateCompleted {
		t.Fatalf("state after budget stop = %q, want completed (reopen-recoverable)", got.State)
	}

	// A SECOND run on the SAME session must succeed (loadAndReopen recovers the
	// StopBudget-completed session); the runaway provider still never stops, so this run
	// hits the budget again — proving the session is recoverable, not bricked.
	run2, err := svc.StartRunContent(context.Background(), sess.ID, "again", nil)
	if err != nil {
		t.Fatalf("StartRunContent #2 on a StopBudget-completed session: %v (must be reopen-recoverable)", err)
	}
	var stop2 session.StopReason
	for ev := range run2.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop2 = ev.Result.Stop
		}
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run2)
	if stop2 != session.StopBudget {
		t.Fatalf("second run terminal stop = %q, want %q (the session reopened and ran again)", stop2, session.StopBudget)
	}
}
