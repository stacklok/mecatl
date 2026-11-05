package session

import historyid "github.com/stacklok/mecatl/engine/kernel/history/id"

// RunView is an immutable run handle pinned to its originating snapshot bounds.
// Zero has no identity, records or inputs. Copying and positional access cost O(1).
type RunView struct {
	id         RunID
	scope      historyid.HistoryID
	records    []*acceptedRecord
	inputs     []int
	allRecords []*acceptedRecord
}

// ID returns the scoped run identity, or zero for a zero handle.
func (v RunView) ID() RunID { return v.id }

// RecordCount includes the start and any explicit interruption in this snapshot.
func (v RunView) RecordCount() int { return len(v.records) }

// InputCount returns this run's accepted input count in this snapshot.
func (v RunView) InputCount() int { return len(v.inputs) }

// RecordAt returns a fact in this run's chronological span, not an ownership
// filter: history-scoped context can appear here and survive this run. Out-of-range
// indices panic.
func (v RunView) RecordAt(i int) RecordView { return RecordView{scope: v.scope, record: v.records[i]} }

// InputAt returns a run-local input; out-of-range indices panic.
func (v RunView) InputAt(i int) InputView {
	return InputView{scope: v.scope, record: v.allRecords[v.inputs[i]]}
}
