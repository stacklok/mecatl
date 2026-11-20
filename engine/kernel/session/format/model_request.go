package format

import (
	"errors"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
)

// SourceHead identifies the exact accepted history commit anchoring construction
// of a model request. Earlier records in the request's own commit also contribute.
// History scope belongs to the enclosing history envelope; zero Sequence or CID
// is invalid. It is a reference, not record framing or a record ID.
//
//dtogen:validate Validate
//dtogen:include
type SourceHead struct {
	// Sequence is the required positive accepted-commit sequence within the
	// enclosing history. Local validation does not prove ancestry or acceptance.
	Sequence int64 `cbor:"s"`
	// CID is the required content identity of Sequence. Local validation checks only
	// that it is nonzero; enclosing history reconstruction checks the reference.
	CID contentid.CID `cbor:"c" view:"manual"`
}

// Validate checks the local commit reference shape, not history membership.
func (v SourceHead) Validate() error {
	if v.Sequence <= 0 || v.CID == (contentid.CID{}) {
		return errors.New("source head requires a positive sequence and CID")
	}
	return nil
}

// ConstructionRecipe commits to a request-construction algorithm without
// interpreting it. Parameters may be nil or empty, which are equivalent empty
// bytes; Check is required and nonempty. Assignment shares byte slices; Clone
// detaches them.
//
//dtogen:validate Validate
//dtogen:include
type ConstructionRecipe struct {
	// Algorithm is the required nonempty UTF-8 name of the construction algorithm.
	// This payload does not resolve or execute the named algorithm.
	Algorithm string `cbor:"a"`
	// Version is the required positive algorithm-defined revision, not the session
	// payload schema version.
	Version int64 `cbor:"v"`
	// Parameters are optional opaque algorithm input bytes. Nil and empty are
	// equivalent; this payload does not interpret their schema.
	Parameters []byte `cbor:"p"`
	// Check is required nonempty opaque bytes committed by Algorithm and Version.
	// Local validation requires presence but does not interpret or verify it.
	Check []byte `cbor:"c"`
}

// Validate checks structural algorithm identifiers and retained bytes only.
func (v ConstructionRecipe) Validate() error {
	if v.Algorithm == "" || !utf8.ValidString(v.Algorithm) || v.Version <= 0 || len(v.Check) == 0 {
		return errors.New("recipe requires an algorithm, positive version, and check")
	}
	return nil
}

// ModelRequest is one invocation commitment. RetryOf, when present, is an earlier
// request ordinal in the same enclosing run; membership and ordering are aggregate
// concerns. Assignment shares nested byte slices; Clone detaches them.
// Session authoring supplies destination scope separately and requires RunID and
// ID zero and Source nil. RetryOf is interpreted within that destination run
// without origin provenance. Validate requires the populated persisted shape.
//
//dtogen:validate validateLocal
//dtogen:include
type ModelRequest struct {
	// RunID is the required positive Session-relative run component; the root
	// history envelope supplies Session scope.
	RunID uint64 `cbor:"r" view:"-"`
	// ID is the required positive numeric request component within RunID. The
	// root history envelope supplies Session scope. Admission checks allocation.
	ID uint64 `cbor:"o" view:"manual"`
	// Source optionally anchors construction to the preceding accepted head. Nil
	// means pre-genesis, valid only in the initial commit. Present anchors require
	// positive Sequence and nonzero CID. All records earlier in this same commit
	// also belong to the source; aggregate admission verifies the exact anchor.
	Source *SourceHead `cbor:"s,omitempty"`
	// Recipe is the required opaque construction commitment. Local traversal checks
	// its syntax, not that it reproduces a request or authorizes invocation.
	Recipe ConstructionRecipe `cbor:"c"`
	// RetryOf optionally references a positive numeric request component in RunID.
	// Nil is absent; zero is invalid. The aggregate checks earlier membership.
	RetryOf *uint64 `cbor:"x,omitempty" view:"manual"`
}

// Validate checks persisted identity, recipe, source and retry shape, not membership.
func (v ModelRequest) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.Source != nil {
		if err := v.Source.Validate(); err != nil {
			return err
		}
	}
	return v.Recipe.Validate()
}

// validateLocal checks ordinals after bounded codec traversal validates children.
func (v ModelRequest) validateLocal() error {
	if v.RunID == 0 || v.ID == 0 {
		return errors.New("model request requires positive run and request ordinals")
	}
	if v.RetryOf != nil && *v.RetryOf == 0 {
		return errors.New("retry request ordinal must be positive")
	}
	return nil
}
