package server

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// runAwareRecorderFunc implements BOTH port.ToolCallRecorder and
// port.RunAwareToolCallRecorder, recording which method was actually called.
type runAwareRecorderFunc struct {
	plainCalls    *int
	runAwareCalls *[]string
}

func (f runAwareRecorderFunc) ToolCall(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration) {
	*f.plainCalls++
}

func (f runAwareRecorderFunc) ToolCallForRun(runID string, _ session.SessionID, _ session.ToolCall, _ session.ToolResult, _, _ time.Duration) {
	*f.runAwareCalls = append(*f.runAwareCalls, runID)
}

// TestCapabilityToolCallRecorderForwardsRunAwareCapability pins the bug found
// in the final whole-branch review of the had_tool_call/tool_calls_per_run/
// time_to_first_value work: GuardToolCallRecorder's returned value is what
// lands in Deps.ToolCallRecorder, so if it implemented only the base
// port.ToolCallRecorder, the engine's dispatch.go type-assertion for
// port.RunAwareToolCallRecorder would ALWAYS fail — silently making
// had_tool_call/tool_calls_per_run/time_to_first_value inert in every real
// binary, despite the wrapped recorder correctly implementing the richer
// interface. This test drives the guarded value exactly the way dispatch.go
// does: type-assert, then call ToolCallForRun if it succeeds.
func TestCapabilityToolCallRecorderForwardsRunAwareCapability(t *testing.T) {
	plainCalls := 0
	var runAwareCalls []string
	next := runAwareRecorderFunc{plainCalls: &plainCalls, runAwareCalls: &runAwareCalls}

	mc := NewSessionMutationCapability(false) // disabled gate: allows(id) always true
	guarded := mc.GuardToolCallRecorder(next)

	aware, ok := guarded.(port.RunAwareToolCallRecorder)
	if !ok {
		t.Fatal("GuardToolCallRecorder's result does not implement port.RunAwareToolCallRecorder — had_tool_call/tool_calls_per_run/time_to_first_value would be inert in production")
	}
	aware.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{}, session.ToolResult{}, 0, 0)

	if plainCalls != 0 {
		t.Errorf("plainCalls = %d, want 0 (the run-aware form must be preferred)", plainCalls)
	}
	if len(runAwareCalls) != 1 || runAwareCalls[0] != "run-1" {
		t.Errorf("runAwareCalls = %v, want [run-1]", runAwareCalls)
	}
}

// TestCapabilityToolCallRecorderGatesRunAwareOnAllows confirms
// ToolCallForRun respects the SAME capability.allows(id) gate ToolCall uses:
// once a session's mutation capability is invalidated, neither method should
// reach the wrapped recorder.
func TestCapabilityToolCallRecorderGatesRunAwareOnAllows(t *testing.T) {
	plainCalls := 0
	var runAwareCalls []string
	next := runAwareRecorderFunc{plainCalls: &plainCalls, runAwareCalls: &runAwareCalls}

	mc := NewSessionMutationCapability(true)
	id := session.SessionID("s")
	mc.Grant(id)
	mc.Invalidate(id)

	guarded := mc.GuardToolCallRecorder(next)
	aware, ok := guarded.(port.RunAwareToolCallRecorder)
	if !ok {
		t.Fatal("GuardToolCallRecorder's result does not implement port.RunAwareToolCallRecorder")
	}
	aware.ToolCallForRun("run-1", id, session.ToolCall{}, session.ToolResult{}, 0, 0)

	if plainCalls != 0 || len(runAwareCalls) != 0 {
		t.Errorf("plainCalls=%d runAwareCalls=%v, want both empty (invalidated capability must block ToolCallForRun same as ToolCall)", plainCalls, runAwareCalls)
	}
}
