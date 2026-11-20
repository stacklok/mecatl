package session

import (
	"iter"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// Snapshot is an immutable accepted-state handle, not a restore token. Capture
// and copying cost O(1). Concurrent readers need no locks, even while the owner
// appends. Zero is unscoped and empty. Handles may retain extra backing capacity;
// GC owns their lifetime. Indexed methods panic outside [0, count).
type Snapshot struct {
	historyID historyid.HistoryID
	head      history.CommitRef
	lifecycle lifecycleState
	records   []*materializedRecord
	// finalRequests is the final request count for each superseded run, indexed by
	// run ordinal minus one. The latest run's count remains in lifecycle.requests,
	// including after end.
	finalRequests    []uint64
	runs             []runEntry
	inputs           []int
	commits          []commitEntry
	userInstructions *materializedRecord
	rootInstructions *materializedRecord
	catalog          *materializedRecord
	binding          *materializedRecord
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

// RecordsBetween returns every record in accepted commits (after, through], in
// chronological order, including intermediate replacements and whole commits.
// Zero after denotes pre-genesis; through must be a positive accepted reference.
// Shape and sequence-order validation precede exact-reference membership checks:
// malformed or reversed bounds return codec.ErrInvalidData; absent references
// return ErrIdentity. All failures return a nil sequence, synchronously. Equal
// accepted endpoints return a nonnil empty sequence, including on repeated reads.
// Zero and uncreated snapshots have no accepted through endpoint.
//
// Setup costs O(1) time and space using the captured commit index. Traversal costs
// O(records yielded), with no decoding, payload copying or backend I/O. The
// repeatable, early-stoppable sequence and its borrowed, history-scoped views stay
// pinned across later appends; they may retain backing beyond the selected range.
// RecordView.MutableClone explicitly detaches data; these views confer no append authority.
func (v Snapshot) RecordsBetween(after, through history.CommitRef) (iter.Seq[RecordView], error) {
	if after != (history.CommitRef{}) && after.Validate() != nil {
		return nil, codec.ErrInvalidData
	}
	if through.Validate() != nil || after.Sequence > through.Sequence {
		return nil, codec.ErrInvalidData
	}
	start := 0
	if after != (history.CommitRef{}) {
		if after.Sequence > int64(len(v.commits)) {
			return nil, ErrIdentity
		}
		entry := v.commits[int(after.Sequence-1)] //nolint:gosec // Positive sequence is bounded by the captured int-sized length above.
		if entry.ref != after {
			return nil, ErrIdentity
		}
		start = entry.recordStart + entry.recordCount
	}
	if through.Sequence > int64(len(v.commits)) {
		return nil, ErrIdentity
	}
	entry := v.commits[int(through.Sequence-1)] //nolint:gosec // Positive sequence is bounded by the captured int-sized length above.
	if entry.ref != through {
		return nil, ErrIdentity
	}
	records := v.records[start : entry.recordStart+entry.recordCount]
	scope := v.historyID
	return func(yield func(RecordView) bool) {
		for _, record := range records {
			if !yield(RecordView{scope: scope, record: record}) {
				return
			}
		}
	}, nil
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

// Usages constructs a caller-retained first-definition/latest-report join over
// the whole pinned snapshot, including withdrawn identities and late reports
// correlated with earlier runs. It scans O(records) once and allocates O(usage
// identities) locators, borrowing immutable payloads without copying them. Order
// is first-definition occurrence order. Traversal and lookup reuse the locators;
// another invocation rebuilds them. There is no hidden cache. The join and its
// entries retain backing for their lifetime and never change after later appends.
func (v Snapshot) Usages() UsageEntriesView {
	result := UsageEntriesView{scope: v.historyID}
	if v.lifecycle.usages == 0 {
		return result
	}
	// Admission allocates contiguous identities, each with an accepted first
	// definition, so the count and each index are bounded by len(v.records).
	result.entries = make([]usageEntryLocator, int(v.lifecycle.usages)) //nolint:gosec // Bounded by the captured int-sized record count.
	for _, record := range v.records {
		if record.value.Kind != sessionformat.KindUsage {
			continue
		}
		usage := &record.usage
		entry := &result.entries[int(usage.ID-1)] //nolint:gosec // Accepted identity is bounded by the allocated count above.
		if usage.Definition != nil {
			entry.first = usage
		}
		entry.latest = usage
	}
	return result
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
	if id.runID.historyID != v.historyID || id.runID.ordinal == 0 || id.runID.ordinal > uint64(len(v.runs)) || id.ordinal == 0 {
		return UserInputView{}, false
	}
	i := int(id.runID.ordinal - 1) //nolint:gosec // Positive ordinal is bounded by len(v.runs) above.
	r := v.runs[i]
	if r.id != id.runID {
		return UserInputView{}, false
	}
	end := len(v.inputs)
	if i+1 < len(v.runs) {
		end = v.runs[i+1].inputStart
	}
	if id.ordinal > uint64(end-r.inputStart) { //nolint:gosec // Accepted run locators are monotonic and within pinned input bounds.
		return UserInputView{}, false
	}
	record := v.records[v.inputs[r.inputStart+int(id.ordinal-1)]] //nolint:gosec // Positive ordinal is bounded by the int-sized run span above.
	if record.input.RunID != id.runID.ordinal || record.input.ID != id.ordinal {
		return UserInputView{}, false
	}
	return UserInputView{scope: v.historyID, value: &record.input}, true
}

// UserInstructions returns the latest accepted prepared user-guidance replacement
// in O(1), including explicit emptiness. No replacement returns zero, false. Run
// starts and root-instruction replacements do not reset it.
func (v Snapshot) UserInstructions() (UserInstructionsView, bool) {
	if v.userInstructions == nil {
		return UserInstructionsView{}, false
	}
	return UserInstructionsView{scope: v.historyID, value: &v.userInstructions.userInstructions}, true
}

// RootInstructions returns the latest accepted harness root-contract replacement
// at this head, including explicit emptiness. Before any replacement it returns
// zero, false in O(1).
func (v Snapshot) RootInstructions() (RootInstructionsView, bool) {
	if v.rootInstructions == nil {
		return RootInstructionsView{}, false
	}
	return RootInstructionsView{scope: v.historyID, value: &v.rootInstructions.rootInstructions}, true
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
// record payloads. Pointer envelope fields are copied into scalar values. Metadata
// has stable per-commit backing shared with accepted records, never a pointer into
// this growable index.
type commitEntry struct {
	ref           history.CommitRef
	metadata      *CommitMetadata
	formatVersion int64
	historyID     historyid.HistoryID // Nonzero only for genesis.
	parent        contentid.CID       // Nonzero only for successors.
	recordStart   int
	recordCount   int
}
