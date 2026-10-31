package format

import (
	"errors"
	"math"
	"unicode/utf8"

	"github.com/jcalabro/atmos/cbor"
)

// headerSize is the minimal CBOR header size for a nonnegative length.
func headerSize(n int) int {
	switch {
	case n < 24:
		return 1
	case n <= math.MaxUint8:
		return 2
	case n <= math.MaxUint16:
		return 3
	case uint64(n) <= math.MaxUint32:
		return 5
	default:
		return 9
	}
}

// versionHeaderSize is the minimal canonical CBOR header size for a positive
// version. Record and commit validation establishes the precondition.
func versionHeaderSize(n int64) int {
	switch {
	case n < 24:
		return 1
	case n <= math.MaxUint8:
		return 2
	case n <= math.MaxUint16:
		return 3
	case n <= math.MaxUint32:
		return 5
	default:
		return 9
	}
}

// cborDecoder is a bounded cursor for the fixed-depth closed schema. The first
// error is sticky; later reads neither advance nor allocate. It never consults
// or changes Atmos's global resource policies, nor borrows decoded DTO storage.
type cborDecoder struct {
	data []byte
	pos  int
	err  error
}

// header reads a minimal definite-length header of the required major type.
func (d *cborDecoder) header(want byte) uint64 {
	if d.err != nil {
		return 0
	}
	major, n, next, err := cbor.ReadHeader(d.data, d.pos)
	if err != nil || major != want {
		d.err = errors.New("invalid canonical CBOR header or type")
		return 0
	}
	d.pos = next
	return n
}

// literal requires an exact wire byte, including canonical null and tag bytes.
func (d *cborDecoder) literal(want byte) {
	if d.err != nil {
		return
	}
	if d.pos == len(d.data) || d.data[d.pos] != want {
		d.err = errors.New("unexpected schema item")
		return
	}
	d.pos++
}

// key requires the next canonical one-byte schema key, rejecting unknown keys,
// duplicates, missing fields, and noncanonical map ordering without generic skip.
func (d *cborDecoder) key(want byte) {
	d.literal(0x61)
	d.literal(want)
}

// scalar bounds length before conversion and slicing; text must be UTF-8. The
// borrowed view is internal only and is copied when installed into a DTO.
func (d *cborDecoder) scalar(major byte, maxBytes int) []byte {
	n := d.header(major)
	if d.err != nil {
		return nil
	}
	remaining := len(d.data) - d.pos
	if n > uint64(remaining) { // #nosec G115 -- the decoder cursor stays within data, so remaining is nonnegative.
		d.err = errors.New("scalar exceeds limit or remaining data")
		return nil
	}
	length := int(n) // #nosec G115 -- n is bounded by remaining, an int.
	if length > maxBytes {
		d.err = errors.New("scalar exceeds limit or remaining data")
		return nil
	}
	value := d.data[d.pos : d.pos+length]
	d.pos += length
	if major == 3 && !utf8.Valid(value) {
		d.err = errors.New("text is not UTF-8")
		return nil
	}
	return value
}

// version accepts only positive minimal integers representable as int64.
func (d *cborDecoder) version() int64 {
	n := d.header(0)
	if d.err != nil {
		return 0
	}
	if n == 0 || n > math.MaxInt64 {
		d.err = errors.New("version must be positive int64")
		return 0
	}
	return int64(n)
}
