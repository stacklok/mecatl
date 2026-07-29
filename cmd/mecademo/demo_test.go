package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestRunScenarioOffline runs the demo's offline scenario against mockllm and
// asserts the emitted event sequence proves the whole shape of the loop: a turn
// boundary, a tool call, a tool result, a permission ask (then auto-approved),
// and a successful terminal result.
func TestRunScenarioOffline(t *testing.T) {
	events, err := RunScenario(context.Background(), mockProvider(), demoModel)
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}

	seen := make(map[session.EventType]bool)
	for _, ev := range events {
		seen[ev.Type] = true
	}

	for _, want := range []session.EventType{
		session.EvTurnStart,
		session.EvToolCall,
		session.EvToolResult,
		session.EvPermissionAsk,
		session.EvResult,
	} {
		if !seen[want] {
			t.Errorf("event sequence missing %q", want)
		}
	}

	// The terminal result must be a successful end_turn, proving the loop resumed
	// after the approval and the model finished normally.
	last := events[len(events)-1]
	if last.Type != session.EvResult {
		t.Fatalf("last event = %q, want %q", last.Type, session.EvResult)
	}
	if last.Result == nil {
		t.Fatal("terminal result has no payload")
	}
	if last.Result.Stop != session.StopEndTurn {
		t.Errorf("terminal stop reason = %q, want %q", last.Result.Stop, session.StopEndTurn)
	}
	if last.Result.Text == "" {
		t.Error("terminal result has empty final text")
	}

	// The permission ask must precede the approved tool's result, and a Write
	// result (the approved tool) must appear and be non-error.
	assertApprovalResumed(t, events)
}

// TestRunTeamScenarioOffline runs the demo's offline team scenario and asserts the
// outcome is the lead's CONSOLIDATED synthesis (the team's deliverable), not a bare
// per-member concatenation — proving the new aggregation shape end to end.
func TestRunTeamScenarioOffline(t *testing.T) {
	outcome, err := RunTeamScenario(context.Background())
	if err != nil {
		t.Fatalf("RunTeamScenario: %v", err)
	}
	if !strings.Contains(outcome.Report, "Consolidated report") {
		t.Errorf("team Report = %q, want the lead's consolidated synthesis", outcome.Report)
	}
	if strings.Contains(outcome.Report, "=== ") {
		t.Errorf("team Report must not be a header-only concatenation: %q", outcome.Report)
	}
	if len(outcome.Members) != 2 {
		t.Errorf("team had %d members, want 2", len(outcome.Members))
	}
}

// TestRunBackgroundScenarioOffline runs the demo's background-subagent act and
// asserts the whole I3a+I3b flow: the immediate started-result, the harness
// completion NOTICE injected into history at the next turn boundary (ids + stop
// labels only — exact text), the SubagentStatus collection delivering the
// child's body, and a clean terminal.
func TestRunBackgroundScenarioOffline(t *testing.T) {
	events, notes := RunBackgroundScenario(context.Background())

	var startedResult, collected string
	var sawBackgroundStart bool
	for _, ev := range events {
		switch ev.Type {
		case session.EvSubagentStart:
			if ev.Subagent != nil && ev.Subagent.Background {
				sawBackgroundStart = true
			}
		case session.EvToolResult:
			if ev.ToolResult == nil {
				continue
			}
			switch ev.ToolResult.CallID {
			case "call-bg-1":
				startedResult = ev.ToolResult.Content
			case "call-bg-collect":
				collected = ev.ToolResult.Content
			}
		}
	}
	if !sawBackgroundStart {
		t.Error("no subagent.start with background=true was emitted")
	}
	if !strings.Contains(startedResult, "started in the background") ||
		!strings.HasPrefix(startedResult, "agentId: "+demoBackgroundChildID) {
		t.Errorf("Subagent call must return the immediate started-result with the agentId trailer first, got %q", startedResult)
	}
	if !strings.Contains(collected, "Background check complete") {
		t.Errorf("SubagentStatus collection must deliver the child's body, got %q", collected)
	}

	wantNote := "[harness note: 1 background subagent(s) finished: " + demoBackgroundChildID +
		" (end_turn). Collect each result with SubagentStatus before relying on it.]"
	if len(notes) != 1 || notes[0] != wantNote {
		t.Errorf("expected exactly one injected notice %q, got %v", wantNote, notes)
	}

	last := events[len(events)-1]
	if last.Type != session.EvResult || last.Result == nil || last.Result.Stop != session.StopEndTurn {
		t.Errorf("the background act must end on a clean end_turn result, got %+v", last)
	}
}

// assertApprovalResumed verifies a permission.ask was emitted and the loop then
// produced a non-error tool.result for the approved tool (Write) — i.e. the
// approval round-trip resumed the loop rather than denying the call.
func assertApprovalResumed(t *testing.T, events []session.Event) {
	t.Helper()
	var askSeen bool
	var writeResultOK bool
	for _, ev := range events {
		switch ev.Type {
		case session.EvPermissionAsk:
			if ev.Ask == nil || ev.Ask.AskID == "" {
				t.Error("permission.ask without an AskID")
			}
			askSeen = true
		case session.EvToolResult:
			// After approval, the Write tool result should be present and succeed.
			if askSeen && ev.ToolResult != nil && !ev.ToolResult.IsError {
				writeResultOK = true
			}
		}
	}
	if !askSeen {
		t.Error("no permission.ask was emitted")
	}
	if !writeResultOK {
		t.Error("no successful tool result after approval (loop did not resume)")
	}
}

// TestFormatEventPrintsSubagentCause covers the cause= branch formatEvent added for the
// failed-delegation taxonomy (issue #319). mecademo is the runnable example a library
// consumer copies to learn the event surface, so this line is documentation with a
// compiler — and it had no oracle: the demo's offline scenarios never fail a child, so the
// branch was reachable only in production.
//
// Both halves matter. A failed child must PRINT the why, and a clean child must not gain a
// spurious empty cause= (the field is empty on every non-StopError terminal, and printing
// `cause=""` on a healthy run is noise a consumer would copy).
func TestFormatEventPrintsSubagentCause(t *testing.T) {
	failed := formatEvent(session.Event{
		Type: session.EvSubagentEnd,
		Subagent: &session.SubagentPayload{
			ChildID: "subagent-c1", Stop: session.StopError,
			Cause: "upstream 503: model overloaded",
		},
	})
	if !strings.Contains(failed, `cause="upstream 503: model overloaded"`) {
		t.Errorf("a failed subagent.end must print the cause, got %q", failed)
	}
	if !strings.Contains(failed, "child=subagent-c1") || !strings.Contains(failed, "stop=error") {
		t.Errorf("the pre-existing child/stop fields must be unchanged, got %q", failed)
	}

	clean := formatEvent(session.Event{
		Type:     session.EvSubagentEnd,
		Subagent: &session.SubagentPayload{ChildID: "subagent-c2", Stop: session.StopEndTurn},
	})
	if strings.Contains(clean, "cause=") {
		t.Errorf("a clean subagent.end must print no cause= at all, got %q", clean)
	}
}
