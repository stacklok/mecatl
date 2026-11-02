package agent

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestCanonicalResultPublisherFinalOnce(t *testing.T) {
	e := NewEngine(Deps{})
	r := &Run{events: make(chan session.Event, 64)}
	var workers sync.WaitGroup
	for i := range 16 {
		id := session.ToolCallID(fmt.Sprintf("canonical-%d", i))
		e.publishToolResult(r, session.Event{Type: session.EvToolResultAvailable, ToolResult: ptr(session.NewToolResult(id, "provisional"))})
		for range 2 {
			workers.Go(func() {
				e.publishToolResult(r, session.Event{Type: session.EvToolResult, ToolResult: ptr(session.NewToolError(id, "final cancellation"))})
				e.publishToolResult(r, session.Event{Type: session.EvToolResultAvailable, ToolResult: ptr(session.NewToolResult(id, "late"))})
			})
		}
	}
	workers.Wait()
	close(r.events)
	available, final := map[session.ToolCallID]int{}, map[session.ToolCallID]int{}
	for event := range r.events {
		id := event.ToolResult.CallID
		switch event.Type {
		case session.EvToolResultAvailable:
			available[id]++
			if final[id] != 0 || event.ToolResult.Content != "provisional" {
				t.Fatal("availability repeated after final or replaced")
			}
		case session.EvToolResult:
			final[id]++
			if available[id] != 1 || !event.ToolResult.IsError {
				t.Fatal("final did not confirm same available identity")
			}
		}
	}
	if len(available) != 16 || len(final) != 16 {
		t.Fatalf("missing identities: available=%v final=%v", available, final)
	}
	for id := range available {
		if available[id] != 1 || final[id] != 1 {
			t.Fatalf("%s: availability=%d finals=%d", id, available[id], final[id])
		}
	}
}
