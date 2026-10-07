package mcpbrokergrpc

import (
	"context"
	"errors"
	"sync"

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

type sessionAuthorizationTool struct {
	*sessionRemoteTool
	mu       sync.Mutex
	attempts map[c.AuthorizationRef]c.BrokerAttempt
}

func (t *sessionAuthorizationTool) RequestAuthorization(ctx context.Context, call session.ToolCall) (session.ExternalAuthorization, bool, error) {
	if call.Name != t.spec.Name {
		return session.ExternalAuthorization{}, false, errSessionWire
	}
	attempt, ok := tool.BrokerInvocationFromContext(ctx)
	if !ok {
		return session.ExternalAuthorization{}, false, errSessionWire
	}
	request := c.Call{ID: call.ID, Name: call.Name, Arguments: append([]byte(nil), call.Args...)}
	check, err := t.client.CheckAuthorization(ctx, t.ref, t.catalogue, &request, "", attempt)
	if err != nil {
		return session.ExternalAuthorization{}, false, err
	}
	if check.Ready || check.Reason.Valid() {
		t.mu.Lock()
		for ref, previous := range t.attempts {
			if previous == attempt {
				delete(t.attempts, ref)
			}
		}
		t.mu.Unlock()
	}
	if check.Ready {
		return session.ExternalAuthorization{}, false, nil
	}
	if check.Authorization == "" {
		return session.ExternalAuthorization{}, false, errInvocationNotDispatched
	}
	t.mu.Lock()
	if t.attempts == nil {
		t.attempts = make(map[c.AuthorizationRef]c.BrokerAttempt)
	}
	if previous, exists := t.attempts[check.Authorization]; exists && previous != attempt {
		t.mu.Unlock()
		return session.ExternalAuthorization{}, false, errSessionWire
	}
	t.attempts[check.Authorization] = attempt
	t.mu.Unlock()
	return session.ExternalAuthorization{ID: string(check.Authorization), Binding: session.AuthorizationBinding(check.Authorization), DisplayName: t.spec.Name, ExpiresAt: check.ExpiresAt}, true, nil
}

func (t *sessionAuthorizationTool) AbortAuthorization(ctx context.Context, authorization session.ExternalAuthorization) error {
	ref := c.AuthorizationRef(authorization.ID)
	if !validSessionRef(authorization.ID) || authorization.Binding != session.AuthorizationBinding(ref) {
		return errSessionWire
	}
	t.mu.Lock()
	attempt, ok := t.attempts[ref]
	delete(t.attempts, ref)
	t.mu.Unlock()
	if !ok {
		return errRemoteSessionOperationUnavailable
	}
	_, err := t.client.CancelAuthorization(ctx, t.ref, ref, attempt)
	return err
}

func (t *sessionAuthorizationTool) BeginAuthorization(ctx context.Context, authorization session.ExternalAuthorization) (c.BrowserPrompt, error) {
	if authorization.Binding != session.AuthorizationBinding(authorization.ID) || !validSessionRef(authorization.ID) {
		return c.BrowserPrompt{}, errSessionWire
	}
	return t.client.BeginAuthorization(ctx, t.ref, c.AuthorizationRef(authorization.ID))
}

func (t *sessionAuthorizationTool) ObserveAuthorization(ctx context.Context, authorization session.ExternalAuthorization) (c.FlowStatus, error) {
	if authorization.Binding != session.AuthorizationBinding(authorization.ID) || !validSessionRef(authorization.ID) {
		return c.FlowStatus{}, errSessionWire
	}
	return t.client.ObserveAuthorization(ctx, t.ref, c.AuthorizationRef(authorization.ID))
}

func (t *sessionAuthorizationTool) ResumeTool(ctx context.Context, authorization session.ExternalAuthorization, adopted c.CatalogueRef) (c.InvocationOutcome, error) {
	ref := c.AuthorizationRef(authorization.ID)
	if authorization.Binding != session.AuthorizationBinding(ref) || !validSessionRef(string(ref)) {
		return unknownInvocation(errSessionWire)
	}
	t.mu.Lock()
	attempt, ok := t.attempts[ref]
	delete(t.attempts, ref)
	t.mu.Unlock()
	if !ok {
		return unknownInvocation(errRemoteSessionOperationUnavailable)
	}
	return t.client.ResumeTool(ctx, t.ref, ref, adopted, attempt)
}

type sessionAuthorizationSerialTool struct{ *sessionAuthorizationTool }

func (*sessionAuthorizationSerialTool) DispatchSerialTool() {}

func sessionToolMarkers(base *sessionRemoteTool, authorization, serial bool) tool.Tool {
	if authorization {
		requester := &sessionAuthorizationTool{sessionRemoteTool: base}
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
