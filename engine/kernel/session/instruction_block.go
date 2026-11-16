package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// InstructionBlock retains ordered prepared model-input content, required Key,
// and provenance. Key is 1..256 valid UTF-8 bytes with no unicode.IsSpace or
// unicode.IsControl rune, preserved without trimming, normalization, or case
// folding. Its vocabulary is open and uniqueness is confined to its whole
// RootInstructions or UserInstructions value. Keys are bookkeeping, not trust,
// authority, precedence, refresh behavior, or logical request content/CID;
// producers conventionally retain them across text/source changes. Slice order
// determines contribution order; there is no patch API. Assignment shares storage;
// Clone detaches it. Zero is invalid.
type InstructionBlock = sessionformat.InstructionBlock

// InstructionBlockView retains immutable backing storage. Capture and navigation
// cost O(1); zero has an empty key, no parts, and a zero source. Key exposes exact
// bookkeeping bytes, not model-input content. MutableClone detaches this block's
// mutable storage and preserves its key; changing the clone cannot affect backing.
//
//dtogen:include
type InstructionBlockView struct {
	value *sessionformat.InstructionBlock
}
