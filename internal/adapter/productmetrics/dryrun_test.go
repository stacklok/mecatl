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
