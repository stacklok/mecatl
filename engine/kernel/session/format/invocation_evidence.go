package format

import (
	"errors"
	"unicode/utf8"
)

// OpaqueProviderData retains producer-owned bytes without interpreting their schema.
// Producer, Format, and Version identify the producer's format revision; they do not
// make the bytes executable, safe to disclose, or replayable. Data may be nil or
// empty; both encode as equivalent empty bytes. Assignment shares Data; Clone
// detaches it.
//
// Design status: retaining these metadata fields is normative, but their exact
// cross-adapter naming and versioning semantics are provisional pending adapter
// usage. Future consumers must reassess the metadata contract rather than treating
// it as settled precedent. This does not permit reinterpretation of stored bytes or
// bypass compatibility review.
//
//dtogen:validate Validate
type OpaqueProviderData struct {
	// Producer is the required nonempty UTF-8 producer identifier selected by the
	// producer that owns Data. It is not a kernel identity or authority.
	Producer string `cbor:"p"`
	// Format is the required nonempty UTF-8 name of the producer-owned Data schema.
	// The kernel does not validate or interpret that schema.
	Format string `cbor:"f"`
	// Version is the required positive producer-defined revision of Format. It is
	// not the enclosing session payload schema version.
	Version int64 `cbor:"v"`
	// Data is retained opaque producer-owned payload bytes. Nil and empty are
	// equivalent empty bytes; validation does not inspect their schema or content.
	Data []byte `cbor:"d"`
}

// Validate checks required metadata syntax and version, not Data semantics or an
// adapter's ownership of the declared metadata.
func (v OpaqueProviderData) Validate() error {
	if v.Producer == "" || v.Format == "" || !utf8.ValidString(v.Producer) || !utf8.ValidString(v.Format) || v.Version <= 0 {
		return errors.New("opaque provider data requires UTF-8 producer, format, and positive version")
	}
	return nil
}

// InvocationUsage retains optional provider-reported counters and optional
// usage-only opaque provider evidence. Input and Output are inclusive counts;
// CacheRead, CacheWrite, and Reasoning are breakdowns rather than additional spend.
// Nil counters are absent and distinct from reported zero. Assignment shares
// pointers and Raw's bytes; Clone detaches them.
//
//dtogen:validate validateLocal
type InvocationUsage struct {
	// Input is an optional nonnegative provider-reported inclusive input count.
	Input *int64 `cbor:"i,omitempty"`
	// Output is an optional nonnegative provider-reported inclusive output count.
	Output *int64 `cbor:"o,omitempty"`
	// CacheRead is an optional nonnegative input-cache-read breakdown, not extra spend.
	CacheRead *int64 `cbor:"r,omitempty"`
	// CacheWrite is an optional nonnegative input-cache-write breakdown, not extra spend.
	CacheWrite *int64 `cbor:"w,omitempty"`
	// Reasoning is an optional nonnegative reasoning breakdown, not extra spend.
	Reasoning *int64 `cbor:"n,omitempty"`
	// Raw is optional usage-only producer evidence. It is absent when nil and must
	// not contain a response body; codec traversal validates its local metadata.
	Raw *OpaqueProviderData `cbor:"x,omitempty"`
}

// Validate checks nonnegative reported counters and raw metadata, not accounting
// equations or whether a provider actually charged for an invocation.
func (v InvocationUsage) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.Raw != nil {
		return v.Raw.Validate()
	}
	return nil
}

// validateLocal checks counters after bounded codec traversal validates Raw.
func (v InvocationUsage) validateLocal() error {
	for _, n := range []*int64{v.Input, v.Output, v.CacheRead, v.CacheWrite, v.Reasoning} {
		if n != nil && *n < 0 {
			return errors.New("usage count must be nonnegative")
		}
	}
	return nil
}

// ObservedEvidence retains optional diagnostic observations associated with an
// invocation. Detail is diagnostic context (for example, an adapter-observed
// transport status), whereas an InvocationFailure or RunEnded Explanation is the
// owner's human-readable account of that outcome. Correlation is a safe
// provider-supplied diagnostic identifier for relating observations; it is not a
// session, run, request, or other local identity. ProviderStopReason separately
// retains the provider's stop/finish reason. Nil fields are absent; non-nil empty
// strings are retained. Assignment shares pointers; Clone detaches them.
//
// Callers are responsible for disclosure safety. UTF-8 validation is not
// sanitization and proves neither safety to display nor remote processing, spend,
// execution, or replay.
//
// Design status: the current retention, UTF-8 validation, encoding, and ownership
// contract is normative. The usefulness and vocabulary of Detail and Correlation
// are provisional pending adapter usage; future consumers must reassess them rather
// than treating their semantics as settled precedent. This does not permit silently
// reinterpreting persisted values or bypassing compatibility review.
//
//dtogen:validate Validate
type ObservedEvidence struct {
	// Detail is optional UTF-8 diagnostic context observed by the caller or adapter.
	// It is not the owner's Explanation or a provider stop/finish reason.
	Detail *string `cbor:"d,omitempty"`
	// Correlation is an optional UTF-8 safe provider diagnostic identifier. It is
	// not a locally allocated identity and has no identity or ordering semantics.
	Correlation *string `cbor:"c,omitempty"`
}

// Validate checks UTF-8 only; disclosure safety and the relationship to a provider
// observation are supplied by the caller, not this payload-local validator.
func (v ObservedEvidence) Validate() error {
	for _, s := range []*string{v.Detail, v.Correlation} {
		if s != nil && !utf8.ValidString(*s) {
			return errors.New("observed evidence must be UTF-8")
		}
	}
	return nil
}
