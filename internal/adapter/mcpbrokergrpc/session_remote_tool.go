package mcpbrokergrpc

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

var errInvocationNotDispatched = errors.New("mcpbrokergrpc: invocation not dispatched")

type sessionRemoteTool struct {
	client        *SessionClient
	ref           c.SessionRef
	catalogue     c.CatalogueRef
	spec          tool.ToolSpec
	readOnly      bool
	resume        c.AuthorizationRef
	resumeAttempt c.BrokerAttempt
	resumeID      session.ToolCallID
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
func (t *sessionRemoteTool) InspectBrokerAttempt(ctx context.Context, attempt session.BrokerAttempt) (tool.BrokerAttemptStatus, error) {
	out, err := t.client.InspectAttempt(ctx, t.ref, attempt)
	return tool.BrokerAttemptStatus{Attempt: out.Attempt, Phase: out.Phase, Disposition: out.Disposition}, err
}
func (t *sessionRemoteTool) AcknowledgeBrokerAttempt(ctx context.Context, attempt session.BrokerAttempt) (tool.BrokerAttemptStatus, error) {
	out, err := t.client.AcknowledgeAttempt(ctx, t.ref, attempt)
	return tool.BrokerAttemptStatus{Attempt: out.Attempt, Phase: out.Phase, Disposition: out.Disposition}, err
}

func (t *sessionRemoteTool) Spec() tool.ToolSpec {
	s := t.spec
	s.Schema = append([]byte(nil), s.Schema...)
	return s
}
func (t *sessionRemoteTool) ReadOnly() bool { return t.readOnly }
func (t *sessionRemoteTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	if call.Name != t.spec.Name || (t.resume != "" && call.ID != t.resumeID) {
		return session.ToolResult{}, errSessionWire
	}
	attempt, ok := tool.BrokerInvocationFromContext(ctx)
	if !ok || (t.resume != "" && attempt != t.resumeAttempt) {
		return session.ToolResult{}, errSessionWire
	}
	var out c.InvocationOutcome
	var err error
	if t.resume != "" {
		out, err = t.client.ResumeTool(ctx, t.ref, t.resume, t.catalogue, attempt)
	} else {
		out, err = t.client.InvokeTool(ctx, t.ref, t.catalogue, c.Call{ID: call.ID, Name: call.Name, Arguments: call.Args}, attempt)
	}
	if out.Kind == c.InvocationOutcomeUnknown {
		return session.ToolResult{}, errors.Join(ErrSessionOutcomeUnknown, err)
	}
	if err != nil {
		return session.ToolResult{}, err
	}
	if out.Kind == c.InvocationNotDispatched {
		return session.ToolResult{}, errInvocationNotDispatched
	}
	if out.Kind != c.InvocationCompleted {
		return session.ToolResult{}, errors.New("mcpbrokergrpc: invocation not completed")
	}
	if out.Result.CallID != call.ID {
		return session.ToolResult{}, errors.Join(ErrSessionOutcomeUnknown, errSessionWire)
	}
	return *out.Result, nil
}

type sessionSerialTool struct{ *sessionRemoteTool }

func (*sessionSerialTool) DispatchSerialTool() {}

type sessionAuthorizationTool struct{ *sessionRemoteTool }

func (t *sessionAuthorizationTool) RequestAuthorization(ctx context.Context, call session.ToolCall) (session.ExternalAuthorization, bool, error) {
	if call.Name != t.spec.Name || (t.resume != "" && call.ID != t.resumeID) {
		return session.ExternalAuthorization{}, false, errSessionWire
	}
	attempt, ok := tool.BrokerInvocationFromContext(ctx)
	if !ok || (t.resume != "" && attempt != t.resumeAttempt) {
		return session.ExternalAuthorization{}, false, errSessionWire
	}
	var prepared *c.Call
	if t.resume == "" {
		prepared = &c.Call{ID: call.ID, Name: call.Name, Arguments: call.Args}
	}
	check, err := t.client.CheckAuthorization(ctx, t.ref, t.catalogue, prepared, t.resume, attempt)
	if err != nil {
		return session.ExternalAuthorization{}, false, err
	}
	if check.Ready {
		return session.ExternalAuthorization{}, false, nil
	}
	if check.Authorization == "" {
		return session.ExternalAuthorization{}, false, errors.New("mcpbrokergrpc: authorization check refused")
	}
	return session.ExternalAuthorization{ID: string(check.Authorization), Binding: session.AuthorizationBinding(t.ref), ExpiresAt: check.ExpiresAt}, true, nil
}
func (t *sessionAuthorizationTool) AbortAuthorization(ctx context.Context, a session.ExternalAuthorization) error {
	if a.Binding != session.AuthorizationBinding(t.ref) || !sessionRef(a.ID) || (t.resume != "" && a.ID != string(t.resume)) {
		return errSessionWire
	}
	attempt, ok := tool.BrokerInvocationFromContext(ctx)
	if !ok || (t.resume != "" && attempt != t.resumeAttempt) {
		return errSessionWire
	}
	_, err := t.client.CancelAuthorization(ctx, t.ref, c.AuthorizationRef(a.ID), attempt)
	return err
}

type sessionAuthorizationSerialTool struct{ *sessionAuthorizationTool }

func (*sessionAuthorizationSerialTool) DispatchSerialTool() {}
func sessionToolMarkers(base *sessionRemoteTool, auth, serial bool) tool.Tool {
	if auth {
		a := &sessionAuthorizationTool{base}
		if serial {
			return &sessionAuthorizationSerialTool{a}
		}
		return a
	}
	if serial {
		return &sessionSerialTool{base}
	}
	return base
}

// ResumeToolWrapper binds a host-adopted catalogue to the exact parked flow and
// call ID. It sends only authorization/session/catalogue references, never args.
// The host must save adoption and its unresolved-call fence before exposing this
// tool; the normal engine permission hook still governs its execution.
func (s *SessionClient) ResumeToolWrapper(ref c.SessionRef, cat c.Catalogue, name string, id session.ToolCallID, auth c.AuthorizationRef, attempt c.BrokerAttempt) (tool.Tool, error) {
	if !attempt.Valid() || cat == nil || !cat.Valid() || !validInvocationText(string(id), maxInvocationCallIDBytes) {
		return nil, errSessionWire
	}
	if err := sessionRefs(string(ref), string(cat.Ref()), string(auth)); err != nil {
		return nil, err
	}
	for _, candidate := range cat.Tools() {
		if candidate.Spec().Name != name {
			continue
		}
		_, capable := candidate.(tool.AuthorizationRequester)
		if !capable {
			return nil, errSessionWire
		}
		_, serial := candidate.(tool.DispatchSerial)
		base := &sessionRemoteTool{client: s, ref: ref, catalogue: cat.Ref(), spec: candidate.Spec(), readOnly: candidate.ReadOnly(), resume: auth, resumeID: id, resumeAttempt: attempt}
		return sessionToolMarkers(base, true, serial), nil
	}
	return nil, errSessionWire
}
