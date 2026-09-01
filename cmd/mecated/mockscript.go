package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"os"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const maxMockScriptDelay = 30 * time.Second

type mockScriptDocument struct {
	Turns []mockScriptTurn `json:"turns"`
}

type mockScriptTurn struct {
	DelayMS   int64                `json:"delay_ms,omitempty"`
	Text      *string              `json:"text,omitempty"`
	ToolCalls []mockScriptToolCall `json:"tool_calls,omitempty"`
}

type mockScriptToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// loadMockScript turns the operator-selected JSON document into the existing
// mockllm adapter used by app.Config.MockProvider. delay_ms delays entry into a
// model turn so a real-wire client can deterministically cancel an in-flight run;
// the response itself is still emitted by mockllm rather than a second provider.
func loadMockScript(path string) (port.LLMProvider, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("--mock-script %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var doc mockScriptDocument
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("--mock-script %q: decode JSON: %w", path, err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, fmt.Errorf("--mock-script %q: %w", path, err)
	}
	if len(doc.Turns) == 0 {
		return nil, fmt.Errorf("--mock-script %q: turns must contain at least one turn", path)
	}

	turns := make([]mockllm.Turn, 0, len(doc.Turns))
	delays := make([]time.Duration, 0, len(doc.Turns))
	for i, scripted := range doc.Turns {
		turn, delay, err := compileMockScriptTurn(scripted)
		if err != nil {
			return nil, fmt.Errorf("--mock-script %q: turn %d: %w", path, i+1, err)
		}
		turns = append(turns, turn)
		delays = append(delays, delay)
	}

	return &delayedMockProvider{inner: mockllm.New(turns...), delays: delays}, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return nil
}

func compileMockScriptTurn(scripted mockScriptTurn) (mockllm.Turn, time.Duration, error) {
	if scripted.DelayMS < 0 || scripted.DelayMS > int64(maxMockScriptDelay/time.Millisecond) {
		return mockllm.Turn{}, 0, fmt.Errorf("delay_ms must be between 0 and %d", maxMockScriptDelay/time.Millisecond)
	}
	if (scripted.Text == nil) == (len(scripted.ToolCalls) == 0) {
		return mockllm.Turn{}, 0, fmt.Errorf("set exactly one of text or tool_calls")
	}
	delay := time.Duration(scripted.DelayMS) * time.Millisecond
	if scripted.Text != nil {
		return mockllm.TextTurn(*scripted.Text), delay, nil
	}

	calls := make([]session.ToolCall, 0, len(scripted.ToolCalls))
	for i, call := range scripted.ToolCalls {
		if call.ID == "" || call.Name == "" {
			return mockllm.Turn{}, 0, fmt.Errorf("tool_calls[%d] requires non-empty id and name", i)
		}
		if len(call.Args) == 0 {
			call.Args = json.RawMessage(`{}`)
		}
		if !json.Valid(call.Args) {
			return mockllm.Turn{}, 0, fmt.Errorf("tool_calls[%d].args is not valid JSON", i)
		}
		calls = append(calls, session.NewToolCall(session.ToolCallID(call.ID), call.Name, call.Args))
	}
	return mockllm.ToolCallTurn(calls...), delay, nil
}

// delayedMockProvider is only a context-aware timing decorator. mockllm still
// owns the script cursor and every response chunk; this type adds no mock
// response semantics of its own.
type delayedMockProvider struct {
	inner  *mockllm.Provider
	delays []time.Duration
	mu     sync.Mutex
	next   int
}

func (p *delayedMockProvider) Capabilities() port.ProviderCapabilities {
	return p.inner.Capabilities()
}

func (p *delayedMockProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.mu.Lock()
	index := p.next
	p.next++
	seq, err := p.inner.Stream(ctx, req)
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if index >= len(p.delays) || p.delays[index] == 0 {
		return seq, nil
	}
	delay := p.delays[index]
	return func(yield func(port.Chunk, error) bool) {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		seq(yield)
	}, nil
}
