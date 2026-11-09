package value

import (
	"math"

	"github.com/stacklok/mecatl/engine/kernel/views"
)

// View is a read-only handle retaining native value backing. Zero is safe and
// invalid, not implicit null. Scalar/presence reads and child navigation are O(1)
// and never copy/traverse siblings. Derived collections adapt lazily without
// intermediate []View storage. There is no mutable-storage or whole-graph export.
// Borrowed backing must stay stable for every derived view's lifetime; View does
// not freeze or synchronize data. Views returned by Admit retain detached backing.
type View struct{ value Value }

// Borrow retains v in O(1) time and space without checking or copying descendants.
// It does not freeze or synchronize backing. The owner must keep all reachable
// mutable storage stable for the lifetime of this view and its descendants. Use Admit to
// establish that ownership boundary instead of borrowing mutable author inputs.
func Borrow(v Value) View { return View{value: v} }

// MarshalJSON encodes the retained native value without exporting or cloning its
// backing. Zero is invalid. Value.MarshalJSON's traversal, cost, failure and
// stable-backing contract applies for the entire operation.
func (v View) MarshalJSON() ([]byte, error) { return marshalJSON(v.value) }

// Kind returns the value's kind; zero returns InvalidKind.
func (v View) Kind() Kind { return v.value.kind }

// Bool returns a boolean only when this view holds BoolKind.
func (v View) Bool() (bool, bool) {
	if v.Kind() != BoolKind {
		return false, false
	}
	return v.value.flag, true
}

// String returns text only when this view holds StringKind, preserving empty text.
func (v View) String() (string, bool) {
	if v.Kind() != StringKind {
		return "", false
	}
	return v.value.text, true
}

// Int64 returns an exact signed integer when representable; no narrowing occurs.
func (v View) Int64() (int64, bool) {
	if v.Kind() != IntegerKind || v.value.number > math.MaxInt64 {
		return 0, false
	}
	if v.value.flag {
		return -1 - int64(v.value.number), true
	}
	return int64(v.value.number), true
}

// Uint64 returns an exact nonnegative integer; negative values fail.
func (v View) Uint64() (uint64, bool) {
	if v.Kind() != IntegerKind || v.value.flag {
		return 0, false
	}
	return v.value.number, true
}

// Array returns a lazy borrowed collection only for ArrayKind, preserving empty
// arrays. Elements retain stable backing under View's lifetime contract.
func (v View) Array() (views.SliceView[Value, View], bool) {
	if v.Kind() != ArrayKind {
		return views.SliceView[Value, View]{}, false
	}
	return views.BorrowSliceView(v.value.array, func(child *Value) View { return Borrow(*child) }), true
}

// Object returns a borrowed map only for MapKind, preserving empty maps. Its
// children retain stable backing under View's lifetime contract.
func (v View) Object() (MapView, bool) {
	if v.Kind() != MapKind {
		return MapView{}, false
	}
	return BorrowMap(v.value.object), true
}
