package views_test

import (
	"fmt"
	"iter"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/views"
)

type testItem struct {
	value int
}

type testItemView struct {
	value int
}

func (v testItemView) Value() int {
	return v.value
}

func adaptTestItem(item *testItem) testItemView {
	return testItemView{value: item.value}
}

func TestSliceViewZero(t *testing.T) {
	var view views.SliceView[testItem, testItemView]
	if got := view.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	var got []int
	for _, item := range view.All() {
		got = append(got, item.Value())
	}
	if len(got) != 0 {
		t.Errorf("All() = %v, want empty", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("zero At(0) did not panic")
		}
	}()
	view.At(0)
}

func TestSliceViewAtOutOfBounds(t *testing.T) {
	view := views.BorrowSliceView([]testItem{{value: 1}}, adaptTestItem)
	for _, index := range []int{-1, 1} {
		t.Run("index", func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("At(%d) did not panic", index)
				}
			}()
			view.At(index)
		})
	}
}

func TestSliceViewAllStopsEarlyAndIsRepeatable(t *testing.T) {
	view := views.BorrowSliceView([]testItem{{value: 3}, {value: 5}, {value: 8}}, adaptTestItem)

	var early []int
	for index, item := range view.All() {
		early = append(early, item.Value())
		if index == 1 {
			break
		}
	}
	if want := []int{3, 5}; !reflect.DeepEqual(early, want) {
		t.Errorf("early iteration = %v, want %v", early, want)
	}

	for repeat := range 2 {
		var got []int
		for _, item := range view.All() {
			got = append(got, item.Value())
		}
		if want := []int{3, 5, 8}; !reflect.DeepEqual(got, want) {
			t.Errorf("iteration %d = %v, want %v", repeat, got, want)
		}
	}
}

func TestSliceViewBorrowsBacking(t *testing.T) {
	values := []testItem{{value: 3}}
	view := views.BorrowSliceView(values, adaptTestItem)

	values[0].value = 8
	if got := view.At(0).Value(); got != 8 {
		t.Errorf("At(0).Value() = %d, want borrowed mutation 8", got)
	}
}

func TestBorrowSliceViewRejectsNilAdapter(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("BorrowSliceView with nil adapter did not panic")
		}
	}()
	views.BorrowSliceView[testItem, testItemView](nil, nil)
}

func TestBytesZeroAndOutOfBounds(t *testing.T) {
	var view views.Bytes
	if got := view.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	var got []byte
	for _, value := range view.All() {
		got = append(got, value)
	}
	if len(got) != 0 {
		t.Errorf("All() = %v, want empty", got)
	}
	for _, index := range []int{-1, 1} {
		t.Run("out of bounds", func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("At(%d) did not panic", index)
				}
			}()
			views.BorrowBytes([]byte{1}).At(index)
		})
	}
	defer func() {
		if recover() == nil {
			t.Error("zero At(0) did not panic")
		}
	}()
	view.At(0)
}

func TestBytesAllStopsEarlyAndIsRepeatable(t *testing.T) {
	view := views.BorrowBytes([]byte{3, 5, 8})

	var early []byte
	for index, value := range view.All() {
		early = append(early, value)
		if index == 1 {
			break
		}
	}
	if want := []byte{3, 5}; !reflect.DeepEqual(early, want) {
		t.Errorf("early iteration = %v, want %v", early, want)
	}

	for repeat := range 2 {
		var got []byte
		for _, value := range view.All() {
			got = append(got, value)
		}
		if want := []byte{3, 5, 8}; !reflect.DeepEqual(got, want) {
			t.Errorf("iteration %d = %v, want %v", repeat, got, want)
		}
	}
}

func TestBytesBorrowsBackingAndCopyIsolates(t *testing.T) {
	values := []byte{3, 5}
	view := views.BorrowBytes(values)
	copied := view.Copy()

	values[0] = 8
	if got := view.At(0); got != 8 {
		t.Errorf("At(0) = %d, want borrowed mutation 8", got)
	}
	if got := copied[0]; got != 3 {
		t.Errorf("Copy()[0] = %d, want isolated value 3", got)
	}
	copied[1] = 13
	if got := view.At(1); got != 5 {
		t.Errorf("At(1) = %d, want backing value 5", got)
	}
}

func TestBytesCopyPreservesNilAndEmpty(t *testing.T) {
	if got := views.BorrowBytes(nil).Copy(); got != nil {
		t.Errorf("nil Copy() = %#v, want nil", got)
	}
	if got := (views.Bytes{}).Copy(); got != nil {
		t.Errorf("zero Copy() = %#v, want nil", got)
	}

	empty := views.BorrowBytes([]byte{}).Copy()
	if empty == nil {
		t.Error("non-nil empty Copy() = nil")
	}
	if len(empty) != 0 {
		t.Errorf("non-nil empty Copy() length = %d, want 0", len(empty))
	}
}

var benchmarkItemView testItemView
var benchmarkItemSum int
var benchmarkBytesIterator iter.Seq2[int, byte]
var benchmarkByteSum int
var benchmarkBytesCopy []byte

//go:noinline
func consumeBytes(seq iter.Seq2[int, byte]) {
	for _, value := range seq {
		benchmarkByteSum += int(value)
	}
}

func BenchmarkBytesAll(b *testing.B) {
	view := views.BorrowBytes([]byte{3, 5, 8})
	// Store before timing so reuse includes opaque consumption, not creation.
	benchmarkBytesIterator = view.All()
	for _, mode := range []string{"escaping-create", "opaque-consume", "escaping-reuse"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				switch mode {
				case "escaping-create":
					benchmarkBytesIterator = view.All()
				case "opaque-consume":
					consumeBytes(view.All())
				case "escaping-reuse":
					consumeBytes(benchmarkBytesIterator)
				}
			}
		})
	}
}

func BenchmarkBytesCopy(b *testing.B) {
	for _, size := range []int{0, 32, 16384} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			view := views.BorrowBytes(make([]byte, size))
			b.ReportAllocs()
			for b.Loop() {
				benchmarkBytesCopy = view.Copy()
			}
		})
	}
}

func BenchmarkSliceViewAtByValueAdapter(b *testing.B) {
	view := views.BorrowSliceView([]testItem{{value: 3}}, adaptTestItem)
	b.ReportAllocs()
	for b.Loop() {
		benchmarkItemView = view.At(0)
	}
}

func BenchmarkSliceViewAllByValueAdapter(b *testing.B) {
	view := views.BorrowSliceView([]testItem{{value: 3}, {value: 5}, {value: 8}}, adaptTestItem)
	b.ReportAllocs()
	for b.Loop() {
		for _, item := range view.All() {
			benchmarkItemSum += item.Value()
		}
	}
}
