package format

import (
	"errors"
	"unicode/utf8"
)

// ToolEvidence retains exact diagnostic, preparation or authorization evidence,
// never model content or disclosure permission. Producers must arrange safe
// release and scrubbing before capture; UTF-8 validation is not sanitization.
// Nil fields are absent; present-empty strings survive. Assignment shares mutable
// storage; Clone detaches it. Zero is valid.
//
// Design status: Detail and Correlation are provisional choices for producer
// diagnostics, pending real preparation/authorization/executor usage. Revisit
// their names and semantics with those integrations; current exact preservation,
// presence, bounds, ownership and release obligations remain required. Redesign
// must be explicit, never a silent reinterpretation of retained evidence.
//
//dtogen:validate validateLocal
type ToolEvidence struct {
	// Detail is optional exact UTF-8 diagnostic context, at most 16384 bytes.
	Detail *string `cbor:"d,omitempty" cborlimit:"bytes=16384"`
	// Correlation is an optional opaque UTF-8 diagnostic label, at most 1024 bytes,
	// not a local occurrence identity or authority.
	Correlation *string `cbor:"c,omitempty" cborlimit:"bytes=1024"`
	// Metadata is optional uninterpreted producer evidence; nil means absent.
	Metadata *OpaqueToolData `cbor:"m,omitempty"`
}

// Validate checks bounded strings and metadata, not disclosure safety.
func (v ToolEvidence) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.Metadata != nil {
		return v.Metadata.Validate()
	}
	return nil
}

// validateLocal checks immediate bounded diagnostic strings only.
func (v ToolEvidence) validateLocal() error {
	if v.Detail != nil && (len(*v.Detail) > 16384 || !utf8.ValidString(*v.Detail)) {
		return errors.New("tool detail requires bounded UTF-8")
	}
	if v.Correlation != nil && (len(*v.Correlation) > 1024 || !utf8.ValidString(*v.Correlation)) {
		return errors.New("tool correlation requires bounded UTF-8")
	}
	return nil
}

// OpaqueToolData preserves tool-producer bytes, distinct from provider data,
// without interpreting their schema or granting execution, replay or disclosure.
// Assignment shares Data; Clone detaches it. Zero is invalid.
//
// Design status: cross-producer naming and revision conventions are provisional
// pending producer usage and must be revisited by consumers. Exact preservation
// and validation remain required; provisional naming permits no reinterpretation.
//
//dtogen:validate validateLocal
type OpaqueToolData struct {
	// Producer is a required nonempty UTF-8 producer label, at most 1024 bytes,
	// not a kernel identity or proof of ownership.
	Producer string `cbor:"p" cborlimit:"bytes=1024"`
	// Format is a required nonempty UTF-8 producer-owned schema label, at most
	// 1024 bytes. Its schema is not interpreted here.
	Format string `cbor:"f" cborlimit:"bytes=1024"`
	// Version is the required positive producer-defined revision, not format.Version.
	Version int64 `cbor:"v"`
	// Data is opaque and at most 65536 bytes; nil and empty are equivalent.
	Data []byte `cbor:"d" cborlimit:"bytes=65536"`
}

// Validate checks schema bounds and required metadata, not Data semantics.
func (v OpaqueToolData) Validate() error { return v.validateLocal() }

// validateLocal bounds immediate values before UTF-8 scans.
func (v OpaqueToolData) validateLocal() error {
	if len(v.Producer) == 0 || len(v.Producer) > 1024 || len(v.Format) == 0 || len(v.Format) > 1024 || len(v.Data) > 65536 || v.Version <= 0 {
		return errors.New("opaque tool data requires bounded producer, format, data and positive version")
	}
	if !utf8.ValidString(v.Producer) || !utf8.ValidString(v.Format) {
		return errors.New("opaque tool metadata must be UTF-8")
	}
	return nil
}
