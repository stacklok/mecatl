package productmetrics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type capturingDiag struct {
	lines []string
	args  [][]any
}

func (c *capturingDiag) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	c.lines = append(c.lines, msg)
	c.args = append(c.args, args)
}
func (c *capturingDiag) With(...any) port.Diagnostics { return c }

func TestDryRunRecorderLogsInsteadOfExporting(t *testing.T) {
	diag := &capturingDiag{}
	r := NewDryRunRecorder(diag)

	r.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	r.ToolCall(session.SessionID("s"), session.ToolCall{Name: "sensitive-name"}, session.ToolResult{Content: "sensitive-content"}, 0, time.Millisecond)

	if len(diag.lines) != 2 {
		t.Fatalf("got %d logged lines, want 2: %v", len(diag.lines), diag.lines)
	}
	for i, line := range diag.lines {
		if strings.Contains(line, "sensitive") {
			t.Errorf("dry-run log line leaked sensitive content: %q", line)
		}
		for _, a := range diag.args[i] {
			if s, ok := a.(string); ok && strings.Contains(s, "sensitive") {
				t.Errorf("dry-run log args leaked sensitive content: %v", diag.args[i])
			}
		}
	}
}

// TestDryRunRecorderImplementsPorts pins the compile-time interface guards
// (var _ port.EventSink = ...) via an explicit assignment, so a signature
// drift on either port fails this test with a clear message rather than only
// the package-level var block.
func TestDryRunRecorderImplementsPorts(_ *testing.T) {
	diag := &capturingDiag{}
	r := NewDryRunRecorder(diag)
	var _ port.EventSink = r
	var _ port.ToolCallRecorder = r
	var _ port.RunAwareToolCallRecorder = r
}

// TestDryRunRecorderMirrorsRecorderToolCallAttributes pins the lockstep
// contract: the audit path must log the SAME bounded attributes Recorder
// attaches (an audit surface that understates what is sent defeats its own
// purpose), and the category must still be the closed-set projection — never
// the raw name, never an MCP server/tool name.
func TestDryRunRecorderMirrorsRecorderToolCallAttributes(t *testing.T) {
	diag := &capturingDiag{}
	r := NewDryRunRecorder(diag)

	r.ToolCallForRun("run-1", session.SessionID("s"),
		session.ToolCall{Name: "mcp__evilserver__leak_this_name"},
		session.ToolResult{IsError: true}, 0, 0)
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult, RunID: "run-1",
		Result: &session.ResultPayload{Stop: session.StopError},
	})

	if len(diag.args) != 2 {
		t.Fatalf("got %d logged lines, want 2: %v", len(diag.lines), diag.lines)
	}
	if !hasArg(diag.args[0], attrCategory, categoryMCP) {
		t.Errorf("tool_calls dry-run log args = %v, want %s=%s", diag.args[0], attrCategory, categoryMCP)
	}
	if !hasArg(diag.args[0], attrOutcome, outcomeError) {
		t.Errorf("tool_calls dry-run log args = %v, want %s=%s", diag.args[0], attrOutcome, outcomeError)
	}
	if !hasArg(diag.args[1], attrHadToolCall, false) {
		t.Errorf("runs_completed dry-run log args = %v, want %s=false (the only tool call errored)", diag.args[1], attrHadToolCall)
	}
	for _, args := range diag.args {
		for _, a := range args {
			if s, ok := a.(string); ok && (strings.Contains(s, "evilserver") || strings.Contains(s, "leak_this_name")) {
				t.Errorf("dry-run log leaked an MCP server/tool name: %v", args)
			}
		}
	}
}

// hasArg reports whether a Diagnostics key/value arg slice carries key=want.
func hasArg(args []any, key string, want any) bool {
	for i := 0; i+1 < len(args); i += 2 {
		if k, ok := args[i].(string); ok && k == key && args[i+1] == want {
			return true
		}
	}
	return false
}

// TestDryRunRecorderResultNeverLeaksFreeText covers the EvResult branch (not
// exercised by the brief's original test) with a non-nil ResultPayload,
// asserting the log carries only the bounded stop/token fields and never the
// free-text Text/Error fields on ResultPayload.
func TestDryRunRecorderResultNeverLeaksFreeText(t *testing.T) {
	diag := &capturingDiag{}
	r := NewDryRunRecorder(diag)

	r.Emit(context.Background(), session.Event{
		Type: session.EvResult,
		Result: &session.ResultPayload{
			Stop: session.StopEndTurn,
			Text: "sensitive final answer text",
			Usage: session.Usage{
				InputTokens:  10,
				OutputTokens: 20,
			},
		},
	})

	if len(diag.lines) != 1 {
		t.Fatalf("got %d logged lines, want 1: %v", len(diag.lines), diag.lines)
	}
	for _, a := range diag.args[0] {
		if s, ok := a.(string); ok && strings.Contains(s, "sensitive") {
			t.Errorf("dry-run log args leaked ResultPayload.Text: %v", diag.args[0])
		}
	}
}

func TestDryRunRecorderHeartbeatLogsOnlyEnums(t *testing.T) {
	diag := &capturingDiag{}
	r := NewDryRunRecorder(diag)

	r.Heartbeat(FeatureSnapshot{
		Memory:     true,
		Guardrails: false,
		MCP:        true,
		Scheduling: false,
		Provider:   ProviderAnthropic,
		Mode:       ModeInteractive,
	})

	if len(diag.lines) != 1 {
		t.Fatalf("got %d logged lines, want 1: %v", len(diag.lines), diag.lines)
	}
}
