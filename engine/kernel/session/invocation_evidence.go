package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// OpaqueProviderData retains producer-owned bytes without interpreting their schema.
// Metadata does not imply safe disclosure or replay. Assignment borrows bytes;
// Clone detaches them. See format.OpaqueProviderData for provisional naming semantics.
type OpaqueProviderData = sessionformat.OpaqueProviderData

// OpaqueProviderDataView retains immutable provider-owned metadata backing for its
// lifetime. Zero returns empty scalars and zero Bytes. Reads are O(1); Data borrows
// its leaf and Data().Copy() explicitly detaches only those bytes.
//
//dtogen:include
type OpaqueProviderDataView struct {
	value *sessionformat.OpaqueProviderData
}

// ObservedEvidence retains optional diagnostic context and safe provider
// correlation, not local identities or proof of remote effects. Present-empty
// strings are retained; assignment borrows pointers and Clone detaches them.
// Callers own disclosure safety. See format.ObservedEvidence for provisional vocabulary.
type ObservedEvidence = sessionformat.ObservedEvidence

// ObservedEvidenceView is immutable invocation evidence. Zero and absent fields
// return an empty string, false, preserving explicitly present empty strings. Reads
// are O(1) and expose no mutable storage.
//
//dtogen:include
type ObservedEvidenceView struct {
	value *sessionformat.ObservedEvidence
}

func checkOpaqueSize(v *OpaqueProviderData, b *valueBudget) {
	if v == nil {
		return
	}
	b.pointer()
	b.text(v.Producer)
	b.text(v.Format)
	b.bytes(len(v.Data))
}
func checkEvidenceSize(v *ObservedEvidence, b *valueBudget) {
	if v == nil {
		return
	}
	b.pointer()
	if v.Detail != nil {
		b.pointer()
		b.text(*v.Detail)
	}
	if v.Correlation != nil {
		b.pointer()
		b.text(*v.Correlation)
	}
}
