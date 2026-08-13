package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// scriptTool is a minimal Tool for the server tests: it records whether (and how
// many times) it ran and returns a fixed content body.
type scriptTool struct {
	name     string
	readOnly bool
	content  string
	executed atomic.Bool
	runCount atomic.Int64
}

func (s *scriptTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: s.name, Description: s.name + ": test tool", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (s *scriptTool) ReadOnly() bool { return s.readOnly }
func (s *scriptTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	s.executed.Store(true)
	s.runCount.Add(1)
	return session.NewToolResult(in.ID, s.content), nil
}
func (s *scriptTool) ran() bool   { return s.executed.Load() }
func (s *scriptTool) runs() int64 { return s.runCount.Load() }

// call builds a session.ToolCall.
func call(id, name, args string) session.ToolCall {
	return session.NewToolCall(session.ToolCallID(id), name, json.RawMessage(args))
}

// blockingChunks streams one text delta then blocks until ctx is cancelled.
// Used to exercise the Cancel path: the mock honours ctx within its iterator.
func blockingChunks() []port.Chunk {
	// A long sequence of text chunks; mockllm.Stream stops yielding when ctx is
	// done, so cancellation interrupts the stream mid-flight before ChunkDone.
	chunks := make([]port.Chunk, 0, 2000)
	chunks = append(chunks, port.Chunk{Kind: port.ChunkText, Text: "thinking"})
	for i := 0; i < 1900; i++ {
		chunks = append(chunks, port.Chunk{Kind: port.ChunkText, Text: "."})
	}
	chunks = append(chunks, port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn})
	return chunks
}

// fakeSessionEngineFactory builds a server.SessionEngineFactory that records
// (into called, when non-nil) that it was invoked and always returns a fresh
// minimal engine over a canned mockllm reply. It names the boilerplate the
// per-session-routing tests (worktree_engine_test.go,
// default_model_pending_test.go) otherwise repeat verbatim — the SIX-param
// SessionEngineFactory signature is easy to typo and adds nothing once the
// body itself is this uniform. Tests that need a DIFFERENT engine per call
// (or to assert on the args) still build their own closure inline — this
// helper is only for "any per-session call routes here, and I just need to
// observe THAT it was called."
func fakeSessionEngineFactory(called *bool) server.SessionEngineFactory {
	return func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		if called != nil {
			*called = true
		}
		return server.SessionEngineResult{
			Engine: agent.NewEngine(agent.Deps{
				LLM:     mockllm.New(),
				Catalog: tool.NewCatalog(),
				Policy:  permpolicy.NewPolicy(allowRules(), nil),
				Model:   "test-model",
			}),
			Close: func() error { return nil },
		}, nil
	}
}

// recvAll drains a Converse server stream until EOF.
func recvAll(t *testing.T, stream mecatlv1.HarnessService_ConverseClient) []*mecatlv1.Event {
	t.Helper()
	var out []*mecatlv1.Event
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		out = append(out, resp.GetEvent())
	}
}

// hasType reports whether any event has the given type string.
func hasType(evs []*mecatlv1.Event, ty string) bool {
	for _, e := range evs {
		if e.GetType() == ty {
			return true
		}
	}
	return false
}

// typesOf returns the type strings of the events, for failure messages.
func typesOf(evs []*mecatlv1.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.GetType()
	}
	return out
}

// lastResult returns the Result of the final result event, failing if none.
func lastResult(t *testing.T, evs []*mecatlv1.Event) *mecatlv1.Result {
	t.Helper()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].GetType() == "result" {
			return evs[i].GetResult()
		}
	}
	t.Fatalf("no result event in %v", typesOf(evs))
	return nil
}
