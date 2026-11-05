package id_test

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"

	resourceid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

func historyIDCodecLimits() codec.Limits {
	return codec.Limits{MaxBytes: 100, MaxStringBytes: 25, MaxByteStringBytes: 1, MaxArrayElements: 2, MaxMapPairs: 1, MaxDepth: 1, MaxNodes: 3, MaxCollectionElements: 2}
}

func TestHistoryIDCBOROwnerComposition(t *testing.T) {
	text := "HST" + strings.Repeat("1", 22)
	v, err := historyid.ParseHistoryID(text)
	if err != nil {
		t.Fatal(err)
	}
	resource, _ := resourceid.Parse(text)
	wire := append([]byte{0x78, 25}, text...)
	l := historyIDCodecLimits()
	encoded, err := codec.Encode(v, l)
	if err != nil || !bytes.Equal(encoded, wire) || !bytes.Equal(encoded, resource.AppendCBOR(nil)) {
		t.Fatalf("transparent text = %x, %v", encoded, err)
	}
	input := bytes.Clone(wire)
	decoded, err := codec.Decode[historyid.HistoryID](input, l)
	if err != nil || !v.Equal(decoded) || !v.Equal(v.Clone()) {
		t.Fatal("owner round trip", err)
	}
	clear(input)
	if decoded != v {
		t.Fatal("decoded text borrows input")
	}
	// Both wrapper layers represent one text node, even at the depth boundary.
	l.MaxNodes = 1
	b, _ := codec.NewBudget(l)
	if err := v.CheckCBOR(&b, l.MaxDepth, math.MaxInt); err != nil || b.EstimatedBytes() != 34 {
		t.Fatal("wrapper added nodes/depth", err)
	}
	r, _ := codec.NewReader(wire, l)
	if err := decoded.ReadCBOR(&r, l.MaxDepth, math.MaxInt); err != nil || r.Done() != nil {
		t.Fatal("wrapper added read depth", err)
	}
	var zero historyid.HistoryID
	if err := codec.Check(zero, l); !errors.Is(err, codec.ErrInvalidData) || !zero.Equal(zero.Clone()) {
		t.Fatal("zero identity validity/value operations", err)
	}
	if data, err := codec.Encode(zero, l); data != nil || !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal("zero identity encode", err)
	}
}

func TestHistoryIDCBORSharedBudgetAndCaps(t *testing.T) {
	v, _ := historyid.ParseHistoryID("HST" + strings.Repeat("1", 22))
	wire := v.AppendCBOR(nil)
	for _, tt := range []struct {
		name string
		edit func(*codec.Limits)
	}{
		{"exact", func(*codec.Limits) {}},
		{"nodes", func(l *codec.Limits) { l.MaxNodes-- }},
		{"aggregate bytes", func(l *codec.Limits) { l.MaxBytes = 76 }}, // 3*9 + 2*25 = 77
		{"global text", func(l *codec.Limits) { l.MaxStringBytes-- }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := historyIDCodecLimits()
			tt.edit(&l)
			b, _ := codec.NewBudget(l)
			err := b.CheckArray(2, 0, 2)
			for range 2 {
				if err == nil {
					err = v.CheckCBOR(&b, 1, math.MaxInt)
				}
			}
			data := append(append([]byte{0x82}, wire...), wire...)
			r, _ := codec.NewReader(data, l)
			_, readErr := r.Array(0, 2)
			for range 2 {
				if readErr == nil {
					var child historyid.HistoryID
					readErr = child.ReadCBOR(&r, 1, math.MaxInt)
				}
			}
			if tt.name == "exact" {
				if err != nil || readErr != nil || r.Done() != nil || b.EstimatedBytes() != 77 {
					t.Fatalf("exact accounting: %v, %v", err, readErr)
				}
			} else if !errors.Is(err, codec.ErrLimitExceeded) || !errors.Is(readErr, codec.ErrLimitExceeded) {
				t.Fatalf("owner reset accounting: %v, %v", err, readErr)
			}
		})
	}
	for _, cap := range []int{24, math.MaxInt} {
		l := historyIDCodecLimits()
		if cap == math.MaxInt {
			l.MaxStringBytes = 24
		}
		b, _ := codec.NewBudget(l)
		r, _ := codec.NewReader(wire, l)
		receiver := v
		if !errors.Is(v.CheckCBOR(&b, 0, cap), codec.ErrLimitExceeded) || !errors.Is(receiver.ReadCBOR(&r, 0, cap), codec.ErrLimitExceeded) || receiver != v {
			t.Fatal("caller/global cap lost at owner boundary")
		}
	}
}

func TestHistoryIDCBORReadFailurePreservesReceiver(t *testing.T) {
	text := "HST" + strings.Repeat("1", 22)
	v, _ := historyid.ParseHistoryID(text)
	wire := v.AppendCBOR(nil)
	cases := [][]byte{{0xf6}, {0x60}, append([]byte{0x78, 25}, "RUN"+text[3:]...), append([]byte{0x78, 25}, text[:24]+"0"...)}
	for end := range len(wire) {
		cases = append(cases, wire[:end])
	}
	for _, data := range cases {
		r, _ := codec.NewReader(data, historyIDCodecLimits())
		receiver := v
		if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || receiver != v {
			t.Fatalf("failure changed receiver: %x, %v", data, err)
		}
		if got, err := codec.Decode[historyid.HistoryID](data, historyIDCodecLimits()); !errors.Is(err, codec.ErrInvalidData) || got != (historyid.HistoryID{}) {
			t.Fatal("failure did not return zero", err)
		}
	}
	if got, err := codec.Decode[historyid.HistoryID](append(bytes.Clone(wire), 0), historyIDCodecLimits()); !errors.Is(err, codec.ErrInvalidData) || got != (historyid.HistoryID{}) {
		t.Fatal("root trailing bytes accepted", err)
	}
}
