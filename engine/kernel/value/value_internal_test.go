package value

import (
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

func TestMarshalJSONInvalidRepresentation(t *testing.T) {
	for _, v := range []Value{{kind: Kind(255)}, {kind: IntegerKind, flag: true, number: math.MaxUint64}} {
		for _, input := range []interface{ MarshalJSON() ([]byte, error) }{v, Borrow(v)} {
			if out, err := input.MarshalJSON(); out != nil || !errors.Is(err, codec.ErrInvalidData) {
				t.Fatalf("invalid representation published %q: %v", out, err)
			}
		}
	}
}

func TestParseJSONOwnsMutableStorage(t *testing.T) {
	data := []byte(`[{"text":"original","children":[{"leaf":"original"}]},{"text":"original"}]`)
	l := codec.Limits{MaxBytes: 4096, MaxStringBytes: 1024, MaxByteStringBytes: 1024, MaxArrayElements: 100, MaxMapPairs: 100, MaxDepth: 128, MaxNodes: 100, MaxCollectionElements: 100}
	v, err := ParseJSON(data, l)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ParseJSON(data, l)
	if err != nil {
		t.Fatal(err)
	}
	for i := range data {
		data[i] = 'x'
	}
	if !v.Equal(other) {
		t.Fatal("output aliases input bytes")
	}
	v.array[0].object["text"] = String("edited")
	v.array[0].object["children"].array[0].object["leaf"] = Null()
	v.array[1] = Bool(false)
	if other.array[0].object["text"].text != "original" || other.array[0].object["children"].array[0].object["leaf"].text != "original" || other.array[1].object["text"].text != "original" {
		t.Fatal("separate parses share mutable storage")
	}
}

func TestCloneDetachesEachOccurrence(t *testing.T) {
	shared := Map{"key": String("original")}
	original := Array([]Value{Object(shared), Object(shared)})
	cloned := original.Clone()
	cloned.array[0].object["key"] = String("clone edit")
	if cloned.array[1].object["key"].text != "original" || shared["key"].text != "original" {
		t.Fatal("clone preserved DAG aliases")
	}
	for _, input := range []Value{Array(nil), Array([]Value{}), Object(nil), Object(Map{})} {
		detached := input.Clone()
		if (input.array == nil) != (detached.array == nil) || (input.object == nil) != (detached.object == nil) {
			t.Fatal("clone changed nil/empty backing")
		}
	}
}

func TestNavigationRetainsOnlyHandles(t *testing.T) {
	children := []Value{Object(Map{"leaf": String("selected")}), Array(make([]Value, 4096))}
	// Borrowed navigation is intentionally not admission and need not validate siblings.
	view := Borrow(Array(children))
	array, _ := view.Array()
	selected, _ := array.At(0).Object()
	leaf, ok := selected.Get("leaf")
	if s, _ := leaf.String(); !ok || s != "selected" {
		t.Fatal("selected navigation")
	}
	if &view.value.array[0] != &children[0] || &array.At(1).value.array[0] != &children[1].array[0] {
		t.Fatal("navigation cloned siblings")
	}
}
