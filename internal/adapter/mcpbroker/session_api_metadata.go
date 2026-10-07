package mcpbroker

import (
	"bytes"
	"context"
	"encoding/json"

	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

// A different GET cannot prove that an outstanding SET will not arrive later.
// Keep the candidate immutable until its exact write token is read back.
func (s *SessionAPI) resolveMetadataWrite(ctx context.Context, st *apiState) error {
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

func (s *SessionAPI) saveRecord(ctx context.Context, st *apiState, candidate apiRecord) error {
	if !s.validOperation(ctx) || st.pendingWrite != nil {
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
		if err := s.resolveMetadataWrite(ctx, st); err != nil {
			return nil, err
		}
	}
	b, err := s.redis.Get(ctx, sessionAPIPrefix+string(ref)).Bytes()
	if err != nil {
		return nil, c.ErrStateUnavailable
	}
	var record apiRecord
	if json.Unmarshal(b, &record) != nil || (record.Connection != "" && !validAPIRef(record.Connection)) || ((record.Connected || record.Withdrawing) && record.Connection == "") || record.Ref != ref || record.Owner != o || record.Workload != w || record.Profile != s.profile() || !s.now().Before(record.ExpiresAt) {
		return nil, c.ErrStateUnavailable
	}
	if !s.validOperation(ctx) {
		return nil, c.ErrStateUnavailable
	}
	if !st.loaded {
		op := ctx.Value(apiOperationKey{}).(*apiOperation)
		s.mu.Lock()
		if s.closed || st.deleted || op.generation != st.generation || (st.invalidated && !op.control) {
			s.mu.Unlock()
			return nil, c.ErrStateUnavailable
		}
		st.record, st.snapshot, st.loaded = record, record, true
		s.mu.Unlock()
	}
	return st, nil
}

func (s *SessionAPI) endPark(st *apiState, attempt c.BrokerAttempt, reason c.FailureReason) {
	for _, p := range st.parked {
		if p.attempt == attempt {
			p.call.Arguments = nil
			terminal := c.FlowStatus{Kind: c.FlowFailed, Reason: reason}
			p.terminal = &terminal
		}
	}
}
