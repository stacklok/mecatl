package value

import "testing"

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
