package mcpbroker

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

// Ready is deliberately nonreserving. Only a parked authorization or an actual
// worker occupies the session; neither is recovered after process replacement.
func (s *SessionAPI) prepareInvocation(ctx context.Context, st *apiState, cat c.CatalogueRef, call c.Call, attempt c.BrokerAttempt) (c.InvocationOutcome, *apiReceipt, error) {
	if !attempt.Valid() || !validCall(call) || !validAPIRef(string(cat)) {
		return c.InvocationOutcome{}, nil, c.ErrStateUnavailable
	}
	if !receiptFinished(st.running) {
		return noDispatch(c.FailureCapacity), nil, nil
	}
	for ref, p := range st.parked {
		if p.cleanupPending {
			return noDispatch(c.FailureCapacity), nil, nil
		}
		if p.terminal == nil && !s.now().Before(p.native.ExpiresAt) {
			if _, err := st.attachment.CancelAuthorization(ctx, p.native); err != nil {
				return c.InvocationOutcome{}, nil, err
			}
			status := c.FlowStatus{Kind: c.FlowExpired}
			p.terminal = &status
			p.call.Arguments = nil
			p.native = session.ExternalAuthorization{}
		}
		if p.terminal != nil {
			if p.terminal.Kind == c.FlowCompleted {
				return noDispatch(c.FailureAuthorizationFailed), nil, nil
			}
			delete(st.parked, ref)
			continue
		}
		if p.attempt == attempt && sameCall(p.call, call) {
			return c.InvocationOutcome{Kind: c.InvocationAuthorizationRequired, Authorization: ref}, nil, nil
		}
		return noDispatch(c.FailureCapacity), nil, nil
	}
	if cat != st.record.Catalogue {
		return noDispatch(c.FailureCatalogueChanged), nil, nil
	}
	if !st.record.Connected || st.record.Withdrawing {
		return noDispatch(c.FailureAuthorityWithdrawn), nil, nil
	}
	if st.attachment == nil || st.catalogue == nil || st.record.Catalogue != st.catalogue.Ref() {
		return noDispatch(c.FailureCatalogueChanged), nil, nil
	}
	return s.nativePreflight(ctx, st, cat, call, attempt)
}
