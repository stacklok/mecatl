package agent_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type identityAskPolicy struct{ calls int }

func (p *identityAskPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) port.PermissionResult {
	p.calls++
	return port.PermissionResult{Decision: governance.PermissionDecision{Effect: governance.Ask}}
}
func (*identityAskPolicy) Learn(session.SessionID, session.ToolCall) {}

type identityHooks struct{ toolCalls int }

func (h *identityHooks) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == governance.PhasePreToolUse || ev.Phase == governance.PhasePostToolUse {
		h.toolCalls++
	}
	return governance.HookOutcome{}, nil
}

func TestCanonicalAcceptanceRejectsWholeProviderBatch(t *testing.T) {
	valid := port.LLMToolCall{ID: "pair", ItemID: "item", Name: "Act", Args: []byte(`{}`)}
	for name, batch := range map[string][]port.Chunk{
		"nil":              {{Kind: port.ChunkToolCall}},
		"empty":            {mockllm.ToolCallChunk(session.ToolCall{Name: "Act"})},
		"valid then nil":   {{Kind: port.ChunkToolCall, ToolCall: &valid}, {Kind: port.ChunkToolCall}},
		"valid then empty": {{Kind: port.ChunkToolCall, ToolCall: &valid}, {Kind: port.ChunkToolCall, ToolCall: &port.LLMToolCall{Name: "Act"}}},
		"duplicate":        {{Kind: port.ChunkToolCall, ToolCall: &valid}, {Kind: port.ChunkToolCall, ToolCall: &valid}},
	} {
		t.Run(name, func(t *testing.T) {
			policy, hooks := &identityAskPolicy{}, &identityHooks{}
			executions := 0
			act := &fakeTool{name: "Act", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				executions++
				return session.NewToolResult(call.ID, "ok"), nil
			}}
			chunks := append([]port.Chunk{{Kind: port.ChunkText, Text: "tentative display"}, {Kind: port.ChunkReplay, Replay: &session.ProviderReplay{Format: "test", Version: 1, Data: []byte("private")}}}, batch...)
			chunks = append(chunks, mockllm.UsageChunk(session.Usage{InputTokens: 7, OutputTokens: 3}), mockllm.DoneChunk(session.StopEndTurn))
			e := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ChunksTurn(chunks...)), Catalog: catalogWith(t, act), Policy: policy, Hooks: hooks})
			sess := newSession(t, session.Limits{})
			events := drain(e.Run(t.Context(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
			if lastResult(t, events).Stop != session.StopError || sess.UsageFor(session.UsageKindMain).InputTokens != 7 || sess.UsageFor(session.UsageKindMain).OutputTokens != 3 {
				t.Fatalf("stop/usage: %+v %+v", lastResult(t, events), sess.UsageFor(session.UsageKindMain))
			}
			if executions != 0 || policy.calls != 0 || hooks.toolCalls != 0 {
				t.Fatalf("invalid batch escaped acceptance: executions=%d policy=%d tool hooks=%d", executions, policy.calls, hooks.toolCalls)
			}
			if !containsType(events, session.EvMessageDelta) {
				t.Fatal("failed turn lost streamed display text")
			}
			for _, ev := range events {
				if ev.ToolCall != nil || ev.ToolResult != nil || ev.Ask != nil {
					t.Fatalf("published invalid call/ask/result: %+v", ev)
				}
				if ev.Hook != nil && ev.Hook.CallID != "" {
					t.Fatalf("published tool hook before acceptance: %+v", ev)
				}
			}
			for _, m := range sess.Conversation.Messages {
				if m.Role == session.RoleAssistant || m.Replay != nil || len(m.ToolCalls) != 0 {
					t.Fatalf("partial accepted history: %+v", m)
				}
			}
		})
	}
}

func TestCanonicalAcceptanceCompletedProviderKeyReuse(t *testing.T) {
	call := port.LLMToolCall{ID: "reused", ItemID: "output-item", Name: "Act", Args: []byte(`{}`)}
	var requests []port.LLMRequest
	var executed []session.ToolCallID
	act := &fakeTool{name: "Act", exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		if c.ID == session.ToolCallID(call.ID) {
			t.Errorf("executable call retained provider identity: %+v", c)
		}
		executed = append(executed, c.ID)
		return session.NewToolResult(c.ID, "ok"), nil
	}}
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })}, mockllm.LLMToolCallTurn(call), mockllm.LLMToolCallTurn(call), mockllm.TextTurn("done"))
	sess := newSession(t, session.Limits{})
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, act)})
	events := drain(e.Run(t.Context(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	if lastResult(t, events).Stop != session.StopEndTurn || len(executed) != 2 || executed[0] == executed[1] {
		t.Fatalf("reuse was not accepted as fresh invocations: executed=%v result=%+v", executed, lastResult(t, events))
	}
	calls, results := 0, 0
	for _, ev := range events {
		if ev.Type == session.EvToolCall && ev.ToolCall != nil {
			if ev.ToolCall.ID != executed[calls] {
				t.Fatalf("event ID does not match execution: %+v", ev)
			}
			calls++
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			if ev.ToolResult.CallID != executed[results] {
				t.Fatalf("result ID does not match execution: %+v", ev)
			}
			results++
		}
	}
	if calls != 2 || results != 2 || len(requests) != 3 {
		t.Fatalf("calls=%d results=%d requests=%d", calls, results, len(requests))
	}
	for i, req := range requests[1:] {
		answered := 0
		for _, m := range req.Messages {
			for _, c := range m.ToolCalls {
				if c.ID != call.ID || c.ItemID != call.ItemID {
					t.Fatalf("provider tokens changed: %+v", c)
				}
			}
			if m.ToolResult != nil {
				answered++
				if m.ToolResult.CallID != call.ID {
					t.Fatalf("provider result token changed: %+v", m.ToolResult)
				}
			}
		}
		if answered != i+1 {
			t.Fatalf("request %d answered=%d", i+1, answered)
		}
		assertLLMToolPairing(t, req.Messages)
	}
	occurrence := 0
	for _, m := range sess.Conversation.Messages {
		if len(m.ToolCalls) == 0 {
			continue
		}
		if m.ToolCalls[0].ID != executed[occurrence] || m.Replay == nil || len(m.Replay.Bindings) != 1 || m.Replay.Bindings[0].CallID != executed[occurrence] || m.Replay.Bindings[0].PairingID != call.ID {
			t.Fatalf("history/binding identity diverged: %+v", m)
		}
		occurrence++
	}
}

type identityCancelledProvider struct{ cancel context.CancelFunc }

func (*identityCancelledProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}
func (p *identityCancelledProvider) Stream(context.Context, port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		c := port.LLMToolCall{ID: "tentative", Name: "Act", Args: []byte(`{}`)}
		if !yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &c}, nil) {
			return
		}
		if !yield(port.Chunk{Kind: port.ChunkReplay, Replay: &session.ProviderReplay{Format: "test", Version: 1, Data: []byte("private")}}, nil) {
			return
		}
		p.cancel()
		yield(mockllm.UsageChunk(session.Usage{InputTokens: 11}), nil)
	}, nil
}

func TestCanonicalAcceptanceFailedOrCancelledStream(t *testing.T) {
	for _, name := range []string{"error", "cancel", "error stop", "cancel stop"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			call := port.LLMToolCall{ID: "tentative", Name: "Act", Args: []byte(`{}`)}
			chunks := []port.Chunk{{Kind: port.ChunkToolCall, ToolCall: &call}, {Kind: port.ChunkReplay, Replay: &session.ProviderReplay{Format: "test", Version: 1, Data: []byte("private")}}, mockllm.UsageChunk(session.Usage{InputTokens: 11})}
			var llm port.LLMProvider
			wantStop := session.StopError
			switch name {
			case "error":
				llm = mockllm.New(mockllm.ErrorTurn(errors.New("failed stream"), chunks...))
			case "cancel":
				llm = &identityCancelledProvider{cancel: cancel}
				wantStop = session.StopCancelled
			case "error stop":
				llm = mockllm.New(mockllm.ChunksTurn(append(chunks, mockllm.DoneChunk(session.StopError))...))
			case "cancel stop":
				llm = mockllm.New(mockllm.ChunksTurn(append(chunks, mockllm.DoneChunk(session.StopCancelled))...))
				wantStop = session.StopCancelled
			}
			policy := &identityAskPolicy{}
			e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), Policy: policy})
			sess := newSession(t, session.Limits{})
			events := drain(e.Run(ctx, sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
			if lastResult(t, events).Stop != wantStop || sess.UsageFor(session.UsageKindMain).InputTokens != 11 || policy.calls != 0 {
				t.Fatalf("result=%+v usage=%+v policy=%d", lastResult(t, events), sess.UsageFor(session.UsageKindMain), policy.calls)
			}
			for _, m := range sess.Conversation.Messages {
				if len(m.ToolCalls) > 0 || m.Replay != nil {
					t.Fatalf("tentative state accepted: %+v", m)
				}
			}
			for _, ev := range events {
				if ev.ToolCall != nil || ev.Ask != nil {
					t.Fatalf("tentative state published: %+v", ev)
				}
			}
		})
	}
}

func TestCanonicalAcceptanceConcurrentSessionsUniqueIDs(t *testing.T) {
	const n = 32
	ids := make(chan session.ToolCallID, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			act := &fakeTool{name: "Act", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				ids <- call.ID
				return session.NewToolResult(call.ID, "ok"), nil
			}}
			e := newEngine(agent.Deps{LLM: mockllm.New(mockllm.LLMToolCallTurn(port.LLMToolCall{ID: "same", Name: "Act"}), mockllm.TextTurn("done")), Catalog: catalogWith(t, act)})
			sess := newSession(t, session.Limits{})
			sess.ID = session.SessionID(fmt.Sprintf("identity-%d", i))
			events := drain(e.Run(t.Context(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
			if lastResult(t, events).Stop != session.StopEndTurn {
				t.Errorf("session %d failed", i)
			}
		})
	}
	wg.Wait()
	close(ids)
	seen := map[session.ToolCallID]bool{}
	for id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("duplicate or empty ID %q", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Fatalf("executed %d sessions, want %d", len(seen), n)
	}
}
