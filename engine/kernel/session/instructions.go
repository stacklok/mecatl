package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// InstructionBlock preserves an ordered root-instruction contribution and source.
// Its purpose is instructions by construction. Assignment shares nested storage;
// Clone detaches it. An empty Content still requires a valid Source.
type InstructionBlock struct {
	Content Content
	Source  ContentSource
}

// Instructions replaces the session's complete ordered root instructions. Nil
// and empty Blocks are explicit emptiness, distinct from no retained fact. Zero
// HistoryID is invalid. Assignment shares storage; Clone detaches it.
type Instructions struct {
	HistoryID historyid.HistoryID
	Blocks    []InstructionBlock
}

// Validate checks retained content and provenance without operational limits.
func (v InstructionBlock) Validate() error {
	if err := v.Content.Validate(); err != nil {
		return err
	}
	return v.Source.Validate()
}

// Clone detaches all nested storage.
func (v InstructionBlock) Clone() InstructionBlock {
	return instructionBlockFromFormat(v.toFormat().Clone())
}

// Equal compares exact values, treating nil and empty slices alike.
func (v InstructionBlock) Equal(other InstructionBlock) bool {
	return v.toFormat().Equal(other.toFormat())
}
func (v InstructionBlock) toFormat() sessionformat.InstructionBlock {
	return sessionformat.InstructionBlock{Content: v.Content.toFormat(), Source: v.Source.toFormat()}
}
func instructionBlockFromFormat(v sessionformat.InstructionBlock) InstructionBlock {
	return InstructionBlock{Content: contentFromFormat(v.Content), Source: contentSourceFromFormat(v.Source)}
}

// Validate checks full history scope and all blocks, not active-run admission.
func (v Instructions) Validate() error {
	if err := (Created{HistoryID: v.HistoryID}).Validate(); err != nil {
		return err
	}
	for _, block := range v.Blocks {
		if err := block.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Clone returns independently owned blocks.
func (v Instructions) Clone() Instructions {
	return instructionsFromFormat(v.HistoryID, v.toFormat().Clone())
}

// Equal compares scope and exact ordered blocks.
func (v Instructions) Equal(other Instructions) bool {
	return v.HistoryID == other.HistoryID && v.toFormat().Equal(other.toFormat())
}
func (v Instructions) toFormat() sessionformat.Instructions {
	out := sessionformat.Instructions{}
	if v.Blocks != nil {
		out.Blocks = make([]sessionformat.InstructionBlock, len(v.Blocks))
		for i := range v.Blocks {
			out.Blocks[i] = v.Blocks[i].toFormat()
		}
	}
	return out
}
func instructionsFromFormat(scope historyid.HistoryID, v sessionformat.Instructions) Instructions {
	out := Instructions{HistoryID: scope}
	if v.Blocks != nil {
		out.Blocks = make([]InstructionBlock, len(v.Blocks))
		for i := range v.Blocks {
			out.Blocks[i] = instructionBlockFromFormat(v.Blocks[i])
		}
	}
	return out
}

// NewInstructionsRecord encodes an owned replacement under explicit limits.
// It retains full scope privately; failure returns zero and admits nothing.
func NewInstructionsRecord(v Instructions, limits codec.Limits) (Record, error) {
	if err := (Created{HistoryID: v.HistoryID}).Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	b.collection(len(v.Blocks))
	if b.err == nil {
		for _, block := range v.Blocks {
			block.Content.checkSize(&b)
			block.Source.checkSize(&b)
		}
	}
	if b.err != nil {
		return Record{}, b.err
	}
	payload, err := codec.Encode(v.toFormat(), limits)
	if err != nil {
		return Record{}, err
	}
	return Record{scope: v.HistoryID, value: format.Record{Kind: sessionformat.KindInstructions, KindVersion: sessionformat.InstructionsVersion, Payload: payload}}, nil
}

// InstructionsView is an immutable fixed-size accepted replacement handle.
// Zero exports zero values; all mutable exports detach storage.
type InstructionsView struct {
	scope  historyid.HistoryID
	record *acceptedRecord
}

// HistoryID returns the retained scope, or zero for zero.
func (v InstructionsView) HistoryID() historyid.HistoryID { return v.scope }

// Value returns an independently owned public value, or zero for zero.
func (v InstructionsView) Value() Instructions {
	if v.record == nil {
		return Instructions{}
	}
	return instructionsFromFormat(v.scope, v.record.instructions.Clone())
}

// Format returns a detached DTO, or zero for zero.
func (v InstructionsView) Format() sessionformat.Instructions {
	if v.record == nil {
		return sessionformat.Instructions{}
	}
	return v.record.instructions.Clone()
}
