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
//dtogen:include
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
//dtogen:include
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
