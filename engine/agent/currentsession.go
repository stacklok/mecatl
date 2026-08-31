package agent

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// CurrentSessionToolName is the catalog name of the current-session identity tool.
const CurrentSessionToolName = "CurrentSession"

type currentSessionTool struct{}

// NewCurrentSessionTool constructs a read-only tool that reports the running agent's session ID.
func NewCurrentSessionTool() tool.Tool { return &currentSessionTool{} }

func (*currentSessionTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: CurrentSessionToolName,
		Description: "Returns this running agent's exact session ID. Use it for correlation and debugging, " +
			"including supplying the ID to `mecatui debug`. The ID is an opaque reference that grants no authority.",
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}
}

func (*currentSessionTool) ReadOnly() bool { return true }

func (*currentSessionTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	id, ok := port.SessionIDFromContext(ctx)
	if !ok || id == "" || !utf8.ValidString(string(id)) {
		return session.NewToolError(call.ID, "CurrentSession: running session ID is unavailable"), nil
	}
	return session.NewToolResult(call.ID, string(id)), nil
}

var _ tool.Tool = (*currentSessionTool)(nil)
