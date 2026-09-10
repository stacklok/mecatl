package cliconfig

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func boolPtr(b bool) *bool { return &b }

func TestResolveProductMetricsEnabledPrecedence(t *testing.T) {
	getenvSet := func(string) string { return "1" }
	getenvUnset := func(string) string { return "" }

	cases := []struct {
		name string
		p    ProductMetricsPrecedence
		want bool
	}{
		{"flag true wins over everything", ProductMetricsPrecedence{FlagSet: true, FlagValue: true, Getenv: getenvSet, SettingsEnabled: boolPtr(false)}, true},
		{"flag false wins over everything", ProductMetricsPrecedence{FlagSet: true, FlagValue: false, Getenv: getenvUnset, SettingsEnabled: boolPtr(true)}, false},
		{"DO_NOT_TRACK disables when no flag", ProductMetricsPrecedence{Getenv: getenvSet, SettingsEnabled: boolPtr(true)}, false},
		{"settings.yaml honoured when no flag/env", ProductMetricsPrecedence{Getenv: getenvUnset, SettingsEnabled: boolPtr(false)}, false},
		{"default enabled when nothing set", ProductMetricsPrecedence{Getenv: getenvUnset, SettingsEnabled: nil}, true},
		{"DO_NOT_TRACK=0 is not an opt-out", ProductMetricsPrecedence{Getenv: func(string) string { return "0" }, SettingsEnabled: boolPtr(true)}, true},
		{"DO_NOT_TRACK=false is not an opt-out", ProductMetricsPrecedence{Getenv: func(string) string { return "false" }, SettingsEnabled: boolPtr(true)}, true},
		{"DO_NOT_TRACK=true disables", ProductMetricsPrecedence{Getenv: func(string) string { return "true" }, SettingsEnabled: boolPtr(true)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveProductMetricsEnabled(tc.p); got != tc.want {
				t.Errorf("ResolveProductMetricsEnabled(%+v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

func TestTeeToolCallRecorderCallsEveryNonNilRecorder(t *testing.T) {
	var calls []string
	rec := func(name string) port.ToolCallRecorder {
		return recorderFunc(func(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration) {
			calls = append(calls, name)
		})
	}
	tee := TeeToolCallRecorder(rec("a"), nil, rec("b"))
	tee.ToolCall(session.SessionID(""), session.ToolCall{}, session.ToolResult{}, 0, 0)

	if len(calls) != 2 || calls[0] != "a" || calls[1] != "b" {
		t.Errorf("calls = %v, want [a b] (nil skipped, order preserved)", calls)
	}
}

// recorderFunc adapts a plain func to port.ToolCallRecorder for this test.
type recorderFunc func(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration)

func (f recorderFunc) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	f(id, call, result, queued, took)
}

// runAwareRecorderFunc additionally implements port.RunAwareToolCallRecorder,
// so tests can distinguish which method a caller actually invoked.
type runAwareRecorderFunc struct {
	plain    func(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration)
	runAware func(string, session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration)
}

func (f runAwareRecorderFunc) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	f.plain(id, call, result, queued, took)
}

func (f runAwareRecorderFunc) ToolCallForRun(runID string, id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	f.runAware(runID, id, call, result, queued, took)
}

// TestTeeToolCallRecorderForwardsRunAwareCapability pins the bug found in the
// final whole-branch review of the had_tool_call/tool_calls_per_run/
// time_to_first_value work: TeeToolCallRecorder's returned value is what
// lands in Deps.ToolCallRecorder, so if it implemented only the base
// port.ToolCallRecorder, the engine's dispatch.go type-assertion for
// port.RunAwareToolCallRecorder would ALWAYS fail — silently making
// had_tool_call/tool_calls_per_run/time_to_first_value inert in every real
// binary, despite productmetrics.Recorder itself correctly implementing the
// richer interface. This test drives the composed value exactly the way
// dispatch.go does: type-assert, then call ToolCallForRun if it succeeds.
func TestTeeToolCallRecorderForwardsRunAwareCapability(t *testing.T) {
	var runAwareCalls []string
	var plainCalls []string

	runAware := runAwareRecorderFunc{
		plain: func(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration) {
			plainCalls = append(plainCalls, "runaware-recorder-plain")
		},
		runAware: func(runID string, _ session.SessionID, _ session.ToolCall, _ session.ToolResult, _, _ time.Duration) {
			runAwareCalls = append(runAwareCalls, runID)
		},
	}
	baseOnly := recorderFunc(func(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration) {
		plainCalls = append(plainCalls, "base-only-recorder")
	})

	tee := TeeToolCallRecorder(baseOnly, runAware)

	aware, ok := tee.(port.RunAwareToolCallRecorder)
	if !ok {
		t.Fatal("TeeToolCallRecorder's result does not implement port.RunAwareToolCallRecorder — had_tool_call/tool_calls_per_run/time_to_first_value would be inert in production")
	}
	aware.ToolCallForRun("run-1", session.SessionID(""), session.ToolCall{}, session.ToolResult{}, 0, 0)

	if len(runAwareCalls) != 1 || runAwareCalls[0] != "run-1" {
		t.Errorf("runAwareCalls = %v, want the run-aware element to receive ToolCallForRun with runID %q", runAwareCalls, "run-1")
	}
	if len(plainCalls) != 1 || plainCalls[0] != "base-only-recorder" {
		t.Errorf("plainCalls = %v, want the base-only element to fall back to ToolCall exactly once", plainCalls)
	}
}
