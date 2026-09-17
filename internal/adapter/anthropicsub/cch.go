package anthropicsub

import (
	"bytes"
	"encoding/hex"
	"math/bits"
)

// The first-party client stamps an attestation over the serialized request
// body: a seeded XXH64 of the bytes with the placeholder still in place, whose
// low 20 bits are written back over the placeholder's five zeros.
//
// XXH64 is implemented here rather than taken from a dependency because the
// attestation needs a non-zero seed and the vendored xxhash package exposes
// only the seedless form.
const (
	cchSeed        = 0x4d659218e32a3268
	cchPlaceholder = "cch=00000"
	cchDigits      = 5
	// cchSearchWindow bounds how far past the billing block's marker the
	// placeholder may sit, so a placeholder-shaped string elsewhere in the
	// payload cannot be mistaken for the real one.
	cchSearchWindow = 150
)

// billingMarker anchors the placeholder to the first system block. Anchoring
// matters because the same literal appearing in user content must not be
// rewritten.
var billingMarker = []byte(`"system":[{"type":"text","text":"` + billingHeaderPrefix)

// patchCch rewrites the attestation placeholder in place and reports whether
// it did. A body with no billing block is left untouched, which is the correct
// outcome for an API-key request.
func patchCch(body []byte) bool {
	marker := bytes.Index(body, billingMarker)
	if marker < 0 {
		return false
	}
	searchFrom := marker + len(billingMarker)
	offset := bytes.Index(body[searchFrom:], []byte(cchPlaceholder))
	if offset < 0 || offset > cchSearchWindow {
		return false
	}
	at := searchFrom + offset + len(cchPlaceholder) - cchDigits

	// Hashed with the placeholder still in place, matching the client's
	// in-place behaviour.
	sum := xxh64(body, cchSeed) & 0xfffff
	digits := hex.EncodeToString([]byte{
		byte(sum >> 16 & 0xff), byte(sum >> 8 & 0xff), byte(sum & 0xff),
	})
	// 20 bits render as five hex digits; the encoding above yields six, so the
	// leading nibble of the first byte is dropped.
	copy(body[at:at+cchDigits], digits[1:])
	return true
}

const (
	prime1 = 11400714785074694791
	prime2 = 14029467366897019727
	prime3 = 1609587929392839161
	prime4 = 9650029242287828579
	prime5 = 2870177450012600261
)

// xxh64 is XXH64 with an explicit seed.
func xxh64(input []byte, seed uint64) uint64 {
	var acc uint64
	length := len(input)
	remaining := input

	if length >= 32 {
		v1 := seed + prime1 + prime2
		v2 := seed + prime2
		v3 := seed
		v4 := seed - prime1
		for len(remaining) >= 32 {
			v1 = round(v1, u64(remaining[0:8]))
			v2 = round(v2, u64(remaining[8:16]))
			v3 = round(v3, u64(remaining[16:24]))
			v4 = round(v4, u64(remaining[24:32]))
			remaining = remaining[32:]
		}
		acc = bits.RotateLeft64(v1, 1) + bits.RotateLeft64(v2, 7) +
			bits.RotateLeft64(v3, 12) + bits.RotateLeft64(v4, 18)
		acc = mergeRound(acc, v1)
		acc = mergeRound(acc, v2)
		acc = mergeRound(acc, v3)
		acc = mergeRound(acc, v4)
	} else {
		acc = seed + prime5
	}

	acc += uint64(length)

	for len(remaining) >= 8 {
		acc ^= round(0, u64(remaining[0:8]))
		acc = bits.RotateLeft64(acc, 27)*prime1 + prime4
		remaining = remaining[8:]
	}
	if len(remaining) >= 4 {
		acc ^= uint64(u32(remaining[0:4])) * prime1
		acc = bits.RotateLeft64(acc, 23)*prime2 + prime3
		remaining = remaining[4:]
	}
	for _, b := range remaining {
		acc ^= uint64(b) * prime5
		acc = bits.RotateLeft64(acc, 11) * prime1
	}

	acc ^= acc >> 33
	acc *= prime2
	acc ^= acc >> 29
	acc *= prime3
	acc ^= acc >> 32
	return acc
}

func round(acc, input uint64) uint64 {
	acc += input * prime2
	return bits.RotateLeft64(acc, 31) * prime1
}

func mergeRound(acc, val uint64) uint64 {
	acc ^= round(0, val)
	return acc*prime1 + prime4
}

func u64(b []byte) uint64 {
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}

func u32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
