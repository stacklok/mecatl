package mcpbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"math"

	"github.com/stacklok/mecatl/engine/session"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

type apiSlotRecord struct {
	Sequence    uint64
	Digest      [32]byte
	Catalogue   c.CatalogueRef
	Phase       string
	Disposition session.BrokerAttemptDisposition
	Reason      c.FailureReason
}

func validAPISlots(b []byte, record apiRecord) bool {
	var framing struct{ Slots []apiSlotRecord }
	if json.Unmarshal(b, &framing) != nil || len(framing.Slots) != 64 {
		return false
	}
	for _, slot := range record.Slots {
		if slot.Sequence == 0 {
			if slot != (apiSlotRecord{}) {
				return false
			}
			continue
		}
		if slot.Digest == ([32]byte{}) || !validAPIRef(string(slot.Catalogue)) {
			return false
		}
		switch slot.Phase {
		case "reserved", "parked", "dispatched":
			if slot.Disposition != "" || slot.Reason != c.FailureUnspecified {
				return false
			}
		case "terminal":
			switch slot.Disposition {
			case session.BrokerAttemptNotDispatched:
				if !slot.Reason.Valid() {
					return false
				}
			case session.BrokerAttemptCompleted, session.BrokerAttemptUnknown:
				if slot.Reason != c.FailureUnspecified {
					return false
				}
			default:
				return false
			}
		default:
			return false
		}
	}
	return true
}

// The candidate is immutable until an exact readback resolves a lost SET ack.
// A different GET cannot prove that an outstanding SET will not arrive later.
func (s *SessionAPI) resolveSlotWrite(ctx context.Context, st *apiState) error {
	if st.pendingWrite == nil {
		return nil
	}
	expected, _ := json.Marshal(st.pendingWrite)
	stored, err := s.redis.Get(ctx, sessionAPIPrefix+string(st.record.Ref)).Bytes()
	if err != nil || !bytes.Equal(expected, stored) {
		return c.ErrStateUnavailable
	}
	op := ctx.Value(apiOperationKey{}).(*apiOperation)
	s.mu.Lock()
	if s.closed || st.deleted || op.generation != st.generation || (st.invalidated && !op.control) {
		s.mu.Unlock()
		return c.ErrStateUnavailable
	}
	completedWithdrawal := st.record.Withdrawing && !st.pendingWrite.Withdrawing && !st.pendingWrite.Connected
	st.record = *st.pendingWrite
	st.snapshot = st.record
	st.pendingWrite = nil
	s.mu.Unlock()
	if completedWithdrawal {
		st.completeDisconnect()
	}
	return nil
}
func (s *SessionAPI) saveSlot(ctx context.Context, st *apiState, attempt c.BrokerAttempt, slot apiSlotRecord) error {
	if err := s.resolveSlotWrite(ctx, st); err != nil {
		return err
	}
	candidate := st.record
	candidate.Slots[attempt.Slot] = slot
	return s.saveRecord(ctx, st, candidate)
}

func (s *SessionAPI) saveRecord(ctx context.Context, st *apiState, candidate apiRecord) error {
	if !s.validOperation(ctx) {
		return c.ErrStateUnavailable
	}
	if st.pendingWrite != nil {
		return c.ErrStateUnavailable
	}
	candidate.WriteToken = st.snapshot.WriteToken
	current, _ := json.Marshal(st.snapshot)
	next, _ := json.Marshal(candidate)
	if st.loaded && bytes.Equal(current, next) {
		return nil
	}
	candidate.WriteToken = apiRef()
	temporary := &apiState{record: candidate}
	if err := s.save(ctx, temporary); err != nil {
		if temporary.pendingWrite != nil {
			st.pendingWrite = &candidate
		}
		return err
	}
	op := ctx.Value(apiOperationKey{}).(*apiOperation)
	s.mu.Lock()
	if s.closed || st.deleted || op.generation != st.generation || (st.invalidated && !op.control) {
		st.pendingWrite = &candidate
		s.mu.Unlock()
		return c.ErrStateUnavailable
	}
	st.record, st.snapshot = candidate, candidate
	s.mu.Unlock()
	return nil
}

// metadataState never attaches, discovers, or recovers native credentials.
func (s *SessionAPI) metadataState(ctx context.Context, ref c.SessionRef) (*apiState, error) {
	if !s.validOperation(ctx) || !validAPIRef(string(ref)) {
		return nil, c.ErrStateUnavailable
	}
	o, w, err := s.partitions(ctx)
	if err != nil {
		return nil, err
	}
	st := ctx.Value(apiOperationKey{}).(*apiOperation).state
	if st.loaded || st.pendingWrite != nil {
		if st.record.Owner != o || st.record.Workload != w || st.record.Profile != s.profile() || !s.now().Before(st.record.ExpiresAt) {
			return nil, c.ErrStateUnavailable
		}
		if err := s.resolveSlotWrite(ctx, st); err != nil {
			return nil, err
		}
	}
	b, err := s.redis.Get(ctx, sessionAPIPrefix+string(ref)).Bytes()
	if err != nil {
		return nil, c.ErrStateUnavailable
	}
	var record apiRecord
	if json.Unmarshal(b, &record) != nil || !validAPISlots(b, record) || (record.Connection != "" && !validAPIRef(record.Connection)) || ((record.Connected || record.Withdrawing) && record.Connection == "") || record.Ref != ref || record.Owner != o || record.Workload != w || record.Profile != s.profile() || !s.now().Before(record.ExpiresAt) {
		return nil, c.ErrStateUnavailable
	}
	if !s.validOperation(ctx) {
		return nil, c.ErrStateUnavailable
	}
	cold := !st.loaded
	if cold {
		op := ctx.Value(apiOperationKey{}).(*apiOperation)
		s.mu.Lock()
		if s.closed || st.deleted || op.generation != st.generation || (st.invalidated && !op.control) {
			s.mu.Unlock()
			return nil, c.ErrStateUnavailable
		}
		st.record, st.snapshot, st.loaded = record, record, true
		s.mu.Unlock()
	}
	for i, slot := range st.record.Slots {
		if slot.Sequence == 0 {
			continue
		}
		r := st.receipts[i]
		if r != nil && r.attempt.Sequence != slot.Sequence {
			st.receipts[i] = nil
			r = nil
		}
		switch slot.Phase {
		case "reserved", "parked":
			if r != nil && r.check != nil {
				continue
			}
			slot.Phase, slot.Disposition, slot.Reason = "terminal", session.BrokerAttemptNotDispatched, c.FailureInterrupted
		case "dispatched":
			if r != nil && r.running && !receiptFinished(r) {
				continue
			}
			slot.Phase, slot.Disposition = "terminal", session.BrokerAttemptUnknown
		case "terminal":
			if slot.Disposition == session.BrokerAttemptCompleted && (cold || (r != nil && receiptFinished(r) && r.outcome.Kind != c.InvocationCompleted)) {
				slot.Disposition = session.BrokerAttemptUnknown
			}
		}
		if slot != st.record.Slots[i] {
			if err := s.saveSlot(ctx, st, c.BrokerAttempt{Slot: uint32(i), Sequence: slot.Sequence}, slot); err != nil {
				return nil, err
			}
		}
	}
	return st, nil
}
func (s *SessionAPI) slotStatus(st *apiState, attempt c.BrokerAttempt) (c.AttemptStatus, error) {
	slot := st.record.Slots[attempt.Slot]
	status := c.AttemptStatus{Attempt: attempt}
	if slot.Sequence != math.MaxUint64 && attempt.Sequence == slot.Sequence+1 {
		status.Phase = "not_admitted"
		return status, nil
	}
	if attempt.Sequence != slot.Sequence {
		return c.AttemptStatus{}, c.ErrStateUnavailable
	}
	status.Phase, status.Disposition = slot.Phase, slot.Disposition
	if slot.Phase == "terminal" && slot.Disposition == session.BrokerAttemptNotDispatched {
		out := noDispatch(slot.Reason)
		status.Outcome = &out
	}
	if r := st.receipts[attempt.Slot]; r != nil {
		if slot.Phase == "terminal" {
			select {
			case <-r.done:
				out, _ := c.NewInvocationOutcome(r.outcome.Kind, r.outcome.Result, r.outcome.Authorization, r.outcome.Reason)
				if out.Valid() {
					status.Outcome = &out
				}
			default:
			}
		} else if slot.Phase == "parked" && r.check != nil {
			out := c.InvocationOutcome{Kind: c.InvocationAuthorizationRequired, Authorization: r.check.Authorization}
			status.Outcome = &out
		}
	}
	if status.Disposition == session.BrokerAttemptCompleted && (status.Outcome == nil || status.Outcome.Kind != c.InvocationCompleted) {
		status.Disposition = session.BrokerAttemptUnknown
	}
	return status, nil
}
func (s *SessionAPI) InspectAttempt(ctx context.Context, ref c.SessionRef, attempt c.BrokerAttempt) (c.AttemptStatus, error) {
	ctx, release, err := s.operation(ctx, ref, apiControl{passive: true})
	if err != nil {
		return c.AttemptStatus{}, err
	}
	defer release()
	if !attempt.Valid() {
		return c.AttemptStatus{}, c.ErrStateUnavailable
	}
	st, err := s.metadataState(ctx, ref)
	if err != nil {
		return c.AttemptStatus{}, err
	}
	return s.slotStatus(st, attempt)
}
func (s *SessionAPI) terminalSlot(ctx context.Context, st *apiState, attempt c.BrokerAttempt, reason c.FailureReason) error {
	slot := st.record.Slots[attempt.Slot]
	if slot.Sequence != attempt.Sequence || (slot.Phase != "reserved" && slot.Phase != "parked") {
		return c.ErrStateUnavailable
	}
	slot.Phase, slot.Disposition, slot.Reason = "terminal", session.BrokerAttemptNotDispatched, reason
	if err := s.saveSlot(ctx, st, attempt, slot); err != nil {
		return err
	}
	if r := st.receipts[attempt.Slot]; r != nil {
		r.outcome = noDispatch(reason)
		close(r.done)
	}
	for _, p := range st.parked {
		if p.attempt == attempt {
			p.call.Arguments = nil
			terminal := c.FlowStatus{Kind: c.FlowFailed, Reason: reason}
			p.terminal = &terminal
		}
	}
	return nil
}
func (s *SessionAPI) AcknowledgeAttempt(ctx context.Context, ref c.SessionRef, attempt c.BrokerAttempt) (c.AttemptStatus, error) {
	ctx, release, err := s.operation(ctx, ref, apiControl{passive: true})
	if err != nil {
		return c.AttemptStatus{}, err
	}
	defer release()
	if !attempt.Valid() {
		return c.AttemptStatus{}, c.ErrStateUnavailable
	}
	st, err := s.metadataState(ctx, ref)
	if err != nil {
		return c.AttemptStatus{}, err
	}
	slot := st.record.Slots[attempt.Slot]
	if slot.Sequence != attempt.Sequence || slot.Phase == "dispatched" {
		return c.AttemptStatus{}, c.ErrStateUnavailable
	}
	if slot.Phase != "terminal" {
		if err := s.terminalSlot(ctx, st, attempt, c.FailureInterrupted); err != nil {
			return c.AttemptStatus{}, err
		}
	}
	if st.record.Slots[attempt.Slot].Disposition == session.BrokerAttemptNotDispatched {
		for _, p := range st.parked {
			if p.attempt == attempt && st.attachment != nil {
				if _, err := st.attachment.CancelAuthorization(ctx, p.native); err != nil {
					return c.AttemptStatus{}, err
				}
			}
		}
	}
	st.receipts[attempt.Slot] = nil
	for ref, p := range st.parked {
		if p.attempt == attempt {
			delete(st.parked, ref)
		}
	}
	return s.slotStatus(st, attempt)
}
