package codec_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture"
	"github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture/value"
)

func assertSampleRejected(t *testing.T, v fixture.Sample, data []byte, l codec.Limits, want error) {
	t.Helper()
	if err := fixture.CheckSample(v, l); !errors.Is(err, want) {
		t.Fatalf("check: %v; want %v", err, want)
	}
	if got, err := fixture.EncodeSample(v, l); got != nil || !errors.Is(err, want) {
		t.Fatalf("encode: %x %v; want %v", got, err, want)
	}
	if got, err := fixture.DecodeSample(data, l); !reflect.DeepEqual(got, fixture.Sample{}) || !errors.Is(err, want) {
		t.Fatalf("decode: %#v %v; want %v", got, err, want)
	}
}
func assertCatalogRejected(t *testing.T, v fixture.Catalog, data []byte, l codec.Limits, want error) {
	t.Helper()
	if err := fixture.CheckCatalog(v, l); !errors.Is(err, want) {
		t.Fatalf("check: %v; want %v", err, want)
	}
	if got, err := fixture.EncodeCatalog(v, l); got != nil || !errors.Is(err, want) {
		t.Fatalf("encode: %x %v; want %v", got, err, want)
	}
	if got, err := fixture.DecodeCatalog(data, l); !reflect.DeepEqual(got, fixture.Catalog{}) || !errors.Is(err, want) {
		t.Fatalf("decode: %#v %v; want %v", got, err, want)
	}
}

func TestGeneratedPolicyThresholdsAndEstimateSymmetry(t *testing.T) {
	// Sample zero: 1 root + 11 keys + 11 values, 34 key bytes + 3 opaque
	// bytes. The conservative symmetric estimate is 23*9+37=244, not wire size.
	exact := codec.Limits{MaxBytes: 244, MaxStringBytes: 5, MaxByteStringBytes: 3, MaxArrayElements: 1, MaxMapPairs: 11, MaxDepth: 1, MaxNodes: 23, MaxCollectionElements: 11}
	data := unhex(t, sampleZeroHex)
	if err := fixture.CheckSample(fixture.Sample{}, exact); err != nil {
		t.Fatal(err)
	}
	b, err := fixture.EncodeSample(fixture.Sample{}, exact)
	if err != nil || !bytes.Equal(data, b) {
		t.Fatal(err)
	}
	v, err := fixture.DecodeSample(data, exact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.EncodeSample(v, exact); err != nil {
		t.Fatal("decode-reencode asymmetry", err)
	}
	for _, tt := range []struct {
		name  string
		lower func(*codec.Limits)
	}{
		{"estimate", func(l *codec.Limits) { l.MaxBytes-- }},
		{"key length", func(l *codec.Limits) { l.MaxStringBytes-- }},
		{"byte length", func(l *codec.Limits) { l.MaxByteStringBytes-- }},
		{"struct pairs", func(l *codec.Limits) { l.MaxMapPairs-- }},
		{"nodes", func(l *codec.Limits) { l.MaxNodes-- }},
		{"aggregate declarations", func(l *codec.Limits) { l.MaxCollectionElements-- }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := exact
			tt.lower(&l)
			assertSampleRejected(t, fixture.Sample{}, data, l, codec.ErrLimitExceeded)
		})
	}
	// Every global numeric field is mandatory even when a shape never uses it.
	typ := reflect.TypeFor[codec.Limits]()
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Name == "Named" {
			continue
		}
		for _, bad := range []int{0, -1} {
			l := generatedLimits()
			reflect.ValueOf(&l).Elem().Field(i).SetInt(int64(bad))
			assertSampleRejected(t, fixture.Sample{Label: "\xff"}, []byte{0xff}, l, codec.ErrInvalidLimits)
		}
	}
	// A nonempty nested container at depth one must fail before child allocation.
	vcat := richCatalog()
	vcat.Cache = nil
	catdata, err := fixture.EncodeCatalog(vcat, generatedLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, dimension := range []string{"array", "depth", "map", "nodes", "aggregate"} {
		l := generatedLimits()
		switch dimension {
		case "array":
			l.MaxArrayElements = 1
		case "depth":
			l.MaxDepth = 1
		case "map":
			l.MaxMapPairs = 3
		case "nodes":
			l.MaxNodes = 12
		case "aggregate":
			l.MaxCollectionElements = 5
		}
		assertCatalogRejected(t, vcat, catdata, l, codec.ErrLimitExceeded)
	}
}

func TestGeneratedFieldBudgets(t *testing.T) {
	t.Run("UTF8 bytes not runes and literal cap", func(t *testing.T) {
		v := fixture.Sample{Label: strings.Repeat("é", 16)}
		l := generatedLimits()
		data, err := fixture.EncodeSample(v, l)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.DecodeSample(data, l); err != nil {
			t.Fatal(err)
		}
		v.Label += "é"
		data, err = fixture.EncodeSample(v, func() codec.Limits { p := l; p.MaxStringBytes = 16; return p }())
		if data != nil || !errors.Is(err, codec.ErrLimitExceeded) {
			t.Fatal(err)
		}
		// Construct over-literal data independently; Encode cannot bypass its cap.
		raw := unhex(t, strings.TrimSuffix(sampleZeroHex, "60"))
		raw = append(raw, 0x78, 34)
		raw = append(raw, []byte(v.Label)...)
		assertSampleRejected(t, v, raw, l, codec.ErrLimitExceeded)
	})
	t.Run("named absent branches resolved first", func(t *testing.T) {
		var b value.Blob
		v := fixture.Catalog{Required: &b}
		data := unhex(t, catalogEmptyHex)
		for _, name := range []string{"entries", "payload", "optional", "detail"} {
			for _, bad := range []int{0, -1} {
				l := generatedLimits()
				l.Named[name] = bad
				assertCatalogRejected(t, v, data, l, codec.ErrInvalidLimits)
				assertCatalogRejected(t, v, []byte{0xff}, l, codec.ErrInvalidLimits)
			}
			l := generatedLimits()
			delete(l.Named, name)
			assertCatalogRejected(t, v, data, l, codec.ErrInvalidLimits)
		}
		l := generatedLimits()
		l.Named = nil
		assertCatalogRejected(t, v, data, l, codec.ErrInvalidLimits)
		l = generatedLimits()
		l.Named["ignored"] = -1
		before := map[string]int{}
		for k, n := range l.Named {
			before[k] = n
		}
		if _, err := fixture.EncodeCatalog(v, l); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.DecodeCatalog(data, l); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, l.Named) {
			t.Fatal("policy dictionary mutated")
		}
		if _, err := fixture.EncodeSample(fixture.Sample{}, l); err != nil {
			t.Fatal("unreferenced dictionary entries inspected", err)
		}
	})
	t.Run("named and global tighter caps", func(t *testing.T) {
		v := richCatalog()
		v.Cache = nil
		v.Payload = value.Blob{1, 2, 3, 4}
		data, err := fixture.EncodeCatalog(v, generatedLimits())
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range []codec.Limits{
			func() codec.Limits { l := generatedLimits(); l.Named["payload"] = 3; return l }(),
			func() codec.Limits { l := generatedLimits(); l.MaxByteStringBytes = 3; return l }(),
		} {
			assertCatalogRejected(t, v, data, l, codec.ErrLimitExceeded)
		}
		l := generatedLimits()
		l.Named["payload"] = 4
		l.MaxByteStringBytes = 4
		if _, err := fixture.DecodeCatalog(data, l); err != nil {
			t.Fatal("threshold", err)
		}
	})
	t.Run("pointer presence arrays map counts and nested limits", func(t *testing.T) {
		v := richCatalog()
		v.Cache = nil
		data, err := fixture.EncodeCatalog(v, generatedLimits())
		if err != nil {
			t.Fatal(err)
		}
		l := generatedLimits()
		l.Named["entries"] = 1
		l.Named["optional"] = 1
		if _, err := fixture.DecodeCatalog(data, l); err != nil {
			t.Fatal(err)
		}
		v.Entries["second"] = nil
		data, err = fixture.EncodeCatalog(v, generatedLimits())
		if err != nil {
			t.Fatal(err)
		}
		assertCatalogRejected(t, v, data, l, codec.ErrLimitExceeded)
		delete(v.Entries, "second")
		*v.Optional = []string{"a", "b"}
		data, err = fixture.EncodeCatalog(v, generatedLimits())
		if err != nil {
			t.Fatal(err)
		}
		assertCatalogRejected(t, v, data, l, codec.ErrLimitExceeded)
		// Literal item cap is independent of each element's byte length.
		v = richCatalog()
		v.Cache = nil
		v.Names = []string{"a", "b", "c", "d", "e"}
		raw := unhex(t, "a4656e616d6573856161616261636164616567656e7472696573a0677061796c6f61644068726571756972656440")
		assertCatalogRejected(t, v, raw, generatedLimits(), codec.ErrLimitExceeded)
		// Repeated imported values share one resolved field budget while each
		// instance still satisfies both the field cap and global byte cap.
		v = richCatalog()
		v.Cache = nil
		v.Entries["x"][0].Data = value.Blob{1, 2, 3}
		data, err = fixture.EncodeCatalog(v, generatedLimits())
		if err != nil {
			t.Fatal(err)
		}
		l = generatedLimits()
		l.MaxByteStringBytes = 2
		assertCatalogRejected(t, v, data, l, codec.ErrLimitExceeded)
		l = generatedLimits()
		l.Named["detail"] = 2
		assertCatalogRejected(t, v, data, l, codec.ErrLimitExceeded)
		v.Entries["x"][0].Data = value.Blob{1, 2}
		v.Entries["x"] = append(v.Entries["x"], &value.Detail{Data: value.Blob{3, 4}})
		data, err = fixture.EncodeCatalog(v, l)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.DecodeCatalog(data, l); err != nil {
			t.Fatal(err)
		}
	})
}

func TestGeneratedReservationsAndEarlyRejectionBytes(t *testing.T) {
	// Prebuild adversarial inputs outside measurement. An oversized declared map
	// must not cause a typed allocation before aggregate/node reservation fails.
	data := unhex(t, "a4656e616d65738067656e7472696573b91900")
	// Supply enough minimum bytes to pass the remaining-input guard, but not policy.
	data = append(data, make([]byte, 12800)...)
	l := generatedLimits()
	l.MaxMapPairs = 10000
	l.Named["entries"] = 10000
	l.MaxNodes = 20
	got, err := fixture.DecodeCatalog(data, l)
	if !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, fixture.Catalog{}) {
		t.Fatal(got, err)
	}
	v := richCatalog()
	v.Entries = make(map[value.Key][]*value.Detail, 10000)
	for i := 0; i < 10000; i++ {
		v.Entries[value.Key(strings.Repeat("x", i%20)+string(rune(i+32)))] = nil
	}
	l = generatedLimits()
	// Every generic declaration/aggregate/size/node limit admits this map; the
	// named field budget alone must reject before sorting or encoding allocation.
	l.MaxMapPairs = 10000
	l.MaxCollectionElements = 10128
	l.MaxNodes = 20128
	l.MaxBytes = 1 << 22
	l.Named["entries"] = 1
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			if _, err := fixture.EncodeCatalog(v, l); !errors.Is(err, codec.ErrLimitExceeded) {
				b.Fatal(err)
			}
		}
	})
	if result.AllocedBytesPerOp() > 2048 {
		t.Fatalf("rejection allocated %d bytes/op before map admission", result.AllocedBytesPerOp())
	}
	huge := make([]byte, 1<<20)
	small := generatedLimits()
	small.MaxBytes = 32
	result = testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			if _, err := fixture.DecodeSample(huge, small); !errors.Is(err, codec.ErrLimitExceeded) {
				b.Fatal(err)
			}
		}
	})
	if result.AllocedBytesPerOp() > 128 {
		t.Fatalf("oversized input copied: %d bytes/op", result.AllocedBytesPerOp())
	}
	// A stable policy proves successful Check itself allocates nothing.
	stable := generatedLimits()
	result = testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			if err := fixture.CheckSample(fixture.Sample{}, stable); err != nil {
				b.Fatal(err)
			}
		}
	})
	if result.AllocedBytesPerOp() != 0 {
		t.Fatalf("Check allocated %d bytes/op", result.AllocedBytesPerOp())
	}
}
