package views_test

import (
	"fmt"
	"iter"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/views"
)

type mapKey string

func TestMapViewZeroScalarAndLazy(t *testing.T) {
	var zero views.MapView[mapKey, string, string]
	if zero.Len() != 0 {
		t.Fatal("nonempty zero")
	}
	if x, ok := zero.Get("missing"); ok || x != "" {
		t.Fatal("zero Get")
	}
	for range zero.All() {
		t.Fatal("zero iteration")
	}
	calls := 0
	backing := map[mapKey]string{"empty": "", "other": "payload"}
	v := views.BorrowMapView(backing, func(x string) string { calls++; return x })
	if calls != 0 || v.Len() != 2 {
		t.Fatal("eager construction")
	}
	if x, ok := v.Get("missing"); ok || x != "" || calls != 0 {
		t.Fatal("adapted miss")
	}
	if x, ok := v.Get("empty"); !ok || x != "" || calls != 1 {
		t.Fatal("lost present empty")
	}
	calls = 0
	for range v.All() {
		break
	}
	if calls != 1 {
		t.Fatal("adapted after early stop")
	}
	seq := v.All()
	for range 2 {
		seen := map[mapKey]bool{}
		for key, x := range seq {
			if x != backing[key] {
				t.Fatal("bad scalar")
			}
			seen[key] = true
		}
		if len(seen) != 2 {
			t.Fatal("lost entries")
		}
	}
	// This deliberately mutates borrowed backing between uses, proving no freeze.
	backing["empty"] = "changed"
	if x, _ := v.Get("empty"); x != "changed" {
		t.Fatal("not borrowed")
	}
	for _, m := range []map[mapKey]string{nil, {}} {
		empty := views.BorrowMapView(m, func(x string) string { return x })
		if empty.Len() != 0 {
			t.Fatal("nonempty")
		}
	}
}

func TestBorrowMapViewRejectsNilAdapter(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil adapter accepted")
		}
	}()
	views.BorrowMapView[string, int, int](nil, nil)
}

type mapEntry struct {
	label    string
	data     []byte
	children map[string]*mapEntry
}

type scopedMapEntry struct {
	entry *mapEntry
	scope string
}

func (v scopedMapEntry) Data() views.Bytes { return views.BorrowBytes(v.entry.data) }
func (v scopedMapEntry) Children() views.MapView[string, *mapEntry, scopedMapEntry] {
	return scopedEntries(v.entry.children, v.scope)
}
func scopedEntries(m map[string]*mapEntry, scope string) views.MapView[string, *mapEntry, scopedMapEntry] {
	return views.BorrowMapView(m, func(e *mapEntry) scopedMapEntry { return scopedMapEntry{e, scope} })
}

func mapFixture(n int) map[string]*mapEntry {
	m := make(map[string]*mapEntry, n)
	for i := range n {
		key := fmt.Sprint(i)
		m[key] = &mapEntry{label: key, data: make([]byte, 4096), children: map[string]*mapEntry{
			"leaf": {label: "leaf", data: []byte{3, 5}}, "sibling": {label: "sibling", data: []byte{8, 13}},
		}}
	}
	return m
}

func TestMapViewScopedTraversalAndLeafCopy(t *testing.T) {
	backing := mapFixture(4)
	first, second := scopedEntries(backing, "first"), scopedEntries(backing, "second")
	count := 0
	for key, parent := range first.All() {
		if parent.entry.label != key || parent.scope != "first" || parent.Data().Len() != 4096 {
			t.Fatal("lost parent/context")
		}
		other, _ := second.Get(key)
		if other.scope != "second" {
			t.Fatal("mixed scope")
		}
		for index, b := range parent.Data().All() {
			if b != backing[key].data[index] {
				t.Fatal("bad parent byte")
			}
			count++
		}
		for name, child := range parent.Children().All() {
			if child.scope != "first" || child.entry.label != name {
				t.Fatal("lost child scope")
			}
			for index, b := range child.Data().All() {
				if b != backing[key].children[name].data[index] {
					t.Fatal("bad borrowed byte")
				}
				count++
			}
		}
	}
	if count != 4*4096+16 {
		t.Fatalf("visited %d bytes", count)
	}
	parent, _ := first.Get("0")
	leaf, _ := parent.Children().Get("leaf")
	copied := leaf.Data().Copy()
	copied[0] = 99
	if leaf.Data().At(0) != 3 || backing["0"].children["sibling"].data[0] != 8 {
		t.Fatal("Copy exposed backing")
	}
	if &copied[0] == &backing["0"].children["leaf"].data[0] {
		t.Fatal("Copy aliases")
	}
	// Leaf navigation retains the original sibling allocations, not copies.
	if parent.entry != backing["0"] || leaf.entry != backing["0"].children["leaf"] {
		t.Fatal("copied siblings")
	}
}

var benchmarkMap views.MapView[string, *mapEntry, scopedMapEntry]
var benchmarkMapEntry scopedMapEntry
var benchmarkMapSeq iter.Seq2[string, scopedMapEntry]

func BenchmarkMapView(b *testing.B) {
	backing := mapFixture(64)
	b.Run("construct", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkMap = views.BorrowMapView(backing, func(e *mapEntry) scopedMapEntry { return scopedMapEntry{entry: e} })
		}
	})
	b.Run("construct-captured", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkMap = scopedEntries(backing, "scope")
		}
	})
	v := scopedEntries(backing, "scope")
	b.Run("get", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkMapEntry, _ = v.Get("0")
		}
	})
	b.Run("iterator-escaping", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkMapSeq = v.All()
		}
	})
	b.Run("traverse-captured", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, parent := range v.All() {
				benchmarkMapEntry = parent
				for _, x := range parent.Data().All() {
					benchmarkByteSum += int(x)
				}
				for _, child := range parent.Children().All() {
					benchmarkMapEntry = child
					for _, x := range child.Data().All() {
						benchmarkByteSum += int(x)
					}
				}
			}
		}
	})
}
