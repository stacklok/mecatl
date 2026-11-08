package format

import (
	"errors"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
)

// SourceHead identifies the exact accepted history commit immediately preceding a
// model request. History scope belongs to the enclosing history envelope; zero
// Sequence or CID is invalid. It is a reference, not record framing or a record ID.
//
//dtogen:validate Validate
type SourceHead struct {
	// Sequence is the required positive accepted-commit sequence within the
	// enclosing history. Local validation does not prove ancestry or acceptance.
	Sequence int64 `cbor:"s"`
	// CID is the required content identity of Sequence. Local validation checks only
	// that it is nonzero; enclosing history reconstruction checks the reference.
	CID contentid.CID `cbor:"c"`
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
//
//dtogen:validate Validate
type ModelRequest struct {
	// RunID is the required positive numeric run component within the history
	// supplied by the enclosing envelope.
	RunID uint64 `cbor:"r"`
	// ID is the required positive numeric request component within RunID. The
	// enclosing history envelope supplies history scope. Admission checks allocation.
	ID uint64 `cbor:"o"`
	// Source is the required accepted head used for request construction. Local
	// traversal validates its shape; aggregate reconstruction verifies membership.
	Source SourceHead `cbor:"s"`
	// Recipe is the required opaque construction commitment. Local traversal checks
	// its syntax, not that it reproduces a request or authorizes invocation.
	Recipe ConstructionRecipe `cbor:"c"`
	// RetryOf optionally references a positive numeric request component in RunID.
	// Nil is absent; zero is invalid. The aggregate checks earlier membership.
	RetryOf *uint64 `cbor:"x,omitempty"`
}

// Validate checks local ordinals and optional retry shape, not source membership.
func (v ModelRequest) Validate() error {
	if v.RunID == 0 || v.ID == 0 {
		return errors.New("model request requires positive run and request ordinals")
	}
	if v.RetryOf != nil && *v.RetryOf == 0 {
		return errors.New("retry request ordinal must be positive")
	}
	return nil
}
