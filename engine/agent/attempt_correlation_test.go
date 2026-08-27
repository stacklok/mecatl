package agent_test

import (
	"context"
	"encoding/json"
	"iter"
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

func TestModelCallsCarryRunAndTurnCorrelation(t *testing.T) {
	provider := &correlationProvider{}
	catalog := tool.NewCatalog()
	catalog.MustRegister(correlationTool{})
	engine := agent.NewEngine(agent.Deps{LLM: provider, Catalog: catalog, Policy: permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil), Model: "test"})
	sess := session.New("correlation", session.ModeDefault, "/ws", session.Limits{}, time.Now())
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, ws, nil)
	for range engine.Run(context.Background(), sess, env, agent.RunRequest{Text: "go"}).Events() {
	}
	if len(provider.turns) != 2 || provider.turns[0] != 0 || provider.turns[1] != 1 {
		t.Fatalf("turn indexes = %v, want [0 1]", provider.turns)
	}
	if provider.serials[0] <= 0 || provider.serials[1] != provider.serials[0] {
		t.Fatalf("run serials = %v, want one positive process-local serial", provider.serials)
	}
}
