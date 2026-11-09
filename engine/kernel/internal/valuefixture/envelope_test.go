package valuefixture_test

import (
	"bytes"
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/internal/valuefixture"
	"github.com/stacklok/mecatl/engine/kernel/internal/valuefixture/fixtureview"
	"github.com/stacklok/mecatl/engine/kernel/value"
)

func limits() codec.Limits {
	return codec.Limits{MaxBytes: 1 << 20, MaxStringBytes: 1 << 16, MaxByteStringBytes: 1 << 16, MaxArrayElements: 1000, MaxMapPairs: 1000, MaxDepth: 256, MaxNodes: 10000, MaxCollectionElements: 10000}
}

func envelope() valuefixture.Envelope {
	return valuefixture.Envelope{
		Value:  value.Array([]value.Value{value.Null(), value.Int64(-2), value.Uint64(math.MaxUint64), value.Object(value.Map{"empty": value.String("")})}),
		Map:    value.Map{"nested": value.Array([]value.Value{value.Bool(true), value.String("text")})},
		Labels: map[valuefixture.Key]string{"zero": "", "label": "name"}, Counts: map[string]uint64{"count": 42},
		Children: map[string]*valuefixture.Child{
			"one": {Label: "child", Data: []byte{1, 2, 3}, Children: []valuefixture.Leaf{{Data: []byte{4, 5}}, {Data: []byte{6, 7}}}},
			"two": {Label: "sibling", Data: bytes.Repeat([]byte{8}, 4096), Children: []valuefixture.Leaf{{Data: []byte{9}}}},
		}, Sibling: bytes.Repeat([]byte{10}, 4096),
	}
}

func TestEnvelopeCompositionAndOwnership(t *testing.T) {
	author := envelope()
	detached := author.Clone()
	if !author.Equal(detached) {
		t.Fatal("clone differs")
	}
	encoded, err := codec.Encode(author, limits())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode[valuefixture.Envelope](encoded, limits())
	if err != nil {
		t.Fatal(err)
	}
	if !author.Equal(decoded) {
		t.Fatal("roundtrip")
	}
	again, err := codec.Encode(decoded, limits())
	if err != nil || !bytes.Equal(encoded, again) {
		t.Fatal("canonical roundtrip", err)
	}
	// Every mutable owner, including primitive descendants and pointer map DTOs,
	// must detach independently of the original authoring input.
	author.Map["nested"] = value.Null()
	author.Value = value.Null()
	author.Labels["zero"] = "changed"
	author.Counts["count"] = 0
	author.Children["one"].Data[0] = 99
	author.Children["one"].Children[0].Data[0] = 99
	author.Sibling[0] = 99
	if author.Equal(detached) || !detached.Equal(decoded) {
		t.Fatal("mutable storage aliased")
	}
	v := fixtureview.Borrow(&detached)
	a, ok := v.Value().Array()
	if !ok || a.Len() != 4 {
		t.Fatal("native array")
	}
	if x, _ := a.At(2).Uint64(); x != math.MaxUint64 {
		t.Fatal("lossy integer")
	}
	if x, _ := v.Labels().Get("zero"); x != "" {
		t.Fatal("scalar map")
	}
	if _, ok := v.Children().Get("absent"); ok {
		t.Fatal("missing entry")
	}
	total := 0
	for _, child := range v.Children().All() {
		total += len(child.Label()) + child.Data().Len()
		for _, leaf := range child.Children().All() {
			total += leaf.Data().Len()
		}
	}
	if total != 4116 {
		t.Fatal("complete traversal", total)
	}
	child, _ := v.Children().Get("one")
	detachedLeaf := child.Children().At(0).Data().Copy()
	detachedLeaf[0] = 99
	if child.Children().At(0).Data().At(0) != 4 || v.Sibling().At(0) != 10 {
		t.Fatal("leaf copy exposed backing")
	}
	// Borrowed handles retain the previously detached backing through later work.
	later := decoded.Clone()
	later.Children["one"].Data[0] = 88
	if child.Data().At(0) != 1 {
		t.Fatal("retained view changed")
	}
}

func TestEnvelopePrimitiveCloneDetachesDescendants(t *testing.T) {
	array := []value.Value{value.String("original")}
	object := value.Map{"array": value.Array(array)}
	e := valuefixture.Envelope{Value: value.Object(object), Map: value.Map{"object": value.Object(object)}}
	clone := e.Clone()
	if !clone.Equal(e) {
		t.Fatal("clone changed values")
	}
	array[0] = value.Null()
	object["extra"] = value.Bool(true)
	if clone.Equal(e) {
		t.Fatal("primitive descendants alias author input")
	}
	v := fixtureview.Borrow(&clone)
	m, _ := v.Value().Object()
	a, _ := m.Get("array")
	items, _ := a.Array()
	if text, _ := items.At(0).String(); text != "original" {
		t.Fatal("Value clone retained array")
	}
	o, _ := v.Map().Get("object")
	m, _ = o.Object()
	if m.Len() != 1 {
		t.Fatal("Map clone retained object")
	}
	var zero valuefixture.Envelope
	if !zero.Equal(zero.Clone()) {
		t.Fatal("clone/equal must preserve invalid zero")
	}
	if _, err := codec.Encode(zero, limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal("admission accepted invalid zero", err)
	}
}

func TestEnvelopeSharedBudgets(t *testing.T) {
	e := valuefixture.Envelope{Value: value.Array([]value.Value{value.Null(), value.Null()}), Map: value.Map{"a": value.Null()}}
	l := limits()
	l.MaxDepth = 2
	l.MaxNodes = 17
	l.MaxCollectionElements = 9
	data, err := codec.Encode(e, l)
	if err != nil {
		t.Fatal("exact boundary", err)
	}
	if _, err := codec.Decode[valuefixture.Envelope](data, l); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*codec.Limits)
	}{
		{"nodes", func(l *codec.Limits) { l.MaxNodes = 16 }},
		{"aggregate collections", func(l *codec.Limits) { l.MaxCollectionElements = 8 }},
		{"depth", func(l *codec.Limits) { l.MaxDepth = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			tight := l
			test.change(&tight)
			if out, err := codec.Encode(e, tight); !errors.Is(err, codec.ErrLimitExceeded) || out != nil {
				t.Fatal("encode reset shared accounting", err)
			}
			if out, err := codec.Decode[valuefixture.Envelope](data, tight); !errors.Is(err, codec.ErrLimitExceeded) || value.Borrow(out.Value).Kind() != value.InvalidKind {
				t.Fatal("decode published partial/reset budget", err)
			}
		})
	}
}

func TestNativeWireHasNoEnvelope(t *testing.T) {
	for _, v := range []value.Value{value.Null(), value.Uint64(math.MaxUint64), value.Object(value.Map{"a": value.Array(nil)})} {
		want, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Encode(valuefixture.Native{Value: v}, limits())
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("native owner gained framing", err)
		}
	}
}

func TestEnvelopeImmediateCapsAndRequiredPointers(t *testing.T) {
	e := valuefixture.Envelope{Value: value.Null(), Map: value.Map{"a": value.Null(), "b": value.Null(), "c": value.Array([]value.Value{value.Null(), value.Null(), value.Null(), value.Null()})}}
	if _, err := codec.Encode(e, limits()); err != nil {
		t.Fatal("cap must not propagate to descendants", err)
	}
	e.Map["d"] = value.Null()
	if _, err := codec.Encode(e, limits()); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatal("map cap", err)
	}
	// Build intentionally unadmitted native bytes to exercise the decoding cap.
	data := e.AppendCBOR(nil)
	if _, err := codec.Decode[valuefixture.Envelope](data, limits()); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatal("decode cap", err)
	}
	for _, v := range []value.Value{value.String("four"), value.Array([]value.Value{value.Null(), value.Null(), value.Null(), value.Null()}), value.Object(e.Map)} {
		n := valuefixture.Native{Value: v}
		b, _ := codec.NewBudget(limits())
		if err := n.CheckCBOR(&b, 0, 3); !errors.Is(err, codec.ErrLimitExceeded) {
			t.Fatal("native immediate cap", err)
		}
		r, _ := codec.NewReader(n.AppendCBOR(nil), limits())
		dst := valuefixture.Native{Value: value.Bool(true)}
		if err := dst.ReadCBOR(&r, 0, 3); !errors.Is(err, codec.ErrLimitExceeded) || !dst.Value.Equal(value.Bool(true)) {
			t.Fatal("native transactional cap", err)
		}
	}
	e.Map = nil
	e.Children = map[string]*valuefixture.Child{"bad": nil}
	if _, err := codec.Encode(e, limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal("required nil pointer accepted", err)
	}
	// A map element null is still not an ordinary required DTO pointer.
	e.Children = nil
	data = e.AppendCBOR(nil)
	// Replace the trailing ch empty map with a one-entry map containing null.
	if !bytes.HasSuffix(data, []byte{0x62, 'c', 'h', 0xa0}) {
		t.Fatal("fixture canonical layout changed")
	}
	data = append(data[:len(data)-1], 0xa1, 0x61, 'x', 0xf6)
	if _, err := codec.Decode[valuefixture.Envelope](data, limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal("required pointer null accepted", err)
	}
}

func nested(depth int) value.Value {
	v := value.Null()
	for range depth {
		v = value.Array([]value.Value{v})
	}
	return v
}

func TestEnvelopeDepthPanicPropagation(t *testing.T) {
	for _, test := range []struct {
		name  string
		depth int
	}{{"allowed", 128}, {"exceeded", 129}} {
		for _, owner := range []string{"Value", "Map"} {
			t.Run(owner+"/"+test.name, func(t *testing.T) {
				v := nested(test.depth)
				m := value.Map{"x": nested(test.depth - 1)}
				e := valuefixture.Envelope{Value: v}
				directClone, directEqual := func() { v.Clone() }, func() { v.Equal(v) }
				if owner == "Map" {
					e = valuefixture.Envelope{Value: value.Null(), Map: m}
					directClone, directEqual = func() { m.Clone() }, func() { m.Equal(m) }
				}
				p := valuefixture.Parent{Envelope: e}
				if test.depth == 128 {
					if _, err := codec.Encode(p, limits()); err != nil {
						t.Fatal(err)
					}
					if !p.Equal(p.Clone()) {
						t.Fatal("accepted depth unsafe")
					}
					return
				}
				for _, fn := range []func(){directClone, directEqual, func() { e.Clone() }, func() { e.Equal(e) }, func() { p.Clone() }, func() { p.Equal(p) }} {
					func() {
						defer func() {
							if got := recover(); got != value.ErrDepthExceeded {
								t.Fatalf("panic=%v", got)
							}
						}()
						fn()
					}()
				}
				if _, err := codec.Encode(p, limits()); !errors.Is(err, value.ErrDepthExceeded) {
					t.Fatal("admission must error", err)
				}
				// Append is prechecked-only; crafting excessive native input here is intentional.
				data := p.AppendCBOR(nil)
				if _, err := codec.Decode[valuefixture.Parent](data, limits()); !errors.Is(err, value.ErrDepthExceeded) {
					t.Fatal("decode must error", err)
				}
			})
		}
	}
}

func TestEnvelopeViewsZeroAndTraversalAllocations(t *testing.T) {
	var z fixtureview.EnvelopeView
	if z.Value().Kind() != value.InvalidKind || z.Map().Len() != 0 || z.Labels().Len() != 0 || z.Counts().Len() != 0 || z.Children().Len() != 0 || z.Sibling().Len() != 0 {
		t.Fatal("zero")
	}
	if _, ok := z.Children().Get("x"); ok {
		t.Fatal("zero map missing")
	}
	e := envelope()
	v := fixtureview.Borrow(&e)
	if a := testing.AllocsPerRun(100, func() {
		for _, child := range v.Children().All() {
			childSink = child
			_ = child.Label()
			_ = child.Data().Len()
			for _, leaf := range child.Children().All() {
				leafSink = leaf
				_ = leaf.Data().At(0)
			}
		}
	}); a != 0 {
		t.Fatal("map traversal boxed entries", a)
	}
}

var childSink fixtureview.ChildView
var leafSink fixtureview.LeafView
