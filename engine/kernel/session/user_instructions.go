package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

// UserInstructions is a complete replacement for prepared user-level standing
// guidance, including prepared SOUL.md/persona and applicable AGENTS.md or
// CLAUDE.md guidance. ReplaceUserInstructions supplies Session scope. It is
// selected by intended placement and authority, not producer, file, or provider
// message role. Dynamically triggered guidance enters chronological EffectiveInput
// with attribution rather than replacing this value. Nil and empty Blocks
// explicitly clear it; no run boundary resets it. Assignment shares storage;
// Clone detaches it.
type UserInstructions = sessionformat.UserInstructions

// newUserInstructionsRecord owns a replacement under explicit limits.
// Failure returns nil and admits nothing.
func newUserInstructionsRecord(v UserInstructions, limits codec.Limits) (*materializedRecord, error) {
	b := newValueBudget(limits)
	b.collection(len(v.Blocks))
	if b.err == nil {
		for _, block := range v.Blocks {
			b.text(block.Key)
			checkPartsSize(block.Parts, &b)
			checkContentSourceSize(block.Source, &b)
		}
	}
	if b.err != nil {
		return nil, b.err
	}
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindUserInstructions, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// UserInstructionsView retains immutable accepted or proposed backing,
// including its record. Capture and navigation cost O(1); zero has no Session scope or
// blocks.
//
//dtogen:include
type UserInstructionsView struct {
	scope sessionid.SessionID
	value *sessionformat.UserInstructions
}

// SessionID returns the retained scope, or zero for zero.
func (v UserInstructionsView) SessionID() sessionid.SessionID { return v.scope }
