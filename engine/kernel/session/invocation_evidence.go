package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// OpaqueProviderData retains producer-owned bytes without interpreting their schema.
// Metadata does not imply safe disclosure or replay. Assignment borrows bytes;
// Clone detaches them. See format.OpaqueProviderData for provisional naming semantics.
type OpaqueProviderData = sessionformat.OpaqueProviderData

// OpaqueProviderDataView retains immutable provider-owned metadata backing for its
// lifetime. Zero returns empty scalars and zero Bytes. Reads are O(1); Data borrows
// its leaf and Data().Copy() explicitly detaches only those bytes.
type OpaqueProviderDataView struct {
	value *sessionformat.OpaqueProviderData
}

// InvocationUsage retains optional reported counters and usage-only raw evidence.
// Nil counters differ from reported zero. Assignment borrows pointers and bytes;
// Clone detaches them. Breakdowns do not add to inclusive input/output spend.
type InvocationUsage = sessionformat.InvocationUsage

// InvocationUsageView is immutable reported invocation usage. Zero and absent
// counters return zero, false; Raw returns a zero view, false when absent. All reads
// are O(1), and Raw shares no mutable access with callers.
type InvocationUsageView struct {
	value *sessionformat.InvocationUsage
}

// ObservedEvidence retains optional diagnostic context and safe provider
// correlation, not local identities or proof of remote effects. Present-empty
// strings are retained; assignment borrows pointers and Clone detaches them.
// Callers own disclosure safety. See format.ObservedEvidence for provisional vocabulary.
type ObservedEvidence = sessionformat.ObservedEvidence

// ObservedEvidenceView is immutable invocation evidence. Zero and absent fields
// return an empty string, false, preserving explicitly present empty strings. Reads
// are O(1) and expose no mutable storage.
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
func checkUsageSize(v *InvocationUsage, b *valueBudget) {
	if v == nil {
		return
	}
	b.pointer()
	for _, p := range []*int64{v.Input, v.Output, v.CacheRead, v.CacheWrite, v.Reasoning} {
		if p != nil {
			b.pointer()
		}
	}
	checkOpaqueSize(v.Raw, b)
}
