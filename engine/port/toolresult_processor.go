package port

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
)

// ToolResultProcessor rewrites an effective tool result before it reaches
// audit, events, or conversation history. A failed rewrite must be treated as
// a tool error; callers must not retain the input result.
type ToolResultProcessor interface {
	ProcessToolResult(context.Context, session.SessionID, session.ToolResult) (session.ToolResult, error)
}
