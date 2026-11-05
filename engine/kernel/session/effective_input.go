package session

import (
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
	RunID  RunID
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
		if in.UserInput != nil && in.UserInput.RunID != v.RunID {
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
	out := sessionformat.EffectiveInput{RunID: v.RunID.Ordinal}
	if v.Inputs != nil {
		out.Inputs = make([]sessionformat.InputItem, len(v.Inputs))
		for i := range v.Inputs {
			out.Inputs[i] = v.Inputs[i].toFormat()
		}
	}
	return out
}

// effectiveInputFromFormat restores enclosing scope without resolving references.
// The DTO must already be detached before exporting its nested storage.
func effectiveInputFromFormat(scope historyid.HistoryID, v sessionformat.EffectiveInput) EffectiveInput {
	out := EffectiveInput{RunID: RunID{HistoryID: scope, Ordinal: v.RunID}}
	if v.Inputs != nil {
		out.Inputs = make([]InputItem, len(v.Inputs))
		for i := range v.Inputs {
			out.Inputs[i] = inputItemFromFormat(out.RunID, v.Inputs[i])
		}
	}
	return out
}

// NewEffectiveInputRecord encodes an owned increment under explicit limits.
// Failure returns zero. Construction checks full reference scope but does not
// prove membership or order; aggregate admission validates every reference.
func NewEffectiveInputRecord(v EffectiveInput, limits codec.Limits) (Record, error) {
	if err := v.RunID.Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	b.collection(len(v.Inputs))
	if b.err == nil {
		for _, in := range v.Inputs {
			in.checkSize(&b)
		}
	}
	if b.err != nil {
		return Record{}, b.err
	}
	if err := v.Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	fact := v.toFormat()
	payload, err := codec.Encode(fact, limits)
	if err != nil {
		return Record{}, err
	}
	return Record{scope: v.RunID.HistoryID, effectiveInput: fact.Clone(), value: format.Record{Kind: sessionformat.KindEffectiveInput, KindVersion: sessionformat.Version, Payload: payload}}, nil
}

// EffectiveInputView is an immutable fixed-size incremental-input handle. Zero
// exports zero values; all mutable exports detach storage.
type EffectiveInputView struct {
	scope  historyid.HistoryID
	record *acceptedRecord
}

// RunID returns the admitting run, or zero for zero.
func (v EffectiveInputView) RunID() RunID {
	if v.record == nil {
		return RunID{}
	}
	return RunID{HistoryID: v.scope, Ordinal: v.record.effective.RunID}
}

// Value returns an independently owned public value, or zero for zero.
func (v EffectiveInputView) Value() EffectiveInput {
	if v.record == nil {
		return EffectiveInput{}
	}
	return effectiveInputFromFormat(v.scope, v.record.effective.Clone())
}

// Format returns a detached DTO, or zero for zero.
func (v EffectiveInputView) Format() sessionformat.EffectiveInput {
	if v.record == nil {
		return sessionformat.EffectiveInput{}
	}
	return v.record.effective.Clone()
}
