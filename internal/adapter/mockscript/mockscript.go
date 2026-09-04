// Package mockscript loads bounded deterministic mock LLM response scripts.
package mockscript

import (
	"bytes"
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

const (
	maxScriptBytes = 1 << 20
	maxDelay       = 30 * time.Second
)

type document struct {
	Turns []turn `json:"turns"`
}

type turn struct {
	DelayMS   int64      `json:"delay_ms,omitempty"`
	Text      *string    `json:"text,omitempty"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
}

type toolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Load reads a bounded JSON document and returns its deterministic mock LLM.
// Script values are response data only; they are never interpreted as commands.
func Load(path string) (port.LLMProvider, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("--mock-script %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	body, err := io.ReadAll(io.LimitReader(f, maxScriptBytes+1))
	if err != nil {
		return nil, fmt.Errorf("--mock-script %q: read JSON: %w", path, err)
	}
	if len(body) > maxScriptBytes {
		return nil, fmt.Errorf("--mock-script %q: JSON exceeds %d bytes", path, maxScriptBytes)
	}

	var doc document
	dec := json.NewDecoder(bytes.NewReader(body))
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
		turn, delay, err := compileTurn(scripted)
		if err != nil {
			return nil, fmt.Errorf("--mock-script %q: turn %d: %w", path, i+1, err)
		}
		turns = append(turns, turn)
		delays = append(delays, delay)
	}

	return &delayedProvider{inner: mockllm.New(turns...), delays: delays}, nil
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

func compileTurn(scripted turn) (mockllm.Turn, time.Duration, error) {
	if scripted.DelayMS < 0 || scripted.DelayMS > int64(maxDelay/time.Millisecond) {
		return mockllm.Turn{}, 0, fmt.Errorf("delay_ms must be between 0 and %d", maxDelay/time.Millisecond)
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

// delayedProvider only adds context-aware turn-entry delays; mockllm owns the
// script cursor and response chunks.
type delayedProvider struct {
	inner  *mockllm.Provider
	delays []time.Duration
	mu     sync.Mutex
	next   int
}

func (p *delayedProvider) Capabilities() port.ProviderCapabilities {
	return p.inner.Capabilities()
}

func (p *delayedProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
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
