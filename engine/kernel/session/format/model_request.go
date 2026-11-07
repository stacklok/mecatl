package format

import (
	"errors"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
)

// SourceHead is the exact accepted commit immediately preceding a model request.
// It is scoped by the enclosing history envelope. Zero is invalid.
//
//cborgen:validate Validate
type SourceHead struct {
	Sequence int64         `cbor:"s"`
	CID      contentid.CID `cbor:"c"`
}

// Validate checks the local commit reference shape, not history membership.
func (v SourceHead) Validate() error {
	if v.Sequence <= 0 || v.CID == (contentid.CID{}) {
		return errors.New("source head requires a positive sequence and CID")
	}
	return nil
}

// ConstructionRecipe commits to a request-construction algorithm without interpreting
// it. Parameters may be empty; Check must be nonempty. Assignment shares byte slices;
// Clone detaches them.
//
//cborgen:validate Validate
type ConstructionRecipe struct {
	Algorithm  string `cbor:"a"`
	Version    int64  `cbor:"v"`
	Parameters []byte `cbor:"p"`
	Check      []byte `cbor:"c"`
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
//cborgen:validate Validate
type ModelRequest struct {
	RunID   uint64             `cbor:"r"`
	Ordinal uint64             `cbor:"o"`
	Source  SourceHead         `cbor:"s"`
	Recipe  ConstructionRecipe `cbor:"c"`
	RetryOf *uint64            `cbor:"x,omitempty"`
}

// Validate checks local ordinals and optional retry shape, not source membership.
func (v ModelRequest) Validate() error {
	if v.RunID == 0 || v.Ordinal == 0 {
		return errors.New("model request requires positive run and request ordinals")
	}
	if v.RetryOf != nil && *v.RetryOf == 0 {
		return errors.New("retry request ordinal must be positive")
	}
	return nil
}
