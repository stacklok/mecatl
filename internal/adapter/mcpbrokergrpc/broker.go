// Package mcpbrokergrpc adapts the neutral internal MCP broker seam to the
// versioned mecatl.broker.v1 RPC protocol. It deliberately carries only logical
// bindings, process-local handles, frozen tool descriptors, and invocation data.
package mcpbrokergrpc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

const (
	defaultMaxHandles         = 128
	defaultMaxActiveExecutes  = 64
	maxTerminalReceiptBytes   = 1024
	ambiguousOutcomeMessage   = "remote tool outcome is unknown because the broker response was lost; the operation may already have completed. Do not automatically repeat it. Reconcile through a safe status/read path first; if unavailable, report the uncertainty and seek operator direction."
	sessionUnavailableMessage = "tool temporarily unavailable"
)

// Config bounds transport calls and server-side attachment retention.
type Config struct {
	DialTimeout        time.Duration
	RPCDeadline        time.Duration
	ExecuteDeadline    time.Duration
	HandleIdleTimeout  time.Duration
	OwnerRetention     time.Duration
	SweepInterval      time.Duration
	CleanupTimeout     time.Duration
	MaxHandles         int
	MaxOwners          int
	MaxReceipts        int
	MaxReceiptBytes    int
	MaxPendingControls int
	MaxActiveExecutes  int
}

// DefaultConfig returns finite production defaults for the initial single-process broker.
func DefaultConfig() Config {
	return Config{
		DialTimeout: 5 * time.Second, RPCDeadline: 10 * time.Second,
		ExecuteDeadline: 2 * time.Minute, HandleIdleTimeout: 5 * time.Minute,
		OwnerRetention: 24 * time.Hour, SweepInterval: 30 * time.Second, CleanupTimeout: 10 * time.Second,
		MaxHandles: defaultMaxHandles, MaxOwners: defaultMaxHandles, MaxReceipts: 4096, MaxReceiptBytes: 8 << 20, MaxPendingControls: 1024, MaxActiveExecutes: defaultMaxActiveExecutes,
	}
}

func (c Config) valid() bool {
	return c.DialTimeout > 0 && c.RPCDeadline > 0 && c.ExecuteDeadline > 0 &&
		c.HandleIdleTimeout > 0 && c.OwnerRetention > 0 && c.SweepInterval > 0 && c.CleanupTimeout > 0 && c.MaxHandles > 0 && c.MaxOwners > 0 &&
		c.MaxReceipts > 0 && c.MaxReceiptBytes > 0 && c.MaxPendingControls > 0 && c.MaxActiveExecutes > 0
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
	service         mcpbroker.Service
	mu              sync.Mutex
	handles         map[string]*serverAttachment
	owners          map[session.SessionID]*sessionOwner
	maxHandles      int
	incarnation     string
	cfg             Config
	closed          bool
	done            chan struct{}
	stop            chan struct{}
	executeCtx      context.Context
	executeStop     context.CancelFunc
	executeWG       sync.WaitGroup
	activeExecutes  int
	pendingControls int
}
type lifecycleOperation uint8

const (
	lifecycleNone lifecycleOperation = iota
	lifecycleAbort
	lifecycleClose
)

type executeReceipt struct {
	digest   [sha256.Size]byte
	bytes    int
	started  bool
	done     chan struct{}
	response *brokerv1.ExecuteResponse
	err      error
}

type sessionOwner struct {
	principal session.Principal
	pending   int
	handles   int
	expiresAt time.Time
	retiring  bool
}

type serverAttachment struct {
	attachment      mcpbroker.Attachment
	principal       *session.Principal
	logicalID       session.SessionID
	tools           map[string]tool.Tool
	active          int
	expiresAt       time.Time
	changed         chan struct{}
	receipts        map[session.ToolCallID]*executeReceipt
	receiptBytes    int
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
	if cfg.MaxReceipts < 0 || cfg.MaxReceiptBytes < 0 || cfg.MaxPendingControls < 0 || cfg.MaxOwners < 0 || cfg.MaxActiveExecutes < 0 {
		return nil, errors.New("mcpbrokergrpc: capacities must not be negative")
	}
	defaults := DefaultConfig()
	if cfg.MaxOwners == 0 {
		cfg.MaxOwners = defaults.MaxOwners
	}
	if cfg.MaxReceipts == 0 {
		cfg.MaxReceipts = defaults.MaxReceipts
	}
	if cfg.MaxReceiptBytes == 0 {
		cfg.MaxReceiptBytes = defaults.MaxReceiptBytes
	}
	if cfg.MaxPendingControls == 0 {
		cfg.MaxPendingControls = defaults.MaxPendingControls
	}
	if cfg.MaxActiveExecutes == 0 {
		cfg.MaxActiveExecutes = defaults.MaxActiveExecutes
	}
	if !cfg.valid() {
		return nil, errors.New("mcpbrokergrpc: all deadlines and capacities must be positive")
	}
	incarnation, err := newHandle()
	if err != nil {
		return nil, fmt.Errorf("mcpbrokergrpc: mint broker incarnation: %w", err)
	}
	executeCtx, executeStop := context.WithCancel(context.Background())
	s := &Server{service: service, handles: make(map[string]*serverAttachment), owners: make(map[session.SessionID]*sessionOwner), maxHandles: cfg.MaxHandles, incarnation: incarnation, cfg: cfg, done: make(chan struct{}), stop: make(chan struct{}), executeCtx: executeCtx, executeStop: executeStop}
	go s.sweep()
	return s, nil
}

// ExecuteDeadline returns the server-side upper bound used for Execute calls.
func (s *Server) ExecuteDeadline() time.Duration { return s.cfg.ExecuteDeadline }

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

func (s *Server) bindSession(ctx context.Context, id session.SessionID) (*session.Principal, bool, error) {
	if !mcpbroker.ValidLogicalSessionID(id) {
		return nil, false, invalid("session_id is invalid")
	}
	principal := session.PrincipalFromContext(ctx)
	if principal == nil {
		return nil, false, nil // Direct adapter calls are test-only; the network boundary always installs a principal.
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := s.owners[id]
	if owner != nil && owner.retiring {
		return nil, false, reasonStatus(codes.Unavailable, "broker session is being retired", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	if owner != nil && !principal.SameIdentity(&owner.principal) {
		return nil, false, status.Error(codes.PermissionDenied, "broker session is not available")
	}
	created := false
	if owner == nil {
		if len(s.owners) >= s.cfg.MaxOwners {
			return nil, false, reasonStatus(codes.ResourceExhausted, "broker session capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, "")
		}
		owner = &sessionOwner{principal: *principal, expiresAt: time.Now().Add(s.cfg.OwnerRetention)}
		s.owners[id] = owner
		created = true
	}
	owner.pending++
	return principal.Clone(), created, nil
}

func (s *Server) authorizeSession(ctx context.Context, id session.SessionID) error {
	principal := session.PrincipalFromContext(ctx)
	if principal == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := s.owners[id]
	if owner == nil || !principal.SameIdentity(&owner.principal) {
		return status.Error(codes.PermissionDenied, "broker session is not available")
	}
	return nil
}

func (s *Server) removeOwnerLocked(id session.SessionID) {
	delete(s.owners, id)
}

func (s *Server) finishSessionBind(id session.SessionID, attached, newlyCreated bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := s.owners[id]
	if owner == nil {
		return
	}
	owner.pending--
	if attached {
		owner.handles++
	}
	if newlyCreated && !attached && owner.pending == 0 && owner.handles == 0 {
		s.removeOwnerLocked(id)
	}
	// Ownership is deliberately retained after the last handle closes. The
	// logical session's absolute retention, delete, or shutdown reclaims it.
}

func authorizeHandle(ctx context.Context, attachment *serverAttachment) error {
	if attachment == nil || attachment.principal == nil {
		return nil
	}
	principal := session.PrincipalFromContext(ctx)
	if principal == nil || !principal.SameIdentity(attachment.principal) {
		return status.Error(codes.PermissionDenied, "broker attachment is not available")
	}
	return nil
}

// Attach binds an authenticated workload to a logical broker session.
func (s *Server) Attach(ctx context.Context, req *brokerv1.AttachRequest) (*brokerv1.AttachResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	if req.GetSessionId() == "" {
		return nil, invalid("session_id is required")
	}
	if err := s.checkIncarnation(req.GetBrokerIncarnation(), true); err != nil {
		return nil, err
	}
	logicalID := session.SessionID(req.GetSessionId())
	if !mcpbroker.ValidLogicalSessionID(logicalID) {
		return nil, invalid("session_id is invalid")
	}
	principal, newlyCreated, err := s.bindSession(ctx, logicalID)
	if err != nil {
		return nil, err
	}
	attached := false
	defer func() { s.finishSessionBind(logicalID, attached, newlyCreated) }()
	a, outcome, err := s.service.AttachSession(ctx, logicalID)
	if err != nil {
		return nil, brokerStatus(err)
	}
	desc, tools, err := descriptors(a.Tools())
	if err != nil {
		s.discardUnpublishedAttachment(a, outcome)
		return nil, invalid(err.Error())
	}
	h, err := newHandle()
	if err != nil {
		s.discardUnpublishedAttachment(a, outcome)
		return nil, status.Error(codes.Internal, "mint attachment handle")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.discardUnpublishedAttachment(a, outcome)
		return nil, reasonStatus(codes.Unavailable, "broker state unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	if len(s.handles) >= s.maxHandles {
		s.discardUnpublishedAttachment(a, outcome)
		return nil, reasonStatus(codes.ResourceExhausted, "attachment handle capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, "")
	}
	_, enrollment := a.(mcpbroker.WorkspaceEnrollmentAttachment)
	now := time.Now()
	s.handles[h] = &serverAttachment{attachment: a, principal: principal, logicalID: logicalID, tools: tools, expiresAt: now.Add(s.cfg.HandleIdleTimeout), changed: make(chan struct{}), receipts: make(map[session.ToolCallID]*executeReceipt)}
	attached = true
	return &brokerv1.AttachResponse{Binding: string(a.Binding()), Handle: h, Outcome: string(outcome), Tools: desc, BrokerIncarnation: s.incarnation, WorkspaceEnrollment: enrollment}, nil
}

func (s *Server) discardUnpublishedAttachment(attachment mcpbroker.Attachment, outcome mcpbroker.AttachOutcome) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.CleanupTimeout)
	defer cancel()
	if outcome == mcpbroker.AttachCreated {
		_ = attachment.Abort(ctx)
		return
	}
	_, _ = attachment.Close(ctx)
}

func (s *Server) checkIncarnation(got string, allowEmpty bool) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return reasonStatus(codes.Unavailable, "broker state unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	if got == "" && allowEmpty {
		return nil
	}
	if got == "" || got != s.incarnation {
		return reasonStatus(codes.FailedPrecondition, "broker incarnation mismatch", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_INCARNATION_LOST, "")
	}
	return nil
}

func signalAttachment(a *serverAttachment) {
	close(a.changed)
	a.changed = make(chan struct{})
}

func (s *Server) get(ctx context.Context, incarnation, handle string) (*serverAttachment, func(), error) {
	if err := s.checkIncarnation(incarnation, false); err != nil {
		return nil, nil, err
	}
	if handle == "" {
		return nil, nil, invalid("handle is required")
	}
	s.mu.Lock()
	a := s.handles[handle]
	if err := authorizeHandle(ctx, a); err != nil {
		s.mu.Unlock()
		return nil, nil, err
	}
	if a == nil || s.closed || !time.Now().Before(a.expiresAt) || a.running != lifecycleNone || a.terminal != lifecycleNone {
		s.mu.Unlock()
		return nil, nil, reasonStatus(codes.FailedPrecondition, "attachment handle unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	a.active++
	signalAttachment(a)
	s.mu.Unlock()
	return a, func() {
		s.mu.Lock()
		a.active--
		s.releaseClosedReceiptsLocked(a)
		signalAttachment(a)
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
		if err := authorizeHandle(ctx, a); err != nil {
			s.mu.Unlock()
			return nil, "", false, err
		}
		if a == nil || s.closed || !time.Now().Before(a.expiresAt) {
			s.mu.Unlock()
			return nil, "", false, reasonStatus(codes.FailedPrecondition, "attachment handle unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
		}
		if a.terminal != lifecycleNone {
			if a.terminal != operation {
				s.mu.Unlock()
				return nil, "", false, reasonStatus(codes.FailedPrecondition, "attachment handle unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
			}
			outcome := a.terminalOutcome
			s.mu.Unlock()
			return nil, outcome, true, nil
		}
		if a.running != lifecycleNone || a.active != 0 {
			done := a.changed
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, "", false, status.FromContextError(ctx.Err()).Err()
			}
		}
		if s.pendingControls >= s.cfg.MaxPendingControls {
			s.mu.Unlock()
			return nil, "", false, reasonStatus(codes.ResourceExhausted, "broker control capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, "")
		}
		a.running = operation
		s.pendingControls++
		a.runningDone = make(chan struct{})
		a.active++
		signalAttachment(a)
		s.mu.Unlock()
		return a, "", false, nil
	}
}

func (s *Server) releaseOwnerHandleLocked(id session.SessionID) {
	owner := s.owners[id]
	if owner == nil || owner.handles == 0 {
		return
	}
	owner.handles--
}

func releaseReceiptsLocked(attachment *serverAttachment) {
	clear(attachment.receipts)
	attachment.receiptBytes = 0
}

func (s *Server) releaseClosedReceiptsLocked(attachment *serverAttachment) {
	if s.closed && attachment.active == 0 {
		releaseReceiptsLocked(attachment)
	}
}

func (s *Server) finishLifecycle(a *serverAttachment, operation lifecycleOperation, outcome mcpbroker.CloseOutcome, terminal bool) {
	s.mu.Lock()
	if terminal {
		s.releaseOwnerHandleLocked(a.logicalID)
	}
	a.active--
	s.releaseClosedReceiptsLocked(a)
	if terminal {
		a.terminal = operation
		a.terminalOutcome = outcome
	}
	if a.running != lifecycleNone {
		s.pendingControls--
	}
	a.running = lifecycleNone
	done := a.runningDone
	a.runningDone = nil
	close(done)
	signalAttachment(a)
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
			type ownerRetirement struct {
				id    session.SessionID
				owner *sessionOwner
			}
			var retirements []ownerRetirement
			for handle, attachment := range s.handles {
				if attachment.active == 0 && !now.Before(attachment.expiresAt) {
					releaseReceiptsLocked(attachment)
					delete(s.handles, handle)
					if attachment.terminal == lifecycleNone {
						s.releaseOwnerHandleLocked(attachment.logicalID)
						expired = append(expired, attachment)
					}
				}
			}
			for id, owner := range s.owners {
				if owner.pending == 0 && owner.handles == 0 && !owner.retiring && !now.Before(owner.expiresAt) {
					owner.retiring = true
					retirements = append(retirements, ownerRetirement{id: id, owner: owner})
				}
			}
			s.mu.Unlock()
			for _, attachment := range expired {
				s.closeAttachment(attachment)
			}
			for _, retirement := range retirements {
				s.retireOwner(retirement.id, retirement.owner)
			}
		}
	}
}

func (s *Server) retireOwner(id session.SessionID, owner *sessionOwner) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.CleanupTimeout)
	_, err := s.service.DeleteSession(ctx, id)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owners[id] != owner {
		return
	}
	if err == nil {
		s.removeOwnerLocked(id)
		return
	}
	owner.retiring = false
	owner.expiresAt = time.Now().Add(s.cfg.SweepInterval)
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
	toClose := make([]*serverAttachment, 0, len(s.handles))
	for handle, attachment := range s.handles {
		delete(s.handles, handle)
		attachments = append(attachments, attachment)
		if attachment.terminal == lifecycleNone {
			toClose = append(toClose, attachment)
		}
	}
	clear(s.owners)
	s.mu.Unlock()
	s.executeStop()
	executeDone := make(chan struct{})
	go func() {
		s.executeWG.Wait()
		close(executeDone)
	}()
	select {
	case <-executeDone:
		s.mu.Lock()
		for _, attachment := range attachments {
			releaseReceiptsLocked(attachment)
		}
		s.mu.Unlock()
	case <-ctx.Done():
		return ctx.Err()
	}
	for _, attachment := range toClose {
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
func (s *Server) Commit(ctx context.Context, req *brokerv1.CommitRequest) (*brokerv1.CommitResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, e := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
	if e != nil {
		return nil, e
	}
	defer release()
	if e = a.attachment.Commit(ctx); e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.CommitResponse{}, nil
}

// Abort aborts and releases one exact handle.
func (s *Server) Abort(ctx context.Context, req *brokerv1.AbortRequest) (*brokerv1.AbortResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, _, receipt, e := s.beginLifecycle(ctx, req.GetBrokerIncarnation(), req.GetHandle(), lifecycleAbort)
	if e != nil {
		return nil, e
	}
	if receipt {
		return &brokerv1.AbortResponse{}, nil
	}
	e = a.attachment.Abort(ctx)
	s.finishLifecycle(a, lifecycleAbort, "", e == nil)
	if e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.AbortResponse{}, nil
}

// Close releases one exact handle without deleting logical state.
func (s *Server) Close(ctx context.Context, req *brokerv1.CloseRequest) (*brokerv1.CloseResponse, error) {
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
	if !mcpbroker.ValidLogicalSessionID(session.SessionID(req.GetSessionId())) {
		return nil, invalid("session_id is invalid")
	}
	if req.GetBinding() == "" {
		return nil, invalid("binding is required")
	}
	if err := s.checkIncarnation(req.GetBrokerIncarnation(), true); err != nil {
		return nil, err
	}
	if err := s.authorizeSession(ctx, session.SessionID(req.GetSessionId())); err != nil {
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
	s.mu.Lock()
	s.removeOwnerLocked(session.SessionID(req.GetSessionId()))
	s.mu.Unlock()
	return &brokerv1.DeleteResponse{Outcome: string(out)}, nil
}

const executeMethod = "/mecatl.broker.v1.BrokerService/Execute"

func preDispatchError(err error) error {
	st := status.Convert(err)
	return reasonStatus(st.Code(), st.Message(), brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_DISPATCH_NOT_STARTED, executeMethod)
}

// Execute dispatches one tool invocation and retains one immutable bounded receipt.
func (s *Server) Execute(ctx context.Context, req *brokerv1.ExecuteRequest) (*brokerv1.ExecuteResponse, error) {
	ctx, cancel := s.bounded(ctx, true)
	defer cancel()
	if err := s.checkIncarnation(req.GetBrokerIncarnation(), false); err != nil {
		return nil, err
	}
	call, err := callFrom(req.GetName(), req.GetCallId(), req.GetArgs(), req.GetItemId())
	if err != nil {
		return nil, preDispatchError(invalid(err.Error()))
	}
	if req.GetHandle() == "" {
		return nil, preDispatchError(invalid("handle is required"))
	}
	digest := invocationDigest(call)

	s.mu.Lock()
	a := s.handles[req.GetHandle()]
	if err := authorizeHandle(ctx, a); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if a == nil || s.closed || !time.Now().Before(a.expiresAt) || a.running != lifecycleNone || a.terminal != lifecycleNone {
		s.mu.Unlock()
		return nil, reasonStatus(codes.FailedPrecondition, "attachment handle unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	target := a.tools[call.Name]
	if target == nil {
		s.mu.Unlock()
		return nil, preDispatchError(invalid("unknown tool"))
	}
	receipt := a.receipts[call.ID]
	if receipt != nil {
		if receipt.digest != digest {
			s.mu.Unlock()
			return nil, preDispatchError(invalid("call_id was reused with different invocation content"))
		}
		if receipt.started {
			s.mu.Unlock()
			return waitExecuteReceipt(ctx, receipt)
		}
		receipt.started = true
	} else {
		if len(a.receipts) >= s.cfg.MaxReceipts || a.receiptBytes+receiptReservationBytes(call) > s.cfg.MaxReceiptBytes {
			s.mu.Unlock()
			return nil, reasonStatus(codes.ResourceExhausted, "broker receipt capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, executeMethod)
		}
		receipt = &executeReceipt{digest: digest, bytes: receiptReservationBytes(call), started: true, done: make(chan struct{})}
		a.receipts[call.ID] = receipt
		a.receiptBytes += receipt.bytes
	}
	if s.activeExecutes >= s.cfg.MaxActiveExecutes {
		s.finishExecuteLocked(a, receipt, call, nil, executeCapacityError(), false)
		s.mu.Unlock()
		return waitExecuteReceipt(ctx, receipt)
	}
	s.activeExecutes++
	a.active++
	signalAttachment(a)
	s.executeWG.Add(1)
	s.mu.Unlock()

	go s.executeOwner(a, receipt, target, call)
	return waitExecuteReceipt(ctx, receipt)
}

func invocationReceiptBytes(call session.ToolCall) int {
	return len(call.Name) + len(call.ID) + len(call.ItemID) + len(call.Args)
}

// receiptReservationBytes guarantees that an executed invocation can retain a
// deterministic terminal result even when the tool's successful response is too large.
func receiptReservationBytes(call session.ToolCall) int {
	return invocationReceiptBytes(call) + maxTerminalReceiptBytes
}

func executeCapacityError() error {
	return reasonStatus(codes.ResourceExhausted, "broker Execute capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, executeMethod)
}

func receiptOversizeResponse(call session.ToolCall) *brokerv1.ExecuteResponse {
	return &brokerv1.ExecuteResponse{Result: &brokerv1.ToolResult{
		CallId:  string(call.ID),
		Content: "tool completed, but its result exceeded the broker retained-receipt limit; do not retry this call",
		IsError: true,
	}}
}

func invocationDigest(call session.ToolCall) [sha256.Size]byte {
	h := sha256.New()
	var size [8]byte
	for _, field := range [][]byte{[]byte(call.Name), []byte(call.ItemID), call.Args} {
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(field)
	}
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

func (s *Server) executeOwner(a *serverAttachment, receipt *executeReceipt, target tool.Tool, call session.ToolCall) {
	defer s.executeWG.Done()
	var response *brokerv1.ExecuteResponse
	var executeErr error
	func() {
		defer func() {
			if recover() != nil {
				executeErr = status.Error(codes.Internal, "tool execution panicked")
			}
		}()
		ctx, cancel := context.WithTimeout(s.executeCtx, s.cfg.ExecuteDeadline)
		defer cancel()
		env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: string(a.logicalID), Revision: s.incarnation}, nofs.New(), memledger.New(), nil)
		result, err := target.Execute(ctx, call, env)
		switch {
		case err != nil:
			executeErr = brokerStatus(err)
		case result.CallID != call.ID:
			executeErr = status.Error(codes.Internal, "tool result call_id mismatch")
		default:
			wire, err := resultToWire(result)
			if err != nil {
				executeErr = status.Error(codes.Internal, "malformed tool result")
				return
			}
			response = &brokerv1.ExecuteResponse{Result: wire}
		}
	}()

	s.mu.Lock()
	s.finishExecuteLocked(a, receipt, call, response, executeErr, true)
	s.mu.Unlock()
}

func (s *Server) finishExecuteLocked(a *serverAttachment, receipt *executeReceipt, call session.ToolCall, response *brokerv1.ExecuteResponse, err error, dispatched bool) {
	bytes := invocationReceiptBytes(call)
	switch {
	case response != nil:
		bytes += proto.Size(response)
	case err != nil:
		bytes += proto.Size(status.Convert(err).Proto())
	}
	if a.receiptBytes-receipt.bytes+bytes > s.cfg.MaxReceiptBytes {
		response = receiptOversizeResponse(call)
		err = nil
		bytes = invocationReceiptBytes(call) + proto.Size(response)
	}
	a.receiptBytes += bytes - receipt.bytes
	receipt.bytes = bytes
	receipt.response = response
	receipt.err = err
	close(receipt.done)
	if dispatched {
		s.activeExecutes--
		a.active--
	}
	s.releaseClosedReceiptsLocked(a)
	signalAttachment(a)
}

func waitExecuteReceipt(ctx context.Context, receipt *executeReceipt) (*brokerv1.ExecuteResponse, error) {
	select {
	case <-receipt.done:
		if receipt.response == nil {
			return nil, receipt.err
		}
		return proto.Clone(receipt.response).(*brokerv1.ExecuteResponse), receipt.err
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
}

// RequestAuthorization begins authorization for one exact invocation.
func (s *Server) RequestAuthorization(ctx context.Context, req *brokerv1.RequestAuthorizationRequest) (*brokerv1.RequestAuthorizationResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, e := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
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
	digest := invocationDigest(call)
	s.mu.Lock()
	receipt := a.receipts[call.ID]
	if receipt != nil && receipt.digest != digest {
		s.mu.Unlock()
		return nil, invalid("call_id was reused with different invocation content")
	}
	if receipt == nil {
		if len(a.receipts) >= s.cfg.MaxReceipts || a.receiptBytes+receiptReservationBytes(call) > s.cfg.MaxReceiptBytes {
			s.mu.Unlock()
			return nil, reasonStatus(codes.ResourceExhausted, "broker receipt capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, "")
		}
		receipt := &executeReceipt{digest: digest, bytes: receiptReservationBytes(call), done: make(chan struct{})}
		a.receipts[call.ID] = receipt
		a.receiptBytes += receipt.bytes
	}
	s.mu.Unlock()
	auth, required, e := target.RequestAuthorization(ctx, call)
	if e != nil {
		return nil, brokerStatus(e)
	}
	if !required {
		return &brokerv1.RequestAuthorizationResponse{}, nil
	}
	return &brokerv1.RequestAuthorizationResponse{Authorization: authToWire(auth), Required: true}, nil
}

// AbortAuthorization aborts one exact tool authorization.
func (s *Server) AbortAuthorization(ctx context.Context, req *brokerv1.AbortAuthorizationRequest) (*brokerv1.AbortAuthorizationResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, e := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
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
				return &brokerv1.AbortAuthorizationResponse{}, nil
			}
		}
	}
	if e == nil {
		return nil, invalid("tool is not authorization-capable")
	}
	return nil, brokerStatus(e)
}

// PresentAuthorization returns the ephemeral URL for one exact authorization.
func (s *Server) PresentAuthorization(ctx context.Context, req *brokerv1.PresentAuthorizationRequest) (*brokerv1.PresentAuthorizationResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, err := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
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
	return &brokerv1.PresentAuthorizationResponse{Url: url}, nil
}

// AuthorizationStatus observes one exact authorization.
func (s *Server) AuthorizationStatus(ctx context.Context, req *brokerv1.AuthorizationStatusRequest) (*brokerv1.AuthorizationStatusResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, err := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
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
func (s *Server) CancelAuthorization(ctx context.Context, req *brokerv1.CancelAuthorizationRequest) (*brokerv1.CancelAuthorizationResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, err := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
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
	return &brokerv1.CancelAuthorizationResponse{Outcome: string(out)}, nil
}

// BeginWorkspaceEnrollment begins a pre-prompt enrollment.
func (s *Server) BeginWorkspaceEnrollment(ctx context.Context, req *brokerv1.BeginWorkspaceEnrollmentRequest) (*brokerv1.BeginWorkspaceEnrollmentResponse, error) {
	ctx, cancel := s.bounded(ctx, false)
	defer cancel()
	a, release, err := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
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
	return &brokerv1.BeginWorkspaceEnrollmentResponse{Ref: workspaceRefToWire(presentation.Ref), Url: presentation.URL}, nil
}

// ObserveWorkspaceEnrollment observes one exact pre-prompt enrollment.
func (s *Server) ObserveWorkspaceEnrollment(ctx context.Context, req *brokerv1.ObserveWorkspaceEnrollmentRequest) (*brokerv1.ObserveWorkspaceEnrollmentResponse, error) {
	result, err := s.workspaceResult(ctx, req.GetBrokerIncarnation(), req.GetHandle(), req.GetRef(), false)
	if err != nil {
		return nil, err
	}
	return &brokerv1.ObserveWorkspaceEnrollmentResponse{Ref: result.ref, Status: result.status, Tools: result.tools}, nil
}

// CancelWorkspaceEnrollment cancels one exact pre-prompt enrollment.
func (s *Server) CancelWorkspaceEnrollment(ctx context.Context, req *brokerv1.CancelWorkspaceEnrollmentRequest) (*brokerv1.CancelWorkspaceEnrollmentResponse, error) {
	result, err := s.workspaceResult(ctx, req.GetBrokerIncarnation(), req.GetHandle(), req.GetRef(), true)
	if err != nil {
		return nil, err
	}
	return &brokerv1.CancelWorkspaceEnrollmentResponse{Ref: result.ref, Status: result.status, Tools: result.tools}, nil
}

type workspaceResultWire struct {
	ref    *brokerv1.WorkspaceRef
	status string
	tools  []*brokerv1.ToolDescriptor
}

func (s *Server) workspaceResult(ctx context.Context, incarnation, handle string, wireRef *brokerv1.WorkspaceRef, cancel bool) (workspaceResultWire, error) {
	ctx, stop := s.bounded(ctx, false)
	defer stop()
	a, release, err := s.get(ctx, incarnation, handle)
	if err != nil {
		return workspaceResultWire{}, err
	}
	defer release()
	enroller, ok := a.attachment.(mcpbroker.WorkspaceEnrollmentAttachment)
	if !ok {
		return workspaceResultWire{}, status.Error(codes.FailedPrecondition, "workspace enrollment unsupported")
	}
	ref, err := workspaceRefFromWire(wireRef)
	if err != nil {
		return workspaceResultWire{}, err
	}
	var result mcpbroker.WorkspaceEnrollmentResult
	if cancel {
		result, err = enroller.CancelWorkspaceEnrollment(ctx, ref)
	} else {
		result, err = enroller.ObserveWorkspaceEnrollment(ctx, ref)
	}
	if err != nil {
		return workspaceResultWire{}, brokerStatus(err)
	}
	if !result.Valid() {
		return workspaceResultWire{}, status.Error(codes.Internal, "invalid workspace result")
	}
	var desc []*brokerv1.ToolDescriptor
	if result.Catalogue != nil {
		desc, _, err = descriptors(result.Catalogue.Tools())
		if err != nil {
			return workspaceResultWire{}, status.Error(codes.Internal, "invalid workspace catalogue")
		}
	}
	return workspaceResultWire{ref: workspaceRefToWire(result.Ref), status: string(result.Status), tools: desc}, nil
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

func reasonStatus(code codes.Code, message string, reason brokerv1.BrokerErrorReason, method string) error {
	st := status.New(code, message)
	withDetail, err := st.WithDetails(&brokerv1.BrokerErrorDetail{Reason: reason, DispatchMethod: method})
	if err != nil {
		return status.Error(codes.Internal, "encode broker error reason")
	}
	return withDetail.Err()
}

func brokerStatus(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, mcpbroker.ErrAttachmentClosed):
		return reasonStatus(codes.FailedPrecondition, "attachment closed", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_ATTACHMENT_CLOSED, "")
	case errors.Is(err, mcpbroker.ErrStateUnavailable):
		return reasonStatus(codes.Unavailable, "broker state unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	case errors.Is(err, mcpbroker.ErrCapacity):
		return reasonStatus(codes.ResourceExhausted, "broker capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, "")
	case errors.Is(err, mcpbroker.ErrAuthorizationNotFound):
		return reasonStatus(codes.NotFound, "authorization not found", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_AUTHORIZATION_NOT_FOUND, "")
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
	tools, e := remoteTools(c, r)
	if e != nil {
		c.discardAttachResponse(r)
		return nil, "", e
	}
	base := &clientAttachment{client: c, handle: r.GetHandle(), binding: session.ExternalBinding(r.GetBinding()), incarnation: r.GetBrokerIncarnation(), tools: tools}
	c.mu.Lock()
	if c.incarnation != "" && c.incarnation != r.GetBrokerIncarnation() {
		c.mu.Unlock()
		c.discardAttachResponse(r)
		return nil, "", errors.Join(mcpbroker.ErrStateUnavailable, mcpbroker.ErrBrokerIncarnationLost)
	}
	c.incarnation = r.GetBrokerIncarnation()
	c.mu.Unlock()
	if r.GetWorkspaceEnrollment() {
		return &clientEnrollmentAttachment{clientAttachment: base}, mcpbroker.AttachOutcome(r.GetOutcome()), nil
	}
	return base, mcpbroker.AttachOutcome(r.GetOutcome()), nil
}

func (c *Client) discardAttachResponse(response *brokerv1.AttachResponse) {
	attachment := &clientAttachment{
		client:      c,
		handle:      response.GetHandle(),
		incarnation: response.GetBrokerIncarnation(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.RPCDeadline)
	defer cancel()
	if response.GetOutcome() == string(mcpbroker.AttachCreated) {
		_ = attachment.Abort(ctx)
		return
	}
	_, _ = attachment.Close(ctx)
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
func (a *clientAttachment) Commit(ctx context.Context) error {
	rpcCtx, cancel := a.client.bounded(ctx, false)
	defer cancel()
	_, e := a.client.rpc.Commit(rpcCtx, &brokerv1.CommitRequest{Handle: a.handle, BrokerIncarnation: a.incarnation})
	return clientError(e)
}
func (a *clientAttachment) Abort(ctx context.Context) error {
	rpcCtx, cancel := a.client.bounded(ctx, false)
	defer cancel()
	_, e := a.client.rpc.Abort(rpcCtx, &brokerv1.AbortRequest{Handle: a.handle, BrokerIncarnation: a.incarnation})
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
	r, e := a.client.rpc.Close(rpcCtx, &brokerv1.CloseRequest{Handle: a.handle, BrokerIncarnation: a.incarnation})
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
func (a *clientAttachment) PresentAuthorization(ctx context.Context, auth session.ExternalAuthorization) (string, error) {
	rpcCtx, cancel := a.client.bounded(ctx, false)
	defer cancel()
	r, err := a.client.rpc.PresentAuthorization(rpcCtx, &brokerv1.PresentAuthorizationRequest{Handle: a.handle, Authorization: authToWire(auth), BrokerIncarnation: a.incarnation})
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
	r, err := a.client.rpc.AuthorizationStatus(rpcCtx, &brokerv1.AuthorizationStatusRequest{Handle: a.handle, Authorization: authToWire(auth), BrokerIncarnation: a.incarnation})
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
	r, err := a.client.rpc.CancelAuthorization(rpcCtx, &brokerv1.CancelAuthorizationRequest{Handle: a.handle, Authorization: authToWire(auth), BrokerIncarnation: a.incarnation})
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
	r, err := a.client.rpc.BeginWorkspaceEnrollment(rpcCtx, &brokerv1.BeginWorkspaceEnrollmentRequest{Handle: a.handle, BrokerIncarnation: a.incarnation})
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
	var r workspaceResultResponse
	var err error
	if cancelOperation {
		r, err = a.client.rpc.CancelWorkspaceEnrollment(rpcCtx, &brokerv1.CancelWorkspaceEnrollmentRequest{Handle: a.handle, Ref: workspaceRefToWire(ref), BrokerIncarnation: a.incarnation})
	} else {
		r, err = a.client.rpc.ObserveWorkspaceEnrollment(rpcCtx, &brokerv1.ObserveWorkspaceEnrollmentRequest{Handle: a.handle, Ref: workspaceRefToWire(ref), BrokerIncarnation: a.incarnation})
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
	r, e := t.client.rpc.Execute(rpcCtx, &brokerv1.ExecuteRequest{Handle: t.handle, Name: call.Name, CallId: string(call.ID), Args: append([]byte(nil), call.Args...), ItemId: call.ItemID, BrokerIncarnation: t.incarnation})
	if e != nil {
		if isDefinitiveSessionLoss(e) {
			return session.NewToolError(call.ID, sessionUnavailableMessage), nil
		}
		if dispatchNotStarted(e) {
			return session.ToolResult{}, clientError(e)
		}
		return session.NewToolError(call.ID, ambiguousOutcomeMessage), nil
	}
	result, err := resultFromWire(r.GetResult())
	if err != nil {
		return session.ToolResult{}, err
	}
	if result.CallID != call.ID {
		return session.ToolResult{}, errors.New("mcpbrokergrpc: tool result call_id mismatch")
	}
	return result, nil
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
	if !r.GetRequired() {
		if r.GetAuthorization() != nil {
			return session.ExternalAuthorization{}, false, errors.New("mcpbrokergrpc: unexpected authorization")
		}
		return session.ExternalAuthorization{}, false, nil
	}
	a, e := authFromWire(r.GetAuthorization())
	return a, true, e
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
func brokerReason(err error) (brokerv1.BrokerErrorReason, string, bool, error) {
	var found *brokerv1.BrokerErrorDetail
	for _, detail := range status.Convert(err).Details() {
		candidate, ok := detail.(*brokerv1.BrokerErrorDetail)
		if !ok {
			continue
		}
		if found != nil {
			return 0, "", false, errors.New("mcpbrokergrpc: multiple broker error reasons")
		}
		found = candidate
	}
	if found == nil {
		return 0, "", false, nil
	}
	switch found.GetReason() {
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE,
		brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_INCARNATION_LOST,
		brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_ATTACHMENT_CLOSED,
		brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_AUTHORIZATION_NOT_FOUND:
		if found.GetDispatchMethod() != "" {
			return 0, "", false, errors.New("mcpbrokergrpc: malformed broker error reason")
		}
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED:
		if found.GetDispatchMethod() != "" && found.GetDispatchMethod() != executeMethod {
			return 0, "", false, errors.New("mcpbrokergrpc: malformed capacity reason")
		}
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_DISPATCH_NOT_STARTED:
		if found.GetDispatchMethod() != executeMethod {
			return 0, "", false, errors.New("mcpbrokergrpc: malformed dispatch proof")
		}
	default:
		return 0, "", false, errors.New("mcpbrokergrpc: unknown broker error reason")
	}
	return found.GetReason(), found.GetDispatchMethod(), true, nil
}

func dispatchNotStarted(err error) bool {
	reason, method, ok, protocolErr := brokerReason(err)
	return protocolErr == nil && ok && method == executeMethod &&
		(reason == brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_DISPATCH_NOT_STARTED ||
			reason == brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED)
}

func isDefinitiveSessionLoss(err error) bool {
	reason, _, ok, protocolErr := brokerReason(err)
	return protocolErr == nil && ok && (reason == brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE || reason == brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_INCARNATION_LOST)
}

func clientError(err error) error {
	if err == nil {
		return nil
	}
	reason, _, ok, protocolErr := brokerReason(err)
	if protocolErr != nil {
		return protocolErr
	}
	if !ok {
		return err
	}
	switch reason {
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE:
		return mcpbroker.ErrStateUnavailable
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_INCARNATION_LOST:
		return errors.Join(mcpbroker.ErrStateUnavailable, mcpbroker.ErrBrokerIncarnationLost)
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_ATTACHMENT_CLOSED:
		return mcpbroker.ErrAttachmentClosed
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_AUTHORIZATION_NOT_FOUND:
		return mcpbroker.ErrAuthorizationNotFound
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED:
		return mcpbroker.ErrCapacity
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_DISPATCH_NOT_STARTED:
		return err
	default:
		return errors.New("mcpbrokergrpc: unknown broker error reason")
	}
}

const (
	maxInvocationCallIDBytes = 256
	maxInvocationNameBytes   = 256
	maxInvocationItemIDBytes = 1024
	maxInvocationArgsBytes   = 256 << 10
)

func callFrom(name, id string, args []byte, item string) (session.ToolCall, error) {
	if !validInvocationText(name, maxInvocationNameBytes) || !validInvocationText(id, maxInvocationCallIDBytes) || (item != "" && !validInvocationText(item, maxInvocationItemIDBytes)) || len(args) == 0 || len(args) > maxInvocationArgsBytes || !json.Valid(args) {
		return session.ToolCall{}, errors.New("mcpbrokergrpc: malformed invocation")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(args, &object) != nil {
		return session.ToolCall{}, errors.New("mcpbrokergrpc: malformed invocation")
	}
	return session.ToolCall{ID: session.ToolCallID(id), Name: name, Args: append([]byte(nil), args...), ItemID: item}, nil
}

func validInvocationText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
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

type workspaceResultResponse interface {
	GetRef() *brokerv1.WorkspaceRef
	GetStatus() string
	GetTools() []*brokerv1.ToolDescriptor
}

func workspaceResultFromWire(c *Client, handle, incarnation string, r workspaceResultResponse) (mcpbroker.WorkspaceEnrollmentResult, error) {
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
