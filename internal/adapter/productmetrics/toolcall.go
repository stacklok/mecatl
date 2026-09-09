package productmetrics

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// ToolCall records ONLY that a tool call happened — no tool name, no
// session id, no result content, no duration. It satisfies
// port.ToolCallRecorder. The three typed parameters it ignores (id, call,
// result) are accepted only because the port's signature requires them; not
// one of their fields is ever read.
func (r *Recorder) ToolCall(_ session.SessionID, _ session.ToolCall, _ session.ToolResult, _, _ time.Duration) {
	r.toolCalls.Add(context.Background(), 1)
}
