package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// RootInstructions is a complete replacement for the harness root operating
// contract. ReplaceRootInstructions supplies history scope. It is selected by
// intended placement and authority, not producer, file, or provider message
// role. Nil and empty Blocks explicitly clear it. Assignment shares storage;
// Clone detaches it.
type RootInstructions = sessionformat.RootInstructions

// newRootInstructionsRecord encodes an owned replacement under explicit limits.
// Failure returns nil and admits nothing.
func newRootInstructionsRecord(v RootInstructions, limits codec.Limits) (*materializedRecord, error) {
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
	return decodeMaterialized(format.Record{Kind: sessionformat.KindRootInstructions, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// RootInstructionsView retains immutable accepted or proposed backing storage,
// including its record. Capture and navigation cost O(1); zero has no history or
// blocks.
//
//dtogen:include
type RootInstructionsView struct {
	scope historyid.HistoryID
	value *sessionformat.RootInstructions
}

// HistoryID returns the retained scope, or zero for zero.
func (v RootInstructionsView) HistoryID() historyid.HistoryID { return v.scope }
