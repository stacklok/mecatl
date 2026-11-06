package session

import (
	"iter"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

// Snapshot is an immutable accepted-state handle, not a restore token. Capture
// and copying cost O(1). Concurrent readers need no locks, even while the owner
// appends. Zero is unscoped and empty. Handles may retain extra backing capacity;
// GC owns their lifetime. Indexed methods panic outside [0, count).
type Snapshot struct {
	historyID    historyid.HistoryID
	head         history.CommitRef
	lifecycle    lifecycleState
	records      []*acceptedRecord
	runs         []runEntry
	inputs       []int
	commits      []commitEntry
	instructions *acceptedRecord
	catalog      *acceptedRecord
	binding      *acceptedRecord
}

// HistoryID returns the scope, including before creation.
func (v Snapshot) HistoryID() historyid.HistoryID { return v.historyID }

// Head returns the accepted head, or zero before creation.
func (v Snapshot) Head() history.CommitRef { return v.head }

// Created reports whether explicit creation has been accepted.
func (v Snapshot) Created() bool { return v.lifecycle.created }

// ActiveRun returns the active identity, or zero while idle.
func (v Snapshot) ActiveRun() RunID { return v.lifecycle.activeRun }

// RunCount returns the number of accepted runs.
func (v Snapshot) RunCount() int { return len(v.runs) }

// RecordCount returns the number of accepted chronological facts.
func (v Snapshot) RecordCount() int { return len(v.records) }

// Records returns a repeatable sequence of immutable records in chronological order.
// Zero yields nothing. The sequence stays pinned to this snapshot's bounds.
func (v Snapshot) Records() iter.Seq[RecordView] {
	return func(yield func(RecordView) bool) {
		for i := 0; i < v.RecordCount(); i++ {
			if !yield(v.RecordAt(i)) {
				return
			}
		}
	}
}

// Runs returns a repeatable sequence of immutable runs in chronological order.
// Zero yields nothing. The runs and sequence stay pinned to this snapshot's bounds.
func (v Snapshot) Runs() iter.Seq[RunView] {
	return func(yield func(RunView) bool) {
		for i := 0; i < v.RunCount(); i++ {
			if !yield(v.RunAt(i)) {
				return
			}
		}
	}
}

// RecordAt returns a chronological record; out-of-range indices panic.
func (v Snapshot) RecordAt(i int) RecordView {
	return RecordView{scope: v.historyID, record: v.records[i]}
}

// RunAt returns a run with bounds pinned to this snapshot; out-of-range indices panic.
func (v Snapshot) RunAt(i int) RunView {
	r := v.runs[i]
	recordEnd := len(v.records)
	if i+1 < len(v.runs) {
		recordEnd = v.runs[i+1].recordStart
	}
	return RunView{id: r.id, scope: v.historyID, records: v.records[r.recordStart:recordEnd:recordEnd]}
}

// UserInputByID returns an accepted submission in O(1), with bounds pinned to this
// snapshot. Invalid, foreign or absent identities return zero, false. An accepted
// empty submission is present, including after its run is interrupted.
func (v Snapshot) UserInputByID(id UserInputID) (UserInputView, bool) {
	if id.RunID.HistoryID != v.historyID || id.RunID.Ordinal == 0 || id.RunID.Ordinal > uint64(len(v.runs)) || id.Ordinal == 0 {
		return UserInputView{}, false
	}
	i := int(id.RunID.Ordinal - 1) //nolint:gosec // Positive ordinal is bounded by len(v.runs) above.
	r := v.runs[i]
	if r.id != id.RunID {
		return UserInputView{}, false
	}
	end := len(v.inputs)
	if i+1 < len(v.runs) {
		end = v.runs[i+1].inputStart
	}
	if id.Ordinal > uint64(end-r.inputStart) { //nolint:gosec // Accepted run locators are monotonic and within pinned input bounds.
		return UserInputView{}, false
	}
	record := v.records[v.inputs[r.inputStart+int(id.Ordinal-1)]] //nolint:gosec // Positive ordinal is bounded by the int-sized run span above.
	if record.input.RunID != id.RunID.Ordinal || record.input.ID != id.Ordinal {
		return UserInputView{}, false
	}
	return UserInputView{scope: v.historyID, value: &record.input}, true
}

// Instructions returns the latest accepted replacement at this head, including
// explicit emptiness. Before any replacement it returns zero, false in O(1).
func (v Snapshot) Instructions() (InstructionsView, bool) {
	if v.instructions == nil {
		return InstructionsView{}, false
	}
	return InstructionsView{scope: v.historyID, value: &v.instructions.instructions}, true
}

// ToolCatalog returns the latest accepted replacement at this head in O(1).
// Explicit emptiness is present; no retained fact returns zero, false.
func (v Snapshot) ToolCatalog() (ToolCatalogView, bool) {
	if v.catalog == nil {
		return ToolCatalogView{}, false
	}
	return ToolCatalogView{scope: v.historyID, value: &v.catalog.catalog}, true
}

// ModelBinding returns the latest accepted replacement at this head in O(1), or
// zero, false before any replacement. Handles remain pinned after later writes.
func (v Snapshot) ModelBinding() (ModelBindingView, bool) {
	if v.binding == nil {
		return ModelBindingView{}, false
	}
	return ModelBindingView{scope: v.historyID, value: &v.binding.binding}, true
}

// runEntry is immutable. Endpoints derive from the next entry or captured lengths,
// never a retroactive update when an active run receives another fact.
type runEntry struct {
	id          RunID
	recordStart int
	inputStart  int
}

// commitEntry preserves verified envelope fields and grouping without duplicating
// record payloads. Pointer envelope fields are copied into scalar values.
type commitEntry struct {
	ref           history.CommitRef
	formatVersion int64
	historyID     historyid.HistoryID // Nonzero only for genesis.
	parent        contentid.CID       // Nonzero only for successors.
	recordStart   int
	recordCount   int
}
