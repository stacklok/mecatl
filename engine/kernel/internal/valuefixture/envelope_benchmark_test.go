package valuefixture_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/internal/valuefixture"
	"github.com/stacklok/mecatl/engine/kernel/internal/valuefixture/fixtureview"
	"github.com/stacklok/mecatl/engine/kernel/value"
	"github.com/stacklok/mecatl/engine/kernel/views"
)

var (
	envelopeMapSink   views.MapView[string, *valuefixture.Child, fixtureview.ChildView]
	envelopeChildSink fixtureview.ChildView
	envelopeLeafSink  fixtureview.LeafView
	envelopeValueSink value.View
	envelopeCopySink  []byte
	envelopeSumSink   uint64
)

// BenchmarkEnvelopeViews measures the generated fixture seam, not persistence
// encoding/admission or owned DTO construction. The fixture's adapters are
// context-free; captured-adapter costs are measured by views.BenchmarkMapView.
func BenchmarkEnvelopeViews(b *testing.B) {
	dto := envelope()
	v := fixtureview.Borrow(&dto)
	b.Run("construct-map", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			envelopeMapSink = v.Children()
		}
	})
	children := v.Children()
	b.Run("lookup", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			envelopeChildSink, _ = children.Get("one")
		}
	})
	b.Run("traverse", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			sum := sumStructuredView(v.Value())
			for key, child := range v.Map().All() {
				sum += uint64(len(key)) + sumStructuredView(child)
			}
			for key, text := range v.Labels().All() {
				sum += uint64(len(key) + len(text))
			}
			for key, count := range v.Counts().All() {
				sum += uint64(len(key)) + count
			}
			for key, child := range children.All() {
				envelopeChildSink = child
				sum += uint64(len(key)+len(child.Label())) + sumBytesView(child.Data())
				for _, leaf := range child.Children().All() {
					envelopeLeafSink = leaf
					sum += sumBytesView(leaf.Data())
				}
			}
			envelopeSumSink = sum + sumBytesView(v.Sibling())
		}
	})
	child, _ := children.Get("one")
	leaf := child.Children().At(0).Data()
	b.Run("copy-selected-leaf", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(leaf.Len()))
		for b.Loop() {
			envelopeCopySink = leaf.Copy()
		}
	})
}

func sumBytesView(v views.Bytes) uint64 {
	var sum uint64
	for _, b := range v.All() {
		sum += uint64(b)
	}
	return sum
}

func sumStructuredView(v value.View) uint64 {
	envelopeValueSink = v
	sum := uint64(len(v.Kind().String()))
	switch v.Kind() {
	case value.BoolKind:
		if x, _ := v.Bool(); x {
			sum++
		}
	case value.StringKind:
		x, _ := v.String()
		sum += uint64(len(x))
	case value.IntegerKind:
		if x, ok := v.Uint64(); ok {
			sum += x
		} else {
			x, _ := v.Int64()
			sum += uint64(x)
		}
	case value.ArrayKind:
		x, _ := v.Array()
		for _, child := range x.All() {
			sum += sumStructuredView(child)
		}
	case value.MapKind:
		x, _ := v.Object()
		for key, child := range x.All() {
			sum += uint64(len(key)) + sumStructuredView(child)
		}
	}
	return sum
}
