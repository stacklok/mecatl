package mcp

import (
	"context"
	"errors"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcp/jq"
)

// QueryToolOnce invokes an advertised tool on this exact connected session,
// without reconnect/replay, and returns only its bounded jq projection.
func (s *Server) QueryToolOnce(ctx context.Context, call session.ToolCall, filter string) (session.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return session.ToolResult{}, err
	}
	if err := jq.Validate(filter); err != nil {
		return session.ToolResult{}, errors.New("invalid jq expression")
	}
	s.mu.Lock()
	conn := s.session
	closed := s.closed
	var remote string
	for _, candidate := range s.tools {
		if t, ok := candidate.(*remoteTool); ok && t.spec.Name == call.Name {
			remote = t.remoteName
			break
		}
	}
	s.mu.Unlock()
	if closed || conn == nil || remote == "" {
		return session.ToolResult{}, errors.New("query target unavailable")
	}
	args, err := argsFor(call.Args)
	if err != nil {
		return session.ToolResult{}, errors.New("invalid query arguments")
	}
	res, err := conn.CallTool(ctx, &mcpsdk.CallToolParams{Name: remote, Arguments: args})
	if err != nil {
		return session.ToolResult{}, errors.New("query transport failed")
	}
	result, err := FilterCallResult(ctx, call.ID, s.name, remote, filter, rawCallResult(s.name, remote, res))
	if err != nil || result.IsError {
		return session.ToolResult{}, errors.New("query result could not be projected within limits")
	}
	return result, nil
}
