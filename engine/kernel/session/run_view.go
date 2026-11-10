package session

import (
	"iter"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// RunView is an immutable run handle pinned to its originating snapshot bounds.
// Zero has no identity or records. Copying and positional record access cost O(1).
type RunView struct {
	id      RunID
	scope   historyid.HistoryID
	records []*materializedRecord
}

// ID returns the scoped run identity, or zero for a zero handle.
func (v RunView) ID() RunID { return v.id }

// RecordCount includes the start, ending, and any following idle context in this
// snapshot's chronological span, until the next run starts.
func (v RunView) RecordCount() int { return len(v.records) }

// Records returns a repeatable sequence of immutable records in chronological order.
// Zero yields nothing. The sequence stays pinned to this run view's snapshot bounds.
func (v RunView) Records() iter.Seq[RecordView] {
	return func(yield func(RecordView) bool) {
		for i := 0; i < v.RecordCount(); i++ {
			if !yield(v.RecordAt(i)) {
				return
			}
		}
	}
}

// ToolCalls constructs a caller-retained join of all requested calls within the
// pinned run bounds, in response order then requested-call order. Each call's raw
// facts borrow immutable payloads; no payload is cloned. Construction takes
// expected O(records + response items + calls) time and O(calls) locator storage
// under the admitted session limits. Repeated traversal reuses these locators;
// another ToolCalls invocation rebuilds them. There is no hidden cache.
func (v RunView) ToolCalls() ToolCallsView {
	result := ToolCallsView{run: v.id}
	for record := range v.Records() {
		switch record.Kind() {
		case sessionformat.KindModelResponse:
			response, _ := record.ModelResponse()
			for call := range response.Calls() {
				if result.byID == nil {
					result.byID = make(map[RequestedCallID]int)
				}
				result.byID[call.ID()] = len(result.calls)
				result.calls = append(result.calls, toolCallLocator{
					request: call.id.requestID.requestOrdinal, call: call.id.ordinal, requested: call.value,
				})
			}
		case sessionformat.KindToolInvocation:
			invocation, _ := record.ToolInvocation()
			i := result.byID[invocation.CallID()]
			result.calls[i].invocation = invocation.value
		case sessionformat.KindToolDisposition:
			disposition, _ := record.ToolDisposition()
			i := result.byID[disposition.CallID()]
			result.calls[i].disposition = disposition.value
		case sessionformat.KindRunEnded:
			ending, _ := record.RunEnded()
			if ending.BulkToolCloseout() {
				result.bulk = ending
			}
		}
	}
	return result
}

// RecordAt returns a fact in this run's chronological span, not an ownership
// filter: history-scoped context can appear here and survive this run. Out-of-range
// indices panic.
func (v RunView) RecordAt(i int) RecordView { return RecordView{scope: v.scope, record: v.records[i]} }
