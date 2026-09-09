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
