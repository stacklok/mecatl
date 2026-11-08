package views

import "iter"

// Bytes is a read-only view over caller-owned bytes. It retains its backing
// without copying. The zero value is empty and represents nil backing. Bytes
// neither freezes nor synchronizes access; callers must keep the backing valid
// and stable for the lifetime of the view.
type Bytes struct {
	values []byte
}

// BorrowBytes returns a view that borrows values without copying. Callers must
// own values and keep it stable for the returned view's lifetime. It does not
// freeze data or synchronize access. Copy returns owned bytes when needed.
func BorrowBytes(values []byte) Bytes {
	return Bytes{values: values}
}

// Len returns the number of retained bytes. It is zero for a zero view and for
// non-nil empty backing.
func (v Bytes) Len() int {
	return len(v.values)
}

// At returns the byte at index. It panics when index is out of bounds,
// including for a zero view.
func (v Bytes) At(index int) byte {
	return v.values[index]
}

// All returns a repeatable iterator over retained bytes in index order. It
// stops immediately when yield returns false. The iterator borrows the backing,
// which callers must keep stable for its lifetime.
func (v Bytes) All() iter.Seq2[int, byte] {
	return func(yield func(int, byte) bool) {
		for index, value := range v.values {
			if !yield(index, value) {
				return
			}
		}
	}
}

// Copy returns an owned copy of the retained bytes. It preserves nil backing
// and returns a non-nil empty slice for non-nil empty backing.
func (v Bytes) Copy() []byte {
	if v.values == nil {
		return nil
	}
	copied := make([]byte, len(v.values))
	copy(copied, v.values)
	return copied
}
