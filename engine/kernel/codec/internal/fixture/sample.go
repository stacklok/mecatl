package fixture

import "github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture/value"

// Sample exercises fixed-width values, byte arrays and optional scalar presence.
type Sample struct {
	I8       int8           `cbor:"i8"`
	I16      int16          `cbor:"i16"`
	I32      int32          `cbor:"i32"`
	I64      int64          `cbor:"i64"`
	U8       uint8          `cbor:"u8"`
	U16      uint16         `cbor:"u16"`
	U32      uint32         `cbor:"u32"`
	U64      uint64         `cbor:"u64"`
	OK       bool           `cbor:"ok"`
	Label    string         `cbor:"label" cborlimit:"bytes=32"`
	Fixed    [3]value.Octet `cbor:"fixed"`
	Optional *int64         `cbor:"optional,omitempty"`
}

// Validate is a planted domain hook: codecs and value methods must not call it.
func (Sample) Validate() error { panic("domain validation is owner-controlled") }
