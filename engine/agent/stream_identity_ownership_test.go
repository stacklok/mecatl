package agent_test

import (
	"context"
	"iter"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type overwritingIdentityProvider struct{ requests []port.LLMRequest }

func (*overwritingIdentityProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}
func (p *overwritingIdentityProvider) Stream(_ context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.requests = append(p.requests, req)
	first := len(p.requests) == 1
	return func(yield func(port.Chunk, error) bool) {
		if first {
			c := port.LLMToolCall{ID: "pair", ItemID: "item", Name: "Act", Args: []byte(`{"path":"yielded"}`)}
			if !yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &c}, nil) {
				return
			}
			for i := range c.Args {
				c.Args[i] = '!'
			}
			c.ID = "overwritten"
			c.ItemID = "overwritten"
			replay := session.ProviderReplay{Format: "opaque", Version: 1, Data: []byte("yielded replay")}
			if !yield(port.Chunk{Kind: port.ChunkReplay, Replay: &replay}, nil) {
				return
			}
			for i := range replay.Data {
				replay.Data[i] = '!'
			}
			replay.Format = "overwritten"
		} else if !yield(port.Chunk{Kind: port.ChunkText, Text: "done"}, nil) {
			return
		}
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

func TestStreamAcceptanceOwnsYieldedArgumentsAndReplay(t *testing.T) {
	p := &overwritingIdentityProvider{}
	executed := 0
	act := &fakeTool{name: "Act", exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executed++
		if string(c.Args) != `{"path":"yielded"}` {
			t.Error("execution retained provider-owned arguments")
		}
		return session.NewToolResult(c.ID, "ok"), nil
	}}
	e := newEngine(agent.Deps{LLM: p, Catalog: catalogWith(t, act)})
	s := newSession(t, session.Limits{})
	events := drain(e.Run(t.Context(), s, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	if lastResult(t, events).Stop != session.StopEndTurn || executed != 1 || len(p.requests) != 2 {
		t.Fatal("fixture failed to execute and request continuation")
	}
	assert := func(messages []session.Message) {
		t.Helper()
		found := false
		for _, m := range messages {
			if len(m.ToolCalls) > 0 {
				found = true
				if string(m.ToolCalls[0].Args) != `{"path":"yielded"}` || m.Replay == nil || m.Replay.Provider.Format != "opaque" || string(m.Replay.Provider.Data) != "yielded replay" || m.Replay.Bindings[0].PairingID != "pair" || m.Replay.Bindings[0].ItemID != "item" {
					t.Fatal("history/snapshot retained overwritten provider storage")
				}
			}
		}
		if !found {
			t.Fatal("no accepted call")
		}
	}
	assert(s.Conversation.Messages)
	snap, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := sessnap.Unmarshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	assert(restored.Conversation.Messages)
	found := false
	for _, m := range p.requests[1].Messages {
		if len(m.ToolCalls) > 0 {
			found = true
			if string(m.ToolCalls[0].Args) != `{"path":"yielded"}` || m.ToolCalls[0].ID != "pair" || m.ToolCalls[0].ItemID != "item" || string(m.Replay.Data) != "yielded replay" {
				t.Fatal("next request used overwritten bytes")
			}
		}
	}
	if !found {
		t.Fatal("next request lost call")
	}
}
