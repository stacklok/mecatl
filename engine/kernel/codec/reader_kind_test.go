package codec_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

func TestReaderNullAndPeekKind(t *testing.T) {
	limits := codec.Limits{MaxBytes: 100, MaxStringBytes: 10, MaxByteStringBytes: 10, MaxArrayElements: 10, MaxMapPairs: 10, MaxDepth: 10, MaxNodes: 10, MaxCollectionElements: 10}
	for _, tc := range []struct {
		data []byte
		kind codec.Kind
	}{
		{[]byte{0xf6}, codec.NullKind}, {[]byte{0xf4}, codec.BoolKind},
		{[]byte{0}, codec.UintKind}, {[]byte{0x20}, codec.IntKind},
		{[]byte{0x60}, codec.StringKind}, {[]byte{0x80}, codec.ArrayKind}, {[]byte{0xa0}, codec.MapKind},
	} {
		r, err := codec.NewReader(tc.data, limits)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			k, err := r.PeekKind()
			if err != nil || k != tc.kind {
				t.Fatalf("PeekKind = %v, %v", k, err)
			}
		}
		if tc.kind == codec.NullKind {
			if err := r.Null(0); err != nil {
				t.Fatal(err)
			}
			if err := r.Done(); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, data := range [][]byte{nil, {0x40}, {0xc0}, {0xf7}, {0xf8, 0x16}, {0xf9, 0, 0}, {0x18, 0}, {0x9f}} {
		r, _ := codec.NewReader(data, limits)
		if _, err := r.PeekKind(); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("PeekKind(%x) = %v", data, err)
		}
	}
	var zero codec.Reader
	if _, err := zero.PeekKind(); !errors.Is(err, codec.ErrInvalidLimits) {
		t.Fatal(err)
	}
	if err := zero.Null(0); !errors.Is(err, codec.ErrInvalidLimits) {
		t.Fatal(err)
	}
	r, _ := codec.NewReader([]byte{0xf4}, limits)
	if err := r.Null(0); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal(err)
	}
	limits.MaxBytes = 8
	r, _ = codec.NewReader([]byte{0xf6}, limits)
	if err := r.Null(0); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatal(err)
	}
}
