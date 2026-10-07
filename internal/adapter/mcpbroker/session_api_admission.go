package mcpbroker

import (
	"context"
	"math"
	"time"

	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func (s *SessionAPI) prepareInvocation(ctx context.Context, st *apiState, cat c.CatalogueRef, call c.Call, attempt c.BrokerAttempt) (c.InvocationOutcome, *apiReceipt, error) {
	if !attempt.Valid() || !validCall(call) || !validAPIRef(string(cat)) {
		return c.InvocationOutcome{}, nil, c.ErrStateUnavailable
	}
	slot := st.record.Slots[attempt.Slot]
	digest := callDigest(call)
	if attempt.Sequence == slot.Sequence {
		if slot.Digest != digest {
			return noDispatch(c.FailureCallChanged), nil, nil
		}
		r := st.receipts[attempt.Slot]
		switch slot.Phase {
		case "terminal":
			status, err := s.slotStatus(st, attempt)
			if err != nil {
				return c.InvocationOutcome{}, nil, err
			}
			if status.Outcome != nil {
				return *status.Outcome, nil, nil
			}
			return c.InvocationOutcome{Kind: c.InvocationOutcomeUnknown}, nil, nil
		case "dispatched":
			return c.InvocationOutcome{}, r, nil
		case "parked":
			if r != nil && r.check != nil {
				return c.InvocationOutcome{Kind: c.InvocationAuthorizationRequired, Authorization: r.check.Authorization}, nil, nil
			}
		case "reserved":
			if r != nil && r.check != nil && r.check.Ready {
				return c.InvocationOutcome{}, nil, nil
			}
		}
		return c.InvocationOutcome{}, nil, c.ErrStateUnavailable
	}
	if slot.Sequence == math.MaxUint64 || attempt.Sequence != slot.Sequence+1 {
		return c.InvocationOutcome{}, nil, c.ErrStateUnavailable
	}
	if slot.Sequence != 0 && slot.Phase != "terminal" {
		return noDispatch(c.FailureCapacity), nil, nil
	}
	if cat != st.record.Catalogue {
		return noDispatch(c.FailureCatalogueChanged), nil, nil
	}
	if !st.record.Connected || st.record.Withdrawing {
		return noDispatch(c.FailureAuthorityWithdrawn), nil, nil
	}
	active := 0
	for _, p := range st.parked {
		if p.terminal == nil {
			active++
		}
	}
	if active >= 16 {
		return noDispatch(c.FailureCapacity), nil, nil
	}
	// Recovery is permitted only for a new attempt, never for a duplicate.
	if _, err := s.state(ctx, st.record.Ref); err != nil {
		return c.InvocationOutcome{}, nil, err
	}
	if cat != st.record.Catalogue || find(st.catalogue, call.Name) == nil {
		return noDispatch(c.FailureCatalogueChanged), nil, nil
	}
	slot = apiSlotRecord{Sequence: attempt.Sequence, Digest: digest, Catalogue: cat, Phase: "reserved"}
	if err := s.saveSlot(ctx, st, attempt, slot); err != nil {
		return c.InvocationOutcome{}, nil, err
	}
	for ref, p := range st.parked {
		if p.attempt.Slot == attempt.Slot {
			delete(st.parked, ref)
		}
	}
	r := &apiReceipt{attempt: attempt, digest: digest, prepared: make(chan struct{}), done: make(chan struct{})}
	defer close(r.prepared)
	st.receipts[attempt.Slot] = r
	out, _, err := s.nativePreflight(ctx, st, cat, call, attempt)
	check := c.AuthorizationCheck{Ready: true}
	if err != nil || out.Kind == c.InvocationNotDispatched {
		reason := out.Reason
		if !reason.Valid() {
			reason = c.FailureInterrupted
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		saveErr := s.terminalSlot(cleanup, st, attempt, reason)
		cancel()
		if saveErr != nil {
			return c.InvocationOutcome{}, nil, saveErr
		}
		out = noDispatch(reason)
		check = c.AuthorizationCheck{Reason: reason}
	} else if out.Kind == c.InvocationAuthorizationRequired {
		slot.Phase = "parked"
		if err := s.saveSlot(ctx, st, attempt, slot); err != nil {
			return c.InvocationOutcome{}, nil, err
		}
		check = c.AuthorizationCheck{Authorization: out.Authorization, ExpiresAt: st.parked[out.Authorization].native.ExpiresAt}
	}
	r.check = &check
	return out, nil, err
}
