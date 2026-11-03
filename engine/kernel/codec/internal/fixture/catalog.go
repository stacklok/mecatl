package fixture

import "github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture/value"

// Catalog exercises nested containers, imported DTOs, pointers and excluded state.
type Catalog struct {
	Entries  map[value.Key][]*value.Detail `cbor:"entries" cborlimit:"items=4096"`
	Payload  value.Blob                    `cbor:"payload" cborlimit:"bytes=262144"`
	Names    []string                      `cbor:"names" cborlimit:"items=4"`
	Required *value.Blob                   `cbor:"required" cborlimit:"bytes=8"`
	Optional *[]string                     `cbor:"optional,omitempty" cborlimit:"items=4"`
	Cache    map[string]*CacheEntry        `cbor:"-"`
}

// CacheEntry is excluded in-memory state with no persistence tags.
type CacheEntry struct {
	Bytes []byte
	Lists map[string][]*int64
}
