package productmetrics

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestRecorderToolCallCountsWithoutIdentity(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCall(
		session.SessionID("sensitive-session-id"),
		session.ToolCall{Name: "read_secret_file"},
		session.ToolResult{Content: "super secret content", IsError: true},
		10*time.Millisecond, 20*time.Millisecond,
	)
	r.ToolCall(session.SessionID("other"), session.ToolCall{Name: "another_tool"}, session.ToolResult{}, 0, 0)

	agg := collect(t, reader)["mecatl.adoption.tool_calls"]
	if got := sumValue(t, agg); got != 2 {
		t.Errorf("tool_calls = %d, want 2", got)
	}
}
