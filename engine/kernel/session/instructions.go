package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// InstructionBlock retains ordered instruction content and required provenance.
// Assignment shares nested storage; Clone detaches it.
type InstructionBlock = sessionformat.InstructionBlock

// Instructions is a complete ordered root-instruction replacement. History scope
// is supplied separately to ReplaceInstructions. Assignment shares storage;
// Clone detaches it. Nil and empty Blocks explicitly clear the instructions.
type Instructions = sessionformat.Instructions

// newInstructionsRecord encodes an owned replacement under explicit limits.
// Failure returns nil and admits nothing.
func newInstructionsRecord(v Instructions, limits codec.Limits) (*materializedRecord, error) {
	b := newValueBudget(limits)
	b.collection(len(v.Blocks))
	if b.err == nil {
		for _, block := range v.Blocks {
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
	return decodeMaterialized(format.Record{Kind: sessionformat.KindInstructions, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// InstructionsView retains immutable accepted backing storage, including its
// record. Capture and navigation cost O(1); zero has no history or blocks.
type InstructionsView struct {
	scope historyid.HistoryID
	value *sessionformat.Instructions
}

// HistoryID returns the retained scope, or zero for zero.
func (v InstructionsView) HistoryID() historyid.HistoryID { return v.scope }

// InstructionBlockView retains immutable backing storage. Capture and navigation
// cost O(1); zero has no parts and a zero source.
type InstructionBlockView struct {
	value *sessionformat.InstructionBlock
}
