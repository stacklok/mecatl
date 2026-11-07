package session

import (
	"bytes"

	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// OpaqueProviderData retains producer-owned bytes without interpreting their schema.
// Producer, Format, and Version identify the producer's format revision; they do not
// make Data executable, safe to disclose, or replayable. Data may be nil or empty,
// which are equivalent empty bytes. Assignment borrows Data; Clone detaches it.
//
// Design status: retaining this metadata is normative, but its cross-adapter naming
// and versioning semantics are provisional pending adapter usage. Future consumers
// must reassess the contract rather than treat it as settled precedent. This does
// not permit reinterpreting stored bytes or bypassing compatibility review.
type OpaqueProviderData struct {
	// Producer identifies the producer that owns Data; it is not a kernel identity.
	Producer string
	// Format names the producer-owned Data schema, which the kernel does not interpret.
	Format string
	// Version is the positive producer-defined Format revision.
	Version int64
	// Data contains opaque producer-owned bytes; nil and empty are equivalent.
	Data []byte
}

// Validate checks the local provider-data shape.
func (v OpaqueProviderData) Validate() error { return v.toFormat().Validate() }

// Clone detaches provider-owned bytes.
func (v OpaqueProviderData) Clone() OpaqueProviderData { v.Data = bytes.Clone(v.Data); return v }

// Equal compares exact provider-data values.
func (v OpaqueProviderData) Equal(other OpaqueProviderData) bool {
	return v.toFormat().Equal(other.toFormat())
}
func (v OpaqueProviderData) toFormat() sessionformat.OpaqueProviderData {
	return sessionformat.OpaqueProviderData{Producer: v.Producer, Format: v.Format, Version: v.Version, Data: v.Data}
}

// InvocationUsage retains optional per-invocation reported counters. Input and
// Output are inclusive counts; CacheRead, CacheWrite, and Reasoning are breakdowns
// that do not add to spend. Nil is absent and distinct from reported zero. Raw
// contains usage-only provider evidence, never a response body. Assignment borrows
// pointers and data; Clone detaches all storage.
type InvocationUsage struct {
	Input, Output, CacheRead, CacheWrite, Reasoning *int64
	Raw                                             *OpaqueProviderData
}

// Validate checks counter nonnegativity and raw evidence structure.
func (v InvocationUsage) Validate() error {
	if err := v.toFormat().Validate(); err != nil {
		return err
	}
	if v.Raw != nil {
		return v.Raw.Validate()
	}
	return nil
}

// Clone detaches counters and raw evidence.
func (v InvocationUsage) Clone() InvocationUsage {
	out := v
	for _, p := range []*(*int64){&out.Input, &out.Output, &out.CacheRead, &out.CacheWrite, &out.Reasoning} {
		if *p != nil {
			n := **p
			*p = &n
		}
	}
	if out.Raw != nil {
		raw := out.Raw.Clone()
		out.Raw = &raw
	}
	return out
}

// Equal compares optional counter presence and raw evidence exactly.
func (v InvocationUsage) Equal(other InvocationUsage) bool {
	return v.toFormat().Equal(other.toFormat())
}
func (v InvocationUsage) toFormat() sessionformat.InvocationUsage {
	return sessionformat.InvocationUsage{Input: v.Input, Output: v.Output, CacheRead: v.CacheRead, CacheWrite: v.CacheWrite, Reasoning: v.Reasoning, Raw: opaqueToFormat(v.Raw)}
}

// ObservedEvidence retains optional diagnostic observations associated with an
// invocation. Detail is diagnostic context, whereas a failure or ending Explanation
// is the owner's human-readable account; ProviderStopReason is the provider's
// original stop/finish reason. Correlation is a safe provider diagnostic identifier,
// not a local session, run, or request ID. Nil fields are absent and non-nil empty
// values are preserved. Assignment borrows pointers; Clone detaches them.
//
// Callers are responsible for disclosure safety. UTF-8 validation is not
// sanitization and proves neither safe display nor remote processing, spend,
// execution, or replay.
//
// Design status: current retention, validation, encoding, and ownership are
// normative. Detail and Correlation vocabulary is provisional pending adapter usage;
// future consumers must reassess it rather than treat it as settled precedent.
type ObservedEvidence struct {
	// Detail is optional diagnostic context, not an owner Explanation or stop reason.
	Detail *string
	// Correlation is an optional safe provider diagnostic identifier, not a local ID.
	Correlation *string
}

// Validate checks UTF-8 evidence fields.
func (v ObservedEvidence) Validate() error { return v.toFormat().Validate() }

// Clone detaches optional evidence strings.
func (v ObservedEvidence) Clone() ObservedEvidence {
	if v.Detail != nil {
		s := *v.Detail
		v.Detail = &s
	}
	if v.Correlation != nil {
		s := *v.Correlation
		v.Correlation = &s
	}
	return v
}

// Equal compares optional evidence presence and exact strings.
func (v ObservedEvidence) Equal(other ObservedEvidence) bool {
	return v.toFormat().Equal(other.toFormat())
}
func (v ObservedEvidence) toFormat() sessionformat.ObservedEvidence {
	return sessionformat.ObservedEvidence{Detail: v.Detail, Correlation: v.Correlation}
}

func opaqueToFormat(v *OpaqueProviderData) *sessionformat.OpaqueProviderData {
	if v == nil {
		return nil
	}
	x := v.toFormat()
	return &x
}
func evidenceToFormat(v *ObservedEvidence) *sessionformat.ObservedEvidence {
	if v == nil {
		return nil
	}
	x := v.toFormat()
	return &x
}
func usageToFormat(v *InvocationUsage) *sessionformat.InvocationUsage {
	if v == nil {
		return nil
	}
	x := v.toFormat()
	return &x
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
func opaqueFromFormat(v *sessionformat.OpaqueProviderData) *OpaqueProviderData {
	if v == nil {
		return nil
	}
	return &OpaqueProviderData{Producer: v.Producer, Format: v.Format, Version: v.Version, Data: v.Data}
}
func evidenceFromFormat(v *sessionformat.ObservedEvidence) *ObservedEvidence {
	if v == nil {
		return nil
	}
	return &ObservedEvidence{Detail: v.Detail, Correlation: v.Correlation}
}
func usageFromFormat(v *sessionformat.InvocationUsage) *InvocationUsage {
	if v == nil {
		return nil
	}
	return &InvocationUsage{Input: v.Input, Output: v.Output, CacheRead: v.CacheRead, CacheWrite: v.CacheWrite, Reasoning: v.Reasoning, Raw: opaqueFromFormat(v.Raw)}
}
