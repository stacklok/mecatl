package agent_test

import (
	"context"
	"iter"
	"reflect"
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

type contextObservingCompactor struct {
	mu  sync.Mutex
	ids []session.SessionID
}

func (c *contextObservingCompactor) Compact(ctx context.Context, conv *session.Conversation) ([]session.Message, string, error) {
	id, _ := port.SessionIDFromContext(ctx)
	c.mu.Lock()
	c.ids = append(c.ids, id)
	c.mu.Unlock()
	return session.CloneMessages(conv.Messages), "compacted", nil
}

func (c *contextObservingCompactor) sessionIDs() []session.SessionID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]session.SessionID(nil), c.ids...)
}

func TestADR_0290_ProviderUsesAuthoritativeRunContext(t *testing.T) {
	const id session.SessionID = "persisted/session:exact-543"
	policy := permpolicy.NewPolicy(nil, permstore.New())
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))

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
	resumeEvents(resumed.ResumeApproval(port.WithSessionID(context.Background(), "ingress-forged"), restored, agent.MemEnv("/ws"), askID, session.VerdictAllowOnce))

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

	for _, tc := range []struct {
		name      string
		id        session.SessionID
		recovered bool
	}{
		{name: "child", id: "subagent-child-authoritative"},
		{name: "member", id: "team-group-member-authoritative"},
		{name: "recovered", id: "recovered-authoritative", recovered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &contextObservingProvider{inner: mockllm.New(mockllm.TextTurn("done"))}
			eng := newEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)})
			runSession := session.New(tc.id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
			if tc.recovered {
				if err := runSession.BeginTurn(); err != nil {
					t.Fatal(err)
				}
				if err := runSession.Fail(); err != nil {
					t.Fatal(err)
				}
				if err := runSession.Recover(); err != nil {
					t.Fatal(err)
				}
			}
			ctx := port.WithSessionID(context.Background(), "ingress-forged")
			for range eng.Run(ctx, runSession, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}).Events() {
			}
			if got := provider.sessionIDs(); len(got) != 1 || got[0] != tc.id {
				t.Fatalf("provider session IDs = %v, want [%q]", got, tc.id)
			}
		})
	}

	compactor := &contextObservingCompactor{}
	compactionProvider := &contextObservingProvider{inner: mockllm.New(mockllm.TextTurn("done"))}
	compactionEngine := newEngine(agent.Deps{
		LLM: compactionProvider, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil),
		Compactor: compactor, TokenCounter: alwaysCompactCounter{},
		ContextWindow: func() int { return 2 }, CompactionRatio: 0.5,
	})
	const compactionID session.SessionID = "compaction-authoritative"
	compactionSession := session.New(compactionID, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	ctx := port.WithSessionID(context.Background(), "ingress-forged")
	for range compactionEngine.Run(ctx, compactionSession, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}).Events() {
	}
	if got := compactor.sessionIDs(); len(got) == 0 || got[0] != compactionID {
		t.Fatalf("compactor session IDs = %v, want authoritative %q", got, compactionID)
	}
	if got := compactionProvider.sessionIDs(); len(got) != 1 || got[0] != compactionID {
		t.Fatalf("post-compaction provider session IDs = %v, want [%q]", got, compactionID)
	}

	requestType := reflect.TypeOf(port.LLMRequest{})
	wantFields := []string{"System", "Messages", "Tools", "Model"}
	if requestType.NumField() != len(wantFields) {
		t.Fatalf("LLMRequest fields = %d, want unchanged provider-neutral surface %v", requestType.NumField(), wantFields)
	}
	for i, want := range wantFields {
		if got := requestType.Field(i).Name; got != want {
			t.Errorf("LLMRequest field %d = %q, want %q", i, got, want)
		}
	}
}
