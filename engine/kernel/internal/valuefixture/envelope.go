package valuefixture

import "github.com/stacklok/mecatl/engine/kernel/value"

//go:generate go -C ../../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/internal/valuefixture -emit cbor,clone,equal -out zz_data_generated.go

// Envelope is a test-only-in-purpose persistence DTO, not a versioned history
// record. All fields are required; nil maps/slices mean empty. Mutable authoring
// inputs may share backing until Check/Encode/Clone under stable input ownership.
//
//dtogen:include
type Envelope struct {
	// Value is one required initialized native structured value.
	Value value.Value `cbor:"v"`
	// Map holds required initialized values, with an immediate three-pair cap.
	Map value.Map `cbor:"m" cborlimit:"items=3"`
	// Labels preserves exact string keys and scalar values.
	Labels map[Key]string `cbor:"l"`
	// Counts is a scalar map with ordinary string keys.
	Counts map[string]uint64 `cbor:"c"`
	// Children retains required nonnil DTO pointers, keyed by exact text.
	Children map[string]*Child `cbor:"ch"`
	// Sibling is an unrelated required byte payload, empty when nil.
	Sibling []byte `cbor:"s"`
}

// Key is a string-underlying fixture map key.
type Key string

// Child owns a leaf byte payload and ordered nested children. Every slice
// element is required, while nil means an empty slice.
type Child struct {
	// Label is required text, including empty.
	Label string `cbor:"l"`
	// Data is required opaque bytes, including empty.
	Data []byte `cbor:"d"`
	// Children contains ordered required nested leaf DTOs.
	Children []Leaf `cbor:"c"`
}

// Leaf owns one required byte-string payload; nil means empty.
type Leaf struct {
	// Data is required opaque bytes.
	Data []byte `cbor:"d"`
}

// Native transparently composes a Value, preserving the caller's immediate cap.
//
//dtogen:include
type Native struct {
	// Value is required and initialized; no extra wire envelope is introduced.
	Value value.Value `cbor:",value"` //nolint:revive // Transparent representation uses the generator's cbor ",value" tag.
}

// Parent proves transitive propagation through a generated containing DTO.
//
//dtogen:include
type Parent struct {
	// Envelope is a required by-value child.
	Envelope Envelope `cbor:"e"`
}
