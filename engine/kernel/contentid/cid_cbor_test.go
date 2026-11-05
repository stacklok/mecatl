package contentid

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

func cidCodecLimits() codec.Limits {
	return codec.Limits{MaxBytes: 55, MaxStringBytes: 1, MaxByteStringBytes: 37, MaxArrayElements: 1, MaxMapPairs: 1, MaxDepth: 1, MaxNodes: 2, MaxCollectionElements: 1}
}

func TestCIDCBOR(t *testing.T) {
	// Independent profile/framing vector; a zero digest is valid opaque identity.
	wire, err := hex.DecodeString("d82a582500017112200000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseBinary(wire[5:])
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Encode(v, cidCodecLimits())
	if err != nil || !bytes.Equal(got, wire) {
		t.Fatalf("link framing = %x, %v", got, err)
	}
	input := bytes.Clone(wire)
	decoded, err := codec.Decode[CID](input, cidCodecLimits())
	if err != nil || !decoded.Equal(v) || v.Clone() != v {
		t.Fatal("link round trip", err)
	}
	clear(input)
	if decoded != v {
		t.Fatal("decoded binary borrows input")
	}
	for _, invalid := range []CID{{}, {binary: [36]byte{2, 0x71, 0x12, 32}}, {binary: [36]byte{1, 0x70, 0x12, 32}}, {binary: [36]byte{1, 0x71, 0x13, 32}}, {binary: [36]byte{1, 0x71, 0x12, 31}}} {
		if err := codec.Check(invalid, cidCodecLimits()); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatal("invalid profile accepted", err)
		}
		if data, err := codec.Encode(invalid, cidCodecLimits()); data != nil || !errors.Is(err, codec.ErrInvalidData) {
			t.Fatal("invalid profile output", err)
		}
		if !invalid.Equal(invalid.Clone()) {
			t.Fatal("Clone/Equal validated invalid profile")
		}
	}
	for _, tt := range []struct {
		name string
		edit func(*codec.Limits)
	}{
		{"exact", func(*codec.Limits) {}},
		{"estimated bytes", func(l *codec.Limits) { l.MaxBytes-- }},
		{"nodes", func(l *codec.Limits) { l.MaxNodes-- }},
		{"bytes include prefix", func(l *codec.Limits) { l.MaxByteStringBytes-- }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := cidCodecLimits()
			tt.edit(&l)
			check := codec.Check(v, l)
			data, enc := codec.Encode(v, l)
			value, dec := codec.Decode[CID](wire, l)
			if tt.name == "exact" {
				if check != nil || enc != nil || dec != nil || value != v || !bytes.Equal(data, wire) {
					t.Fatalf("exact accounting: %v, %v, %v", check, enc, dec)
				}
			} else if !errors.Is(check, codec.ErrLimitExceeded) || !errors.Is(enc, codec.ErrLimitExceeded) || !errors.Is(dec, codec.ErrLimitExceeded) || data != nil || value != (CID{}) {
				t.Fatalf("policy disagreement: %v, %v, %v", check, enc, dec)
			}
		})
	}
}

func TestCIDCBORReadFailurePreservesReceiver(t *testing.T) {
	v := Sum([]byte("existing receiver"))
	wire := v.AppendCBOR(nil)
	cases := [][]byte{{0xf6}, {0xd8, 41}, {0xd8, 42, 0xf6}, append([]byte{0xd8, 42}, wire...), append([]byte{0xd9, 0, 42}, wire[2:]...), append([]byte{0xd8, 42, 0x58, 36}, wire[4:]...), append([]byte{0xd8, 42, 0x58, 38}, append(bytes.Clone(wire[4:]), 0)...)}
	for end := range len(wire) {
		cases = append(cases, wire[:end])
	}
	for i := 4; i < 9; i++ { // multibase zero prefix and each profile varint
		bad := bytes.Clone(wire)
		bad[i]++
		cases = append(cases, bad)
	}
	for _, data := range cases {
		before := bytes.Clone(data)
		r, _ := codec.NewReader(data, cidCodecLimits())
		receiver := v
		if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || receiver != v || !bytes.Equal(data, before) {
			t.Fatalf("failed link read changed receiver/input: %x, %v", data, err)
		}
		if got, err := codec.Decode[CID](data, cidCodecLimits()); !errors.Is(err, codec.ErrInvalidData) || got != (CID{}) {
			t.Fatal("failed root decode is not zero", err)
		}
	}
	for _, depth := range []int{0, 1} {
		l := cidCodecLimits()
		if depth == 0 {
			l.MaxByteStringBytes--
		}
		r, _ := codec.NewReader(wire, l)
		receiver := v
		if err := receiver.ReadCBOR(&r, depth, math.MaxInt); !errors.Is(err, codec.ErrLimitExceeded) || receiver != v {
			t.Fatal("limited read changed receiver", err)
		}
	}
	if got, err := codec.Decode[CID](append(bytes.Clone(wire), 0), cidCodecLimits()); !errors.Is(err, codec.ErrInvalidData) || got != (CID{}) {
		t.Fatal("trailing bytes accepted", err)
	}
}
