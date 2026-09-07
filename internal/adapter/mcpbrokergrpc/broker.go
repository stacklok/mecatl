// Package mcpbrokergrpc adapts the neutral internal MCP broker seam to the
// versioned mecatl.broker.v1 RPC protocol. It deliberately carries only logical
// bindings, process-local handles, frozen tool descriptors, and invocation data.
package mcpbrokergrpc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

const (
	defaultMaxHandles         = 128
	ambiguousOutcomeMessage   = "remote tool outcome is unknown because the broker response was lost; the operation may already have completed. Do not automatically repeat it. Reconcile through a safe status/read path first; if unavailable, report the uncertainty and seek operator direction."
	sessionUnavailableMessage = "tool temporarily unavailable"
	dispatchStateTrailer      = "mecatl-broker-dispatch"
	dispatchNotStarted        = "not-started"
)

// Config bounds transport calls and server-side attachment retention.
type Config struct {
	DialTimeout       time.Duration
	RPCDeadline       time.Duration
	ExecuteDeadline   time.Duration
	HandleIdleTimeout time.Duration
	SweepInterval     time.Duration
	CleanupTimeout    time.Duration
	MaxHandles        int
}

// DefaultConfig returns finite production defaults for the initial single-process broker.
func DefaultConfig() Config {
	return Config{
		DialTimeout: 5 * time.Second, RPCDeadline: 10 * time.Second,
		ExecuteDeadline: 2 * time.Minute, HandleIdleTimeout: 5 * time.Minute,
		SweepInterval: 30 * time.Second, CleanupTimeout: 10 * time.Second,
		MaxHandles: defaultMaxHandles,
	}
}

func (c Config) valid() bool {
	return c.DialTimeout > 0 && c.RPCDeadline > 0 && c.ExecuteDeadline > 0 &&
		c.HandleIdleTimeout > 0 && c.SweepInterval > 0 && c.CleanupTimeout > 0 && c.MaxHandles > 0
}

// Dial establishes a connection within Config.DialTimeout and returns a bounded client.
func Dial(ctx context.Context, target string, cfg Config, opts ...grpc.DialOption) (*Client, *grpc.ClientConn, error) {
	if !cfg.valid() {
		return nil, nil, errors.New("mcpbrokergrpc: all deadlines and capacities must be positive")
	}
	dialCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, nil, status.Error(codes.Unavailable, "broker connection unavailable")
	}
	conn.Connect()
	for conn.GetState() != connectivity.Ready {
		state := conn.GetState()
		if state == connectivity.Shutdown || !conn.WaitForStateChange(dialCtx, state) {
			_ = conn.Close()
			return nil, nil, status.Error(codes.Unavailable, "broker connection unavailable")
		}
	}
	client, err := NewClientWithConfig(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return client, conn, nil
}

// Server adapts one mcpbroker.Service incarnation to the broker RPC service.
type Server struct {
	brokerv1.UnimplementedBrokerServiceServer
	service     mcpbroker.Service
	mu          sync.Mutex
	handles     map[string]*serverAttachment
	maxHandles  int
	incarnation string
	cfg         Config
	closed      bool
	done        chan struct{}
	stop        chan struct{}
}
type lifecycleOperation uint8

const (
	lifecycleNone lifecycleOperation = iota
	lifecycleAbort
	lifecycleClose
)

type serverAttachment struct {
	attachment      mcpbroker.Attachment
	tools           map[string]tool.Tool
	active          int
	lastUsed        time.Time
	running         lifecycleOperation
	runningDone     chan struct{}
	terminal        lifecycleOperation
	terminalOutcome mcpbroker.CloseOutcome
}

// NewServer constructs a server with default deadlines and the requested handle bound.
func NewServer(service mcpbroker.Service, maxHandles int) (*Server, error) {
	cfg := DefaultConfig()
	if maxHandles > 0 {
		cfg.MaxHandles = maxHandles
	}
	return NewServerWithConfig(service, cfg)
}

// NewServerWithConfig constructs one authoritative broker-process incarnation.
func NewServerWithConfig(service mcpbroker.Service, cfg Config) (*Server, error) {
	if service == nil {
		return nil, errors.New("mcpbrokergrpc: service is required")
	}
	if !cfg.valid() {
		return nil, errors.New("mcpbrokergrpc: all deadlines and capacities must be positive")
	}
	incarnation, err := newHandle()
	if err != nil {
		return nil, fmt.Errorf("mcpbrokergrpc: mint broker incarnation: %w", err)
	}
	s := &Server{service: service, handles: make(map[string]*serverAttachment), maxHandles: cfg.MaxHandles, incarnation: incarnation, cfg: cfg, done: make(chan struct{}), stop: make(chan struct{})}
	go s.sweep()
	return s, nil
}

// RegisterServer registers an explicitly owned server so its cleanup can be joined.
func RegisterServer(reg grpc.ServiceRegistrar, server *Server) {
	brokerv1.RegisterBrokerServiceServer(reg, server)
}

func (s *Server) bounded(ctx context.Context, execute bool) (context.Context, context.CancelFunc) {
	d := s.cfg.RPCDeadline
	if execute {
		d = s.cfg.ExecuteDeadline
	}
	return context.WithTimeout(ctx, d)
}

// Attach opens a process-bound attachment handle.
func (s *Server) Attach(ctx context.Context, req *brokerv1.AttachRequest) (*brokerv1.AttachResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	if req.GetSessionId() == "" {
		return nil, invalid("session_id is required")
	}
	if err := s.checkIncarnation(req.GetBrokerIncarnation(), true); err != nil {
		return nil, err
	}
	a, outcome, err := s.service.AttachSession(ctx, session.SessionID(req.GetSessionId()))
	if err != nil {
		return nil, brokerStatus(err)
	}
	desc, tools, err := descriptors(a.Tools())
	if err != nil {
		_, _ = a.Close(context.Background())
		return nil, invalid(err.Error())
	}
	h, err := newHandle()
	if err != nil {
		_, _ = a.Close(context.Background())
		return nil, status.Error(codes.Internal, "mint attachment handle")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_, _ = a.Close(context.Background())
		return nil, status.Error(codes.Unavailable, mcpbroker.ErrStateUnavailable.Error())
	}
	if len(s.handles) >= s.maxHandles {
		_, _ = a.Close(context.Background())
		return nil, status.Error(codes.ResourceExhausted, "attachment handle capacity reached")
	}
	_, enrollment := a.(mcpbroker.WorkspaceEnrollmentAttachment)
	s.handles[h] = &serverAttachment{attachment: a, tools: tools, lastUsed: time.Now()}
	return &brokerv1.AttachResponse{Binding: string(a.Binding()), Handle: h, Outcome: string(outcome), Tools: desc, BrokerIncarnation: s.incarnation, WorkspaceEnrollment: enrollment}, nil
}

func (s *Server) checkIncarnation(got string, allowEmpty bool) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return status.Error(codes.Unavailable, mcpbroker.ErrStateUnavailable.Error())
	}
	if got == "" && allowEmpty {
		return nil
	}
	if got == "" || got != s.incarnation {
		return status.Error(codes.FailedPrecondition, "broker incarnation mismatch")
	}
	return nil
}

func (s *Server) get(incarnation, handle string) (*serverAttachment, func(), error) {
	if err := s.checkIncarnation(incarnation, false); err != nil {
		return nil, nil, err
	}
	if handle == "" {
		return nil, nil, invalid("handle is required")
	}
	s.mu.Lock()
	a := s.handles[handle]
	if a == nil || s.closed || a.running != lifecycleNone || a.terminal != lifecycleNone {
		s.mu.Unlock()
		return nil, nil, status.Error(codes.FailedPrecondition, "attachment handle unavailable")
	}
	a.active++
	a.lastUsed = time.Now()
	s.mu.Unlock()
	return a, func() {
		s.mu.Lock()
		a.active--
		a.lastUsed = time.Now()
		s.mu.Unlock()
	}, nil
}

func (s *Server) beginLifecycle(ctx context.Context, incarnation, handle string, operation lifecycleOperation) (*serverAttachment, mcpbroker.CloseOutcome, bool, error) {
	if err := s.checkIncarnation(incarnation, false); err != nil {
		return nil, "", false, err
	}
	if handle == "" {
		return nil, "", false, invalid("handle is required")
	}
	for {
		s.mu.Lock()
		a := s.handles[handle]
		if a == nil || s.closed {
			s.mu.Unlock()
			return nil, "", false, status.Error(codes.FailedPrecondition, "attachment handle unavailable")
		}
		if a.terminal != lifecycleNone {
			if a.terminal != operation {
				s.mu.Unlock()
				return nil, "", false, status.Error(codes.FailedPrecondition, "attachment handle unavailable")
			}
			a.lastUsed = time.Now()
			outcome := a.terminalOutcome
			s.mu.Unlock()
			return nil, outcome, true, nil
		}
		if a.running != lifecycleNone {
			done := a.runningDone
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, "", false, status.FromContextError(ctx.Err()).Err()
			}
		}
		a.running = operation
		a.runningDone = make(chan struct{})
		a.active++
		a.lastUsed = time.Now()
		s.mu.Unlock()
		return a, "", false, nil
	}
}

func (s *Server) finishLifecycle(a *serverAttachment, operation lifecycleOperation, outcome mcpbroker.CloseOutcome, terminal bool) {
	s.mu.Lock()
	a.active--
	if terminal {
		a.terminal = operation
		a.terminalOutcome = outcome
	}
	a.running = lifecycleNone
	done := a.runningDone
	a.runningDone = nil
	a.lastUsed = time.Now()
	close(done)
	s.mu.Unlock()
}

func (s *Server) sweep() {
	defer close(s.done)
	ticker := time.NewTicker(s.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-ticker.C:
			s.mu.Lock()
			var expired []*serverAttachment
			for handle, attachment := range s.handles {
				if attachment.active == 0 && now.Sub(attachment.lastUsed) >= s.cfg.HandleIdleTimeout {
					delete(s.handles, handle)
					if attachment.terminal == lifecycleNone {
						expired = append(expired, attachment)
					}
				}
			}
			s.mu.Unlock()
			for _, attachment := range expired {
				s.closeAttachment(attachment)
			}
		}
	}
}

func (s *Server) closeAttachment(attachment *serverAttachment) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.CleanupTimeout)
	defer cancel()
	_, _ = attachment.attachment.Close(ctx)
}

// Shutdown rejects new operations and bounds closure of every orphaned handle.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	attachments := make([]*serverAttachment, 0, len(s.handles))
	for handle, attachment := range s.handles {
		delete(s.handles, handle)
		if attachment.terminal == lifecycleNone {
			attachments = append(attachments, attachment)
		}
	}
	s.mu.Unlock()
	for _, attachment := range attachments {
		closeCtx, cancel := context.WithTimeout(ctx, s.cfg.CleanupTimeout)
		_, err := attachment.attachment.Close(closeCtx)
		cancel()
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Commit commits the provisional state behind one exact handle.
func (s *Server) Commit(ctx context.Context, req *brokerv1.HandleRequest) (*brokerv1.Empty, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, e := s.get(req.GetBrokerIncarnation(), req.GetHandle())
	if e != nil {
		return nil, e
	}
	defer release()
	if e = a.attachment.Commit(ctx); e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.Empty{}, nil
}

// Abort aborts and releases one exact handle.
func (s *Server) Abort(ctx context.Context, req *brokerv1.HandleRequest) (*brokerv1.Empty, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, _, receipt, e := s.beginLifecycle(ctx, req.GetBrokerIncarnation(), req.GetHandle(), lifecycleAbort)
	if e != nil {
		return nil, e
	}
	if receipt {
		return &brokerv1.Empty{}, nil
	}
	e = a.attachment.Abort(ctx)
	s.finishLifecycle(a, lifecycleAbort, "", e == nil)
	if e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.Empty{}, nil
}

// Close releases one exact handle without deleting logical state.
func (s *Server) Close(ctx context.Context, req *brokerv1.HandleRequest) (*brokerv1.CloseResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, out, receipt, e := s.beginLifecycle(ctx, req.GetBrokerIncarnation(), req.GetHandle(), lifecycleClose)
	if e != nil {
		return nil, e
	}
	if receipt {
		return &brokerv1.CloseResponse{Outcome: string(out)}, nil
	}
	out, e = a.attachment.Close(ctx)
	terminal := out == mcpbroker.CloseClosed || out == mcpbroker.CloseAlreadyClosed
	s.finishLifecycle(a, lifecycleClose, out, terminal)
	if e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.CloseResponse{Outcome: string(out)}, nil
}

// Delete deletes only the exact logical state in the addressed broker incarnation.
func (s *Server) Delete(ctx context.Context, req *brokerv1.DeleteRequest) (*brokerv1.DeleteResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	if req.GetSessionId() == "" {
		return nil, invalid("session_id is required")
	}
	if req.GetBinding() == "" {
		return nil, invalid("binding is required")
	}
	if err := s.checkIncarnation(req.GetBrokerIncarnation(), true); err != nil {
		return nil, err
	}
	d, ok := s.service.(mcpbroker.BindingSessionDeleter)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "binding delete is unsupported")
	}
	out, err := d.DeleteSessionIfBinding(ctx, session.SessionID(req.GetSessionId()), session.ExternalBinding(req.GetBinding()))
	if err != nil {
		return nil, brokerStatus(err)
	}
	return &brokerv1.DeleteResponse{Outcome: string(out)}, nil
}

func preDispatchError(ctx context.Context, err error) error {
	_ = grpc.SetTrailer(ctx, metadata.Pairs(dispatchStateTrailer, dispatchNotStarted))
	return err
}

// Run dispatches one tool invocation without application-level retry.
func (s *Server) Run(ctx context.Context, req *brokerv1.RunRequest) (*brokerv1.RunResponse, error) {
	ctx, cancel := s.bounded(ctx, true)
	defer cancel()
	a, release, e := s.get(req.GetBrokerIncarnation(), req.GetHandle())
	if e != nil {
		return nil, preDispatchError(ctx, e)
	}
	defer release()
	call, e := callFrom(req.GetName(), req.GetCallId(), req.GetArgs(), req.GetItemId())
	if e != nil {
		return nil, preDispatchError(ctx, e)
	}
	target := a.tools[call.Name]
	if target == nil {
		return nil, preDispatchError(ctx, invalid("unknown tool"))
	}
	result, e := target.Execute(ctx, call, tool.Environment{})
	if e != nil {
		return nil, brokerStatus(e)
	}
	wire, e := resultToWire(result)
	if e != nil {
		return nil, invalid(e.Error())
	}
	return &brokerv1.RunResponse{Result: wire}, nil
}

// RequestAuthorization begins authorization for one exact invocation.
func (s *Server) RequestAuthorization(ctx context.Context, req *brokerv1.RequestAuthorizationRequest) (*brokerv1.RequestAuthorizationResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, e := s.get(req.GetBrokerIncarnation(), req.GetHandle())
	if e != nil {
		return nil, e
	}
	defer release()
	call, e := callFrom(req.GetName(), req.GetCallId(), req.GetArgs(), req.GetItemId())
	if e != nil {
		return nil, e
	}
	target, ok := a.tools[call.Name].(tool.AuthorizationRequester)
	if !ok {
		return nil, invalid("tool is not authorization-capable")
	}
	auth, required, e := target.RequestAuthorization(ctx, call)
	if e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.RequestAuthorizationResponse{Authorization: authToWire(auth), Required: required}, nil
}

// AbortAuthorization aborts one exact tool authorization.
func (s *Server) AbortAuthorization(ctx context.Context, req *brokerv1.AbortAuthorizationRequest) (*brokerv1.Empty, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, e := s.get(req.GetBrokerIncarnation(), req.GetHandle())
	if e != nil {
		return nil, e
	}
	defer release()
	auth, e := authFromWire(req.GetAuthorization())
	if e != nil {
		return nil, e
	}
	for _, target := range a.tools {
		if requester, ok := target.(tool.AuthorizationRequester); ok {
			if e = requester.AbortAuthorization(ctx, auth); e == nil {
				return &brokerv1.Empty{}, nil
			}
		}
	}
	if e == nil {
		return nil, invalid("tool is not authorization-capable")
	}
	return nil, brokerStatus(e)
}

// PresentAuthorization returns the ephemeral URL for one exact authorization.
func (s *Server) PresentAuthorization(ctx context.Context, req *brokerv1.AuthorizationRequest) (*brokerv1.PresentationResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, err := s.get(req.GetBrokerIncarnation(), req.GetHandle())
	if err != nil {
		return nil, err
	}
	defer release()
	auth, err := authFromWire(req.GetAuthorization())
	if err != nil {
		return nil, err
	}
	url, err := a.attachment.PresentAuthorization(ctx, auth)
	if err != nil {
		return nil, brokerStatus(err)
	}
	if url == "" || !utf8.ValidString(url) {
		return nil, invalid("invalid presentation")
	}
	return &brokerv1.PresentationResponse{Url: url}, nil
}

// AuthorizationStatus observes one exact authorization.
func (s *Server) AuthorizationStatus(ctx context.Context, req *brokerv1.AuthorizationRequest) (*brokerv1.AuthorizationStatusResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, err := s.get(req.GetBrokerIncarnation(), req.GetHandle())
	if err != nil {
		return nil, err
	}
	defer release()
	auth, err := authFromWire(req.GetAuthorization())
	if err != nil {
		return nil, err
	}
	out, err := a.attachment.AuthorizationStatus(ctx, auth)
	if err != nil {
		return nil, brokerStatus(err)
	}
	if !validAuthorizationStatus(out) {
		return nil, status.Error(codes.Internal, "invalid authorization status")
	}
	return &brokerv1.AuthorizationStatusResponse{Status: string(out)}, nil
}

// CancelAuthorization cancels one exact authorization.
func (s *Server) CancelAuthorization(ctx context.Context, req *brokerv1.AuthorizationRequest) (*brokerv1.CancelResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, err := s.get(req.GetBrokerIncarnation(), req.GetHandle())
	if err != nil {
		return nil, err
	}
	defer release()
	auth, err := authFromWire(req.GetAuthorization())
	if err != nil {
		return nil, err
	}
	out, err := a.attachment.CancelAuthorization(ctx, auth)
	if err != nil {
		return nil, brokerStatus(err)
	}
	if !validCancelOutcome(out) {
		return nil, status.Error(codes.Internal, "invalid cancel outcome")
	}
	return &brokerv1.CancelResponse{Outcome: string(out)}, nil
}

// BeginWorkspaceEnrollment begins a pre-prompt enrollment.
func (s *Server) BeginWorkspaceEnrollment(ctx context.Context, req *brokerv1.HandleRequest) (*brokerv1.WorkspacePresentationResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, err := s.get(req.GetBrokerIncarnation(), req.GetHandle())
	if err != nil {
		return nil, err
	}
	defer release()
	enroller, ok := a.attachment.(mcpbroker.WorkspaceEnrollmentAttachment)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "workspace enrollment unsupported")
	}
	presentation, err := enroller.BeginWorkspaceEnrollment(ctx)
	if err != nil {
		return nil, brokerStatus(err)
	}
	if !presentation.Valid() {
		return nil, status.Error(codes.Internal, "invalid workspace presentation")
	}
	return &brokerv1.WorkspacePresentationResponse{Ref: workspaceRefToWire(presentation.Ref), Url: presentation.URL}, nil
}

// ObserveWorkspaceEnrollment observes one exact pre-prompt enrollment.
func (s *Server) ObserveWorkspaceEnrollment(ctx context.Context, req *brokerv1.WorkspaceRequest) (*brokerv1.WorkspaceResultResponse, error) {
	return s.workspaceResult(ctx, req, false)
}

// CancelWorkspaceEnrollment cancels one exact pre-prompt enrollment.
func (s *Server) CancelWorkspaceEnrollment(ctx context.Context, req *brokerv1.WorkspaceRequest) (*brokerv1.WorkspaceResultResponse, error) {
	return s.workspaceResult(ctx, req, true)
}

func (s *Server) workspaceResult(ctx context.Context, req *brokerv1.WorkspaceRequest, cancel bool) (*brokerv1.WorkspaceResultResponse, error) {
	ctx, stop := s.bounded(ctx, false)
	defer stop()
	a, release, err := s.get(req.GetBrokerIncarnation(), req.GetHandle())
	if err != nil {
		return nil, err
	}
	defer release()
	enroller, ok := a.attachment.(mcpbroker.WorkspaceEnrollmentAttachment)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "workspace enrollment unsupported")
	}
	ref, err := workspaceRefFromWire(req.GetRef())
	if err != nil {
		return nil, err
	}
	var result mcpbroker.WorkspaceEnrollmentResult
	if cancel {
		result, err = enroller.CancelWorkspaceEnrollment(ctx, ref)
	} else {
		result, err = enroller.ObserveWorkspaceEnrollment(ctx, ref)
	}
	if err != nil {
		return nil, brokerStatus(err)
	}
	if !result.Valid() {
		return nil, status.Error(codes.Internal, "invalid workspace result")
	}
	var desc []*brokerv1.ToolDescriptor
	if result.Catalogue != nil {
		desc, _, err = descriptors(result.Catalogue.Tools())
		if err != nil {
			return nil, status.Error(codes.Internal, "invalid workspace catalogue")
		}
	}
	return &brokerv1.WorkspaceResultResponse{Ref: workspaceRefToWire(result.Ref), Status: string(result.Status), Tools: desc}, nil
}

func descriptors(in []tool.Tool) ([]*brokerv1.ToolDescriptor, map[string]tool.Tool, error) {
	out := make([]*brokerv1.ToolDescriptor, 0, len(in))
	tools := make(map[string]tool.Tool, len(in))
	for _, t := range in {
		if t == nil {
			return nil, nil, errors.New("nil tool")
		}
		spec := t.Spec()
		if spec.Name == "" || !utf8.ValidString(spec.Name) || !utf8.ValidString(spec.Description) || !validJSONObject(spec.Schema) {
			return nil, nil, errors.New("invalid tool descriptor")
		}
		if _, ok := tools[spec.Name]; ok {
			return nil, nil, errors.New("duplicate tool descriptor")
		}
		_, serial := t.(tool.DispatchSerial)
		_, auth := t.(tool.AuthorizationRequester)
		out = append(out, &brokerv1.ToolDescriptor{Name: spec.Name, Description: spec.Description, Schema: append([]byte(nil), spec.Schema...), ReadOnly: t.ReadOnly(), DispatchSerial: serial, AuthorizationCapable: auth})
		tools[spec.Name] = t
	}
	return out, tools, nil
}
func validJSONObject(raw []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}
func newHandle() (string, error) {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func invalid(msg string) error { return status.Error(codes.InvalidArgument, msg) }
func brokerStatus(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, mcpbroker.ErrAttachmentClosed):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, mcpbroker.ErrStateUnavailable):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, mcpbroker.ErrAuthorizationNotFound):
		return status.Error(codes.NotFound, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// Client implements mcpbroker.Service over the generated RPC client.
type Client struct {
	rpc         brokerv1.BrokerServiceClient
	cfg         Config
	mu          sync.Mutex
	incarnation string
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

func (c *Client) bounded(ctx context.Context, execute bool) (context.Context, context.CancelFunc) {
	d := c.cfg.RPCDeadline
	if execute {
		d = c.cfg.ExecuteDeadline
	}
	return context.WithTimeout(ctx, d)
}
func (c *Client) brokerIncarnation() string { c.mu.Lock(); defer c.mu.Unlock(); return c.incarnation }

// AttachSession opens a handle while pinning the first observed broker incarnation.
func (c *Client) AttachSession(ctx context.Context, id session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	if id == "" {
		return nil, "", errors.New("mcpbrokergrpc: session id is required")
	}
	expected := c.brokerIncarnation()
	rpcCtx, cancel := c.bounded(ctx, false)
	defer cancel()
	r, e := c.rpc.Attach(rpcCtx, &brokerv1.AttachRequest{SessionId: string(id), BrokerIncarnation: expected})
	if e != nil {
		return nil, "", clientError(e)
	}
	if r.GetHandle() == "" || r.GetBinding() == "" || r.GetBrokerIncarnation() == "" || !validAttachOutcome(r.GetOutcome()) {
		return nil, "", errors.New("mcpbrokergrpc: malformed attach response")
	}
	c.mu.Lock()
	if c.incarnation != "" && c.incarnation != r.GetBrokerIncarnation() {
		c.mu.Unlock()
		return nil, "", mcpbroker.ErrStateUnavailable
	}
	c.incarnation = r.GetBrokerIncarnation()
	c.mu.Unlock()
	tools, e := remoteTools(c, r)
	if e != nil {
		return nil, "", e
	}
	base := &clientAttachment{client: c, handle: r.GetHandle(), binding: session.ExternalBinding(r.GetBinding()), incarnation: r.GetBrokerIncarnation(), tools: tools}
	if r.GetWorkspaceEnrollment() {
		return &clientEnrollmentAttachment{clientAttachment: base}, mcpbroker.AttachOutcome(r.GetOutcome()), nil
	}
	return base, mcpbroker.AttachOutcome(r.GetOutcome()), nil
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
	rpcCtx, cancel := c.bounded(ctx, false)
	defer cancel()
	r, e := c.rpc.Delete(rpcCtx, &brokerv1.DeleteRequest{SessionId: string(id), Binding: string(binding), BrokerIncarnation: c.brokerIncarnation()})
	if e != nil {
		return "", clientError(e)
	}
	if r.GetOutcome() != string(mcpbroker.DeleteDeleted) && r.GetOutcome() != string(mcpbroker.DeleteNotFound) {
		return "", errors.New("mcpbrokergrpc: invalid delete outcome")
	}
	return mcpbroker.DeleteOutcome(r.GetOutcome()), nil
}

type clientAttachment struct {
	client      *Client
	handle      string
	binding     session.ExternalBinding
	incarnation string
	tools       []tool.Tool
	mu          sync.Mutex
	closed      bool
}

func (a *clientAttachment) Binding() session.ExternalBinding { return a.binding }
func (a *clientAttachment) Tools() []tool.Tool               { return append([]tool.Tool(nil), a.tools...) }
func (a *clientAttachment) request() *brokerv1.HandleRequest {
	return &brokerv1.HandleRequest{Handle: a.handle, BrokerIncarnation: a.incarnation}
}
func (a *clientAttachment) Commit(ctx context.Context) error {
	rpcCtx, cancel := a.client.bounded(ctx, false)
	defer cancel()
	_, e := a.client.rpc.Commit(rpcCtx, a.request())
	return clientError(e)
}
func (a *clientAttachment) Abort(ctx context.Context) error {
	rpcCtx, cancel := a.client.bounded(ctx, false)
	defer cancel()
	_, e := a.client.rpc.Abort(rpcCtx, a.request())
	return clientError(e)
}
func (a *clientAttachment) Close(ctx context.Context) (mcpbroker.CloseOutcome, error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return mcpbroker.CloseAlreadyClosed, nil
	}
	a.mu.Unlock()
	rpcCtx, cancel := a.client.bounded(ctx, false)
	defer cancel()
	r, e := a.client.rpc.Close(rpcCtx, a.request())
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
func (a *clientAttachment) authRequest(auth session.ExternalAuthorization) *brokerv1.AuthorizationRequest {
	return &brokerv1.AuthorizationRequest{Handle: a.handle, Authorization: authToWire(auth), BrokerIncarnation: a.incarnation}
}
func (a *clientAttachment) PresentAuthorization(ctx context.Context, auth session.ExternalAuthorization) (string, error) {
	rpcCtx, cancel := a.client.bounded(ctx, false)
	defer cancel()
	r, err := a.client.rpc.PresentAuthorization(rpcCtx, a.authRequest(auth))
	if err != nil {
		return "", clientError(err)
	}
	if r.GetUrl() == "" || !utf8.ValidString(r.GetUrl()) {
		return "", errors.New("mcpbrokergrpc: malformed presentation response")
	}
	return r.GetUrl(), nil
}
func (a *clientAttachment) AuthorizationStatus(ctx context.Context, auth session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	rpcCtx, cancel := a.client.bounded(ctx, false)
	defer cancel()
	r, err := a.client.rpc.AuthorizationStatus(rpcCtx, a.authRequest(auth))
	if err != nil {
		return "", clientError(err)
	}
	out := session.AuthorizationStatus(r.GetStatus())
	if !validAuthorizationStatus(out) {
		return "", errors.New("mcpbrokergrpc: invalid authorization status")
	}
	return out, nil
}
func (a *clientAttachment) CancelAuthorization(ctx context.Context, auth session.ExternalAuthorization) (mcpbroker.CancelOutcome, error) {
	rpcCtx, cancel := a.client.bounded(ctx, false)
	defer cancel()
	r, err := a.client.rpc.CancelAuthorization(rpcCtx, a.authRequest(auth))
	if err != nil {
		return "", clientError(err)
	}
	out := mcpbroker.CancelOutcome(r.GetOutcome())
	if !validCancelOutcome(out) {
		return "", errors.New("mcpbrokergrpc: invalid cancel outcome")
	}
	return out, nil
}

type clientEnrollmentAttachment struct{ *clientAttachment }

func (a *clientEnrollmentAttachment) BeginWorkspaceEnrollment(ctx context.Context) (mcpbroker.WorkspaceEnrollmentPresentation, error) {
	rpcCtx, cancel := a.client.bounded(ctx, false)
	defer cancel()
	r, err := a.client.rpc.BeginWorkspaceEnrollment(rpcCtx, a.request())
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
func (a *clientEnrollmentAttachment) ObserveWorkspaceEnrollment(ctx context.Context, ref mcpbroker.WorkspaceEnrollmentRef) (mcpbroker.WorkspaceEnrollmentResult, error) {
	return a.workspaceResult(ctx, ref, false)
}
func (a *clientEnrollmentAttachment) CancelWorkspaceEnrollment(ctx context.Context, ref mcpbroker.WorkspaceEnrollmentRef) (mcpbroker.WorkspaceEnrollmentResult, error) {
	return a.workspaceResult(ctx, ref, true)
}
func (a *clientEnrollmentAttachment) workspaceResult(ctx context.Context, ref mcpbroker.WorkspaceEnrollmentRef, cancelOperation bool) (mcpbroker.WorkspaceEnrollmentResult, error) {
	rpcCtx, cancel := a.client.bounded(ctx, false)
	defer cancel()
	req := &brokerv1.WorkspaceRequest{Handle: a.handle, Ref: workspaceRefToWire(ref), BrokerIncarnation: a.incarnation}
	var r *brokerv1.WorkspaceResultResponse
	var err error
	if cancelOperation {
		r, err = a.client.rpc.CancelWorkspaceEnrollment(rpcCtx, req)
	} else {
		r, err = a.client.rpc.ObserveWorkspaceEnrollment(rpcCtx, req)
	}
	if err != nil {
		return mcpbroker.WorkspaceEnrollmentResult{}, clientError(err)
	}
	return workspaceResultFromWire(a.client, a.handle, a.incarnation, r)
}

type remoteTool struct {
	client      *Client
	handle      string
	incarnation string
	spec        tool.ToolSpec
	readOnly    bool
}

func (t *remoteTool) Spec() tool.ToolSpec {
	s := t.spec
	s.Schema = append([]byte(nil), s.Schema...)
	return s
}
func (t *remoteTool) ReadOnly() bool { return t.readOnly }
func (t *remoteTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	if call.Name != t.spec.Name {
		return session.ToolResult{}, errors.New("mcpbrokergrpc: tool name does not match descriptor")
	}
	if _, e := callFrom(call.Name, string(call.ID), call.Args, call.ItemID); e != nil {
		return session.ToolResult{}, e
	}
	rpcCtx, cancel := t.client.bounded(ctx, true)
	defer cancel()
	var trailer metadata.MD
	r, e := t.client.rpc.Run(rpcCtx, &brokerv1.RunRequest{Handle: t.handle, Name: call.Name, CallId: string(call.ID), Args: append([]byte(nil), call.Args...), ItemId: call.ItemID, BrokerIncarnation: t.incarnation}, grpc.Trailer(&trailer))
	if e != nil {
		if isDefinitiveSessionLoss(e) {
			return session.NewToolError(call.ID, sessionUnavailableMessage), nil
		}
		marker := trailer.Get(dispatchStateTrailer)
		if len(marker) == 1 && marker[0] == dispatchNotStarted {
			return session.ToolResult{}, clientError(e)
		}
		return session.NewToolError(call.ID, ambiguousOutcomeMessage), nil
	}
	return resultFromWire(r.GetResult())
}

type remoteSerialTool struct{ *remoteTool }

func (*remoteSerialTool) DispatchSerialTool() {}

type remoteAuthorizationTool struct{ *remoteTool }

func (t *remoteAuthorizationTool) RequestAuthorization(ctx context.Context, call session.ToolCall) (session.ExternalAuthorization, bool, error) {
	rpcCtx, cancel := t.client.bounded(ctx, false)
	defer cancel()
	r, e := t.client.rpc.RequestAuthorization(rpcCtx, &brokerv1.RequestAuthorizationRequest{Handle: t.handle, Name: call.Name, CallId: string(call.ID), Args: append([]byte(nil), call.Args...), ItemId: call.ItemID, BrokerIncarnation: t.incarnation})
	if e != nil {
		return session.ExternalAuthorization{}, false, clientError(e)
	}
	a, e := authFromWire(r.GetAuthorization())
	return a, r.GetRequired(), e
}
func (t *remoteAuthorizationTool) AbortAuthorization(ctx context.Context, a session.ExternalAuthorization) error {
	rpcCtx, cancel := t.client.bounded(ctx, false)
	defer cancel()
	_, e := t.client.rpc.AbortAuthorization(rpcCtx, &brokerv1.AbortAuthorizationRequest{Handle: t.handle, Authorization: authToWire(a), BrokerIncarnation: t.incarnation})
	return clientError(e)
}

type remoteAuthorizationSerialTool struct{ *remoteAuthorizationTool }

func (*remoteAuthorizationSerialTool) DispatchSerialTool() {}
func remoteTools(c *Client, r *brokerv1.AttachResponse) ([]tool.Tool, error) {
	out := make([]tool.Tool, 0, len(r.GetTools()))
	seen := map[string]bool{}
	for _, d := range r.GetTools() {
		if d == nil || d.GetName() == "" || seen[d.GetName()] || !utf8.ValidString(d.GetName()) || !utf8.ValidString(d.GetDescription()) || !validJSONObject(d.GetSchema()) {
			return nil, errors.New("mcpbrokergrpc: malformed tool descriptor")
		}
		seen[d.GetName()] = true
		base := &remoteTool{client: c, handle: r.GetHandle(), incarnation: r.GetBrokerIncarnation(), spec: tool.ToolSpec{Name: d.GetName(), Description: d.GetDescription(), Schema: append([]byte(nil), d.GetSchema()...)}, readOnly: d.GetReadOnly()}
		if d.GetAuthorizationCapable() {
			a := &remoteAuthorizationTool{remoteTool: base}
			if d.GetDispatchSerial() {
				out = append(out, &remoteAuthorizationSerialTool{a})
			} else {
				out = append(out, a)
			}
		} else if d.GetDispatchSerial() {
			out = append(out, &remoteSerialTool{base})
		} else {
			out = append(out, base)
		}
	}
	return out, nil
}
func validAttachOutcome(v string) bool {
	return v == string(mcpbroker.AttachCreated) || v == string(mcpbroker.AttachReattached)
}
func isDefinitiveSessionLoss(err error) bool {
	st := status.Convert(err)
	return st.Code() == codes.FailedPrecondition ||
		(st.Code() == codes.Unavailable && st.Message() == mcpbroker.ErrStateUnavailable.Error())
}

func clientError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Unavailable:
		if status.Convert(err).Message() == mcpbroker.ErrStateUnavailable.Error() {
			return mcpbroker.ErrStateUnavailable
		}
		return err
	case codes.FailedPrecondition:
		if status.Convert(err).Message() == mcpbroker.ErrAttachmentClosed.Error() {
			return mcpbroker.ErrAttachmentClosed
		}
		return mcpbroker.ErrStateUnavailable
	case codes.NotFound:
		return mcpbroker.ErrAuthorizationNotFound
	default:
		return err
	}
}
func callFrom(name, id string, args []byte, item string) (session.ToolCall, error) {
	if name == "" || id == "" || !utf8.ValidString(name) || !utf8.ValidString(id) || !utf8.ValidString(item) || !json.Valid(args) {
		return session.ToolCall{}, errors.New("mcpbrokergrpc: malformed invocation")
	}
	return session.ToolCall{ID: session.ToolCallID(id), Name: name, Args: append([]byte(nil), args...), ItemID: item}, nil
}
func resultToWire(r session.ToolResult) (*brokerv1.ToolResult, error) {
	if r.CallID == "" || !utf8.ValidString(string(r.CallID)) || !utf8.ValidString(r.Content) || !validParts(r.Parts) {
		return nil, errors.New("mcpbrokergrpc: malformed tool result")
	}
	parts := make([]*brokerv1.ResultPart, 0, len(r.Parts))
	for _, p := range r.Parts {
		parts = append(parts, &brokerv1.ResultPart{BlockKind: string(p.BlockKind), MediaKind: string(p.Kind), MimeType: p.MIMEType, Data: append([]byte(nil), p.Data...), Url: p.URL, Text: p.Text, Name: p.Name, Title: p.Title, Description: p.Description, Size: p.Size, Audience: append([]string(nil), p.Audience...), Priority: p.Priority, LastModified: p.LastModified})
	}
	return &brokerv1.ToolResult{CallId: string(r.CallID), Content: r.Content, IsError: r.IsError, Parts: parts}, nil
}
func resultFromWire(r *brokerv1.ToolResult) (session.ToolResult, error) {
	if r == nil || r.GetCallId() == "" || !utf8.ValidString(r.GetCallId()) || !utf8.ValidString(r.GetContent()) {
		return session.ToolResult{}, errors.New("mcpbrokergrpc: malformed tool result")
	}
	parts := make([]session.Content, 0, len(r.GetParts()))
	for _, p := range r.GetParts() {
		if p == nil {
			return session.ToolResult{}, errors.New("mcpbrokergrpc: malformed result part")
		}
		q := session.Content{BlockKind: session.BlockKind(p.GetBlockKind()), Kind: session.MediaKind(p.GetMediaKind()), MIMEType: p.GetMimeType(), Data: append([]byte(nil), p.GetData()...), URL: p.GetUrl(), Text: p.GetText(), Name: p.GetName(), Title: p.GetTitle(), Description: p.GetDescription(), Size: p.GetSize(), Audience: append([]string(nil), p.GetAudience()...), Priority: p.GetPriority(), LastModified: p.GetLastModified()}
		parts = append(parts, q)
	}
	if !validParts(parts) {
		return session.ToolResult{}, errors.New("mcpbrokergrpc: malformed tool result")
	}
	return session.ToolResult{CallID: session.ToolCallID(r.GetCallId()), Content: r.GetContent(), IsError: r.GetIsError(), Parts: parts}, nil
}
func validParts(parts []session.Content) bool {
	for _, p := range parts {
		for _, s := range []string{string(p.BlockKind), string(p.Kind), p.MIMEType, p.URL, p.Text, p.Name, p.Title, p.Description, p.LastModified} {
			if !utf8.ValidString(s) {
				return false
			}
		}
		for _, a := range p.Audience {
			if !utf8.ValidString(a) {
				return false
			}
		}
	}
	return session.ValidateToolResultParts(parts) == nil
}
func authToWire(a session.ExternalAuthorization) *brokerv1.Authorization {
	return &brokerv1.Authorization{Id: a.ID, Binding: string(a.Binding), ExpiresAt: timestamppb.New(a.ExpiresAt)}
}
func authFromWire(a *brokerv1.Authorization) (session.ExternalAuthorization, error) {
	if a == nil || a.GetId() == "" || a.GetBinding() == "" || !utf8.ValidString(a.GetId()) || !utf8.ValidString(a.GetBinding()) || a.GetExpiresAt() == nil || !a.GetExpiresAt().IsValid() {
		return session.ExternalAuthorization{}, errors.New("mcpbrokergrpc: malformed authorization")
	}
	return session.ExternalAuthorization{ID: a.GetId(), Binding: session.AuthorizationBinding(a.GetBinding()), ExpiresAt: a.GetExpiresAt().AsTime()}, nil
}

func workspaceRefToWire(ref mcpbroker.WorkspaceEnrollmentRef) *brokerv1.WorkspaceRef {
	return &brokerv1.WorkspaceRef{Id: string(ref.ID), RequiredServices: ref.RequiredServices, ExpiresAt: timestamppb.New(ref.ExpiresAt)}
}
func workspaceRefFromWire(ref *brokerv1.WorkspaceRef) (mcpbroker.WorkspaceEnrollmentRef, error) {
	if ref == nil || ref.GetExpiresAt() == nil || !ref.GetExpiresAt().IsValid() {
		return mcpbroker.WorkspaceEnrollmentRef{}, invalid("malformed workspace reference")
	}
	out := mcpbroker.WorkspaceEnrollmentRef{ID: session.WorkspaceEnrollmentID(ref.GetId()), RequiredServices: ref.GetRequiredServices(), ExpiresAt: ref.GetExpiresAt().AsTime()}
	if !out.Valid() {
		return mcpbroker.WorkspaceEnrollmentRef{}, invalid("malformed workspace reference")
	}
	return out, nil
}
func workspaceResultFromWire(c *Client, handle, incarnation string, r *brokerv1.WorkspaceResultResponse) (mcpbroker.WorkspaceEnrollmentResult, error) {
	if r == nil {
		return mcpbroker.WorkspaceEnrollmentResult{}, errors.New("mcpbrokergrpc: malformed workspace result")
	}
	ref, err := workspaceRefFromWire(r.GetRef())
	if err != nil {
		return mcpbroker.WorkspaceEnrollmentResult{}, err
	}
	out := mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentStatus(r.GetStatus())}
	if out.Status == mcpbroker.WorkspaceEnrollmentConnected {
		attach := &brokerv1.AttachResponse{Handle: handle, BrokerIncarnation: incarnation, Tools: r.GetTools()}
		tools, toolsErr := remoteTools(c, attach)
		if toolsErr != nil {
			return mcpbroker.WorkspaceEnrollmentResult{}, toolsErr
		}
		out.Catalogue, err = mcpbroker.NewWorkspaceCatalogue(ref, tools)
		if err != nil {
			return mcpbroker.WorkspaceEnrollmentResult{}, err
		}
	}
	if !out.Valid() {
		return mcpbroker.WorkspaceEnrollmentResult{}, errors.New("mcpbrokergrpc: malformed workspace result")
	}
	return out, nil
}
func validAuthorizationStatus(s session.AuthorizationStatus) bool {
	switch s {
	case session.AuthorizationPending, session.AuthorizationGranted, session.AuthorizationDenied,
		session.AuthorizationCancelled, session.AuthorizationExpired, session.AuthorizationInterrupted,
		session.AuthorizationFailed, session.AuthorizationClosed:
		return true
	default:
		return false
	}
}
func validCancelOutcome(out mcpbroker.CancelOutcome) bool {
	return out == mcpbroker.CancelCancelled || out == mcpbroker.CancelAlreadyCancelled || out == mcpbroker.CancelAlreadyResolved
}

var _ mcpbroker.Service = (*Client)(nil)
var _ mcpbroker.BindingSessionDeleter = (*Client)(nil)
