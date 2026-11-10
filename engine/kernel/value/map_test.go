package value_test

import (
	"bytes"
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/value"
)

func TestMapDepthAndEmbeddedOffset(t *testing.T) {
	for _, offset := range []int{0, 3} {
		for _, childDepth := range []int{127, 128} {
			// The map root contributes one level to its child's local depth.
			m := value.Map{"k": chain(childDepth, value.Null())}
			b, _ := codec.NewBudget(limits())
			for depth := range offset {
				if err := b.CheckArray(1, depth, 1); err != nil {
					t.Fatal(err)
				}
			}
			err := m.CheckCBOR(&b, offset, 1)
			if childDepth == 128 {
				if !errors.Is(err, value.ErrDepthExceeded) {
					t.Fatal("map Check ceiling", err)
				}
				mustDepthPanic(t, func() { m.Clone() })
				mustDepthPanic(t, func() { m.Equal(m) })
			} else {
				if err != nil {
					t.Fatal("map Check 128", err)
				}
				if !m.Equal(m.Clone()) {
					t.Fatal("map Clone 128")
				}
			}
			wire := append(bytes.Repeat([]byte{0x81}, offset), 0xa1, 0x61, 'k')
			wire = append(wire, arrayWire(childDepth, 0xf6)...)
			l := limits()
			l.MaxDepth = offset + 129
			r, _ := codec.NewReader(wire, l)
			for depth := range offset {
				if _, err := r.Array(depth, 1); err != nil {
					t.Fatal(err)
				}
			}
			sentinel := value.Map{"keep": value.Null()}
			err = sentinel.ReadCBOR(&r, offset, math.MaxInt)
			if childDepth == 128 {
				if !errors.Is(err, value.ErrDepthExceeded) || !sentinel.Equal(value.Map{"keep": value.Null()}) {
					t.Fatal("map Read ceiling/publication", err)
				}
			} else {
				if err != nil {
					t.Fatal("map Read 128", err)
				}
				if err := r.Done(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestMapReadOwnsStorageAndReencodesCanonically(t *testing.T) {
	author := value.Map{"é": value.String("unicode"), "z": value.Array([]value.Value{value.Uint64(math.MaxUint64), value.Null()})}
	encoded, err := codec.Encode(author, limits())
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(encoded)
	decoded, err := codec.Decode[value.Map](encoded, limits())
	if err != nil {
		t.Fatal(err)
	}
	for i := range encoded {
		encoded[i] = 0
	}
	reencoded, err := codec.Encode(decoded, limits())
	if err != nil || !bytes.Equal(reencoded, original) {
		t.Fatal("borrowed input or unstable encoding", err)
	}
	view, err := decoded.Admit(limits())
	if err != nil {
		t.Fatal(err)
	}
	decoded["é"] = value.String("mutated")
	text, _ := view.Get("é")
	if x, _ := text.String(); x != "unicode" {
		t.Fatal("admission retained decoded mutable map")
	}
}
