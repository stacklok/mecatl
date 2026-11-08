package session

import (
	"iter"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// EffectiveInput is the canonical ordered incremental contribution, not a request
// or replacement. AddEffectiveInput takes its destination separately and requires
// RunID zero. References are destination-run-relative, without origin provenance.
// Assignment shares nested storage; Clone detaches it.
type EffectiveInput = sessionformat.EffectiveInput

// newEffectiveInputRecord sizes before populating the local copy and encoding.
// Destination and zero authoring fields have been checked by the caller.
func newEffectiveInputRecord(v EffectiveInput, run RunID, limits codec.Limits) (*materializedRecord, error) {
	b := newValueBudget(limits)
	b.collection(len(v.Inputs))
	if b.err == nil {
		for _, in := range v.Inputs {
			checkInputItemSize(in, &b)
		}
	}
	if b.err != nil {
		return nil, b.err
	}
	v.RunID = run.ordinal
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindEffectiveInput, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// EffectiveInputView retains immutable accepted backing storage, including its
// record. Capture and navigation cost O(1); zero has no run or items.
type EffectiveInputView struct {
	scope historyid.HistoryID
	value *sessionformat.EffectiveInput
}

// RunID returns the admitting run, or zero for zero.
func (v EffectiveInputView) RunID() RunID {
	if v.value == nil {
		return RunID{}
	}
	return RunID{historyID: v.scope, ordinal: v.value.RunID}
}

// InputCount returns the number of ordered items, or zero for zero.
func (v EffectiveInputView) InputCount() int {
	if v.value == nil {
		return 0
	}
	return len(v.value.Inputs)
}

// Inputs returns a repeatable sequence of immutable items in contribution order.
// Zero yields nothing. The sequence stays pinned to this view's accepted storage.
func (v EffectiveInputView) Inputs() iter.Seq[InputItemView] {
	return func(yield func(InputItemView) bool) {
		for i := 0; i < v.InputCount(); i++ {
			if !yield(v.InputAt(i)) {
				return
			}
		}
	}
}

// InputAt returns an immutable item. It panics outside [0, InputCount()).
func (v EffectiveInputView) InputAt(i int) InputItemView {
	return InputItemView{run: v.RunID(), value: &v.value.Inputs[i]}
}
