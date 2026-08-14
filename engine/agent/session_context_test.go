package agent_test

import (
	"context"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type contextObservingProvider struct {
	inner port.LLMProvider
	mu    sync.Mutex
	ids   []session.SessionID
}

func (p *contextObservingProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	id, _ := port.SessionIDFromContext(ctx)
	p.mu.Lock()
	p.ids = append(p.ids, id)
	p.mu.Unlock()
	return p.inner.Stream(ctx, req)
}

func (p *contextObservingProvider) Capabilities() port.ProviderCapabilities {
	return p.inner.Capabilities()
}

func (p *contextObservingProvider) sessionIDs() []session.SessionID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]session.SessionID(nil), p.ids...)
}

func TestStartRunBindsExactLoadedSessionID(t *testing.T) {
	const id session.SessionID = "persisted/session:exact-543"
	policy := permpolicy.NewPolicy(nil, permstore.New())
	sess := session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))

	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	initialProvider := &contextObservingProvider{inner: mockllm.New(
		mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"a.go"}`)),
	)}
	initial := newEngine(agent.Deps{
		LLM: initialProvider, Catalog: catalogWith(t, write), Policy: policy,
	})
	askID, restored := driveToAwaiting(t, initial, sess, agent.MemEnv("/ws"), "go")

	resumedProvider := &contextObservingProvider{inner: mockllm.New(mockllm.TextTurn("done"))}
	resumed := newEngine(agent.Deps{
		LLM: resumedProvider, Catalog: catalogWith(t, write), Policy: policy,
	})
	resumeEvents(resumed.ResumeApproval(context.Background(), restored, agent.MemEnv("/ws"), askID, session.VerdictAllowOnce))

	for name, got := range map[string][]session.SessionID{
		"initial": initialProvider.sessionIDs(),
		"resumed": resumedProvider.sessionIDs(),
	} {
		if len(got) == 0 {
			t.Fatalf("%s provider observed no calls", name)
		}
		for i, observed := range got {
			if observed != id {
				t.Errorf("%s call %d session id = %q, want exact loaded id %q", name, i, observed, id)
			}
		}
	}
}
