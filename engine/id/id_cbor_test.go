package id

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

func idCodecLimits() codec.Limits {
	return codec.Limits{MaxBytes: 34, MaxStringBytes: 25, MaxByteStringBytes: 1, MaxArrayElements: 1, MaxMapPairs: 1, MaxDepth: 1, MaxNodes: 1, MaxCollectionElements: 1}
}

func TestIDCBOR(t *testing.T) {
	text := "HST" + strings.Repeat("1", 22)
	v, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	wire := append([]byte{0x78, 25}, text...)
	limits := idCodecLimits()
	got, err := codec.Encode(v, limits)
	if err != nil || !bytes.Equal(got, wire) {
		t.Fatalf("text encoding = %x, %v", got, err)
	}
	input := bytes.Clone(wire)
	decoded, err := codec.Decode[ID](input, limits)
	if err != nil || !v.Equal(decoded) || v.Clone() != v {
		t.Fatalf("round trip = %v, %v", decoded, err)
	}
	clear(input)
	if decoded != v {
		t.Fatal("decoded text borrows input")
	}
	b, _ := codec.NewBudget(limits)
	if err := v.CheckCBOR(&b, limits.MaxDepth, math.MaxInt); err != nil || b.EstimatedBytes() != 34 {
		t.Fatalf("transparent depth/accounting = %d, %v", b.EstimatedBytes(), err)
	}
	r, _ := codec.NewReader(wire, limits)
	if err := decoded.ReadCBOR(&r, limits.MaxDepth, math.MaxInt); err != nil || r.Done() != nil {
		t.Fatal("transparent read depth", err)
	}

	for _, invalid := range []ID{{}, {value: "RUN" + text[3:]}, {value: text[:24] + "0"}, {value: "HST"}} {
		if err := codec.Check(invalid, limits); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatal("invalid ID accepted", err)
		}
		if data, err := codec.Encode(invalid, limits); data != nil || !errors.Is(err, codec.ErrInvalidData) {
			t.Fatal("invalid ID encode output", err)
		}
		if !invalid.Equal(invalid.Clone()) {
			t.Fatal("Clone/Equal validate invalid state")
		}
	}
	// The literal owner cap rejects oversize before semantic parsing.
	if err := codec.Check(ID{value: text + "1"}, codec.Limits{MaxBytes: 100, MaxStringBytes: 100, MaxByteStringBytes: 1, MaxArrayElements: 1, MaxMapPairs: 1, MaxDepth: 1, MaxNodes: 1, MaxCollectionElements: 1}); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatal("owner cap ignored", err)
	}
}

func TestIDCBORReadFailurePreservesReceiver(t *testing.T) {
	text := "HST" + strings.Repeat("1", 22)
	v, _ := Parse(text)
	wire := append([]byte{0x78, 25}, text...)
	cases := [][]byte{{0xf6}, {0xa0}, {0x60}, append([]byte{0x78, 25}, "RUN"+text[3:]...), append([]byte{0x78, 25}, text[:24]+"0"...)}
	for end := range len(wire) {
		cases = append(cases, wire[:end])
	}
	for _, data := range cases {
		before := bytes.Clone(data)
		r, _ := codec.NewReader(data, idCodecLimits())
		receiver := v
		if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || receiver != v || !bytes.Equal(data, before) {
			t.Fatalf("failed read changed receiver/input: %x, %v", data, err)
		}
		if got, err := codec.Decode[ID](data, idCodecLimits()); !errors.Is(err, codec.ErrInvalidData) || got != (ID{}) {
			t.Fatal("failed root decode is not zero", err)
		}
	}
	for _, cap := range []int{24, 25} {
		l := idCodecLimits()
		l.MaxStringBytes = cap
		r, _ := codec.NewReader(wire, l)
		receiver := v
		err := receiver.ReadCBOR(&r, 0, 24)
		if !errors.Is(err, codec.ErrLimitExceeded) || receiver != v {
			t.Fatal("caller/global cap not enforced", err)
		}
	}
	l := idCodecLimits()
	l.MaxStringBytes--
	if data, err := codec.Encode(v, l); data != nil || !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatal("global text cap not enforced", err)
	}
	if got, err := codec.Decode[ID](append(bytes.Clone(wire), 0), idCodecLimits()); got != (ID{}) || !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal("trailing bytes accepted", err)
	}
}
