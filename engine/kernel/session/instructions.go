package session

import (
	"iter"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// InstructionBlock preserves an ordered root-instruction contribution and source.
// Its purpose is instructions by construction. Assignment shares nested storage;
// Clone detaches it. Empty Parts still requires a valid Source.
type InstructionBlock struct {
	// Parts is the ordered instruction content; assignment borrows nested storage.
	Parts []ContentPart
	// Source is the required provenance for this block.
	Source ContentSource
}

// Instructions replaces the session's complete ordered root instructions. Nil
// and empty Blocks are explicit emptiness, distinct from no retained fact. Zero
// HistoryID is invalid. Assignment shares storage; Clone detaches it.
type Instructions struct {
	// HistoryID is the required scope of this replacement.
	HistoryID historyid.HistoryID
	// Blocks is the complete ordered instruction set; assignment borrows nested storage.
	Blocks []InstructionBlock
}

// Validate checks retained content and provenance without operational limits.
func (v InstructionBlock) Validate() error {
	if err := validateParts(v.Parts); err != nil {
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
	return sessionformat.InstructionBlock{Parts: partsToFormat(v.Parts), Source: v.Source.toFormat()}
}
func instructionBlockFromFormat(v sessionformat.InstructionBlock) InstructionBlock {
	return InstructionBlock{Parts: partsFromFormat(v.Parts), Source: contentSourceFromFormat(v.Source)}
}

// Validate checks full history scope and all blocks, not active-run admission.
func (v Instructions) Validate() error {
	if err := validateHistoryID(v.HistoryID); err != nil {
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

// newInstructionsRecord encodes an owned replacement under explicit limits.
// Failure returns nil and admits nothing.
func newInstructionsRecord(v Instructions, limits codec.Limits) (*materializedRecord, error) {
	if err := validateHistoryID(v.HistoryID); err != nil {
		return nil, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	b.collection(len(v.Blocks))
	if b.err == nil {
		for _, block := range v.Blocks {
			checkPartsSize(block.Parts, &b)
			block.Source.checkSize(&b)
		}
	}
	if b.err != nil {
		return nil, b.err
	}
	payload, err := codec.Encode(v.toFormat(), limits)
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

// BlockCount returns the number of ordered blocks, or zero for zero.
func (v InstructionsView) BlockCount() int {
	if v.value == nil {
		return 0
	}
	return len(v.value.Blocks)
}

// Blocks returns a repeatable sequence of immutable blocks in instruction order.
// Zero yields nothing. The sequence stays pinned to this view's accepted storage.
func (v InstructionsView) Blocks() iter.Seq[InstructionBlockView] {
	return func(yield func(InstructionBlockView) bool) {
		for i := 0; i < v.BlockCount(); i++ {
			if !yield(v.BlockAt(i)) {
				return
			}
		}
	}
}

// BlockAt returns an immutable block. It panics outside [0, BlockCount()).
func (v InstructionsView) BlockAt(i int) InstructionBlockView {
	return InstructionBlockView{value: &v.value.Blocks[i]}
}

// InstructionBlockView retains immutable backing storage. Capture and navigation
// cost O(1); zero has no parts and a zero source.
type InstructionBlockView struct {
	value *sessionformat.InstructionBlock
}

// PartCount returns the number of ordered parts, or zero for zero.
func (v InstructionBlockView) PartCount() int {
	if v.value == nil {
		return 0
	}
	return len(v.value.Parts)
}

// Parts returns a repeatable sequence of immutable parts in block order.
// Zero yields nothing. The sequence stays pinned to this view's accepted storage.
func (v InstructionBlockView) Parts() iter.Seq[ContentPartView] {
	return func(yield func(ContentPartView) bool) {
		for i := 0; i < v.PartCount(); i++ {
			if !yield(v.PartAt(i)) {
				return
			}
		}
	}
}

// PartAt returns an immutable part. It panics outside [0, PartCount()).
func (v InstructionBlockView) PartAt(i int) ContentPartView {
	return ContentPartView{value: &v.value.Parts[i]}
}

// Source returns the required provenance, or a zero view for zero.
func (v InstructionBlockView) Source() ContentSourceView {
	if v.value == nil {
		return ContentSourceView{}
	}
	return ContentSourceView{value: &v.value.Source}
}
