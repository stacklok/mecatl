package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// MCPAuthorizationControl is safe caller correlation. It carries no success
// assertion, browser code, credential, binding, or tool arguments.
type MCPAuthorizationControl struct {
	SessionID       session.SessionID
	AuthorizationID string
}

// MCPAuthorizationResult is the concrete domain outcome observed by a control.
// Run is non-nil only when that control won and registered a continuation.
type MCPAuthorizationResult struct {
	Status session.AuthorizationStatus
	Event  session.Event
	Run    *agent.Run
}

const (
	maxAuthorizationExpiryRetries = 3
	authorizationExpiryRetryDelay = time.Second
)

// The MCP authorization controls refuse at four distinct gates that all map to
// codes.NotFound. They name the actual reason instead of reusing ErrNotFound's
// bare "session not found", which pointed every prior debugger at session
// lookup when the real cause was an expired or already-resolved authorization.
// The session gate deliberately covers absent and foreign sessions with ONE
// message, so ownership stays undisclosed.
var (
	errAuthorizationSessionUnavailable = fmt.Errorf("%w: session is unavailable to this caller", ErrNotFound)
	errAuthorizationNoPending          = fmt.Errorf("%w: no pending MCP authorization matches this request", ErrNotFound)
	errAuthorizationExpired            = fmt.Errorf("%w: the pending MCP authorization has expired", ErrNotFound)
	errAuthorizationNotPending         = fmt.Errorf("%w: the MCP authorization is no longer pending", ErrNotFound)
	errAuthorizationUnclaimable        = fmt.Errorf("%w: the pending MCP authorization could not be claimed", ErrNotFound)
)

// brokerStateLost reports a genuinely unavailable broker transaction, which a
// live control resolves as AuthorizationInterrupted. A binding mismatch is
// excluded: the broker is present but is a different incarnation, which is a
// hard precondition failure the caller must see, not a soft interruption.
func brokerStateLost(err error) bool {
	return errors.Is(err, brokercontract.ErrStateUnavailable) && !errors.Is(err, ErrBrokerBindingMismatch)
}

type authorizationExpiry struct {
	timer   AuthorizationTimer
	retries int
}

// MCPAuthorizationPresentation returns a live browser URL only after owner
// authorization and an authoritative lock/lease protected reload.
func (s *Service) MCPAuthorizationPresentation(ctx context.Context, id session.SessionID, control MCPAuthorizationControl) (string, error) {
	if _, err := s.GetSession(ctx, id); err != nil { // before caller-selected lock
		return "", errAuthorizationSessionUnavailable
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if err := s.acquireLease(ctx, id); err != nil {
		return "", err
	}
	sess, pending, err := s.loadMatchingAuthorization(ctx, id, control)
	if err != nil {
		return "", err
	}
	if !pending.Authorization.ExpiresAt.After(s.cfg.Now()) {
		return "", errAuthorizationExpired
	}
	attachment, release, err := s.authorizationAttachment(ctx, sess)
	if err != nil {
		return "", err
	}
	defer release()
	status, err := attachment.AuthorizationStatus(ctx, pending.Authorization)
	if err != nil {
		return "", err
	}
	if status != session.AuthorizationPending {
		return "", errAuthorizationNotPending
	}
	url, err := attachment.PresentAuthorization(ctx, pending.Authorization)
	if err != nil {
		return "", err
	}
	return url, nil
}

// RecheckMCPAuthorization observes the exact broker transaction. Pending is
// inert; granted and terminal outcomes have exactly one continuation winner.
func (s *Service) RecheckMCPAuthorization(ctx context.Context, id session.SessionID, control MCPAuthorizationControl) (MCPAuthorizationResult, error) {
	if _, err := s.GetSession(ctx, id); err != nil { // owner check before lock
		return MCPAuthorizationResult{}, errAuthorizationSessionUnavailable
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if err := s.acquireLease(ctx, id); err != nil {
		return MCPAuthorizationResult{}, err
	}
	sess, pending, err := s.loadMatchingAuthorization(ctx, id, control)
	if err != nil {
		return MCPAuthorizationResult{}, err
	}
	attachment, release, err := s.authorizationAttachment(ctx, sess)
	if err != nil {
		if brokerStateLost(err) {
			return s.resolveAuthorizationLocked(ctx, sess, pending, session.AuthorizationInterrupted)
		}
		return MCPAuthorizationResult{}, err
	}
	status, statusErr := attachment.AuthorizationStatus(ctx, pending.Authorization)
	if statusErr != nil {
		release()
		if brokerStateLost(statusErr) {
			return s.resolveAuthorizationLocked(ctx, sess, pending, session.AuthorizationInterrupted)
		}
		return MCPAuthorizationResult{}, statusErr
	}
	if status == session.AuthorizationPending && !pending.Authorization.ExpiresAt.After(s.cfg.Now()) {
		outcome, cancelErr := attachment.CancelAuthorization(ctx, pending.Authorization)
		if cancelErr != nil {
			release()
			return MCPAuthorizationResult{}, cancelErr
		}
		switch outcome {
		case brokercontract.CancelCancelled:
			status = session.AuthorizationExpired
		case brokercontract.CancelAlreadyCancelled:
			status = session.AuthorizationCancelled
		case brokercontract.CancelAlreadyResolved:
			status, statusErr = attachment.AuthorizationStatus(ctx, pending.Authorization)
			if statusErr == nil && status == session.AuthorizationPending {
				statusErr = fmt.Errorf("%w: resolved authorization remained pending", ErrFailedPrecondition)
			}
		default:
			release()
			return MCPAuthorizationResult{}, fmt.Errorf("%w: unknown cancellation outcome", ErrFailedPrecondition)
		}
	}
	release()
	if statusErr != nil {
		return MCPAuthorizationResult{}, statusErr
	}
	return s.applyAuthorizationStatusLocked(ctx, sess, pending, status)
}

// CancelMCPAuthorization precisely cancels and resolves one pending control.
func (s *Service) CancelMCPAuthorization(ctx context.Context, id session.SessionID, control MCPAuthorizationControl) (MCPAuthorizationResult, error) {
	if _, err := s.GetSession(ctx, id); err != nil { // owner check before lock
		return MCPAuthorizationResult{}, errAuthorizationSessionUnavailable
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if err := s.acquireLease(ctx, id); err != nil {
		return MCPAuthorizationResult{}, err
	}
	sess, pending, err := s.loadMatchingAuthorization(ctx, id, control)
	if err != nil {
		return MCPAuthorizationResult{}, err
	}
	attachment, release, attachErr := s.authorizationAttachment(ctx, sess)
	if attachErr != nil {
		if brokerStateLost(attachErr) {
			return s.resolveAuthorizationLocked(ctx, sess, pending, session.AuthorizationInterrupted)
		}
		return MCPAuthorizationResult{}, attachErr
	}
	outcome, cancelErr := attachment.CancelAuthorization(ctx, pending.Authorization)
	if cancelErr != nil {
		release()
		if brokerStateLost(cancelErr) {
			return s.resolveAuthorizationLocked(ctx, sess, pending, session.AuthorizationInterrupted)
		}
		return MCPAuthorizationResult{}, cancelErr
	}
	var status session.AuthorizationStatus
	switch outcome {
	case brokercontract.CancelCancelled, brokercontract.CancelAlreadyCancelled:
		status = session.AuthorizationCancelled
	case brokercontract.CancelAlreadyResolved:
		status, cancelErr = attachment.AuthorizationStatus(ctx, pending.Authorization)
		if cancelErr == nil && status == session.AuthorizationPending {
			cancelErr = fmt.Errorf("%w: resolved authorization remained pending", ErrFailedPrecondition)
		}
	default:
		release()
		return MCPAuthorizationResult{}, fmt.Errorf("%w: unknown cancellation outcome", ErrFailedPrecondition)
	}
	release()
	if cancelErr != nil {
		return MCPAuthorizationResult{}, cancelErr
	}
	return s.applyAuthorizationStatusLocked(ctx, sess, pending, status)
}

func mcpAuthorizationResult(pending session.PendingAuthorization, status session.AuthorizationStatus, run *agent.Run) MCPAuthorizationResult {
	typ := session.EvAuthorizationResolved
	if status == session.AuthorizationPending {
		typ = session.EvAuthorizationRequired
	}
	return MCPAuthorizationResult{Status: status, Event: session.Event{Type: typ, Authorization: &session.AuthorizationPayload{
		AuthorizationID: pending.Authorization.ID,
		DisplayName:     pending.Authorization.DisplayName,
		Call:            pending.Call.ID,
		ExpiresAt:       pending.Authorization.ExpiresAt,
		Status:          status,
	}}, Run: run}
}

func (s *Service) applyAuthorizationStatusLocked(ctx context.Context, sess *session.Session, pending session.PendingAuthorization, status session.AuthorizationStatus) (MCPAuthorizationResult, error) {
	if err := ctx.Err(); err != nil {
		return MCPAuthorizationResult{}, err
	}
	switch status {
	case session.AuthorizationPending:
		return mcpAuthorizationResult(pending, status, nil), nil
	case session.AuthorizationGranted:
		return s.continueGrantedAuthorizationLocked(ctx, sess)
	case session.AuthorizationDenied, session.AuthorizationCancelled, session.AuthorizationExpired,
		session.AuthorizationInterrupted, session.AuthorizationFailed, session.AuthorizationClosed:
		return s.resolveAuthorizationLocked(ctx, sess, pending, status)
	default:
		return MCPAuthorizationResult{}, fmt.Errorf("%w: unknown authorization status", ErrFailedPrecondition)
	}
}

func (s *Service) loadMatchingAuthorization(ctx context.Context, id session.SessionID, control MCPAuthorizationControl) (*session.Session, session.PendingAuthorization, error) {
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, session.PendingAuthorization{}, errAuthorizationSessionUnavailable
	}
	pending, ok := sess.PendingAuthorization()
	if !ok || control.SessionID != id || pending.Authorization.ID != control.AuthorizationID {
		return nil, session.PendingAuthorization{}, errAuthorizationNoPending
	}
	return sess, pending, nil
}

func matchingAuthorization(sess *session.Session, control MCPAuthorizationControl) (session.PendingAuthorization, bool) {
	pending, ok := sess.PendingAuthorization()
	return pending, ok && control.SessionID == sess.ID && pending.Authorization.ID == control.AuthorizationID
}

func (s *Service) recheckExpiredAuthorizationLocked(ctx context.Context, sess *session.Session, pending session.PendingAuthorization) (MCPAuthorizationResult, error) {
	attachment, release, err := s.authorizationAttachment(ctx, sess)
	if err != nil {
		if brokerStateLost(err) {
			return s.resolveAuthorizationLocked(ctx, sess, pending, session.AuthorizationInterrupted)
		}
		return MCPAuthorizationResult{}, err
	}
	status, err := attachment.AuthorizationStatus(ctx, pending.Authorization)
	if err == nil && status == session.AuthorizationPending {
		outcome, cancelErr := attachment.CancelAuthorization(ctx, pending.Authorization)
		err = cancelErr
		if err == nil {
			switch outcome {
			case brokercontract.CancelCancelled:
				status = session.AuthorizationExpired
			case brokercontract.CancelAlreadyCancelled:
				status = session.AuthorizationCancelled
			case brokercontract.CancelAlreadyResolved:
				status, err = attachment.AuthorizationStatus(ctx, pending.Authorization)
				if err == nil && status == session.AuthorizationPending {
					err = fmt.Errorf("%w: resolved authorization remained pending", ErrFailedPrecondition)
				}
			default:
				err = fmt.Errorf("%w: unknown cancellation outcome", ErrFailedPrecondition)
			}
		}
	}
	release()
	if err != nil {
		if brokerStateLost(err) {
			return s.resolveAuthorizationLocked(ctx, sess, pending, session.AuthorizationInterrupted)
		}
		return MCPAuthorizationResult{}, err
	}
	return s.applyAuthorizationStatusLocked(ctx, sess, pending, status)
}

// authorizationAttachment returns a committed exact-binding attachment while
// holding brokerMu until release. Callers must release before engine rebuilding.
func (s *Service) authorizationAttachment(ctx context.Context, sess *session.Session) (brokercontract.Attachment, func(), error) {
	unlock := s.brokerMu.lock(sess.ID)
	local, err := s.openBrokerAttachment(ctx, sess.ID, sess.ExternalBinding, true)
	if err != nil {
		unlock()
		return nil, func() {}, err
	}
	committed := false
	if err := s.commitBrokerAttachment(ctx, sess.ID, local); err != nil {
		s.finalizeBrokerAttachment(local, &committed)
		unlock()
		return nil, func() {}, err
	}
	committed = true
	return local.attachment, unlock, nil
}

func (s *Service) continueGrantedAuthorizationLocked(ctx context.Context, sess *session.Session) (MCPAuthorizationResult, error) {
	engine, env, err := s.engineAndEnvironmentFor(ctx, sess)
	if err != nil {
		return MCPAuthorizationResult{}, fmt.Errorf("%w: continuation engine: %v", ErrFailedPrecondition, err)
	}
	resolution, err := session.NewAuthorizationResolution(session.AuthorizationGranted)
	if err != nil {
		return MCPAuthorizationResult{}, fmt.Errorf("%w: construct granted authorization resolution", ErrInternal)
	}
	claimed, err := sess.ClaimAuthorization()
	if err != nil {
		return MCPAuthorizationResult{}, errAuthorizationUnclaimable
	}
	if err := s.saveSession(ctx, sess); err != nil {
		_ = sess.RestoreAuthorizationClaim(claimed)
		return MCPAuthorizationResult{}, fmt.Errorf("%w: persist authorization claim", ErrInternal)
	}
	s.stopAuthorizationExpiry(sess.ID)
	prepared, err := engine.PrepareAuthorizationContinuation(memory.WithWorkspace(ctx, env.Workspace().Root()), sess, env, claimed, resolution)
	if err != nil {
		if restoreErr := s.restoreAuthorizationClaim(ctx, sess, claimed); restoreErr != nil {
			return MCPAuthorizationResult{}, fmt.Errorf("%w: prepare granted authorization continuation: %v; restore claim: %v", ErrInternal, err, restoreErr)
		}
		return MCPAuthorizationResult{}, fmt.Errorf("%w: prepare granted authorization continuation", ErrInternal)
	}
	if err := s.registerAndStartGrantedAuthorization(ctx, sess, claimed, prepared); err != nil {
		return MCPAuthorizationResult{}, err
	}
	return mcpAuthorizationResult(claimed, session.AuthorizationGranted, prepared.Run()), nil
}

// registerAndStartGrantedAuthorization keeps a prepared continuation inert until
// it is registered. Stopping the cancellation callback is the irreversible
// handoff: cancellation that reaches that point first aborts and restores the
// claim; cancellation after it loses to the registered continuation.
func (s *Service) registerAndStartGrantedAuthorization(ctx context.Context, sess *session.Session, claimed session.PendingAuthorization, prepared *agent.PreparedRun) error {
	var handoff sync.Mutex
	abortOnCancel := context.AfterFunc(ctx, func() {
		handoff.Lock()
		prepared.Abort()
		handoff.Unlock()
	})
	waitForCancellation := func() {
		abortOnCancel()
		handoff.Unlock()
		handoff.Lock()
		handoff.Unlock() //nolint:staticcheck // deliberate empty critical section: wait for the AfterFunc callback's own lock/unlock to complete
		prepared.Abort()
	}

	handoff.Lock()
	if err := ctx.Err(); err != nil {
		waitForCancellation()
		if restoreErr := s.restoreAuthorizationClaim(context.WithoutCancel(ctx), sess, claimed); restoreErr != nil {
			return fmt.Errorf("%w: restore cancelled authorization claim: %v", ErrInternal, restoreErr)
		}
		return err
	}
	if !s.registerPrepared(sess.ID, prepared.Run(), sess) {
		waitForCancellation()
		if err := s.restoreAuthorizationClaim(context.WithoutCancel(ctx), sess, claimed); err != nil {
			return fmt.Errorf("%w: restore unregistered authorization claim: %v", ErrInternal, err)
		}
		return ErrNoActiveRun
	}
	if hook := s.beforeAuthorizationContinuationStart; hook != nil {
		hook()
	}
	if !abortOnCancel() {
		// The cancellation callback is either running or has run. Release the
		// handoff lock and reacquire it to wait for its inert abort before cleanup.
		handoff.Unlock()
		handoff.Lock()
		handoff.Unlock() //nolint:staticcheck // deliberate empty critical section: wait for the AfterFunc callback's own lock/unlock to complete
		s.deregister(sess.ID, prepared.Run())
		if restoreErr := s.restoreAuthorizationClaim(context.WithoutCancel(ctx), sess, claimed); restoreErr != nil {
			return fmt.Errorf("%w: restore cancelled authorization claim: %v", ErrInternal, restoreErr)
		}
		return ctx.Err()
	}
	_, transition := prepared.Start()
	handoff.Unlock()
	if transition != agent.PreparedRunStarted {
		s.deregister(sess.ID, prepared.Run())
		s.repairAuthorizationRegistration(ctx, sess)
		return fmt.Errorf("%w: start authorization continuation: %s", ErrInternal, transition)
	}
	s.stopAuthorizationExpiry(sess.ID)
	return nil
}

func (s *Service) resolveAuthorizationLocked(ctx context.Context, sess *session.Session, pending session.PendingAuthorization, status session.AuthorizationStatus) (MCPAuthorizationResult, error) {
	reason := string(status)
	if status == session.AuthorizationClosed {
		reason = string(session.AuthorizationInterrupted)
		status = session.AuthorizationInterrupted
	}
	resolution, err := session.NewAuthorizationResolution(status)
	if err != nil {
		return MCPAuthorizationResult{}, fmt.Errorf("%w: invalid terminal authorization status %q", ErrInternal, status)
	}
	results, err := sess.AbortAuthorization(reason)
	if err != nil {
		return MCPAuthorizationResult{}, ErrNotFound
	}
	if err := sess.RecordToolResults(results); err != nil {
		return MCPAuthorizationResult{}, fmt.Errorf("%w: record authorization resolution", ErrInternal)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		return MCPAuthorizationResult{}, fmt.Errorf("%w: persist authorization resolution", ErrInternal)
	}
	s.stopAuthorizationExpiry(sess.ID)
	engine, env, err := s.engineAndEnvironmentFor(ctx, sess)
	if err != nil {
		// Engine/environment reconstruction is not required to make a terminal
		// authorization lifecycle reconstructable. The snapshot is already settled;
		// append its exact results and resolution in order without replaying the
		// protected mutation. A future remote attachment can avoid this fallback.
		if appendErr := s.appendAuthorizationResolution(ctx, sess.ID, pending, results, status); appendErr != nil {
			s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "persist terminal authorization lifecycle failed",
				"session", string(sess.ID), "status", string(status), "continuation_err", err.Error(), "err", appendErr.Error())
			return MCPAuthorizationResult{}, fmt.Errorf("%w: continuation unavailable (%v); persist terminal authorization lifecycle: %v", ErrInternal, err, appendErr)
		}
		return mcpAuthorizationResult(pending, status, nil), nil
	}
	prepared, err := engine.PrepareAfterAuthorization(memory.WithWorkspace(ctx, env.Workspace().Root()), sess, env, pending.Authorization, pending.Call.ID, results, resolution)
	if err != nil {
		return MCPAuthorizationResult{}, fmt.Errorf("%w: prepare terminal authorization continuation", ErrInternal)
	}
	if err := s.registerAndStartAuthorizationResolution(ctx, sess, prepared); err != nil {
		return MCPAuthorizationResult{}, err
	}
	return mcpAuthorizationResult(pending, status, prepared.Run()), nil
}

func (s *Service) registerAndStartAuthorizationResolution(ctx context.Context, sess *session.Session, prepared *agent.PreparedRun) error {
	var handoff sync.Mutex
	abortOnCancel := context.AfterFunc(ctx, func() {
		handoff.Lock()
		prepared.Abort()
		handoff.Unlock()
	})
	waitForCancellation := func() {
		abortOnCancel()
		handoff.Unlock()
		handoff.Lock()
		handoff.Unlock() //nolint:staticcheck // deliberate empty critical section: wait for the AfterFunc callback's own lock/unlock to complete
		prepared.Abort()
	}

	handoff.Lock()
	if err := ctx.Err(); err != nil {
		waitForCancellation()
		return err
	}
	if !s.registerPrepared(sess.ID, prepared.Run(), sess) {
		waitForCancellation()
		s.repairAuthorizationRegistration(ctx, sess)
		return ErrNoActiveRun
	}
	if hook := s.beforeAuthorizationContinuationStart; hook != nil {
		hook()
	}
	if !abortOnCancel() {
		// See the granted path: this waits for the abort that won the handoff
		// before removing the relay-visible run.
		handoff.Unlock()
		handoff.Lock()
		handoff.Unlock() //nolint:staticcheck // deliberate empty critical section: wait for the AfterFunc callback's own lock/unlock to complete
		s.deregister(sess.ID, prepared.Run())
		s.repairAuthorizationRegistration(ctx, sess)
		return ctx.Err()
	}
	_, transition := prepared.Start()
	handoff.Unlock()
	if transition != agent.PreparedRunStarted {
		s.deregister(sess.ID, prepared.Run())
		s.repairAuthorizationRegistration(ctx, sess)
		return fmt.Errorf("%w: start authorization resolution: %s", ErrInternal, transition)
	}
	return nil
}

// appendAuthorizationResolution is the no-continuation fallback for an already
// settled snapshot. It follows ordinary relay persistence semantics: each event
// is appended at most once, in order, on a cancellation-detached context. It
// deliberately does not retry because EventLog.Append may report a post-write
// failure; retrying such an ambiguous append could duplicate a lifecycle event.
// Snapshot and event-log persistence are not transactional, so callers must
// surface and diagnose any failure rather than report settled success.
func (s *Service) appendAuthorizationResolution(ctx context.Context, id session.SessionID, pending session.PendingAuthorization, results []session.ToolResult, status session.AuthorizationStatus) error {
	appendCtx := context.WithoutCancel(ctx)
	for i := range results {
		ev := session.Event{Type: session.EvToolResult, ToolResult: &results[i]}
		if err := s.appendEvent(appendCtx, id, ev); err != nil {
			return err
		}
		s.PublishSessionEvent(id, ev)
	}
	ev := session.Event{Type: session.EvAuthorizationResolved, Authorization: &session.AuthorizationPayload{
		AuthorizationID: pending.Authorization.ID,
		DisplayName:     pending.Authorization.DisplayName,
		Call:            pending.Call.ID,
		ExpiresAt:       pending.Authorization.ExpiresAt,
		Status:          status,
	}}
	if err := s.appendEvent(appendCtx, id, ev); err != nil {
		return err
	}
	s.PublishSessionEvent(id, ev)
	return nil
}

func (s *Service) restoreAuthorizationClaim(ctx context.Context, sess *session.Session, pending session.PendingAuthorization) error {
	if err := sess.RestoreAuthorizationClaim(pending); err != nil {
		return err
	}
	return s.saveSession(ctx, sess)
}

func (s *Service) repairAuthorizationRegistration(ctx context.Context, sess *session.Session) {
	if err := sess.Abandon(); err == nil {
		_ = s.saveSession(context.WithoutCancel(ctx), sess)
	}
	s.stopAuthorizationExpiry(sess.ID)
}

func (s *Service) registerPrepared(id session.SessionID, run *agent.Run, sess *session.Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if existing := s.runs[id]; existing != nil && existing.run.Outcome() != agent.RunOutcomeAuthorizationPending {
		return false
	}
	s.runs[id] = &runState{run: run, sess: sess, settled: make(chan struct{})}
	delete(s.steerMsgIDs, id)
	return true
}

func (s *Service) scheduleAuthorizationExpiry(id session.SessionID) {
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	sess, err := s.cfg.Store.Load(context.Background(), id)
	if err != nil {
		return
	}
	pending, ok := sess.PendingAuthorization()
	if !ok {
		return
	}
	delay := pending.Authorization.ExpiresAt.Sub(s.cfg.Now())
	if delay < 0 {
		delay = 0
	}
	entry := &authorizationExpiry{}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	old := s.authorizationExpiry[id]
	s.authorizationExpiry[id] = entry
	s.mu.Unlock()
	if old != nil && old.timer != nil {
		old.timer.Stop()
	}
	timer := s.cfg.AuthorizationTimer(delay, func() { s.expireAuthorization(id, pending.Authorization.ID, entry) })
	s.mu.Lock()
	if s.authorizationExpiry[id] == entry && !s.closed {
		entry.timer = timer
		timer = nil
	}
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (s *Service) stopAuthorizationExpiry(id session.SessionID) {
	s.mu.Lock()
	entry := s.authorizationExpiry[id]
	delete(s.authorizationExpiry, id)
	s.mu.Unlock()
	if entry != nil && entry.timer != nil {
		entry.timer.Stop()
	}
}

func (s *Service) expireAuthorization(id session.SessionID, authorizationID string, entry *authorizationExpiry) {
	s.mu.Lock()
	if s.closed || s.authorizationExpiry[id] != entry {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if err := s.acquireLease(context.Background(), id); err != nil {
		s.retryAuthorizationExpiry(id, authorizationID, entry, err)
		return
	}
	sess, err := s.cfg.Store.Load(context.Background(), id)
	if err != nil {
		s.retryAuthorizationExpiry(id, authorizationID, entry, err)
		return
	}
	pending, ok := matchingAuthorization(sess, MCPAuthorizationControl{SessionID: id, AuthorizationID: authorizationID})
	if !ok {
		s.clearAuthorizationExpiry(id, entry)
		return
	}
	if delay := pending.Authorization.ExpiresAt.Sub(s.cfg.Now()); delay > 0 {
		s.replaceAuthorizationExpiryTimer(id, authorizationID, entry, delay)
		return
	}
	result, err := s.recheckExpiredAuthorizationLocked(context.Background(), sess, pending)
	if err != nil {
		s.retryAuthorizationExpiry(id, authorizationID, entry, err)
		return
	}
	if result.Run != nil {
		go s.drainAuthorizationContinuation(id, result.Run)
	}
}

func (s *Service) retryAuthorizationExpiry(id session.SessionID, authorizationID string, entry *authorizationExpiry, cause error) {
	s.mu.Lock()
	if s.closed || s.authorizationExpiry[id] != entry {
		s.mu.Unlock()
		return
	}
	if entry.retries >= maxAuthorizationExpiryRetries {
		entry.timer = nil
		s.mu.Unlock()
		s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "external authorization expiry retries exhausted",
			"session", string(id), "err", cause.Error())
		return
	}
	entry.retries++
	s.mu.Unlock()
	s.replaceAuthorizationExpiryTimer(id, authorizationID, entry, authorizationExpiryRetryDelay)
}

func (s *Service) replaceAuthorizationExpiryTimer(id session.SessionID, authorizationID string, entry *authorizationExpiry, delay time.Duration) {
	timer := s.cfg.AuthorizationTimer(delay, func() { s.expireAuthorization(id, authorizationID, entry) })
	s.mu.Lock()
	if s.closed || s.authorizationExpiry[id] != entry {
		s.mu.Unlock()
		timer.Stop()
		return
	}
	old := entry.timer
	entry.timer = timer
	s.mu.Unlock()
	if old != nil && old != timer {
		old.Stop()
	}
}

func (s *Service) clearAuthorizationExpiry(id session.SessionID, entry *authorizationExpiry) {
	s.mu.Lock()
	if s.authorizationExpiry[id] == entry {
		delete(s.authorizationExpiry, id)
	}
	s.mu.Unlock()
}

func (s *Service) drainAuthorizationContinuation(id session.SessionID, run *agent.Run) {
	ctx := context.Background()
	recorder := NewRunEventRecorder(ctx, s, id)
	defer recorder.Close()
	for ev := range run.Events() {
		s.relayEvent(ctx, id, ev, false, recorder)
		s.PublishSessionEvent(id, ev)
	}
	s.FinishRun(id, run)
}

func (s *Service) invalidateLocalAuthorization(ctx context.Context, id session.SessionID) {
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		return
	}
	pending, ok := sess.PendingAuthorization()
	if !ok {
		return
	}
	unlock := s.brokerMu.lock(id)
	s.mu.Lock()
	attachment := s.brokerAttachments[id]
	s.mu.Unlock()
	if attachment != nil {
		_, _ = attachment.CancelAuthorization(ctx, pending.Authorization)
	}
	unlock()
}

// interruptRestoredAuthorizationLocked repairs authorizing state whose exact
// process-local transaction is unavailable. The caller holds runEntryMu + lease.
func (s *Service) interruptRestoredAuthorizationLocked(ctx context.Context, sess *session.Session) (*session.Session, bool, error) {
	loaded, err := s.cfg.Store.Load(ctx, sess.ID)
	if err != nil {
		return nil, false, ErrNotFound
	}
	sess = loaded
	if sess.State != session.StateAuthorizing {
		return sess, false, nil
	}
	pending, ok := sess.PendingAuthorization()
	if !ok {
		return nil, false, fmt.Errorf("%w: invalid authorizing session", ErrFailedPrecondition)
	}
	attachment, release, err := s.authorizationAttachment(ctx, sess)
	if err != nil {
		if !errors.Is(err, brokercontract.ErrStateUnavailable) {
			return nil, false, err
		}
	} else {
		_, statusErr := attachment.AuthorizationStatus(ctx, pending.Authorization)
		release()
		if statusErr == nil {
			return nil, false, fmt.Errorf("%w: session %q has a live external authorization", ErrFailedPrecondition, sess.ID)
		}
		if !errors.Is(statusErr, brokercontract.ErrStateUnavailable) {
			return nil, false, statusErr
		}
	}
	results, err := sess.InterruptAuthorization()
	if err != nil || sess.RecordToolResults(results) != nil {
		return nil, false, fmt.Errorf("%w: interrupt restored authorization", ErrInternal)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		return nil, false, fmt.Errorf("%w: persist interrupted authorization", ErrInternal)
	}
	if err := s.appendAuthorizationResolution(ctx, sess.ID, pending, results, session.AuthorizationInterrupted); err != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "persist interrupted authorization lifecycle failed",
			"session", string(sess.ID), "err", err.Error())
		return nil, false, fmt.Errorf("%w: persist interrupted authorization lifecycle: %v", ErrInternal, err)
	}
	return sess, true, nil
}

func (s *Service) prepareAuthorizationClose() bool {
	s.mu.Lock()
	s.closed = true
	ids := make(map[session.SessionID]struct{})
	for id := range s.brokerAttachments {
		ids[id] = struct{}{}
	}
	for id := range s.authorizationExpiry {
		ids[id] = struct{}{}
	}
	for id := range s.heldLeases {
		ids[id] = struct{}{}
	}
	s.mu.Unlock()

	settled := true
	for id := range ids {
		unlock := s.runEntryMu.lock(id)
		if err := s.reaffirmLease(context.Background(), id); err != nil {
			s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "service shutdown authorization settlement deferred",
				"session", string(id), "err", err.Error())
			settled = false
		} else if err := s.settleAuthorizationLocked(context.Background(), id); err != nil {
			settled = false
		}
		unlock()
	}
	if !settled {
		return false
	}
	s.mu.Lock()
	expiries := s.authorizationExpiry
	s.authorizationExpiry = make(map[session.SessionID]*authorizationExpiry)
	s.mu.Unlock()
	for _, entry := range expiries {
		if entry.timer != nil {
			entry.timer.Stop()
		}
	}
	return true
}

// settleAuthorizationLocked pairs a parked call during close/shutdown. The
// caller holds runEntryMu and has acquired the session lease. Any failure keeps
// the local attachment and lease available for a later Close retry.
func (s *Service) settleAuthorizationLocked(ctx context.Context, id session.SessionID) error {
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			return nil
		}
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "load external authorization for settlement failed",
			"session", string(id), "err", err.Error())
		return err
	}
	if sess.State != session.StateAuthorizing {
		return nil
	}
	pending, ok := sess.PendingAuthorization()
	if !ok {
		return fmt.Errorf("%w: invalid authorizing session", ErrFailedPrecondition)
	}
	attachment, release, attachErr := s.authorizationAttachment(ctx, sess)
	if attachErr == nil {
		_, err = attachment.CancelAuthorization(ctx, pending.Authorization)
		release()
		if err != nil && !errors.Is(err, brokercontract.ErrStateUnavailable) {
			s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "cancel external authorization during settlement failed",
				"session", string(id), "err", err.Error())
			return err
		}
	} else if !errors.Is(attachErr, brokercontract.ErrStateUnavailable) {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "attach external authorization for settlement failed",
			"session", string(id), "err", attachErr.Error())
		return attachErr
	}
	results, err := sess.InterruptAuthorization()
	if err != nil {
		return err
	}
	if err := sess.RecordToolResults(results); err != nil {
		return err
	}
	if err := s.saveSession(context.WithoutCancel(ctx), sess); err != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "persist external authorization settlement failed",
			"session", string(id), "err", err.Error())
		return err
	}
	if err := s.appendAuthorizationResolution(ctx, id, pending, results, session.AuthorizationInterrupted); err != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "persist external authorization settlement lifecycle failed",
			"session", string(id), "err", err.Error())
		return err
	}
	return nil
}
