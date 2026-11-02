package codec_test

import (
	"fmt"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture"
	"github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture/value"
)

var (
	qualificationSample  fixture.Sample
	qualificationCatalog fixture.Catalog
	qualificationBytes   []byte
	qualificationEqual   bool
)

func benchmarkPersistence[T any](b *testing.B, v T, l codec.Limits,
	check func(T, codec.Limits) error, encode func(T, codec.Limits) ([]byte, error),
	decode func([]byte, codec.Limits) (T, error), clone func(T) T, equal func(T, T) bool, retain func(T),
) {
	b.Helper()
	data, err := encode(v, l)
	if err != nil {
		b.Fatal(err)
	}
	if w, err := decode(data, l); err != nil || !equal(v, w) {
		b.Fatal("benchmark shape does not roundtrip", err)
	}
	b.Run("Check", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			if err := check(v, l); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Encode", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			var err error
			qualificationBytes, err = encode(v, l)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Decode", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			w, err := decode(data, l)
			if err != nil {
				b.Fatal(err)
			}
			retain(w)
		}
	})
	b.Run("CloneEqual", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			w := clone(v)
			qualificationEqual = equal(v, w)
			retain(w)
			if !qualificationEqual {
				b.Fatal("clone differs")
			}
		}
	})
}

func BenchmarkPersistence(b *testing.B) {
	b.Run("small", func(b *testing.B) {
		v := fixture.Sample{OK: true, Label: "sample"}
		benchmarkPersistence(b, v, generatedLimits(), fixture.CheckSample, fixture.EncodeSample, fixture.DecodeSample,
			fixture.Sample.Clone, fixture.Sample.Equal, func(v fixture.Sample) { qualificationSample = v })
	})
	for _, shape := range []string{"nested", "large", "many-small", "map-heavy"} {
		b.Run(shape, func(b *testing.B) {
			v := richCatalog()
			v.Cache = nil
			l := generatedLimits()
			l.MaxBytes, l.MaxNodes, l.MaxCollectionElements = 1<<22, 100000, 100000
			switch shape {
			case "large":
				v.Payload = make(value.Blob, 1<<18)
				l.MaxByteStringBytes, l.Named["payload"] = len(v.Payload), len(v.Payload)
			case "many-small":
				v.Entries["x"] = make([]*value.Detail, 4096)
				for i := range v.Entries["x"] {
					v.Entries["x"][i] = &value.Detail{Data: value.Blob{value.Octet(i & 255)}}
				}
				l.MaxArrayElements = 4096
			case "map-heavy":
				v.Entries = make(map[value.Key][]*value.Detail, 4096)
				for i := 0; i < 4096; i++ {
					v.Entries[value.Key(fmt.Sprintf("key-%04d", i))] = nil
				}
				l.MaxMapPairs, l.Named["entries"] = 4096, 4096
			}
			benchmarkPersistence(b, v, l, fixture.CheckCatalog, fixture.EncodeCatalog, fixture.DecodeCatalog,
				fixture.Catalog.Clone, fixture.Catalog.Equal, func(v fixture.Catalog) { qualificationCatalog = v })
		})
	}
}
