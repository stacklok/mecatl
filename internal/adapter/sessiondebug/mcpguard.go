package sessiondebug

import (
	"context"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// BindSelectedMCP wraps one selected direct MCP tool with a final target-
// incarnation check at execution time, after any interactive approval wait.
func BindSelectedMCP(candidate tool.Tool, store port.SessionStore, target session.SessionID, expectedFingerprint string, expectedOwner *session.Principal, ownershipEnforced bool) tool.Tool {
	return &targetBoundMCP{
		Tool: candidate, store: store, target: target, expectedFingerprint: expectedFingerprint,
		expectedOwnerScope: session.PrincipalScopeHash(expectedOwner), ownershipEnforced: ownershipEnforced,
	}
}

type targetBoundMCP struct {
	tool.Tool
	store               port.SessionStore
	target              session.SessionID
	expectedFingerprint string
	expectedOwnerScope  [32]byte
	ownershipEnforced   bool
}

func (t *targetBoundMCP) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	current, err := t.store.Load(ctx, t.target)
	principal := session.PrincipalFromContext(ctx)
	if err != nil || current == nil || current.ID != t.target ||
		session.DebugTargetFingerprint(current) != t.expectedFingerprint ||
		t.ownershipEnforced && (session.PrincipalScopeHash(current.Owner) != t.expectedOwnerScope || principal == nil || session.PrincipalScopeHash(principal) != t.expectedOwnerScope) {
		return session.NewToolError(call.ID, "debug target is stale or inaccessible"), nil
	}
	return t.Tool.Execute(ctx, call, env)
}

var _ tool.Tool = (*targetBoundMCP)(nil)
