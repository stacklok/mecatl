package mcpbrokergrpc

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

var errRemoteSessionOperationUnavailable = errors.New("mcpbrokergrpc: remote session operation unavailable")
var errInvocationNotDispatched = errors.New("mcpbrokergrpc: invocation not dispatched")

type sessionRemoteTool struct {
	client    *SessionClient
	ref       c.SessionRef
	catalogue c.CatalogueRef
	spec      tool.ToolSpec
	readOnly  bool
}

func (t *sessionRemoteTool) BrokerInvocationDisposition(err error) session.BrokerAttemptDisposition {
	if err == nil {
		return session.BrokerAttemptCompleted
	}
	if errors.Is(err, errInvocationNotDispatched) {
		return session.BrokerAttemptNotDispatched
	}
	return session.BrokerAttemptUnknown
}

func (t *sessionRemoteTool) Spec() tool.ToolSpec {
	spec := t.spec
	spec.Schema = append([]byte(nil), spec.Schema...)
	return spec
}

func (t *sessionRemoteTool) ReadOnly() bool { return t.readOnly }

func (t *sessionRemoteTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	if call.Name != t.spec.Name {
		return session.ToolResult{}, errSessionWire
	}
	attempt, ok := tool.BrokerInvocationFromContext(ctx)
	if !ok {
		return session.ToolResult{}, errSessionWire
	}
	out, err := t.client.InvokeTool(ctx, t.ref, t.catalogue, c.Call{ID: call.ID, Name: call.Name, Arguments: call.Args}, attempt)
	if out.Kind == c.InvocationOutcomeUnknown {
		return session.ToolResult{}, errors.Join(ErrSessionOutcomeUnknown, err)
	}
	if err != nil {
		return session.ToolResult{}, err
	}
	if out.Kind == c.InvocationNotDispatched || out.Kind == c.InvocationAuthorizationRequired {
		return session.ToolResult{}, errInvocationNotDispatched
	}
	if out.Kind != c.InvocationCompleted || out.Result == nil || out.Result.CallID != call.ID {
		return session.ToolResult{}, errors.Join(ErrSessionOutcomeUnknown, errSessionWire)
	}
	return *out.Result, nil
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
