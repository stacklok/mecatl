package value

import (
	"iter"

	"github.com/stacklok/mecatl/engine/kernel/views"
)

// MapView is a read-only lazy handle retaining map backing. Zero is empty/safe;
// nil and empty backing are equivalent. It exposes no mutable descendants. Borrowed
// backing must stay stable for every derived view's lifetime; this handle neither
// freezes nor synchronizes it. Admit returns views over detached backing.
type MapView struct {
	values views.MapView[string, Value, View]
}

// BorrowMap borrows m without copying or validating in O(1) time and space. The
// owner must keep backing and descendants stable for this and all derived views'
// lifetimes. It does not freeze or synchronize data; use Admit at ownership boundaries.
func BorrowMap(m Map) MapView { return MapView{values: views.BorrowMapView(m, Borrow)} }

// Len returns the number of retained entries in O(1) time and space.
func (v MapView) Len() int { return v.values.Len() }

// Get returns one borrowed child or zero View and false for absence, in O(1)
// lookup work without visiting siblings. Present null/empty values stay present.
func (v MapView) Get(key string) (View, bool) { return v.values.Get(key) }

// All returns a lazy early-stoppable iterator in unspecified map order. Creating
// it is O(1); visiting n entries is O(n) work and O(1) auxiliary space. Derived
// views and the iterator retain backing under MapView's lifetime contract.
func (v MapView) All() iter.Seq2[string, View] { return v.values.All() }
