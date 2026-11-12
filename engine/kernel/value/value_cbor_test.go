package value_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/value"
)

func TestResourceExactBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                                         string
		v                                            value.Value
		size, nodes, elements, strings, arrays, maps int
	}{
		{"null", value.Null(), 9, 1, 1, 1, 1, 1},
		{"text", value.String("é"), 11, 1, 1, 2, 1, 1},
		{"array", value.Array([]value.Value{value.Null(), value.Null()}), 27, 3, 2, 1, 2, 1},
		{"map", value.Object(value.Map{"a": value.Array([]value.Value{value.Null()})}), 37, 4, 2, 1, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := limits()
			l.MaxBytes = tc.size
			l.MaxNodes = tc.nodes
			l.MaxCollectionElements = tc.elements
			l.MaxStringBytes = tc.strings
			l.MaxArrayElements = tc.arrays
			l.MaxMapPairs = tc.maps
			b, err := codec.NewBudget(l)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.v.CheckCBOR(&b, 0, math.MaxInt); err != nil {
				t.Fatal("exact check", err)
			}
			if b.EstimatedBytes() != tc.size {
				t.Fatalf("estimate=%d", b.EstimatedBytes())
			}
			data, err := codec.Encode(tc.v, l)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := codec.Decode[value.Value](data, l); err != nil {
				t.Fatal("exact decode", err)
			}
			for _, reduce := range []func(*codec.Limits){
				func(l *codec.Limits) { l.MaxBytes-- },
				func(l *codec.Limits) {
					if l.MaxNodes > 1 {
						l.MaxNodes--
					} else {
						l.MaxBytes = 8
					}
				},
				func(l *codec.Limits) {
					if l.MaxCollectionElements > 1 {
						l.MaxCollectionElements--
					} else {
						l.MaxBytes = 8
					}
				},
			} {
				tight := l
				reduce(&tight)
				view, err := tc.v.Admit(tight)
				if !errors.Is(err, codec.ErrLimitExceeded) || view.Kind() != value.InvalidKind {
					t.Fatal("admission not atomic", err)
				}
				if _, err := codec.Decode[value.Value](data, tight); !errors.Is(err, codec.ErrLimitExceeded) {
					t.Fatal("decode undercount", err)
				}
			}
		})
	}
	for _, tc := range []struct {
		v       value.Value
		tighten func(*codec.Limits)
	}{
		{value.String("ab"), func(l *codec.Limits) { l.MaxStringBytes = 1 }},
		{value.Array([]value.Value{value.Null(), value.Null()}), func(l *codec.Limits) { l.MaxArrayElements = 1 }},
		{value.Object(value.Map{"a": value.Null(), "b": value.Null()}), func(l *codec.Limits) { l.MaxMapPairs = 1 }},
		{value.Array([]value.Value{value.Array([]value.Value{value.Null()})}), func(l *codec.Limits) { l.MaxDepth = 1 }},
	} {
		data, err := codec.Encode(tc.v, limits())
		if err != nil {
			t.Fatal(err)
		}
		l := limits()
		tc.tighten(&l)
		if err := codec.Check(tc.v, l); !errors.Is(err, codec.ErrLimitExceeded) {
			t.Fatal("cap ignored", err)
		}
		if _, err := codec.Decode[value.Value](data, l); !errors.Is(err, codec.ErrLimitExceeded) {
			t.Fatal("read cap ignored", err)
		}
	}
}

func TestImmediateFieldCapDoesNotLeak(t *testing.T) {
	v := value.Array([]value.Value{value.String("longer than one")})
	b, _ := codec.NewBudget(limits())
	if err := v.CheckCBOR(&b, 0, 1); err != nil {
		t.Fatal(err)
	}
	data, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	r, _ := codec.NewReader(data, limits())
	var got value.Value
	if err := got.ReadCBOR(&r, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.Done(); err != nil {
		t.Fatal(err)
	}
	for _, tooLarge := range []value.Value{value.String("ab"), value.Array([]value.Value{value.Null(), value.Null()}), value.Object(value.Map{"a": value.Null(), "b": value.Null()})} {
		b, _ := codec.NewBudget(limits())
		if err := tooLarge.CheckCBOR(&b, 0, 1); !errors.Is(err, codec.ErrLimitExceeded) {
			t.Fatal(err)
		}
		data, _ := codec.Encode(tooLarge, limits())
		r, _ := codec.NewReader(data, limits())
		if err := got.ReadCBOR(&r, 0, 1); !errors.Is(err, codec.ErrLimitExceeded) {
			t.Fatal(err)
		}
	}
	// A map field cap counts pairs, not key length or descendant collection length.
	m := value.Map{"long key": value.Array([]value.Value{value.Null(), value.Null()})}
	b, _ = codec.NewBudget(limits())
	if err := m.CheckCBOR(&b, 0, 1); err != nil {
		t.Fatal(err)
	}
	data, _ = codec.Encode(m, limits())
	r, _ = codec.NewReader(data, limits())
	var out value.Map
	if err := out.ReadCBOR(&r, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.Done(); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedCBORAndAtomicPublication(t *testing.T) {
	malformed := [][]byte{
		nil, {0xff}, {0xf7}, {0xf8, 0x16}, {0xf8, 0x14}, {0xf9, 0, 0}, {0xfa, 0, 0, 0, 0}, {0xfb, 0, 0, 0, 0, 0, 0, 0, 0},
		{0xc0, 0}, {0x40}, {0x9f, 0xff}, {0xbf, 0xff}, {0x7f, 0xff},
		{0x18, 0}, {0x38, 0}, {0x78, 0}, {0x98, 0}, {0xb8, 0},
		{0x3b, 0x80, 0, 0, 0, 0, 0, 0, 0}, // Negative argument exceeds MaxInt64.
		{0x61, 0xff}, {0x62, 'a'}, {0x81}, {0xa1},
		{0xa1, 0x00, 0xf6},                      // Non-text key.
		{0xa2, 0x61, 'a', 0, 0x61, 'a', 1},      // Duplicate.
		{0xa2, 0x61, 'b', 0, 0x61, 'a', 1},      // Lexically descending.
		{0xa2, 0x62, 'a', 'a', 0, 0x61, 'z', 1}, // Descending byte length.
		{0xa1, 0x61, 0xff, 0},
		{0x81, 0x40}, // Unsupported nested value.
		{0xf6, 0xf6}, // Trailing complete value.
	}
	for _, data := range malformed {
		t.Run(hex.EncodeToString(data), func(t *testing.T) {
			got, err := codec.Decode[value.Value](data, limits())
			if err == nil || value.Borrow(got).Kind() != value.InvalidKind {
				t.Fatalf("accepted %x: %v", data, err)
			}
			sentinel := value.Object(value.Map{"keep": value.Array([]value.Value{value.String("original")})})
			before := sentinel.Clone()
			r, err := codec.NewReader(data, limits())
			if err != nil {
				return
			}
			err = sentinel.ReadCBOR(&r, 0, math.MaxInt)
			// A nested read deliberately allows trailing bytes; codec.Decode rejects them.
			if bytes.Equal(data, []byte{0xf6, 0xf6}) {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !sentinel.Equal(before) {
				t.Fatal("failed read published partial storage", err)
			}
		})
	}
	for _, v := range []value.Value{value.String(string([]byte{0xff})), value.Object(value.Map{string([]byte{0xff}): value.Null()}), value.Array([]value.Value{{}})} {
		if _, err := v.Admit(limits()); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatal("invalid author accepted", err)
		}
	}
	// Map.ReadCBOR must reject scalars and malformed children atomically as well.
	for _, data := range [][]byte{{0xf6}, {0xa1, 0x61, 'a', 0x40}, {0xa2, 0x61, 'a', 0, 0x61, 'a', 1}} {
		m := value.Map{"keep": value.String("safe")}
		before := m.Clone()
		r, _ := codec.NewReader(data, limits())
		if err := m.ReadCBOR(&r, 0, math.MaxInt); err == nil || !m.Equal(before) {
			t.Fatal("map publication", err)
		}
	}
}

func TestInvalidCompositionArguments(t *testing.T) {
	v := value.Null()
	m := value.Map{}
	for _, check := range []func(*codec.Budget, int, int) error{v.CheckCBOR, m.CheckCBOR} {
		if err := check(nil, 0, 1); !errors.Is(err, codec.ErrInvalidLimits) {
			t.Fatal(err)
		}
		var zero codec.Budget
		if err := check(&zero, 0, 1); !errors.Is(err, codec.ErrInvalidLimits) {
			t.Fatal(err)
		}
		b, _ := codec.NewBudget(limits())
		if err := check(&b, 0, 0); !errors.Is(err, codec.ErrInvalidLimits) {
			t.Fatal(err)
		}
		b, _ = codec.NewBudget(limits())
		if err := check(&b, -1, 1); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatal(err)
		}
	}
	var nilValue *value.Value
	var nilMap *value.Map
	if err := nilValue.ReadCBOR(nil, 0, 1); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal(err)
	}
	if err := nilMap.ReadCBOR(nil, 0, 1); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal(err)
	}
	for _, read := range []func(*codec.Reader, int, int) error{v.ReadCBOR, m.ReadCBOR} {
		if err := read(nil, 0, 1); !errors.Is(err, codec.ErrInvalidLimits) {
			t.Fatal(err)
		}
		var r codec.Reader
		if err := read(&r, 0, 1); !errors.Is(err, codec.ErrInvalidLimits) {
			t.Fatal(err)
		}
		r, _ = codec.NewReader([]byte{0xf6}, limits())
		if err := read(&r, 0, 0); !errors.Is(err, codec.ErrInvalidLimits) {
			t.Fatal(err)
		}
	}
	if _, err := v.Admit(codec.Limits{}); !errors.Is(err, codec.ErrInvalidLimits) {
		t.Fatal(err)
	}
}

func FuzzNativeCBOR(f *testing.F) {
	for _, data := range [][]byte{{0xf6}, {0xa0}, {0x81, 0x01}, {0xa1, 0x61, 'a', 0xf5}, {0x3b, 0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}} {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := codec.Decode[value.Value](data, limits())
		if err != nil {
			return
		}
		encoded, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, data) || !v.Equal(v.Clone()) {
			t.Fatal("noncanonical roundtrip")
		}
		if _, err := v.Admit(limits()); err != nil {
			t.Fatal(err)
		}
	})
}
