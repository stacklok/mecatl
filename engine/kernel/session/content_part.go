package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ContentPart holds exactly one text or media branch. Zero is invalid.
// Assignment borrows pointers and bytes; Clone detaches them.
type ContentPart = sessionformat.ContentPart

func checkContentPartSize(v ContentPart, b *valueBudget) {
	if v.Text != nil {
		b.pointer()
		b.text(*v.Text)
	}
	if v.Media != nil {
		b.pointer()
		checkMediaSize(*v.Media, b)
	}
}

// validateParts checks ordered content without imposing operational policy.
func validateParts(v []ContentPart) error {
	for _, p := range v {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ContentPartView retains immutable backing storage. Capture and navigation cost
// O(1); zero has neither union branch present.
type ContentPartView struct{ value *sessionformat.ContentPart }

// checkPartsSize bounds collections and nested data before validation or encoding.
func checkPartsSize(v []ContentPart, b *valueBudget) {
	b.collection(len(v))
	if b.err != nil {
		return
	}
	for _, p := range v {
		checkContentPartSize(p, b)
	}
}
