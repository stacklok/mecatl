package value

// Octet is a named byte element, not an alias of byte.
//
//dtogen:include
type Octet uint8

// Key is a named map key.
//
//dtogen:include
type Key string

// Blob exercises imported named byte strings.
//
//dtogen:include
type Blob []Octet

// Detail is an imported DTO whose mutable fields remain accessible.
//
//dtogen:include
type Detail struct {
	Data Blob `cbor:"data" cborlimit:"bytes=8"`
}
