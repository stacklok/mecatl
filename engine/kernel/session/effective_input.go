package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// EffectiveInput is one complete prepared revision for an earlier active-run
// authored input. Revisions append; they neither change nor consume the source.
// Nil and empty Prompts are explicit emptiness, distinct from absence. Assignment
// shares storage; Clone detaches it. Zero InputID is invalid.
type EffectiveInput struct {
	InputID InputID
	Prompts []PreparedPrompt
}

// Validate checks full source identity and prompt structure, not source membership
// or message bounds. Those require ordered aggregate admission.
func (v EffectiveInput) Validate() error {
	if err := v.InputID.Validate(); err != nil {
		return err
	}
	for _, p := range v.Prompts {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Clone returns deeply detached prompts.
func (v EffectiveInput) Clone() EffectiveInput {
	return effectiveInputFromFormat(v.InputID.RunID.HistoryID, v.toFormat().Clone())
}

// Equal compares source identity and exact ordered prompts.
func (v EffectiveInput) Equal(other EffectiveInput) bool {
	return v.InputID == other.InputID && v.toFormat().Equal(other.toFormat())
}
func (v EffectiveInput) toFormat() sessionformat.EffectiveInput {
	out := sessionformat.EffectiveInput{RunID: v.InputID.RunID.Ordinal, InputID: v.InputID.Ordinal}
	if v.Prompts != nil {
		out.Prompts = make([]sessionformat.PreparedPrompt, len(v.Prompts))
		for i := range v.Prompts {
			out.Prompts[i] = v.Prompts[i].toFormat()
		}
	}
	return out
}
func effectiveInputFromFormat(scope historyid.HistoryID, v sessionformat.EffectiveInput) EffectiveInput {
	out := EffectiveInput{InputID: InputID{RunID: RunID{HistoryID: scope, Ordinal: v.RunID}, Ordinal: v.InputID}}
	if v.Prompts != nil {
		out.Prompts = make([]PreparedPrompt, len(v.Prompts))
		for i := range v.Prompts {
			out.Prompts[i] = preparedPromptFromFormat(v.Prompts[i])
		}
	}
	return out
}

// NewEffectiveInputRecord encodes an owned revision under explicit limits.
// Failure returns zero; construction proves neither source membership nor bounds.
func NewEffectiveInputRecord(v EffectiveInput, limits codec.Limits) (Record, error) {
	if err := v.InputID.Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	b.collection(len(v.Prompts))
	if b.err == nil {
		for _, p := range v.Prompts {
			p.checkSize(&b)
		}
	}
	if b.err != nil {
		return Record{}, b.err
	}
	payload, err := codec.Encode(v.toFormat(), limits)
	if err != nil {
		return Record{}, err
	}
	r := Record{scope: v.InputID.RunID.HistoryID, effectiveInput: v.InputID, value: format.Record{Kind: sessionformat.KindEffectiveInput, KindVersion: sessionformat.EffectiveInputVersion, Payload: payload}}
	// Only the greatest reference is needed to validate all whole-message bounds.
	for _, p := range v.Prompts {
		if p.AuthoredMessage != nil && (!r.hasMessage || *p.AuthoredMessage > r.maxMessage) {
			r.hasMessage = true
			r.maxMessage = *p.AuthoredMessage
		}
	}
	return r, nil
}

// EffectiveInputView is an immutable fixed-size revision handle. Zero exports zero
// values; all mutable exports detach storage.
type EffectiveInputView struct {
	scope  historyid.HistoryID
	record *acceptedRecord
}

// InputID returns the triggering authored occurrence, or zero for zero.
func (v EffectiveInputView) InputID() InputID {
	if v.record == nil {
		return InputID{}
	}
	return InputID{RunID: RunID{HistoryID: v.scope, Ordinal: v.record.effective.RunID}, Ordinal: v.record.effective.InputID}
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
