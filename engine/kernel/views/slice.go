package views

import "iter"

// SliceView is a read-only view over caller-owned elements. It retains its
// backing slice and adapts each element on demand without constructing a []V.
// The zero value is an empty view. SliceView neither freezes nor synchronizes
// access to its backing; the backing must remain valid and stable for the
// lifetime of every derived view.
//
// T is retained only as backing for the adapter. The adapter must not expose a
// mutable element or otherwise let callers mutate retained data.
type SliceView[T any, V any] struct {
	values []T
	adapt  func(*T) V
}

// BorrowSliceView returns a view that borrows values and adapts each element on
// demand. It retains values without copying, so callers must own values and
// keep its elements stable for the lifetime of the returned view and values
// obtained from it. adapt must be non-nil and noncapturing, must not mutate its
// input, and must return a safe immutable view, which may retain the element.
// BorrowSliceView does not freeze data or synchronize access.
func BorrowSliceView[T any, V any](values []T, adapt func(*T) V) SliceView[T, V] {
	if adapt == nil {
		panic("views: nil SliceView adapter")
	}
	return SliceView[T, V]{values: values, adapt: adapt}
}

// Len returns the number of retained elements. It is zero for a zero view.
func (v SliceView[T, V]) Len() int {
	return len(v.values)
}

// At returns the adapted element at index. It panics when index is out of
// bounds, including for a zero view. The returned value may retain the backing
// element; see BorrowSliceView for its lifetime and immutability requirements.
func (v SliceView[T, V]) At(index int) V {
	return v.adapt(&v.values[index])
}

// All returns a repeatable iterator over the retained elements in index order.
// It preserves duplicates and stops immediately when yield returns false. Each
// yielded value may retain its backing element; callers must keep that backing
// stable for the iterator and every derived view's lifetime.
func (v SliceView[T, V]) All() iter.Seq2[int, V] {
	return func(yield func(int, V) bool) {
		for index := range v.values {
			if !yield(index, v.adapt(&v.values[index])) {
				return
			}
		}
	}
}
