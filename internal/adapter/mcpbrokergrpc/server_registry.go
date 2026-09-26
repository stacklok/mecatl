package mcpbrokergrpc

import (
	"context"
	"crypto/sha256"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

// lifecycleOperation identifies the two operations that close a session handle.
// Commit is not terminal: the caller may continue using the committed session handle.
type lifecycleOperation uint8

const (
	lifecycleNone lifecycleOperation = iota
	lifecycleAbort
	lifecycleClose
)

// executeReceipt lets retries of the same call join its execution or replay its
// result rather than dispatching the tool again. Authorization may reserve one
// before Execute arrives. Server.mu protects updates; closing done publishes the
// immutable response and err to waiters.
type executeReceipt struct {
	digest   [sha256.Size]byte         // Detects reuse of a call ID with different invocation content.
	bytes    int                       // Reserved or retained bytes charged to the handle's budget.
	started  bool                      // Execute has claimed the receipt, even if capacity later rejects it.
	done     chan struct{}             // Closed when the terminal response or error is available.
	response *brokerv1.ExecuteResponse // Retained wire result, if execution produced one.
	err      error                     // Retained RPC error, if the invocation failed at the transport layer.
}

// sessionOwner keeps a logical session bound to the same authenticated workload
// across handle closure and reattachment. It is not the logical session itself.
// Server.mu protects its fields.
type sessionOwner struct {
	principal session.Principal // Workload identity allowed to attach to this session ID.
	pending   int               // Admitted Attach calls that have not yet succeeded or failed.
	handles   int               // Published session handles not yet terminal or reclaimed.
	published bool              // A handle was published; retain ownership even after all handles close.
	expiresAt time.Time         // Earliest retirement time; pending calls and open handles postpone it.
	retiring  bool              // Blocks Attach while the sweeper deletes the underlying session.
	changed   chan struct{}     // Closed and replaced when pending or retirement state changes.
}

// serverHandle wraps one consumer's open handle to a logical broker session
// with gRPC bookkeeping. Multiple session handles may refer to the same session;
// closing one releases that handle, whereas deleting the session invalidates all
// of them. The underlying session handle supplies tools and authorization operations.
// Server.mu protects mutable fields. Terminal entries remain in handles until
// expiry so repeated Close or Abort calls can recover the recorded outcome.
type serverHandle struct {
	sessionHandle   mcpbroker.Attachment                   // Underlying broker session handle, not an MCP network connection.
	principal       *session.Principal                     // Caller identity bound to this handle; nil for direct test usage.
	logicalID       session.SessionID                      // Logical session shared with other session handles.
	owner           *sessionOwner                          // Exact ownership entry charged by this handle.
	binding         string                                 // Opaque identity of the exact logical state, used for conditional deletion.
	tools           map[string]tool.Tool                   // Executable registry, replaced after successful workspace enrollment.
	active          int                                    // In-flight operations that keep cleanup from reclaiming the handle.
	expiresAt       time.Time                              // Absolute handle expiry set at Attach; use does not renew it.
	changed         chan struct{}                          // Closed and replaced to wake lifecycle waiters when state changes.
	receipts        map[session.ToolCallID]*executeReceipt // Invocation reservations and retained execution results.
	receiptBytes    int                                    // Total bytes charged to receipts for this handle.
	running         lifecycleOperation                     // Close or Abort currently invoking the underlying broker.
	terminal        lifecycleOperation                     // Successfully settled Close or Abort, if any.
	terminalOutcome mcpbroker.CloseOutcome                 // Outcome replayed to retries of the same terminal operation.
}

// bindSession reserves ownership while Attach calls the backing service without
// holding Server.mu. After a successful bind, the caller must invoke
// finishSessionBind whether Attach succeeds or fails.
func (s *Server) bindSession(ctx context.Context, id session.SessionID) (*session.Principal, *sessionOwner, error) {
	if !mcpbroker.ValidLogicalSessionID(id) {
		return nil, nil, invalid("session_id is invalid")
	}
	principal := session.PrincipalFromContext(ctx)
	if principal == nil {
		return nil, nil, nil // Direct adapter calls are test-only; the network boundary always installs a principal.
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := s.owners[id]
	if owner != nil && owner.retiring {
		return nil, nil, reasonStatus(codes.Unavailable, "broker session is being retired", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	if owner != nil && !principal.SameIdentity(&owner.principal) {
		return nil, nil, status.Error(codes.PermissionDenied, "broker session is not available")
	}
	if owner == nil {
		if len(s.owners) >= s.cfg.MaxOwners {
			return nil, nil, reasonStatus(codes.ResourceExhausted, "broker session capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, "")
		}
		owner = &sessionOwner{principal: *principal, expiresAt: time.Now().Add(s.cfg.OwnerRetention), changed: make(chan struct{})}
		s.owners[id] = owner
	}
	owner.pending++
	return principal.Clone(), owner, nil
}

func (s *Server) bindExistingSession(ctx context.Context, id session.SessionID) (*session.Principal, *sessionOwner, error) {
	principal := session.PrincipalFromContext(ctx)
	if principal == nil {
		return nil, nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := s.owners[id]
	if owner == nil {
		return principal.Clone(), nil, nil
	}
	if owner.retiring {
		return nil, nil, reasonStatus(codes.Unavailable, "broker session is being retired", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	if !principal.SameIdentity(&owner.principal) {
		return nil, nil, status.Error(codes.PermissionDenied, "broker session is not available")
	}
	owner.pending++
	return principal.Clone(), owner, nil
}
func signalOwner(owner *sessionOwner) {
	close(owner.changed)
	owner.changed = make(chan struct{})
}

// beginSessionDelete atomically authorizes and fences one ownership entry before
// waiting for pre-existing Attach calls to finish. A deleting owner blocks new
// binds, so an old Attach cannot publish or recreate state after the delete.
func (s *Server) beginSessionDelete(ctx context.Context, id session.SessionID) (*sessionOwner, error) {
	principal := session.PrincipalFromContext(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := s.owners[id]
	if s.closed {
		return nil, reasonStatus(codes.Unavailable, "broker state unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	if principal != nil && (owner == nil || !principal.SameIdentity(&owner.principal)) {
		return nil, status.Error(codes.PermissionDenied, "broker session is not available")
	}
	if owner == nil { // Direct adapter calls have no process-local owner to fence.
		return nil, nil
	}
	if owner.retiring {
		return nil, reasonStatus(codes.Unavailable, "broker session is being retired", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	owner.retiring = true
	signalOwner(owner)
	for {
		if err := ctx.Err(); err != nil {
			owner.retiring = false
			signalOwner(owner)
			return nil, status.FromContextError(err).Err()
		}
		if s.closed || s.owners[id] != owner {
			owner.retiring = false
			signalOwner(owner)
			return nil, reasonStatus(codes.Unavailable, "broker state unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
		}
		if owner.pending == 0 {
			return owner, nil
		}
		done := owner.changed
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		case <-s.stop:
		}
		s.mu.Lock()
	}
}

func (s *Server) finishSessionDelete(id session.SessionID, owner *sessionOwner, deleted bool) {
	if owner == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owners[id] != owner {
		return
	}
	if deleted {
		delete(s.owners, id)
		return
	}
	owner.retiring = false
	owner.expiresAt = time.Now().Add(s.cfg.OwnerRetention)
	signalOwner(owner)
}

func (s *Server) finishSessionBind(id session.SessionID, owner *sessionOwner, attached bool) {
	if owner == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner.pending > 0 {
		owner.pending--
	}
	signalOwner(owner)
	if s.owners[id] != owner {
		return
	}
	if attached {
		owner.handles++
		return
	}
	if owner.pending == 0 && owner.handles == 0 && !owner.published && !owner.retiring {
		delete(s.owners, id)
	}
	// Ownership is deliberately retained after the last handle closes. The
	// logical session's absolute retention, delete, or shutdown reclaims it.
}

func authorizeHandle(ctx context.Context, handle *serverHandle) error {
	if handle == nil || handle.principal == nil {
		return nil
	}
	principal := session.PrincipalFromContext(ctx)
	if principal == nil || !principal.SameIdentity(handle.principal) {
		return status.Error(codes.PermissionDenied, "broker attachment is not available")
	}
	return nil
}

func (s *Server) checkInstanceID(got string, allowEmpty bool) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return reasonStatus(codes.Unavailable, "broker state unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	if got == "" && allowEmpty {
		return nil
	}
	if got == "" || got != s.instanceID {
		return reasonStatus(codes.FailedPrecondition, "broker incarnation mismatch", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_INCARNATION_LOST, "")
	}
	return nil
}

// signalHandle broadcasts a state change to lifecycle waiters. The caller
// holds Server.mu so waiters cannot miss a change between checking and subscribing.
func signalHandle(a *serverHandle) {
	close(a.changed)
	a.changed = make(chan struct{})
}

// get admits an ordinary operation on an open handle and increments active so
// lifecycle operations and expiry cleanup wait for it. The caller must invoke
// the returned release function exactly once, even if its operation fails.
func (s *Server) get(ctx context.Context, instanceID, handle string) (*serverHandle, func(), error) {
	if err := s.checkInstanceID(instanceID, false); err != nil {
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
	signalHandle(a)
	s.mu.Unlock()
	return a, func() {
		s.mu.Lock()
		a.active--
		s.releaseClosedReceiptsLocked(a)
		signalHandle(a)
		s.mu.Unlock()
	}, nil
}

// beginLifecycle either replays a settled Close/Abort (the bool is true), or
// waits for current work to drain and claims the next lifecycle attempt. A new
// attempt blocks ordinary operations until its caller invokes finishLifecycle.
// Admission reserves control capacity before any waiting, including duplicate
// calls joining a running lifecycle attempt. Settled terminal replay needs no slot.
func (s *Server) beginLifecycle(ctx context.Context, instanceID, handle string, operation lifecycleOperation) (*serverHandle, mcpbroker.CloseOutcome, bool, error) {
	if err := s.checkInstanceID(instanceID, false); err != nil {
		return nil, "", false, err
	}
	if handle == "" {
		return nil, "", false, invalid("handle is required")
	}
	admitted := false
	defer func() {
		if admitted {
			s.mu.Lock()
			s.pendingControls--
			s.mu.Unlock()
		}
	}()
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
		if !admitted {
			if s.pendingControls >= s.cfg.MaxPendingControls {
				s.mu.Unlock()
				return nil, "", false, reasonStatus(codes.ResourceExhausted, "broker control capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, "")
			}
			s.pendingControls++
			admitted = true
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
		a.running = operation
		a.active++
		signalHandle(a)
		admitted = false // finishLifecycle now owns this control slot.
		s.mu.Unlock()
		return a, "", false, nil
	}
}

func releaseOwnerHandleLocked(owner *sessionOwner) {
	if owner == nil || owner.handles == 0 {
		return
	}
	owner.handles--
}

func releaseReceiptsLocked(handle *serverHandle) {
	clear(handle.receipts)
	handle.receiptBytes = 0
}

func (s *Server) releaseClosedReceiptsLocked(handle *serverHandle) {
	if s.closed && handle.active == 0 {
		releaseReceiptsLocked(handle)
	}
}

// finishLifecycle releases an attempt's active/control slots and wakes waiters.
// A terminal attempt retains its outcome for replay; a nonterminal failure leaves
// the handle open for another attempt.
func (s *Server) finishLifecycle(a *serverHandle, operation lifecycleOperation, outcome mcpbroker.CloseOutcome, terminal bool) {
	s.mu.Lock()
	if terminal {
		releaseOwnerHandleLocked(a.owner)
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
	signalHandle(a)
	s.mu.Unlock()
}

// sweep removes expired, inactive handles and selects owner entries eligible for
// retirement under Server.mu. Backing-service cleanup runs outside the lock;
// owners stay marked retiring so Attach cannot reuse their IDs during deletion.
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
			s.sweepContinuityReceiptsLocked(now)
			var expired []*serverHandle
			type ownerRetirement struct {
				id    session.SessionID
				owner *sessionOwner
			}
			var retirements []ownerRetirement
			for id, handle := range s.handles {
				if handle.active == 0 && !now.Before(handle.expiresAt) {
					releaseReceiptsLocked(handle)
					delete(s.handles, id)
					if handle.terminal == lifecycleNone {
						releaseOwnerHandleLocked(handle.owner)
						expired = append(expired, handle)
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
			for _, handle := range expired {
				s.closeHandle(handle)
			}
			for _, retirement := range retirements {
				s.retireOwner(retirement.id, retirement.owner)
			}
		}
	}
}

// retireOwner releases ownership only after backing-session deletion succeeds.
// Failure retains ownership and schedules a later retry. The pointer check keeps
// an old cleanup result from removing a replacement ownership entry.
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
		delete(s.owners, id)
		return
	}
	owner.retiring = false
	owner.expiresAt = time.Now().Add(s.cfg.SweepInterval)
}

func (s *Server) closeHandle(handle *serverHandle) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.CleanupTimeout)
	defer cancel()
	_, _ = handle.sessionHandle.Close(ctx)
}
