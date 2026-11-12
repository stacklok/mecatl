package value_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/value"
)

var benchmarkView value.View
var benchmarkSum uint64

func BenchmarkValue(b *testing.B) {
	author := value.Object(value.Map{
		"items": value.Array([]value.Value{value.Object(value.Map{
			"name": value.String("first"), "enabled": value.Bool(true), "count": value.Uint64(23),
		}), value.Object(value.Map{"name": value.String("second"), "enabled": value.Bool(false), "count": value.Uint64(42)})}),
		"sibling": value.Array([]value.Value{value.String("nonempty sibling"), value.Null()}),
	})
	accepted, err := author.Admit(limits())
	if err != nil {
		b.Fatal(err)
	}
	b.Run("admit", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkView, err = author.Admit(limits())
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("borrow-construction", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkView = value.Borrow(author)
		}
	})
	b.Run("lookup", func(b *testing.B) {
		m, _ := accepted.Object()
		b.ReportAllocs()
		for b.Loop() {
			benchmarkView, _ = m.Get("items")
		}
	})
	b.Run("full-traversal", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			walk(accepted)
		}
	})
}

func walk(v value.View) {
	benchmarkView = v
	switch v.Kind() {
	case value.MapKind:
		m, _ := v.Object()
		for _, child := range m.All() {
			walk(child)
		}
	case value.ArrayKind:
		a, _ := v.Array()
		for _, child := range a.All() {
			walk(child)
		}
	case value.IntegerKind:
		n, _ := v.Uint64()
		benchmarkSum += n
	case value.StringKind:
		s, _ := v.String()
		benchmarkSum += uint64(len(s))
	case value.BoolKind:
		x, _ := v.Bool()
		if x {
			benchmarkSum++
		}
	}
}
