package llmresilience_test

import (
	"context"
	"encoding/json"
	"iter"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
)

type overwritingIdentityProvider struct {
	visible, replayAfterVisible, fail bool
	requests                          []port.LLMRequest
}

func (*overwritingIdentityProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}
func (p *overwritingIdentityProvider) Stream(_ context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.requests = append(p.requests, req)
	first := len(p.requests) == 1
	return func(yield func(port.Chunk, error) bool) {
		if !first {
			if yield(port.Chunk{Kind: port.ChunkText, Text: "done"}, nil) {
				yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
			}
			return
		}
		call := port.LLMToolCall{ID: "yielded-pair", ItemID: "yielded-item", Name: "Count", Args: json.RawMessage(`{"value":"yielded"}`)}
		if !yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &call}, nil) {
			return
		}
		for i := range call.Args {
			call.Args[i] = '!'
		}
		call.ID = "overwritten-pair"
		call.ItemID = "overwritten-item"
		replay := func() bool {
			r := session.ProviderReplay{Format: "yielded-format", Version: 1, Data: []byte("yielded replay")}
			if !yield(port.Chunk{Kind: port.ChunkReplay, Replay: &r}, nil) {
				return false
			}
			for i := range r.Data {
				r.Data[i] = '!'
			}
			r.Format = "overwritten-format"
			r.Version = 99
			return true
		}
		if !p.replayAfterVisible && !replay() {
			return
		}
		if p.visible && !yield(port.Chunk{Kind: port.ChunkText, Text: "visible"}, nil) {
			return
		}
		if p.replayAfterVisible && !replay() {
			return
		}
		usage := session.Usage{InputTokens: 7, OutputTokens: 3}
		if !yield(port.Chunk{Kind: port.ChunkUsage, Usage: &usage}, nil) {
			return
		}
		if p.fail {
			yield(port.Chunk{}, &streamStatusError{msg: "server_error: 503", status: 503})
			return
		}
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

type replayCountingProvider struct {
	port.LLMProvider
	replays int
}

func (p *replayCountingProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	seq, err := p.LLMProvider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return func(yield func(port.Chunk, error) bool) {
		for c, err := range seq {
			if c.Kind == port.ChunkReplay {
				p.replays++
			}
			if !yield(c, err) {
				return
			}
		}
	}, nil
}

type identityOwnershipTool struct{ calls []session.ToolCall }

func (*identityOwnershipTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Count", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*identityOwnershipTool) ReadOnly() bool { return true }
func (t *identityOwnershipTool) Execute(_ context.Context, c session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.calls = append(t.calls, c)
	return session.NewToolResult(c.ID, "executed"), nil
}

func TestDecoratorOwnsRetainedIdentityThroughEngine(t *testing.T) {
	for _, tc := range []struct {
		name           string
		visible, after bool
	}{
		{name: "precommit"},
		{name: "visible with buffered replay", visible: true},
		{name: "visible with deferred replay", visible: true, after: true},
	} {
		for _, fail := range []bool{false, true} {
			outcome := "success"
			if fail {
				outcome = "failure"
			}
			t.Run(tc.name+"/"+outcome, func(t *testing.T) {
				inner := &overwritingIdentityProvider{visible: tc.visible, replayAfterVisible: tc.after, fail: fail}
				llm := &replayCountingProvider{LLMProvider: llmresilience.Wrap(inner, llmresilience.Config{})}
				counter := &identityOwnershipTool{}
				e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, counter)})
				s := newSession(t, session.Limits{})
				env := tool.MustEnvironment(s.EnvironmentRef, memfs.NewWorkspace("/ws"), memledger.New(), nil)
				events := drain(e.Run(t.Context(), s, env, agent.RunRequest{Text: "go"}))
				result := lastResult(t, events)
				if usage := s.UsageFor(session.UsageKindMain); usage.InputTokens != 7 || usage.OutputTokens != 3 {
					t.Fatalf("usage changed: %+v", usage)
				}
				if fail {
					if result.Stop != session.StopError || len(inner.requests) != 1 || len(counter.calls) != 0 || llm.replays != 0 {
						t.Fatalf("failed attempt escaped or retried: result=%+v requests=%d executions=%d replays=%d", result, len(inner.requests), len(counter.calls), llm.replays)
					}
					for _, ev := range events {
						if ev.ToolCall != nil || ev.ToolResult != nil || ev.Ask != nil || (ev.Hook != nil && ev.Hook.CallID != "") {
							t.Fatal("failed attempt published tentative tool semantics")
						}
					}
					for _, m := range s.Conversation.Messages {
						if len(m.ToolCalls) != 0 || m.ToolResult != nil || m.Replay != nil {
							t.Fatal("failed attempt persisted tentative identity/replay")
						}
					}
					return
				}
				if result.Stop != session.StopEndTurn || len(inner.requests) != 2 || len(counter.calls) != 1 || llm.replays != 1 {
					t.Fatalf("success: result=%+v requests=%d executions=%d replays=%d", result, len(inner.requests), len(counter.calls), llm.replays)
				}
				executed := counter.calls[0]
				if executed.ID == "yielded-pair" || executed.ID == "overwritten-pair" || string(executed.Args) != `{"value":"yielded"}` {
					t.Fatal("execution used borrowed identity/arguments")
				}
				assertHistory := func(messages []session.Message) {
					t.Helper()
					calls := 0
					for _, m := range messages {
						if len(m.ToolCalls) > 0 {
							calls += len(m.ToolCalls)
							if m.ToolCalls[0].ID != executed.ID || string(m.ToolCalls[0].Args) != `{"value":"yielded"}` || m.Replay == nil || len(m.Replay.Bindings) != 1 {
								t.Fatal("history lost accepted identity/arguments")
							}
							b := m.Replay.Bindings[0]
							if b.CallID != executed.ID || b.PairingID != "yielded-pair" || b.ItemID != "yielded-item" {
								t.Fatal("history retained overwritten provider bindings")
							}
							if r := m.Replay.Provider; r == nil || r.Format != "yielded-format" || r.Version != 1 || string(r.Data) != "yielded replay" {
								t.Fatal("history retained overwritten replay")
							}
						}
					}
					if calls != 1 {
						t.Fatalf("history calls=%d", calls)
					}
				}
				assertHistory(s.Conversation.Messages)
				raw, err := sessnap.Marshal(s)
				if err != nil {
					t.Fatal(err)
				}
				restored, err := sessnap.Unmarshal(raw)
				if err != nil {
					t.Fatal(err)
				}
				assertHistory(restored.Conversation.Messages)
				calls, results := 0, 0
				for _, m := range inner.requests[1].Messages {
					if len(m.ToolCalls) > 0 {
						calls += len(m.ToolCalls)
						c := m.ToolCalls[0]
						if c.ID != "yielded-pair" || c.ItemID != "yielded-item" || string(c.Args) != `{"value":"yielded"}` {
							t.Fatal("next request retained overwritten call")
						}
						if r := m.Replay; r == nil || r.Format != "yielded-format" || r.Version != 1 || string(r.Data) != "yielded replay" {
							t.Fatal("next request retained overwritten replay")
						}
					}
					if m.ToolResult != nil {
						results++
						if m.ToolResult.CallID != "yielded-pair" {
							t.Fatal("next request lost result pairing")
						}
					}
				}
				if calls != 1 || results != 1 {
					t.Fatal("next request lost accepted call/result")
				}
			})
		}
	}
}
