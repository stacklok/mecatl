package mcpbrokergrpc

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

var errRemoteSessionOperationUnavailable = errors.New("mcpbrokergrpc: remote session operation unavailable")

type sessionRemoteTool struct {
	spec     tool.ToolSpec
	readOnly bool
}

func (t *sessionRemoteTool) Spec() tool.ToolSpec {
	spec := t.spec
	spec.Schema = append([]byte(nil), spec.Schema...)
	return spec
}

func (t *sessionRemoteTool) ReadOnly() bool { return t.readOnly }

// Execute is deliberately fail-closed until the V3 invocation boundary exists.
func (*sessionRemoteTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.ToolResult{}, errRemoteSessionOperationUnavailable
}

type sessionSerialTool struct{ *sessionRemoteTool }

func (*sessionSerialTool) DispatchSerialTool() {}

type sessionAuthorizationTool struct{ *sessionRemoteTool }

// RequestAuthorization never implies that remote authorization is ready.
func (*sessionAuthorizationTool) RequestAuthorization(context.Context, session.ToolCall) (session.ExternalAuthorization, bool, error) {
	return session.ExternalAuthorization{}, false, errRemoteSessionOperationUnavailable
}

func (*sessionAuthorizationTool) AbortAuthorization(context.Context, session.ExternalAuthorization) error {
	return errRemoteSessionOperationUnavailable
}

type sessionAuthorizationSerialTool struct{ *sessionAuthorizationTool }

func (*sessionAuthorizationSerialTool) DispatchSerialTool() {}

func sessionToolMarkers(base *sessionRemoteTool, authorization, serial bool) tool.Tool {
	if authorization {
		requester := &sessionAuthorizationTool{base}
		if serial {
			return &sessionAuthorizationSerialTool{requester}
		}
		return requester
	}
	if serial {
		return &sessionSerialTool{base}
	}
	return base
}
