package views

import "iter"

// MapView is a read-only, lazy view over a caller-owned string-key map. Its zero
// value is empty; nil and empty backing have equivalent meaning. It retains the
// map and adapter context, without freezing or synchronizing either. Both must
// remain stable for the lifetime of every derived view. The adapter must expose
// only immutable values or views, never mutable descendants of T.
type MapView[K ~string, T any, V any] struct {
	values map[K]T
	adapt  func(T) V
}

// BorrowMapView borrows values and captures adapt in O(1) time and space without
// invoking it. Callers retain ownership and must keep backing and captured context
// stable for the lifetime of this view and its descendants. adapt must be non-nil,
// must not mutate backing or context, and must return safe immutable values/views.
// It receives map entries by value, not by address. An escaping captured adapter
// may allocate. This constructor neither freezes data nor synchronizes access.
func BorrowMapView[K ~string, T any, V any](values map[K]T, adapt func(T) V) MapView[K, T, V] {
	if adapt == nil {
		panic("views: nil MapView adapter")
	}
	return MapView[K, T, V]{values: values, adapt: adapt}
}

// Len returns the number of retained entries in O(1) time and space.
func (v MapView[K, T, V]) Len() int { return len(v.values) }

// Get returns the adapted entry, or zero V and false on a miss without invoking
// the adapter. It takes O(1) map lookup work plus at most one adaptation. Returned
// views may retain backing and context under BorrowMapView's lifetime contract.
func (v MapView[K, T, V]) Get(key K) (V, bool) {
	value, ok := v.values[key]
	if !ok {
		var zero V
		return zero, false
	}
	return v.adapt(value), true
}

// All returns a repeatable lazy iterator in unspecified Go-map order. It stops
// immediately when yield returns false. Construction is O(1); visiting n entries
// uses O(n) work and O(1) auxiliary space plus n adaptations. The iterator and
// derived views retain backing/context under BorrowMapView's lifetime contract.
func (v MapView[K, T, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for key, value := range v.values {
			if !yield(key, v.adapt(value)) {
				return
			}
		}
	}
}
