package session

// ToolCallView joins one original requested call with its optional raw facts and
// effective settlement. It retains immutable locator/payload backing for its
// lifetime; all navigation is O(1) and borrows without cloning siblings. Zero has
// no identity, requested data, invocation, disposition or settlement. Answers stay
// pinned to the constructing RunView even after ending or a successor run.
type ToolCallView struct {
	location *toolCallLocator
	bulk     RunEndedView
}

// ID returns the full scoped requested-call identity, or zero for zero.
func (v ToolCallView) ID() RequestedCallID { return v.RequestedCall().ID() }

// RequestedCall returns the original name and exact opaque arguments, not the
// invocation's effective name or structured arguments. Zero returns a zero view.
func (v ToolCallView) RequestedCall() RequestedCallView {
	if v.location == nil {
		return RequestedCallView{}
	}
	return v.location.requested
}

// Invocation returns the explicit intent fact if retained, never a derived fact.
func (v ToolCallView) Invocation() (ToolInvocationView, bool) {
	if v.location == nil {
		return ToolInvocationView{}, false
	}
	return v.location.invocation, v.location.invocation.CallID() != (RequestedCallID{})
}

// Disposition returns the explicit outcome fact if retained. Bulk closeout does
// not fabricate a per-call disposition, diagnostic, reason or output.
func (v ToolCallView) Disposition() (ToolDispositionView, bool) {
	if v.location == nil {
		return ToolDispositionView{}, false
	}
	return v.location.disposition, v.location.disposition.CallID() != (RequestedCallID{})
}

// Settlement returns the effective category and its actual provenance. An
// explicit disposition always wins. Otherwise a bulk ending derives NotInvoked
// without an invocation, or Indeterminate with one. Without either fact the call
// is unsettled and returns zero, false. No execution readiness is implied.
func (v ToolCallView) Settlement() (ToolSettlement, bool) {
	if disposition, ok := v.Disposition(); ok {
		category := ToolSettlementIndeterminate
		if _, ok := disposition.Returned(); ok {
			category = ToolSettlementReturned
		} else if _, ok := disposition.NotInvoked(); ok {
			category = ToolSettlementNotInvoked
		}
		return ToolSettlement{category: category, disposition: disposition}, true
	}
	if v.bulk.RunID() == (RunID{}) {
		return ToolSettlement{}, false
	}
	category := ToolSettlementNotInvoked
	if _, ok := v.Invocation(); ok {
		category = ToolSettlementIndeterminate
	}
	return ToolSettlement{category: category, bulk: v.bulk}, true
}
