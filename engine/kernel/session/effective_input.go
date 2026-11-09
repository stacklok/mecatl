package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
	"github.com/stacklok/mecatl/engine/kernel/views"
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
//
//dtogen:include
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

// Inputs returns ordered immutable items, preserving duplicates; zero is empty.
// The collection and derived items retain accepted backing. Its adapter captures
// only the enclosing run identity; escaping the collection may allocate a closure.
// Construction and indexed navigation take O(1) time without copying payloads.
func (v EffectiveInputView) Inputs() views.SliceView[sessionformat.InputItem, InputItemView] {
	if v.value == nil {
		return views.SliceView[sessionformat.InputItem, InputItemView]{}
	}
	run := v.RunID()
	return views.BorrowSliceView(v.value.Inputs, func(item *sessionformat.InputItem) InputItemView {
		return InputItemView{run: run, value: item}
	})
}
