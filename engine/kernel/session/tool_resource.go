package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ToolResource retains reference metadata, granting no dereference authority.
// Optional labels preserve present-empty values; Clone detaches shared pointers.
type ToolResource = sessionformat.ToolResource

// checkToolResourceSize bounds labels before URI parsing or materialization.
func checkToolResourceSize(v ToolResource, b *valueBudget) {
	b.text(v.URI)
	for _, label := range []*string{v.Name, v.Title, v.MIMEType} {
		if label != nil {
			b.pointer()
			b.text(*label)
		}
	}
}

// ToolResourceView borrows immutable reference metadata for its lifetime.
// Zero has empty URI and absent labels. Reads are O(1), without body copies.
//
//dtogen:include
type ToolResourceView struct{ value *sessionformat.ToolResource }
