package mcpbrokergrpc

import (
	"context"
	"errors"
	"sync"
	"unicode/utf8"

	"google.golang.org/grpc"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

// Client implements mcpbroker.Service over the generated RPC client.
type Client struct {
	rpc        brokerv1.BrokerServiceClient
	cfg        Config
	mu         sync.Mutex
	instanceID string
}

// NewClient constructs a client with finite default deadlines.
func NewClient(conn grpc.ClientConnInterface) *Client {
	client, _ := NewClientWithConfig(conn, DefaultConfig())
	return client
}

// NewClientWithConfig constructs a client whose every RPC has a finite deadline.
func NewClientWithConfig(conn grpc.ClientConnInterface, cfg Config) (*Client, error) {
	if conn == nil {
		return nil, errors.New("mcpbrokergrpc: connection is required")
	}
	if !cfg.valid() {
		return nil, errors.New("mcpbrokergrpc: all deadlines and capacities must be positive")
	}
	return &Client{rpc: brokerv1.NewBrokerServiceClient(conn), cfg: cfg}, nil
}

func (c *Client) brokerInstanceID() string { c.mu.Lock(); defer c.mu.Unlock(); return c.instanceID }

// AttachSession opens a handle while pinning the first observed broker instance ID.
func (c *Client) AttachSession(ctx context.Context, id session.SessionID) (mcpbroker.SessionHandle, mcpbroker.AttachOutcome, error) {
	if id == "" {
		return nil, "", errors.New("mcpbrokergrpc: session id is required")
	}
	expected := c.brokerInstanceID()
	rpcCtx, cancel := context.WithTimeout(ctx, c.cfg.RPCDeadline)
	defer cancel()
	r, e := c.rpc.Attach(rpcCtx, &brokerv1.AttachRequest{SessionId: string(id), BrokerIncarnation: expected})
	if e != nil {
		return nil, "", clientError(e)
	}
	return c.attachResponse(r, false, expected)
}

// AttachSessionExpectedBinding reattaches only an exact persisted binding. It does
// not create state and intentionally omits the client's instance pin so the
// issuing runtime can classify a replacement before allocating a handle.
func (c *Client) AttachSessionExpectedBinding(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (mcpbroker.SessionHandle, mcpbroker.AttachOutcome, error) {
	if id == "" || binding == "" {
		return nil, "", errors.New("mcpbrokergrpc: session id and expected binding are required")
	}
	expected := c.brokerInstanceID()
	rpcCtx, cancel := context.WithTimeout(ctx, c.cfg.RPCDeadline)
	defer cancel()
	r, err := c.rpc.Attach(rpcCtx, &brokerv1.AttachRequest{SessionId: string(id), ExpectedBinding: string(binding)})
	if err != nil {
		return nil, "", clientError(err)
	}
	return c.attachResponse(r, true, expected)
}

func (c *Client) attachResponse(r *brokerv1.AttachResponse, expectedBinding bool, expected string) (mcpbroker.SessionHandle, mcpbroker.AttachOutcome, error) {
	if r.GetHandle() == "" || r.GetBinding() == "" || r.GetBrokerIncarnation() == "" || !validAttachOutcome(r.GetOutcome()) {
		return nil, "", errors.New("mcpbrokergrpc: malformed attach response")
	}
	outcome := mcpbroker.AttachOutcome(r.GetOutcome())
	if expectedBinding && outcome != mcpbroker.AttachReattached && outcome != mcpbroker.AttachRecoveredProvisional {
		c.discardAttachResponse(r)
		return nil, "", errors.New("mcpbrokergrpc: invalid expected-binding attach outcome")
	}
	tools, e := remoteTools(c, r)
	if e != nil {
		c.discardAttachResponse(r)
		return nil, "", e
	}
	base := &clientSessionHandle{client: c, handle: r.GetHandle(), binding: session.ExternalBinding(r.GetBinding()), instanceID: r.GetBrokerIncarnation(), tools: tools, continuity: r.GetCredentialContinuity()}
	c.mu.Lock()
	current := c.instanceID
	if expectedBinding {
		// Expected-binding and recovery responses may adopt a replacement only if
		// the client instance has not changed since this operation started. A
		// concurrent, different replacement fails closed; the same response is an
		// idempotent adoption.
		if current != expected && current != r.GetBrokerIncarnation() {
			c.mu.Unlock()
			c.discardAttachResponse(r)
			return nil, "", errors.Join(mcpbroker.ErrStateUnavailable, mcpbroker.ErrBrokerIncarnationLost)
		}
	} else if current != "" && current != r.GetBrokerIncarnation() {
		c.mu.Unlock()
		c.discardAttachResponse(r)
		return nil, "", errors.Join(mcpbroker.ErrStateUnavailable, mcpbroker.ErrBrokerIncarnationLost)
	}
	c.instanceID = r.GetBrokerIncarnation()
	c.mu.Unlock()
	if r.GetWorkspaceEnrollment() {
		return &clientEnrollmentSessionHandle{clientSessionHandle: base}, mcpbroker.AttachOutcome(r.GetOutcome()), nil
	}
	return base, mcpbroker.AttachOutcome(r.GetOutcome()), nil
}
func (c *Client) discardAttachResponse(response *brokerv1.AttachResponse) {
	handle := &clientSessionHandle{
		client:     c,
		handle:     response.GetHandle(),
		instanceID: response.GetBrokerIncarnation(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.RPCDeadline)
	defer cancel()
	if response.GetOutcome() == string(mcpbroker.AttachCreated) || response.GetOutcome() == string(mcpbroker.AttachRecoveredProvisional) {
		_ = handle.Abort(ctx)
		return
	}
	_, _ = handle.Close(ctx)
}

// DeleteSession is unavailable remotely because remote deletion requires an exact
// persisted binding. Local callers retain the unbound Service compatibility path.
func (*Client) DeleteSession(context.Context, session.SessionID) (mcpbroker.DeleteOutcome, error) {
	return "", errors.New("mcpbrokergrpc: remote deletion requires a binding")
}

// DeleteSessionIfBinding atomically deletes only the exact opaque binding.
func (c *Client) DeleteSessionIfBinding(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (mcpbroker.DeleteOutcome, error) {
	if binding == "" {
		return "", errors.New("mcpbrokergrpc: binding is required")
	}
	return c.delete(ctx, id, binding)
}
func (c *Client) delete(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (mcpbroker.DeleteOutcome, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, c.cfg.RPCDeadline)
	defer cancel()
	r, e := c.rpc.Delete(rpcCtx, &brokerv1.DeleteRequest{SessionId: string(id), Binding: string(binding), BrokerIncarnation: c.brokerInstanceID()})
	if e != nil {
		return "", clientError(e)
	}
	if r.GetOutcome() != string(mcpbroker.DeleteDeleted) && r.GetOutcome() != string(mcpbroker.DeleteNotFound) {
		return "", errors.New("mcpbrokergrpc: invalid delete outcome")
	}
	return mcpbroker.DeleteOutcome(r.GetOutcome()), nil
}

type clientSessionHandle struct {
	client     *Client
	handle     string
	binding    session.ExternalBinding
	instanceID string
	tools      []tool.Tool
	// continuity is the broker's per-attachment credential-continuity offer.
	continuity bool
	mu         sync.Mutex
	closed     bool
}

func (a *clientSessionHandle) Binding() session.ExternalBinding { return a.binding }
func (a *clientSessionHandle) Tools() []tool.Tool               { return append([]tool.Tool(nil), a.tools...) }
func (a *clientSessionHandle) Commit(ctx context.Context) error {
	rpcCtx, cancel := context.WithTimeout(ctx, a.client.cfg.RPCDeadline)
	defer cancel()
	_, e := a.client.rpc.Commit(rpcCtx, &brokerv1.CommitRequest{Handle: a.handle, BrokerIncarnation: a.instanceID})
	return clientError(e)
}
func (a *clientSessionHandle) Abort(ctx context.Context) error {
	rpcCtx, cancel := context.WithTimeout(ctx, a.client.cfg.RPCDeadline)
	defer cancel()
	_, e := a.client.rpc.Abort(rpcCtx, &brokerv1.AbortRequest{Handle: a.handle, BrokerIncarnation: a.instanceID})
	return clientError(e)
}
func (a *clientSessionHandle) Close(ctx context.Context) (mcpbroker.CloseOutcome, error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return mcpbroker.CloseAlreadyClosed, nil
	}
	a.mu.Unlock()
	rpcCtx, cancel := context.WithTimeout(ctx, a.client.cfg.RPCDeadline)
	defer cancel()
	r, e := a.client.rpc.Close(rpcCtx, &brokerv1.CloseRequest{Handle: a.handle, BrokerIncarnation: a.instanceID})
	if e != nil {
		return "", clientError(e)
	}
	if r.GetOutcome() != string(mcpbroker.CloseClosed) && r.GetOutcome() != string(mcpbroker.CloseAlreadyClosed) {
		return "", errors.New("mcpbrokergrpc: invalid close outcome")
	}
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	return mcpbroker.CloseOutcome(r.GetOutcome()), nil
}
func (a *clientSessionHandle) PresentAuthorization(ctx context.Context, auth session.ExternalAuthorization) (string, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, a.client.cfg.RPCDeadline)
	defer cancel()
	r, err := a.client.rpc.PresentAuthorization(rpcCtx, &brokerv1.PresentAuthorizationRequest{Handle: a.handle, Authorization: authToWire(auth), BrokerIncarnation: a.instanceID})
	if err != nil {
		return "", clientError(err)
	}
	if r.GetUrl() == "" || !utf8.ValidString(r.GetUrl()) {
		return "", errors.New("mcpbrokergrpc: malformed presentation response")
	}
	return r.GetUrl(), nil
}
func (a *clientSessionHandle) AuthorizationStatus(ctx context.Context, auth session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, a.client.cfg.RPCDeadline)
	defer cancel()
	r, err := a.client.rpc.AuthorizationStatus(rpcCtx, &brokerv1.AuthorizationStatusRequest{Handle: a.handle, Authorization: authToWire(auth), BrokerIncarnation: a.instanceID})
	if err != nil {
		return "", clientError(err)
	}
	out := session.AuthorizationStatus(r.GetStatus())
	if !validAuthorizationStatus(out) {
		return "", errors.New("mcpbrokergrpc: invalid authorization status")
	}
	return out, nil
}
func (a *clientSessionHandle) CancelAuthorization(ctx context.Context, auth session.ExternalAuthorization) (mcpbroker.CancelOutcome, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, a.client.cfg.RPCDeadline)
	defer cancel()
	r, err := a.client.rpc.CancelAuthorization(rpcCtx, &brokerv1.CancelAuthorizationRequest{Handle: a.handle, Authorization: authToWire(auth), BrokerIncarnation: a.instanceID})
	if err != nil {
		return "", clientError(err)
	}
	out := mcpbroker.CancelOutcome(r.GetOutcome())
	if !validCancelOutcome(out) {
		return "", errors.New("mcpbrokergrpc: invalid cancel outcome")
	}
	return out, nil
}

type clientEnrollmentSessionHandle struct{ *clientSessionHandle }

func (a *clientEnrollmentSessionHandle) BeginWorkspaceEnrollment(ctx context.Context) (mcpbroker.WorkspaceEnrollmentPresentation, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, a.client.cfg.RPCDeadline)
	defer cancel()
	r, err := a.client.rpc.BeginWorkspaceEnrollment(rpcCtx, &brokerv1.BeginWorkspaceEnrollmentRequest{Handle: a.handle, BrokerIncarnation: a.instanceID})
	if err != nil {
		return mcpbroker.WorkspaceEnrollmentPresentation{}, clientError(err)
	}
	ref, err := workspaceRefFromWire(r.GetRef())
	if err != nil {
		return mcpbroker.WorkspaceEnrollmentPresentation{}, err
	}
	out := mcpbroker.WorkspaceEnrollmentPresentation{Ref: ref, URL: r.GetUrl()}
	if !out.Valid() {
		return mcpbroker.WorkspaceEnrollmentPresentation{}, errors.New("mcpbrokergrpc: malformed workspace presentation")
	}
	return out, nil
}
func (a *clientEnrollmentSessionHandle) ObserveWorkspaceEnrollment(ctx context.Context, ref mcpbroker.WorkspaceEnrollmentRef) (mcpbroker.WorkspaceEnrollmentResult, error) {
	return a.workspaceResult(ctx, ref, false)
}
func (a *clientEnrollmentSessionHandle) CancelWorkspaceEnrollment(ctx context.Context, ref mcpbroker.WorkspaceEnrollmentRef) (mcpbroker.WorkspaceEnrollmentResult, error) {
	return a.workspaceResult(ctx, ref, true)
}
func (a *clientEnrollmentSessionHandle) workspaceResult(ctx context.Context, ref mcpbroker.WorkspaceEnrollmentRef, cancelOperation bool) (mcpbroker.WorkspaceEnrollmentResult, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, a.client.cfg.RPCDeadline)
	defer cancel()
	var r workspaceResultResponse
	var err error
	if cancelOperation {
		r, err = a.client.rpc.CancelWorkspaceEnrollment(rpcCtx, &brokerv1.CancelWorkspaceEnrollmentRequest{Handle: a.handle, Ref: workspaceRefToWire(ref), BrokerIncarnation: a.instanceID})
	} else {
		r, err = a.client.rpc.ObserveWorkspaceEnrollment(rpcCtx, &brokerv1.ObserveWorkspaceEnrollmentRequest{Handle: a.handle, Ref: workspaceRefToWire(ref), BrokerIncarnation: a.instanceID})
	}
	if err != nil {
		return mcpbroker.WorkspaceEnrollmentResult{}, clientError(err)
	}
	return workspaceResultFromWire(a.client, a.handle, a.instanceID, r)
}

var _ mcpbroker.Service = (*Client)(nil)
var _ mcpbroker.BindingSessionDeleter = (*Client)(nil)
var _ mcpbroker.ExpectedBindingAttacher = (*Client)(nil)
