package session

// ToolSettlementCategory is the closed effective settlement vocabulary, distinct
// from a persisted disposition. Zero means no category; only Settlement supplies
// nonzero categories. These values do not imply tool success or execution safety.
type ToolSettlementCategory uint8

const (
	// ToolSettlementReturned identifies an explicit terminal tool return.
	ToolSettlementReturned ToolSettlementCategory = iota + 1
	// ToolSettlementNotInvoked identifies known or bulk-derived non-invocation.
	ToolSettlementNotInvoked
	// ToolSettlementIndeterminate identifies explicit or bulk-derived uncertainty.
	ToolSettlementIndeterminate
)

// ToolSettlement is an immutable effective category with exactly one provenance:
// an actual explicit disposition or the actual bulk run ending. It borrows pinned
// payloads and makes no per-call diagnostic/output/reason for a bulk ending. Zero
// has no category or provenance. All inspection is O(1).
type ToolSettlement struct {
	category    ToolSettlementCategory
	disposition ToolDispositionView
	bulk        RunEndedView
}

// Category returns the effective category, or zero for a zero settlement.
func (v ToolSettlement) Category() ToolSettlementCategory { return v.category }

// ExplicitDisposition returns the retained raw disposition when it supplied the
// settlement. It is absent for bulk-derived settlement and zero.
func (v ToolSettlement) ExplicitDisposition() (ToolDispositionView, bool) {
	return v.disposition, v.disposition.CallID() != (RequestedCallID{})
}

// BulkEnding returns the retained bulk ending when it supplied the settlement.
// It is absent for explicit settlement (even in a bulk-ended run) and zero.
func (v ToolSettlement) BulkEnding() (RunEndedView, bool) {
	return v.bulk, v.bulk.RunID() != (RunID{})
}
