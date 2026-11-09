package format

import (
	"errors"
	"unicode/utf8"
)

// ToolInvocation retains effective authorized intent, which may differ from the
// requested call. Commitment is a higher-layer authorization assertion; evidence
// is optional. Reload grants no permission or lease and proves no dispatch.
// Assignment shares mutable storage; Clone detaches it. Zero is invalid.
// History scope is omitted. The authoring contract supplies a full RequestedCallID
// separately with all three destination fields zero; Validate instead checks
// persisted positive components, not aggregate admission.
//
//dtogen:include
//dtogen:validate validateLocal
type ToolInvocation struct {
	// RunID is the required positive run component within the enclosing history.
	RunID uint64 `cbor:"r" view:"-"`
	// RequestID is the required positive request component within RunID.
	RequestID uint64 `cbor:"q" view:"-"`
	// CallID is the required positive requested-call component within the response
	// to RequestID; reconstruction proves neither membership nor authority.
	CallID uint64 `cbor:"c" view:"manual"`
	// Name is exact nonempty UTF-8 effective tool intent, at most 4096 bytes.
	Name string `cbor:"n" cborlimit:"bytes=4096"`
	// Arguments are uninterpreted bytes, at most 4194304; nil and empty are equivalent.
	Arguments []byte `cbor:"a" cborlimit:"bytes=4194304"`
	// Prerequisites is an ordered list of at most 4096 positive response-relative
	// call components, excluding self and duplicates. Equality and canonical bytes
	// preserve list order; dependency eligibility depends only on membership.
	// Nil and empty are equivalent. Membership, cycles and accepted authorization
	// are aggregate obligations, not local validation.
	Prerequisites []uint64 `cbor:"p" cborlimit:"items=4096" view:"manual"`
	// Preparation optionally retains preparation evidence; nil means absent.
	Preparation *ToolEvidence `cbor:"e,omitempty"`
	// Authorization optionally retains separate authorization evidence; nil is absent.
	Authorization *ToolEvidence `cbor:"u,omitempty"`
	// Provenance optionally retains descriptive observations, not executable binding.
	Provenance *ToolProvenance `cbor:"v,omitempty"`
}

// Validate checks persisted identity, bounded intent, prerequisites and children,
// not aggregate acceptance, lifecycle or execution.
func (v ToolInvocation) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.Preparation != nil {
		if err := v.Preparation.Validate(); err != nil {
			return err
		}
	}
	if v.Authorization != nil {
		if err := v.Authorization.Validate(); err != nil {
			return err
		}
	}
	if v.Provenance != nil {
		return v.Provenance.Validate()
	}
	return nil
}

// validateLocal bounds immediate values before scanning or allocating the set.
func (v ToolInvocation) validateLocal() error {
	if v.RunID == 0 || v.RequestID == 0 || v.CallID == 0 {
		return errors.New("tool invocation requires positive run, request and call components")
	}
	if len(v.Name) == 0 || len(v.Name) > 4096 || len(v.Arguments) > 4194304 || len(v.Prerequisites) > 4096 {
		return errors.New("tool invocation exceeds schema bounds or has empty name")
	}
	if !utf8.ValidString(v.Name) {
		return errors.New("tool name must be UTF-8")
	}
	seen := make(map[uint64]struct{}, len(v.Prerequisites))
	for _, call := range v.Prerequisites {
		if call == 0 || call == v.CallID {
			return errors.New("invalid tool prerequisite")
		}
		if _, ok := seen[call]; ok {
			return errors.New("duplicate tool prerequisite")
		}
		seen[call] = struct{}{}
	}
	return nil
}

// ToolProvenance retains independent opaque descriptive labels, not executable
// binding or a universal deployment schema. Partial observations are valid; nil
// labels are absent and present-empty labels are retained. Each label is UTF-8,
// at most 1024 bytes. Assignment shares pointers; Clone detaches them. Zero is valid.
//
//dtogen:validate validateLocal
type ToolProvenance struct {
	// Implementation optionally describes the implementation.
	Implementation *string `cbor:"i,omitempty" cborlimit:"bytes=1024"`
	// Version optionally describes its producer-defined version.
	Version *string `cbor:"v,omitempty" cborlimit:"bytes=1024"`
	// Runner optionally describes the runner.
	Runner *string `cbor:"r,omitempty" cborlimit:"bytes=1024"`
	// Environment optionally describes the environment.
	Environment *string `cbor:"e,omitempty" cborlimit:"bytes=1024"`
	// EnvironmentRevision optionally describes an independently observed revision.
	EnvironmentRevision *string `cbor:"x,omitempty" cborlimit:"bytes=1024"`
}

// Validate checks bounded label syntax without coupling optional observations.
func (v ToolProvenance) Validate() error { return v.validateLocal() }

// validateLocal checks immediate labels only.
func (v ToolProvenance) validateLocal() error {
	for _, s := range []*string{v.Implementation, v.Version, v.Runner, v.Environment, v.EnvironmentRevision} {
		if s != nil && (len(*s) > 1024 || !utf8.ValidString(*s)) {
			return errors.New("tool provenance requires bounded UTF-8 labels")
		}
	}
	return nil
}
