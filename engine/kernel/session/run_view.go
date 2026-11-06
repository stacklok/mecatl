package session

import (
	"iter"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

// RunView is an immutable run handle pinned to its originating snapshot bounds.
// Zero has no identity or records. Copying and positional record access cost O(1).
type RunView struct {
	id      RunID
	scope   historyid.HistoryID
	records []*acceptedRecord
}

// ID returns the scoped run identity, or zero for a zero handle.
func (v RunView) ID() RunID { return v.id }

// RecordCount includes the start and any explicit interruption in this snapshot.
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

// RecordAt returns a fact in this run's chronological span, not an ownership
// filter: history-scoped context can appear here and survive this run. Out-of-range
// indices panic.
func (v RunView) RecordAt(i int) RecordView { return RecordView{scope: v.scope, record: v.records[i]} }
