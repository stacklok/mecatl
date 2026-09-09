package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcp/jq"
)

type callMcpWithQueryArgs struct {
	Server   string          `json:"server"`
	Tool     string          `json:"tool"`
	Args     json.RawMessage `json:"args"`
	JQFilter string          `json:"jq_filter"`
}

// attachmentQueryTool performs both the native broker call and the jq projection
// while its attachment operation remains live. The unfiltered response therefore
// never leaves the attachment as a model-facing ToolResult.
type attachmentQueryTool struct{ attachment *Attachment }

var _ tool.AuthorizationRequester = (*attachmentQueryTool)(nil)

func (*attachmentQueryTool) Spec() tool.ToolSpec { return mcpadapter.CallMcpWithQuerySpec() }
func (*attachmentQueryTool) ReadOnly() bool      { return true }

func (*attachmentQueryTool) target(call session.ToolCall) (session.ToolCall, string, error) {
	var args callMcpWithQueryArgs
	if msg, ok := session.ParseArgs(call, &args); !ok {
		return session.ToolCall{}, "", errors.New(msg)
	}
	server, toolName, filter := strings.TrimSpace(args.Server), strings.TrimSpace(args.Tool), strings.TrimSpace(args.JQFilter)
	if server == "" || toolName == "" || filter == "" {
		return session.ToolCall{}, "", errors.New(`the "server", "tool", and "jq_filter" arguments are required`)
	}
	remoteArgs, msg := mcpadapter.NormalizeRemoteArgs(args.Args)
	if msg != "" {
		return session.ToolCall{}, "", errors.New(msg)
	}
	if err := jq.Validate(filter); err != nil {
		return session.ToolCall{}, "", err
	}
	name := "mcp__" + server + "__" + toolName
	if !strings.HasPrefix(name, "mcp__") || strings.Contains(server, "__") || strings.Contains(toolName, "__") {
		return session.ToolCall{}, "", errors.New("CallMcpWithQuery target is invalid")
	}
	return session.NewToolCall(call.ID, name, append(json.RawMessage(nil), remoteArgs...)), filter, nil
}

func (t *attachmentQueryTool) native(call session.ToolCall, filter string) (tool.Tool, error) {
	route, ok := t.attachment.lookupRoute(call.Name)
	if !ok {
		return nil, errors.New("broker tool route is unavailable")
	}
	base := &sessionTool{attachment: t.attachment, route: route, queryFilter: filter}
	if route.oauth != nil {
		return &protectedSessionTool{sessionTool: base}, nil
	}
	return base, nil
}

func (t *attachmentQueryTool) RequestAuthorization(ctx context.Context, call session.ToolCall) (session.ExternalAuthorization, bool, error) {
	if err := ctx.Err(); err != nil {
		return session.ExternalAuthorization{}, false, err
	}
	native, filter, err := t.target(call)
	if err != nil {
		return session.ExternalAuthorization{}, false, err
	}
	target, err := t.native(native, filter)
	if err != nil {
		return session.ExternalAuthorization{}, false, err
	}
	requester, ok := target.(tool.AuthorizationRequester)
	if !ok {
		return session.ExternalAuthorization{}, false, nil
	}
	return requester.RequestAuthorization(ctx, native)
}

func (t *attachmentQueryTool) AbortAuthorization(ctx context.Context, authorization session.ExternalAuthorization) error {
	_, err := t.attachment.CancelAuthorization(ctx, authorization)
	return err
}

func (t *attachmentQueryTool) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return session.ToolResult{}, err
	}
	native, filter, err := t.target(call)
	if err != nil {
		return session.NewToolError(call.ID, fmt.Sprintf("CallMcpWithQuery: %v", err)), nil
	}
	target, err := t.native(native, filter)
	if err != nil {
		return session.NewToolError(call.ID, fmt.Sprintf("CallMcpWithQuery: %v", err)), nil
	}
	result, err := target.Execute(ctx, native, env)
	if err != nil {
		// The protected route claims before transport; never retry an uncertain call.
		return session.NewToolError(call.ID, "CallMcpWithQuery: target may have succeeded; automatic replay refused after a query transport or projection failure"), nil
	}
	result.CallID = call.ID
	return result, nil
}

// CallMcpWithQueryTool exposes the attachment-bound query wrapper to composition.
// Nil means the attachment has no currently eligible frozen routes.
func (a *Attachment) CallMcpWithQueryTool() tool.Tool {
	if a == nil || a.runtime.queryCaller == nil || len(a.Tools()) == 0 {
		return nil
	}
	return &attachmentQueryTool{attachment: a}
}
