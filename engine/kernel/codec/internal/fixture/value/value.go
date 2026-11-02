package value

// Octet is a named byte element, not an alias of byte.
type Octet uint8

// Key is a named map key.
type Key string

// Blob exercises imported named byte strings.
type Blob []Octet

// Detail is an imported DTO whose mutable fields remain accessible.
type Detail struct {
	Data Blob `cbor:"data" cborlimit:"bytes=@detail"`
}
