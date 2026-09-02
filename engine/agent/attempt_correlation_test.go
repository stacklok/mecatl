package agent_test

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type correlationProvider struct {
	serials []int64
	turns   []int
}

func (*correlationProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *correlationProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	serial, serialOK := port.RunSerialFromContext(ctx)
	turn, turnOK := port.TurnIndexFromContext(ctx)
	if !serialOK || !turnOK {
		return nil, context.Canceled
	}
	p.serials = append(p.serials, serial)
	p.turns = append(p.turns, turn)
	call := len(p.turns)
	return func(yield func(port.Chunk, error) bool) {
		if call == 1 {
			yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &session.ToolCall{ID: "c1", Name: "Echo", Args: json.RawMessage(`{}`)}}, nil)
		} else {
			yield(port.Chunk{Kind: port.ChunkText, Text: "done"}, nil)
		}
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

type correlationTool struct{}

func (correlationTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (correlationTool) ReadOnly() bool { return true }
func (correlationTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "ok"), nil
}

type correlationCompactor struct {
	serials []int64
	turns   []int
}

func (c *correlationCompactor) Compact(ctx context.Context, conv *session.Conversation) ([]session.Message, string, error) {
	serial, serialOK := port.RunSerialFromContext(ctx)
	turn, turnOK := port.TurnIndexFromContext(ctx)
	if !serialOK || !turnOK {
		return nil, "", context.Canceled
	}
	c.serials = append(c.serials, serial)
	c.turns = append(c.turns, turn)
	return session.CloneMessages(conv.Messages), "compacted", nil
}

type alwaysCompactCounter struct{}

func (alwaysCompactCounter) Count(string) int                    { return 1 }
func (alwaysCompactCounter) CountMessages([]session.Message) int { return 2 }

func TestAttemptObserverRejectsProducerControlledPayloadsAtLoopChokePoint(t *testing.T) {
	secrets := []string{"sk-live-SECRET", "Bearer-SECRET", "trace-SECRET"}
	digest, ok := session.NetworkCorrelationDigest("request", secrets[1])
	if !ok {
		t.Fatal("digest rejected test correlation")
	}
	provider := &observingProvider{observations: []session.NetworkAttemptPayload{
		{Attempt: 1, MaxAttempts: 2, RetryDisposition: secrets[0], StreamProgress: "precommit", Decision: "retry", FailureClass: "connect"},
		{Attempt: 1, MaxAttempts: 2, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: "connect", CorrelationKind: "request", CorrelationDigest: secrets[1]},
		{Attempt: 1, MaxAttempts: 2, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: secrets[2]},
		{SessionID: "forged", RunSerial: 999, Turn: 999, Attempt: 1, MaxAttempts: 2, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: "connect", CorrelationKind: "request", CorrelationDigest: digest},
	}}
	engine := agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test", EnableDurableEvidence: true})
	sess := session.New("target", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, memfs.NewWorkspace("/ws"), nil)
	var attempts []session.NetworkAttemptPayload
	var events []session.Event
	for ev := range engine.Run(context.Background(), sess, env, agent.RunRequest{Text: "go"}).Events() {
		events = append(events, ev)
		if ev.Type == session.EvNetworkAttempt {
			attempts = append(attempts, *ev.NetworkAttempt)
		}
	}
	if len(attempts) != 1 {
		t.Fatalf("network attempts = %d, want only canonical observation: %+v", len(attempts), attempts)
	}
	if attempts[0].SessionID != "target" || attempts[0].RunSerial < 1 || attempts[0].Turn != 0 || attempts[0].CorrelationDigest != digest {
		t.Fatalf("canonical attempt = %+v", attempts[0])
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("event stream leaked producer value %q: %s", secret, encoded)
		}
	}
}

type observingProvider struct {
	observations []session.NetworkAttemptPayload
}

func (*observingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}
func (p *observingProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	for _, observation := range p.observations {
		port.ObserveAttempt(ctx, observation)
	}
	return func(yield func(port.Chunk, error) bool) {
		yield(port.Chunk{Kind: port.ChunkText, Text: "done"}, nil)
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

func TestModelCallsCarryRunAndTurnCorrelation(t *testing.T) {
	provider := &correlationProvider{}
	compactor := &correlationCompactor{}
	catalog := tool.NewCatalog()
	catalog.MustRegister(correlationTool{})
	engine := agent.NewEngine(agent.Deps{
		LLM: provider, Catalog: catalog,
		Policy: permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil),
		Model:  "test", Compactor: compactor, TokenCounter: alwaysCompactCounter{},
		ContextWindow: func() int { return 2 }, CompactionRatio: 0.5,
	})
	sess := session.New("correlation", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, ws, nil)
	for range engine.Run(context.Background(), sess, env, agent.RunRequest{Text: "go"}).Events() {
	}
	if len(provider.turns) != 2 || provider.turns[0] != 0 || provider.turns[1] != 1 {
		t.Fatalf("turn indexes = %v, want [0 1]", provider.turns)
	}
	if provider.serials[0] <= 0 || provider.serials[1] != provider.serials[0] {
		t.Fatalf("model run serials = %v, want one positive process-local serial", provider.serials)
	}
	if len(compactor.turns) != 2 || compactor.turns[0] != 0 || compactor.turns[1] != 1 {
		t.Fatalf("compactor turn indexes = %v, want [0 1]", compactor.turns)
	}
	if compactor.serials[0] != provider.serials[0] || compactor.serials[1] != provider.serials[0] {
		t.Fatalf("compactor run serials = %v, want model serial %d", compactor.serials, provider.serials[0])
	}
}
