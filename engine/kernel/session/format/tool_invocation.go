package format

import (
	"errors"
	"math"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/value"
)

// ToolInvocation retains effective authorized intent, which may differ from the
// requested call. Commitment is a higher-layer assertion that the same intent is
// prepared and authorized for execution; supporting preparation/authorization
// observations have no persisted representation here. Reload grants no permission
// or lease and proves no dispatch. Typed ancillary records may later retain those
// observations or execution provenance when producers establish their semantics
// and placement. Assignment shares mutable storage; Clone detaches it. Zero is invalid.
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
	// Arguments is the required effective parameter object, with at most 4096
	// immediate entries. Nil and empty both mean an empty object. Keys and nested
	// values obey value.Map's UTF-8, initialized-value and local-depth contracts;
	// enclosing codec limits bound descendant sizes and total retained work.
	// Assignment shares backing; Clone detaches all mutable descendants. Original
	// model-request bytes remain separate from these effective semantic arguments.
	Arguments value.Map `cbor:"a" cborlimit:"items=4096"`
	// Prerequisites is an ordered list of at most 4096 positive response-relative
	// call components, excluding self and duplicates. Equality and canonical bytes
	// preserve list order; dependency eligibility depends only on membership.
	// Nil and empty are equivalent. Membership, cycles and accepted authorization
	// are aggregate obligations, not local validation.
	Prerequisites []uint64 `cbor:"p" cborlimit:"items=4096" view:"manual"`
}

// Validate checks persisted identity, bounded intent, prerequisites and children,
// not aggregate acceptance, lifecycle or execution.
func (v ToolInvocation) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	// Reuse the primitive's structural checks without choosing operational policy
	// for callers of Validate. Codec admission supplies the real shared limits.
	return codec.Check(v.Arguments, codec.Limits{
		MaxBytes: math.MaxInt, MaxStringBytes: math.MaxInt, MaxByteStringBytes: math.MaxInt,
		MaxArrayElements: math.MaxInt, MaxMapPairs: math.MaxInt, MaxDepth: math.MaxInt,
		MaxNodes: math.MaxInt, MaxCollectionElements: math.MaxInt,
	})
}

// validateLocal bounds immediate values before scanning or allocating the set.
func (v ToolInvocation) validateLocal() error {
	if v.RunID == 0 || v.RequestID == 0 || v.CallID == 0 {
		return errors.New("tool invocation requires positive run, request and call components")
	}
	if len(v.Name) == 0 || len(v.Name) > 4096 || len(v.Arguments) > 4096 || len(v.Prerequisites) > 4096 {
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
