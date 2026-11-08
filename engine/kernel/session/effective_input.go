package session

import (
	"iter"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// EffectiveInput is an ordered incremental contribution admitted by an active
// run, not a complete request, conversation or replacement. RunID is ownership,
// not a model-turn identity or conversation reset. Empty Inputs and duplicates
// are retained without consumption, fallback or deduplication. Empty contributions
// neither clear context nor request generation.
// Assignment shares nested storage; Clone detaches it. Zero RunID is invalid.
type EffectiveInput struct {
	// RunID identifies the required active run that owns this increment.
	RunID RunID
	// Inputs is the ordered contribution; assignment borrows nested storage.
	Inputs []InputItem
}

// Validate checks structure and full same-run reference scope, not membership or
// ordering. References must identify earlier submissions at aggregate admission.
func (v EffectiveInput) Validate() error {
	if err := v.RunID.Validate(); err != nil {
		return err
	}
	for _, in := range v.Inputs {
		if err := in.Validate(); err != nil {
			return err
		}
		if in.UserInputID != nil && in.UserInputID.runID != v.RunID {
			return codec.ErrInvalidData
		}
	}
	return nil
}

// Clone returns deeply detached items without discarding any reference scope.
func (v EffectiveInput) Clone() EffectiveInput {
	out := v
	if v.Inputs != nil {
		out.Inputs = make([]InputItem, len(v.Inputs))
		for i := range v.Inputs {
			out.Inputs[i] = v.Inputs[i].Clone()
		}
	}
	return out
}

// Equal compares full run identity and exact ordered items, including duplicates.
func (v EffectiveInput) Equal(other EffectiveInput) bool {
	if v.RunID != other.RunID || len(v.Inputs) != len(other.Inputs) {
		return false
	}
	for i := range v.Inputs {
		if !v.Inputs[i].Equal(other.Inputs[i]) {
			return false
		}
	}
	return true
}

// toFormat requires validated same-run references before omitting their scope.
func (v EffectiveInput) toFormat() sessionformat.EffectiveInput {
	out := sessionformat.EffectiveInput{RunID: v.RunID.ordinal}
	if v.Inputs != nil {
		out.Inputs = make([]sessionformat.InputItem, len(v.Inputs))
		for i := range v.Inputs {
			out.Inputs[i] = v.Inputs[i].toFormat()
		}
	}
	return out
}

// newEffectiveInputRecord encodes an owned increment under explicit limits.
// Failure returns nil. Construction checks target history and full reference scope
// before omission but does not prove membership or order; admission checks those.
func newEffectiveInputRecord(scope historyid.HistoryID, v EffectiveInput, limits codec.Limits) (*materializedRecord, error) {
	if err := v.RunID.Validate(); err != nil {
		return nil, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	b.collection(len(v.Inputs))
	if b.err == nil {
		for _, in := range v.Inputs {
			in.checkSize(&b)
		}
	}
	if b.err != nil {
		return nil, b.err
	}
	if err := v.Validate(); err != nil {
		return nil, codec.ErrInvalidData
	}
	if scope != v.RunID.historyID {
		return nil, ErrIdentity
	}
	fact := v.toFormat()
	payload, err := codec.Encode(fact, limits)
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
