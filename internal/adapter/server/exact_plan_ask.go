package server

import (
	"context"
	"fmt"
	"sync"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// planContinuation owns one reserved detached relay after a plan allow. It is
// attached to the old runState until that run's terminal relay transfers it to
// the fresh proceed run. The context is independent of the unary request.
type planContinuation struct {
	ctx         context.Context
	stop        context.CancelFunc
	generation  runEntryGeneration
	planRunID   string
	askID       string
	releaseOnce sync.Once
}

func (c *planContinuation) release(s *Service) {
	c.releaseOnce.Do(func() {
		c.stop()
		s.detachedControlWG.Done()
	})
}

// ResolvePlanAsk resolves one plan-originated ask on the exact authorized run.
// It acknowledges acceptance only; durable activity carries both terminals.
func (s *Service) ResolvePlanAsk(ctx context.Context, id session.SessionID, expectedRunID, askID string, verdict session.ApprovalVerdict) (RunAskAcknowledgement, error) {
	if expectedRunID == "" || askID == "" || !validRunAskVerdict(verdict) {
		return RunAskAcknowledgement{}, fmt.Errorf("%w: expected_run_id, ask_id, and a valid verdict are required", ErrInvalidArgument)
	}
	if err := ctx.Err(); err != nil {
		return RunAskAcknowledgement{}, err
	}
	// Ownership precedes every registry or lease lookup.
	if _, err := s.GetSession(ctx, id); err != nil {
		return RunAskAcknowledgement{}, err
	}
	// An accepted allow may later need to report a known continuation-start
	// failure. Refuse before consuming the ask when that report cannot be durable.
	if s.cfg.EventLog == nil {
		return RunAskAcknowledgement{}, ErrNoEventLog
	}
	generation := s.captureRunEntryGeneration(id)
	if s.draining.Load() {
		return RunAskAcknowledgement{}, fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	s.mu.Lock()
	st := s.runs[id]
	live := st != nil && st.run != nil
	s.mu.Unlock()
	if live {
		return s.resolveLivePlanAsk(ctx, id, st, expectedRunID, askID, verdict, generation)
	}
	return s.resolvePersistedPlanAsk(ctx, id, expectedRunID, askID, verdict, generation)
}

func (s *Service) resolveLivePlanAsk(ctx context.Context, id session.SessionID, st *runState, expectedRunID, askID string, verdict session.ApprovalVerdict, generation runEntryGeneration) (RunAskAcknowledgement, error) {
	st.persistMu.Lock()
	defer st.persistMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return RunAskAcknowledgement{}, err
	}
	run, err := s.liveRunForControlLocked(id, st, expectedRunID, generation)
	if err != nil {
		return RunAskAcknowledgement{}, err
	}
	if !st.serverOwnedPlanContinuation {
		return RunAskAcknowledgement{}, fmt.Errorf("%w: live run owns its plan continuation", ErrFailedPrecondition)
	}
	if st.planContinuation != nil {
		return RunAskAcknowledgement{}, ErrAskNotPending
	}
	var continuation *planContinuation
	if verdict != session.VerdictDeny {
		if !s.reserveDetachedControlRelay() {
			return RunAskAcknowledgement{}, fmt.Errorf("%w: %q", ErrUnavailable, id)
		}
		ownedCtx, stop := s.detachedControlContext(ctx)
		continuation = &planContinuation{ctx: ownedCtx, stop: stop, generation: generation, planRunID: run.RunID(), askID: askID}
	}
	result := run.ResolvePlanAsk(askID, verdict)
	if result != agent.AskResolutionResolved {
		if continuation != nil {
			continuation.release(s)
		}
		return RunAskAcknowledgement{}, ErrAskNotPending
	}
	st.resolvedAskID = askID
	// The live relay retains the run starter's context. Only this exact
	// approval belongs to the caller of ResolvePlanAsk, including when that
	// caller has no verified principal. Detach cancellation before the unary
	// request ends so its durable append can finish independently.
	st.exactPlanApprovalCtx = context.WithoutCancel(ctx)
	st.planContinuation = continuation
	return RunAskAcknowledgement{RunID: run.RunID(), AskID: askID}, nil
}

func (s *Service) validatePersistedPlanAsk(sess *session.Session, expectedRunID, askID string) error {
	if err := checkExpectedRun(expectedRunID, sess.RunID()); err != nil {
		return err
	}
	if sess.State != session.StateAwaiting {
		return checkExpectedRun(expectedRunID, "")
	}
	pending, ok := sess.PendingAsk()
	if !ok || pending.AskID != askID || pending.Origin != session.ApprovalOriginPlan || pending.Guardrail != nil {
		return ErrAskNotPending
	}
	return s.validatePersistedWorkspace(sess)
}

// resolvePersistedPlanAsk rechecks authority after acquiring the distributed
// lease. Its request context ends at acceptance; both relays are server-owned.
//
//nolint:gocyclo // Lease, provenance, admission, and relay gates form one ordered transaction.
func (s *Service) resolvePersistedPlanAsk(ctx context.Context, id session.SessionID, expectedRunID, askID string, verdict session.ApprovalVerdict, generation runEntryGeneration) (RunAskAcknowledgement, error) {
	resumeUnlock := s.resumeMu.lock(id)
	defer resumeUnlock()
	entryUnlock := s.runEntryMu.lock(id)
	defer entryUnlock()
	if err := ctx.Err(); err != nil {
		return RunAskAcknowledgement{}, err
	}
	if s.validateRunEntryGeneration(id, generation) != nil {
		return RunAskAcknowledgement{}, checkExpectedRun(expectedRunID, "")
	}
	s.mu.Lock()
	st := s.runs[id]
	live := st != nil && st.run != nil
	s.mu.Unlock()
	if live {
		return s.resolveLivePlanAsk(ctx, id, st, expectedRunID, askID, verdict, generation)
	}
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return RunAskAcknowledgement{}, err
	}
	if err := s.validatePersistedPlanAsk(sess, expectedRunID, askID); err != nil {
		return RunAskAcknowledgement{}, err
	}
	st, admissionCtx, err := s.beginRunAdmission(ctx, id, sess, true)
	if err != nil {
		return RunAskAcknowledgement{}, err
	}
	// A restored ask has no live stream owner. Its resumed run belongs to this
	// exact control and keeps the same verdict gate until it terminates.
	st.serverOwnedPlanContinuation = true
	promoted := false
	defer s.cleanupRunAdmission(id, st, &promoted)
	if err := s.acquireLease(admissionCtx, id); err != nil {
		return RunAskAcknowledgement{}, err
	}
	requestLeaseCtx, stopRequestLease, leaseHeld := s.mutationLeaseContext(admissionCtx, id)
	defer func() {
		if !promoted {
			stopRequestLease()
		}
	}()
	// A peer may have advanced the snapshot during lease acquisition.
	sess, err = s.GetSession(requestLeaseCtx, id)
	if err != nil {
		return RunAskAcknowledgement{}, err
	}
	if err := s.validatePersistedPlanAsk(sess, expectedRunID, askID); err != nil {
		return RunAskAcknowledgement{}, err
	}
	st.persistMu.Lock()
	s.mu.Lock()
	if s.runs[id] != st || st.cancelling || st.cancelSignaled {
		s.mu.Unlock()
		st.persistMu.Unlock()
		return RunAskAcknowledgement{}, checkExpectedRun(expectedRunID, "")
	}
	st.sess = sess
	s.mu.Unlock()
	st.persistMu.Unlock()
	engine, env, err := s.engineAndEnvironmentFor(requestLeaseCtx, sess)
	if err != nil {
		return RunAskAcknowledgement{}, err
	}
	if err := s.awaitContextWindow(requestLeaseCtx, id); err != nil {
		return RunAskAcknowledgement{}, err
	}
	if err := ctx.Err(); err != nil {
		return RunAskAcknowledgement{}, err
	}
	if !leaseHeld() {
		return RunAskAcknowledgement{}, fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
	}
	ownedCtx, stopOwned := s.detachedControlContext(ctx)
	ownedLeaseCtx, stopOwnedLease, _ := s.mutationLeaseContext(ownedCtx, id)
	stopRun := func() { stopOwnedLease(); stopOwned(); stopRequestLease() }
	if !s.reserveDetachedControlRelay() {
		stopRun()
		return RunAskAcknowledgement{}, fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	planRelayReserved := true
	defer func() {
		if planRelayReserved {
			s.detachedControlWG.Done()
		}
	}()
	var continuation *planContinuation
	if verdict != session.VerdictDeny {
		if !s.reserveDetachedControlRelay() {
			stopRun()
			return RunAskAcknowledgement{}, fmt.Errorf("%w: %q", ErrUnavailable, id)
		}
		contCtx, stopCont := s.detachedControlContext(ctx)
		continuation = &planContinuation{ctx: contCtx, stop: stopCont, generation: generation, planRunID: expectedRunID, askID: askID}
	}
	continuationReserved := continuation != nil
	defer func() {
		if continuationReserved {
			continuation.release(s)
		}
	}()
	run, err := s.promoteDetachedRunAdmission(ctx, id, st, stopRun, func() *agent.Run {
		return engine.ResumeApproval(rootedRunContext(ownedLeaseCtx, sess, env), sess, env, askID, verdict)
	})
	if err != nil {
		stopRun()
		return RunAskAcknowledgement{}, err
	}
	promoted = true
	s.mu.Lock()
	st.planContinuation = continuation
	s.mu.Unlock()
	continuationReserved = false
	go func() {
		defer s.detachedControlWG.Done()
		s.relayDetachedControlRun(ownedLeaseCtx, id, run)
	}()
	planRelayReserved = false
	return RunAskAcknowledgement{RunID: expectedRunID, AskID: askID}, nil
}

// finishExactPlanRun persists and removes the terminal plan run, then transfers
// its reserved continuation and entry lock to a detached worker. The old wire
// stream can close while the fresh run waits for lease or model admission.
func (s *Service) finishExactPlanRun(ctx context.Context, id session.SessionID, run *agent.Run) bool {
	s.mu.Lock()
	st := s.runs[id]
	reserved := st != nil && st.run == run && st.planContinuation != nil
	s.mu.Unlock()
	if !reserved {
		return false
	}
	entryUnlock := s.runEntryMu.lock(id)
	s.mu.Lock()
	st = s.runs[id]
	if st == nil || st.run != run || st.planContinuation == nil {
		s.mu.Unlock()
		entryUnlock()
		return false
	}
	continuation := st.planContinuation
	st.planContinuation = nil
	s.mu.Unlock()
	s.completeRelay(ctx, id, run)
	stop, stopped := st.sess.RecordedStopReason()
	s.removeRunState(id, st)
	if !stopped || stop != session.StopPlanApproved {
		continuation.release(s)
		entryUnlock()
		return true
	}
	// The reservation was counted before verdict acceptance. Pass both that
	// ownership and the still-held run-entry lock to the detached worker. A
	// competing prompt stays behind this exact continuation even after the old
	// stream has closed; Close joins the worker through detachedControlWG.
	go s.startExactPlanContinuation(id, continuation, entryUnlock)
	return true
}

func (s *Service) startExactPlanContinuation(id session.SessionID, continuation *planContinuation, entryUnlock func()) {
	unlockOnce := sync.OnceFunc(entryUnlock)
	defer continuation.release(s)
	defer unlockOnce()
	proceed, err := s.startRunContentLocked(continuation.ctx, id, agent.PlanApprovedProceedText, nil, runPurposeChat, continuation.generation, false, false)
	if err != nil {
		s.recordPlanContinuationFailure(continuation.ctx, id, continuation)
		// Admission errors may contain provider or user content. Keep the durable
		// event correlation-only and diagnose through the bounded error taxonomy.
		s.cfg.Diagnostics.Log(continuation.ctx, port.LevelWarn, "accepted plan continuation failed to start", "session", string(id), "error_code", classifyError(err).Code)
		return
	}
	unlockOnce()
	s.relayDetachedControlRun(continuation.ctx, id, proceed)
}

func (s *Service) recordPlanContinuationFailure(ctx context.Context, id session.SessionID, continuation *planContinuation) {
	ev := session.Event{
		Type: session.EvPlanContinuationFailed,
		PlanContinuationFailure: &session.PlanContinuationFailurePayload{
			PlanRunID: continuation.planRunID,
			AskID:     continuation.askID,
		},
	}
	logCtx := context.WithoutCancel(ctx)
	// appendEvent intentionally treats a nil EventLog as a no-op for ordinary
	// relay events. A continuation failure may only be published after a real
	// durable append, so this caller must reject the nil-log case explicitly.
	if s.cfg.EventLog == nil {
		s.cfg.Diagnostics.Log(logCtx, port.LevelWarn, "plan continuation failure event could not be recorded", "session", string(id))
		return
	}
	if err := s.appendEvent(logCtx, id, ev); err != nil {
		// A lost lease forbids both durable mutation and an unrecorded claim of
		// failure. The successor owner may still start the continuation.
		s.cfg.Diagnostics.Log(logCtx, port.LevelWarn, "plan continuation failure event could not be recorded", "session", string(id))
		return
	}
	s.PublishSessionEvent(id, ev)
}
