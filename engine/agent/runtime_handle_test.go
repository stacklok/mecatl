package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// scriptedLLM lets a model fixture construct a later call from the actual prior
// provider-facing result, as a stateless model must. It never rewrites events,
// domain identities, or recorded history.
type scriptedLLM struct {
	mu     sync.Mutex
	turns  []any // mockllm.Turn or func(port.LLMRequest) mockllm.Turn
	cursor int
}

func newScriptedLLM(turns ...any) *scriptedLLM               { return &scriptedLLM{turns: turns} }
func (*scriptedLLM) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }
func (p *scriptedLLM) Calls() int                            { p.mu.Lock(); defer p.mu.Unlock(); return p.cursor }
func (p *scriptedLLM) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.mu.Lock()
	var step any = mockllm.Turn{}
	if p.cursor < len(p.turns) {
		step = p.turns[p.cursor]
	}
	p.cursor++
	p.mu.Unlock()
	var turn mockllm.Turn
	switch step := step.(type) {
	case mockllm.Turn:
		turn = step
	case func(port.LLMRequest) mockllm.Turn:
		turn = step(req)
	default:
		panic("unsupported model fixture turn")
	}
	return mockllm.New(turn).Stream(ctx, req)
}

func fixtureCall(label, name string) session.ToolCall {
	return toolCall(label, name, fmt.Sprintf(`{"fixture":%q}`, label))
}

// fixtureLabel controls test scheduling independently of executable identity.
func fixtureLabel(t *testing.T, call session.ToolCall) string {
	t.Helper()
	var args struct {
		Fixture string `json:"fixture"`
	}
	if err := json.Unmarshal(call.Args, &args); err != nil {
		t.Fatal(err)
	}
	if args.Fixture == "" {
		t.Fatal("missing fixture scheduling label")
	}
	return args.Fixture
}

func eventSubagentCallID(t *testing.T, events []session.Event, childID, name string) session.ToolCallID {
	t.Helper()
	for _, ev := range events {
		if ev.Type == session.EvSubagentTool && ev.Subagent != nil && ev.Subagent.ChildID == childID && ev.Subagent.ToolName == name && ev.Subagent.InnerKind == session.EvToolCall && ev.Subagent.ChildToolCallID != "" {
			return ev.Subagent.ChildToolCallID
		}
	}
	t.Fatalf("no projected accepted %s call for child %q", name, childID)
	return ""
}

func eventSubagentID(t *testing.T, events []session.Event, occurrence int) string {
	t.Helper()
	callID := eventCallID(t, events, "Subagent", occurrence)
	for _, ev := range events {
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil && ev.Subagent.ParentCallID == callID && ev.Subagent.ChildID != "" {
			return ev.Subagent.ChildID
		}
	}
	t.Fatalf("no child start correlated to accepted invocation %q", callID)
	return ""
}

// handleTurn discovers the exact runtime handle on one prior tool result and
// uses it in the next model call. Pairing selects a provider occurrence, not an
// engine invocation ID; ambiguous occurrences fail instead of first/last wins.
func resultHandle(t *testing.T, req port.LLMRequest, pairing session.ProviderCallID) string {
	t.Helper()
	var body string
	found := false
	for _, message := range req.Messages {
		if result := message.ToolResult; result != nil && result.CallID == pairing {
			if found {
				t.Fatalf("ambiguous result occurrence for %q", pairing)
			}
			body, found = result.Content, true
		}
	}
	if !found {
		t.Fatalf("model never received result for %q", pairing)
	}
	var handle string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"agentId: ", "job id: ", "Team id: ", "branch id: "} {
			if strings.HasPrefix(line, prefix) {
				candidate := strings.TrimSpace(strings.TrimPrefix(line, prefix))
				if handle != "" && candidate != handle {
					t.Fatalf("ambiguous runtime handle on result for %q", pairing)
				}
				handle = candidate
			}
		}
	}
	if handle == "" {
		t.Fatalf("result for %q did not expose a runtime handle: %q", pairing, body)
	}
	return handle
}

func handleTurn(t *testing.T, pairing session.ProviderCallID, call session.ToolCall, field string) func(port.LLMRequest) mockllm.Turn {
	t.Helper()
	return func(req port.LLMRequest) mockllm.Turn {
		handle := resultHandle(t, req, pairing)
		var args map[string]json.RawMessage
		if err := json.Unmarshal(call.Args, &args); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(handle)
		if err != nil {
			t.Fatal(err)
		}
		args[field] = encoded
		call.Args, err = json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		return mockllm.ToolCallTurn(call)
	}
}
