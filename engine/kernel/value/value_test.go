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

func limits() codec.Limits {
	return codec.Limits{MaxBytes: 1 << 20, MaxStringBytes: 1 << 16, MaxByteStringBytes: 1 << 16, MaxArrayElements: 1000, MaxMapPairs: 1000, MaxDepth: 1000, MaxNodes: 10000, MaxCollectionElements: 5000}
}

func TestNumericGoldens(t *testing.T) {
	for _, tc := range []struct {
		v        value.Value
		hex      string
		signed   bool
		i        int64
		unsigned bool
		u        uint64
	}{
		{value.Int64(math.MinInt64), "3b7fffffffffffffff", true, math.MinInt64, false, 0},
		{value.Int64(-25), "3818", true, -25, false, 0},
		{value.Int64(-24), "37", true, -24, false, 0},
		{value.Int64(-1), "20", true, -1, false, 0},
		{value.Int64(0), "00", true, 0, true, 0},
		{value.Uint64(23), "17", true, 23, true, 23},
		{value.Int64(24), "1818", true, 24, true, 24},
		{value.Uint64(255), "18ff", true, 255, true, 255},
		{value.Uint64(256), "190100", true, 256, true, 256},
		{value.Uint64(65535), "19ffff", true, 65535, true, 65535},
		{value.Uint64(65536), "1a00010000", true, 65536, true, 65536},
		{value.Uint64(math.MaxUint32), "1affffffff", true, math.MaxUint32, true, math.MaxUint32},
		{value.Uint64(1 << 32), "1b0000000100000000", true, 1 << 32, true, 1 << 32},
		{value.Int64(math.MaxInt64), "1b7fffffffffffffff", true, math.MaxInt64, true, math.MaxInt64},
		{value.Uint64(1 << 63), "1b8000000000000000", false, 0, true, 1 << 63},
		{value.Uint64(math.MaxUint64), "1bffffffffffffffff", false, 0, true, math.MaxUint64},
	} {
		t.Run(tc.hex, func(t *testing.T) {
			encoded, err := codec.Encode(tc.v, limits())
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(encoded) != tc.hex {
				t.Fatalf("got %x", encoded)
			}
			decoded, err := codec.Decode[value.Value](encoded, limits())
			if err != nil {
				t.Fatal(err)
			}
			if !tc.v.Equal(decoded) {
				t.Fatal("roundtrip differs")
			}
			view, err := decoded.Admit(limits())
			if err != nil {
				t.Fatal(err)
			}
			if x, ok := view.Int64(); x != tc.i || ok != tc.signed {
				t.Fatalf("Int64=%d,%v", x, ok)
			}
			if x, ok := view.Uint64(); x != tc.u || ok != tc.unsigned {
				t.Fatalf("Uint64=%d,%v", x, ok)
			}
		})
	}
	for _, n := range []int64{0, 1, 24, math.MaxInt64} {
		if !value.Int64(n).Equal(value.Uint64(uint64(n))) {
			t.Fatal("signedness retained")
		}
	}
}

func TestZeroNullAndEmpty(t *testing.T) {
	var zero value.Value
	if !zero.Equal(zero.Clone()) {
		t.Fatal("zero clone/equality")
	}
	if _, err := zero.Admit(limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal(err)
	}
	var view value.View
	if view.Kind() != value.InvalidKind {
		t.Fatal("implicit null")
	}
	if _, ok := view.Bool(); ok {
		t.Fatal("zero bool")
	}
	if _, ok := view.String(); ok {
		t.Fatal("zero string")
	}
	if _, ok := view.Int64(); ok {
		t.Fatal("zero int")
	}
	if _, ok := view.Uint64(); ok {
		t.Fatal("zero uint")
	}
	if a, ok := view.Array(); ok || a.Len() != 0 {
		t.Fatal("zero array")
	}
	if m, ok := view.Object(); ok || m.Len() != 0 {
		t.Fatal("zero map")
	}
	variants := []value.Value{value.Null(), value.Bool(false), value.String(""), value.Array(nil), value.Object(nil)}
	for i, v := range variants {
		data, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatal(err)
		}
		want := []byte{0xf6, 0xf4, 0x60, 0x80, 0xa0}[i]
		if !bytes.Equal(data, []byte{want}) {
			t.Fatalf("%x != %x", data, want)
		}
		for j, other := range variants {
			if v.Equal(other) != (i == j) {
				t.Fatal("lost kind distinction")
			}
		}
	}
	if !value.Array(nil).Equal(value.Array([]value.Value{})) || !value.Object(nil).Equal(value.Object(value.Map{})) {
		t.Fatal("nil not empty")
	}
	m, err := (value.Map{"null": value.Null(), "empty": value.Array(nil)}).Admit(limits())
	if err != nil {
		t.Fatal(err)
	}
	if x, ok := m.Get("missing"); ok || x.Kind() != value.InvalidKind {
		t.Fatal("missing")
	}
	if x, ok := m.Get("null"); !ok || x.Kind() != value.NullKind {
		t.Fatal("lost present null")
	}
	x, _ := m.Get("empty")
	if a, ok := x.Array(); !ok || a.Len() != 0 {
		t.Fatal("lost empty array")
	}
	var empty value.Map
	if empty.Clone() != nil || !empty.Equal(value.Map{}) {
		t.Fatal("map zero semantics")
	}
	if _, err := empty.Admit(limits()); err != nil {
		t.Fatal(err)
	}
}

func TestMapCanonicalAndOwnership(t *testing.T) {
	children := []value.Value{value.Int64(1), value.Int64(1), value.Object(value.Map{"text": value.String("leaf"), "flag": value.Bool(true)})}
	nested := value.Map{"array": value.Array(children), "empty": value.Object(nil)}
	author := value.Map{"zz": value.Object(nested), "b": value.String("sibling"), "a": value.Null()}
	accepted, err := author.Admit(limits())
	if err != nil {
		t.Fatal(err)
	}
	cloned := author.Clone()
	encoded, err := codec.Encode(author, limits())
	if err != nil {
		t.Fatal(err)
	}
	reversed := value.Map{}
	for _, key := range []string{"a", "b", "zz"} {
		reversed[key] = author[key]
	}
	other, err := codec.Encode(reversed, limits())
	if err != nil || !bytes.Equal(encoded, other) {
		t.Fatal("insertion order changes bytes", err)
	}
	decoded, err := codec.Decode[value.Map](encoded, limits())
	if err != nil || !author.Equal(decoded) {
		t.Fatal("map roundtrip", err)
	}
	children[0] = value.Int64(99)
	nested["empty"] = value.Null()
	author["b"] = value.String("changed")
	if !cloned.Equal(decoded) || author.Equal(cloned) {
		t.Fatal("clone aliases input")
	}
	parent, _ := accepted.Get("zz")
	object, _ := parent.Object()
	arrayValue, _ := object.Get("array")
	array, _ := arrayValue.Array()
	first, _ := array.At(0).Int64()
	if first != 1 {
		t.Fatal("admission aliases array")
	}
	empty, _ := object.Get("empty")
	if m, ok := empty.Object(); !ok || m.Len() != 0 {
		t.Fatal("admission aliases nested map")
	}
	seen, sum := 0, int64(0)
	for key, v := range accepted.All() {
		seen++
		switch key {
		case "a":
			if v.Kind() != value.NullKind {
				t.Fatal("null")
			}
		case "b":
			if s, _ := v.String(); s != "sibling" {
				t.Fatal("sibling changed")
			}
		case "zz":
			m, _ := v.Object()
			for name, child := range m.All() {
				seen++
				if name == "empty" {
					continue
				}
				a, _ := child.Array()
				for _, element := range a.All() {
					seen++
					if n, ok := element.Int64(); ok {
						sum += n
						continue
					}
					leaf, _ := element.Object()
					for key, x := range leaf.All() {
						seen++
						if key == "text" {
							if s, _ := x.String(); s != "leaf" {
								t.Fatal("text")
							}
						} else {
							if b, ok := x.Bool(); !ok || !b {
								t.Fatal("bool")
							}
						}
					}
				}
			}
		}
	}
	if seen != 10 || sum != 2 {
		t.Fatalf("incomplete traversal: %d,%d", seen, sum)
	}
	visits := 0
	for range accepted.All() {
		visits++
		break
	}
	if visits != 1 {
		t.Fatal("early stop")
	}
	if !value.Array([]value.Value{value.Int64(1), value.Int64(1)}).Equal(value.Array([]value.Value{value.Uint64(1), value.Uint64(1)})) {
		t.Fatal("array semantics")
	}
	if value.Array([]value.Value{value.Int64(1), value.Int64(2)}).Equal(value.Array([]value.Value{value.Int64(2), value.Int64(1)})) {
		t.Fatal("array order")
	}
}

// chain alternates arrays and objects to catch accidental local-depth resets.
func chain(n int, leaf value.Value) value.Value {
	for i := range n {
		if i%2 == 0 {
			leaf = value.Array([]value.Value{leaf})
		} else {
			leaf = value.Object(value.Map{"k": leaf})
		}
	}
	return leaf
}
func arrayWire(n int, leaf byte) []byte { return append(bytes.Repeat([]byte{0x81}, n), leaf) }

func mustDepthPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if got := recover(); got != value.ErrDepthExceeded {
			t.Errorf("panic=%v, want ErrDepthExceeded", got)
		}
	}()
	f()
}

func TestLocalDepthAndProgrammerPanics(t *testing.T) {
	for _, leaf := range []value.Value{value.Null(), value.Array(nil), value.Object(nil)} {
		at := chain(128, leaf)
		accepted, err := at.Admit(limits())
		if err != nil || accepted.Kind() == value.InvalidKind {
			t.Fatal("128 rejected", err)
		}
		if !at.Equal(at.Clone()) {
			t.Fatal("128 clone")
		}
		data, err := codec.Encode(at, limits())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = codec.Decode[value.Value](data, limits()); err != nil {
			t.Fatal(err)
		}
		over := chain(129, leaf)
		if _, err = over.Admit(limits()); !errors.Is(err, value.ErrDepthExceeded) {
			t.Fatal("129 admission", err)
		}
		if _, err = codec.Encode(over, limits()); !errors.Is(err, value.ErrDepthExceeded) {
			t.Fatal("129 encode", err)
		}
		mustDepthPanic(t, func() { over.Clone() })
		mustDepthPanic(t, func() { over.Equal(over) })
		if over.Equal(value.Null()) {
			t.Fatal("shallow kind mismatch")
		}
	}
	if _, err := codec.Decode[value.Value](arrayWire(129, 0xf6), limits()); !errors.Is(err, value.ErrDepthExceeded) {
		t.Fatal("129 decode", err)
	}
	for _, leaf := range []byte{0xf6, 0x80, 0xa0} {
		if _, err := codec.Decode[value.Value](arrayWire(128, leaf), limits()); err != nil {
			t.Fatal("128 decode", err)
		}
	}
	for _, cyclic := range cycles() {
		if _, err := cyclic.Admit(limits()); !errors.Is(err, value.ErrDepthExceeded) {
			t.Fatal("cycle admission", err)
		}
		mustDepthPanic(t, func() { cyclic.Clone() })
		mustDepthPanic(t, func() { cyclic.Equal(cyclic) })
		left := value.Array([]value.Value{value.Null(), cyclic})
		right := value.Array([]value.Value{value.Bool(false), cyclic})
		if left.Equal(right) || left.Equal(value.Array(nil)) {
			t.Fatal("shallow short circuit")
		}
	}
	m := value.Map{}
	m["cycle"] = value.Object(m)
	mustDepthPanic(t, func() { m.Clone() })
	mustDepthPanic(t, func() { m.Equal(m) })
	if m.Equal(value.Map{"different": value.Null()}) || m.Equal(nil) {
		t.Fatal("map short circuit")
	}
	if _, err := m.Admit(limits()); !errors.Is(err, value.ErrDepthExceeded) {
		t.Fatal(err)
	}
}

func cycles() []value.Value {
	a := make([]value.Value, 1)
	a[0] = value.Array(a)
	m := value.Map{}
	m["self"] = value.Object(m)
	return []value.Value{value.Array(a), value.Object(m)}
}

func TestRepeatedSubtreesCountPerOccurrence(t *testing.T) {
	shared := []value.Value{value.Null()}
	dag := value.Array([]value.Value{value.Array(shared), value.Array(shared)})
	l := limits()
	l.MaxNodes = 5
	l.MaxCollectionElements = 4
	l.MaxBytes = 45
	if _, err := dag.Admit(l); err != nil {
		t.Fatal("DAG rejected", err)
	}
	l.MaxNodes = 4
	if _, err := dag.Admit(l); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatal("DAG undercount", err)
	}
	detached := dag.Clone()
	shared[0] = value.Bool(true)
	outer, _ := value.Borrow(detached).Array()
	for _, child := range outer.All() {
		inner, _ := child.Array()
		if inner.At(0).Kind() != value.NullKind {
			t.Fatal("DAG clone aliases")
		}
	}
}

func TestEmbeddedLocalDepthAndSharedReader(t *testing.T) {
	for _, offset := range []int{0, 3} {
		l := limits()
		l.MaxDepth = offset + 128
		b, err := codec.NewBudget(l)
		if err != nil {
			t.Fatal(err)
		}
		for depth := range offset {
			if err := b.CheckArray(1, depth, math.MaxInt); err != nil {
				t.Fatal(err)
			}
		}
		at := chain(128, value.Null())
		if err := at.CheckCBOR(&b, offset, math.MaxInt); err != nil {
			t.Fatal("wire offset affected local ceiling", err)
		}
		data := arrayWire(offset+128, 0xf6)
		r, err := codec.NewReader(data, l)
		if err != nil {
			t.Fatal(err)
		}
		for depth := range offset {
			if _, err := r.Array(depth, math.MaxInt); err != nil {
				t.Fatal(err)
			}
		}
		var decoded value.Value
		if err := decoded.ReadCBOR(&r, offset, math.MaxInt); err != nil {
			t.Fatal("embedded read", err)
		}
		if err := r.Done(); err != nil {
			t.Fatal("shared accounting", err)
		}
		l.MaxDepth--
		r, _ = codec.NewReader(data, l)
		for depth := range offset {
			if _, err := r.Array(depth, math.MaxInt); err != nil {
				t.Fatal(err)
			}
		}
		if err := decoded.ReadCBOR(&r, offset, math.MaxInt); !errors.Is(err, codec.ErrLimitExceeded) {
			t.Fatal("wire depth ignored", err)
		}
		// A loose enclosing wire budget cannot relax the primitive's local ceiling.
		l = limits()
		r, _ = codec.NewReader(arrayWire(offset+129, 0xf6), l)
		for depth := range offset {
			if _, err := r.Array(depth, math.MaxInt); err != nil {
				t.Fatal(err)
			}
		}
		if err := decoded.ReadCBOR(&r, offset, math.MaxInt); !errors.Is(err, value.ErrDepthExceeded) {
			t.Fatal("local ceiling relaxed", err)
		}
	}
	// Two siblings share one reader: nested reads must not call Done or reset it.
	r, _ := codec.NewReader([]byte{0x82, 0xf6, 0xa0}, limits())
	if _, err := r.Array(0, 2); err != nil {
		t.Fatal(err)
	}
	var v value.Value
	var m value.Map
	if err := v.ReadCBOR(&r, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := m.ReadCBOR(&r, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.Done(); err != nil {
		t.Fatal(err)
	}
}
