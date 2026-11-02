package agent_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// availabilityBatch starts two real read-only siblings; the test controls each
// completion independently instead of waiting for wall-clock execution delays.
func availabilityBatch(t *testing.T) (*agent.Run, *session.Session, <-chan session.ToolCallID, map[string]chan struct{}) {
	t.Helper()
	started := make(chan session.ToolCallID, 2)
	gates := map[string]chan struct{}{
		"slow": make(chan struct{}),
		"fast": make(chan struct{}),
	}
	t.Cleanup(func() {
		for _, gate := range gates {
			select {
			case <-gate:
			default:
				close(gate)
			}
		}
	})
	read := &fakeTool{name: "Read", readOnly: true, exec: func(ctx context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		label := fixtureLabel(t, call)
		started <- call.ID
		select {
		case <-gates[label]:
			return session.NewToolResultWithParts(call.ID, "result "+label, []session.Content{
				session.NewTextBlock("typed " + label),
				session.NewStructuredContentBlock(`{"safe":true}`),
			}), nil
		case <-ctx.Done():
			return session.NewToolError(call.ID, "cancelled"), nil
		}
	}}
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) {
		var ids []session.ProviderCallID
		for _, message := range request.Messages {
			if message.ToolResult != nil {
				ids = append(ids, message.ToolResult.CallID)
			}
		}
		if len(ids) > 0 && !reflect.DeepEqual(ids, []session.ProviderCallID{"slow", "fast"}) {
			t.Errorf("model results out of call order: %v", ids)
		}
	})},
		mockllm.ToolCallTurn(fixtureCall("slow", "Read"), fixtureCall("fast", "Read")),
		mockllm.TextTurn("done"),
	)
	sess := newSession(t, session.Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	r := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, read)}).Run(ctx, sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read"})
	return r, sess, started, gates
}

func simultaneousAvailabilityBatch(t *testing.T) (*agent.Run, <-chan session.ToolCallID, chan struct{}) {
	t.Helper()
	started := make(chan session.ToolCallID, 2)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	read := &fakeTool{name: "Read", readOnly: true, exec: func(ctx context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		started <- call.ID
		select {
		case <-release:
			return session.NewToolResultWithParts(call.ID, "result "+string(call.ID), []session.Content{
				session.NewTextBlock("typed " + string(call.ID)),
				session.NewStructuredContentBlock(`{"safe":true}`),
			}), nil
		case <-ctx.Done():
			return session.NewToolError(call.ID, "cancelled"), nil
		}
	}}
	llm := mockllm.New(
		mockllm.ToolCallTurn(fixtureCall("slow", "Read"), fixtureCall("fast", "Read")),
		mockllm.TextTurn("done"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	r := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, read)}).Run(ctx, newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "read"})
	return r, started, release
}

func awaitAvailabilityBatchStart(t *testing.T, started <-chan session.ToolCallID) {
	t.Helper()
	seen := map[session.ToolCallID]bool{}
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-time.After(3 * time.Second):
			t.Fatal("siblings did not start concurrently")
		}
	}
}

func awaitBatchEvent(t *testing.T, r *agent.Run, kind session.EventType, id session.ToolCallID) []session.Event {
	t.Helper()
	var events []session.Event
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				t.Fatalf("stream closed before %s for %s; events: %v", kind, id, typesOf(events))
			}
			events = append(events, ev)
			if ev.Type == kind && ev.ToolResult != nil && ev.ToolResult.CallID == id {
				return events
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("missing %s for %s while slow sibling is gated; events: %v", kind, id, typesOf(events))
		}
	}
}

func TestToolResultAvailability_FastSiblingAvailableBeforeSlowSibling(t *testing.T) {
	r, sess, started, gates := availabilityBatch(t)
	awaitAvailabilityBatchStart(t, started)
	close(gates["fast"])
	events := awaitBatchEvent(t, r, session.EvToolResultAvailable, boundCallID(t, sess, "fast"))
	cards := map[session.ToolCallID]bool{}
	for _, ev := range events {
		if ev.Type == session.EvToolCall && ev.ToolCall != nil {
			cards[ev.ToolCall.ID] = true
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			t.Fatalf("canonical result before slow sibling finishes: %s", ev.ToolResult.CallID)
		}
		if ev.Type == session.EvToolResultAvailable && ev.ToolResult != nil && ev.ToolResult.CallID != boundCallID(t, sess, "fast") {
			t.Fatalf("slow sibling settled while blocked: %s", ev.ToolResult.CallID)
		}
	}
	if !cards[boundCallID(t, sess, "slow")] || !cards[boundCallID(t, sess, "fast")] || events[len(events)-1].ToolResult.Content != "result fast" ||
		!reflect.DeepEqual(events[len(events)-1].ToolResult.Parts, []session.Content{
			session.NewTextBlock("typed fast"), session.NewStructuredContentBlock(`{"safe":true}`),
		}) {
		t.Fatalf("fast must settle its existing card while slow card remains pending: %+v", events)
	}
	select {
	case ev := <-r.Events():
		t.Fatalf("unexpected event before slow sibling finishes: %+v", ev)
	default:
	}
	close(gates["slow"])
	for range r.Events() {
	}
}

func TestToolResultAvailability_PresentationAndCanonicalOrdering(t *testing.T) {
	r, sess, started, gates := availabilityBatch(t)
	awaitAvailabilityBatchStart(t, started)
	close(gates["fast"])
	events := awaitBatchEvent(t, r, session.EvToolResultAvailable, boundCallID(t, sess, "fast"))
	for _, ev := range events {
		if ev.Type == session.EvToolResult {
			t.Fatalf("canonical event escaped incomplete batch: %+v", ev)
		}
	}
	// The aggregate has recorded the assistant's calls, but not a tool result.
	// Reading here is safe: the dispatcher is waiting on the gated slow call.
	for _, msg := range sess.Conversation.Messages {
		if msg.ToolResult != nil {
			t.Fatalf("availability advanced model history: %+v", msg)
		}
	}
	if sess.Counters.ToolCalls != 0 {
		t.Fatalf("availability advanced tool counter: %+v", sess.Counters)
	}
	close(gates["slow"])
	for ev := range r.Events() {
		events = append(events, ev)
	}
	var available, canonical []session.ToolCallID
	results := map[session.ToolCallID]session.ToolResult{}
	for _, ev := range events {
		if ev.ToolResult == nil {
			continue
		}
		switch ev.Type {
		case session.EvToolResultAvailable:
			available = append(available, ev.ToolResult.CallID)
			results[ev.ToolResult.CallID] = *ev.ToolResult
		case session.EvToolResult:
			canonical = append(canonical, ev.ToolResult.CallID)
			if !reflect.DeepEqual(results[ev.ToolResult.CallID], *ev.ToolResult) {
				t.Errorf("availability/canonical payload mismatch: %+v / %+v", results[ev.ToolResult.CallID], *ev.ToolResult)
			}
		}
	}
	if !reflect.DeepEqual(available, []session.ToolCallID{boundCallID(t, sess, "fast"), boundCallID(t, sess, "slow")}) || !reflect.DeepEqual(canonical, []session.ToolCallID{boundCallID(t, sess, "slow"), boundCallID(t, sess, "fast")}) {
		t.Fatalf("availability=%v canonical=%v; want completion order and call order", available, canonical)
	}
	if sess.Counters.ToolCalls != 2 {
		t.Fatalf("canonical result accounting: %+v", sess.Counters)
	}
	var recorded []session.ToolCallID
	for _, message := range sess.Conversation.Messages {
		if message.ToolResult != nil {
			recorded = append(recorded, message.ToolResult.CallID)
		}
	}
	if !reflect.DeepEqual(recorded, canonical) {
		t.Fatalf("history order differs from final publication: %v / %v", recorded, canonical)
	}
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatal(err)
	}
}

func TestToolResultAvailability_SerializedPublication(t *testing.T) {
	// A shared release makes both workers eligible to complete at once; the
	// all-started handshake prevents the release from favoring launch order.
	for iteration := range 20 {
		t.Run(fmt.Sprint(iteration), func(t *testing.T) {
			r, started, release := simultaneousAvailabilityBatch(t)
			awaitAvailabilityBatchStart(t, started)
			close(release)

			var previous int64
			available := map[session.ToolCallID]int{}
			calls := map[session.ToolCallID]bool{}
			for ev := range r.Events() {
				if ev.Seq <= previous {
					t.Errorf("channel observation seq %d after %d", ev.Seq, previous)
				}
				previous = ev.Seq
				if ev.Type == session.EvToolCall && ev.ToolCall != nil {
					calls[ev.ToolCall.ID] = true
				}
				if ev.Type == session.EvToolResultAvailable && ev.ToolResult != nil {
					available[ev.ToolResult.CallID]++
				}
			}
			if len(calls) != 2 || len(available) != 2 {
				t.Errorf("availability by call = %v, calls=%v, want exactly two", available, calls)
			}
			for id := range calls {
				if available[id] != 1 {
					t.Errorf("availability for accepted call %q = %d, want exactly one", id, available[id])
				}
			}
		})
	}
}
