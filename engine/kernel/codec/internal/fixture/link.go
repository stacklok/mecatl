package fixture

import "github.com/stacklok/mecatl/engine/kernel/contentid"

// CIDAlias proves aliases use their production owner's methods.
type CIDAlias = contentid.CID

// Link isolates a required link for exact wire and budget qualification.
type Link struct {
	Value CIDAlias `cbor:"v"`
}

// Links exercises presence, containers, opaque bytes and excluded CID state.
type Links struct {
	Required *contentid.CID         `cbor:"r"`
	Optional *CIDAlias              `cbor:"o,omitempty"`
	Nested   map[string][]*CIDAlias `cbor:"n"`
	Opaque   []byte                 `cbor:"b"`
	Excluded contentid.CID          `cbor:"-"`
	Cache    *CIDAlias              `cbor:"-"` //nolint:revive // Multiple excluded fields intentionally share the cbor "-" tag.
}
