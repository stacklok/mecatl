package codec_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture"
	"github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture/value"
)

func generatedLimits() codec.Limits {
	return codec.Limits{MaxBytes: 1 << 20, MaxStringBytes: 1024, MaxByteStringBytes: 4096, MaxArrayElements: 128, MaxMapPairs: 128, MaxDepth: 20, MaxNodes: 10000, MaxCollectionElements: 10000, Named: map[string]int{"entries": 16, "payload": 4096, "optional": 4, "detail": 4096}}
}
func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// These bytes are written independently from the schema/profile, not produced by
// Atmos or by the generator. Key order is UTF-8 byte length, then bytewise order.
const sampleZeroHex = "ab62693800626f6bf46275380063693136006369333200636936340063753136006375333200637536340065666978656443000000656c6162656c60"
const sampleExtremaHex = "ac626938387f626f6bf562753818ff63693136397fff636933323a7fffffff636936343b7fffffffffffffff6375313619ffff637533321affffffff637536341bffffffffffffffff6566697865644300ff80656c6162656c62c3a9686f7074696f6e616c00"
const sampleMaxSignedHex = "ab626938187f626f6bf46275380063693136197fff636933321a7fffffff636936341b7fffffffffffffff63753136006375333200637536340065666978656443000000656c6162656c60"
const catalogEmptyHex = "a4656e616d65738067656e7472696573a0677061796c6f61644068726571756972656440"
const catalogNestedHex = "a5656e616d657382636f6e656374776f67656e7472696573a1617881a16464617461420708677061796c6f6164420506686f7074696f6e616c816170687265717569726564420304"

func TestGeneratedCanonicalGoldens(t *testing.T) {
	l := generatedLimits()
	zero := int64(0)
	extrema := fixture.Sample{I8: math.MinInt8, I16: math.MinInt16, I32: math.MinInt32, I64: math.MinInt64, U8: math.MaxUint8, U16: math.MaxUint16, U32: math.MaxUint32, U64: math.MaxUint64, OK: true, Label: "é", Fixed: [3]value.Octet{0, 255, 128}, Optional: &zero}
	for _, tt := range []struct {
		name string
		v    fixture.Sample
		hex  string
	}{{"zero", fixture.Sample{}, sampleZeroHex}, {"extrema", extrema, sampleExtremaHex}, {"max signed", fixture.Sample{I8: math.MaxInt8, I16: math.MaxInt16, I32: math.MaxInt32, I64: math.MaxInt64}, sampleMaxSignedHex}} {
		t.Run(tt.name, func(t *testing.T) {
			want := unhex(t, tt.hex)
			if err := fixture.CheckSample(tt.v, l); err != nil {
				t.Fatal(err)
			}
			got, err := fixture.EncodeSample(tt.v, l)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("got %x, %v; want %x", got, err, want)
			}
			v, err := fixture.DecodeSample(want, l)
			if err != nil || !v.Equal(tt.v) {
				t.Fatalf("decode %v, %v", v, err)
			}
		})
	}
	var required value.Blob
	v := fixture.Catalog{Required: &required}
	got, err := fixture.EncodeCatalog(v, l)
	want := unhex(t, catalogEmptyHex)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("catalog %x %v; want %x", got, err, want)
	}
	decoded, err := fixture.DecodeCatalog(want, l)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Equal(decoded) || decoded.Entries == nil || decoded.Names == nil || decoded.Payload == nil || decoded.Required == nil || *decoded.Required == nil {
		t.Fatalf("required collections not normalized: %#v", decoded)
	}
	optional := []string(nil)
	v.Optional = &optional
	got, err = fixture.EncodeCatalog(v, l)
	if err != nil || bytes.Equal(got, want) {
		t.Fatalf("present-empty optional lost: %x %v", got, err)
	}
	decoded, err = fixture.DecodeCatalog(got, l)
	if err != nil || !v.Equal(decoded) || decoded.Optional == nil || *decoded.Optional == nil {
		t.Fatal("pointer-to-empty presence", decoded, err)
	}
	v = richCatalog()
	v.Cache = nil
	got, err = fixture.EncodeCatalog(v, l)
	want = unhex(t, catalogNestedHex)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("nested catalog %x %v; want %x", got, err, want)
	}
	decoded, err = fixture.DecodeCatalog(want, l)
	if err != nil || !decoded.Equal(v) {
		t.Fatal("nested catalog decode", decoded, err)
	}
}

func TestGeneratedMapCanonicalOrder(t *testing.T) {
	required := value.Blob{0xff, 0}
	v := fixture.Catalog{Required: &required, Payload: value.Blob{0xfe, 0}}
	keys := []value.Key{"é", "aa", "z", ""}
	// Independent expected wire map has empty arrays as values. The two-byte é key
	// sorts after aa, not by rune count or locale.
	want := unhex(t, "a4656e616d65738067656e7472696573a46080617a806261618062c3a980677061796c6f616442fe0068726571756972656442ff00")
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 32; i++ {
		v.Entries = map[value.Key][]*value.Detail{}
		for _, j := range rng.Perm(len(keys)) {
			v.Entries[keys[j]] = nil
		}
		got, err := fixture.EncodeCatalog(v, generatedLimits())
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("canonical map %x %v", got, err)
		}
		decoded, err := fixture.DecodeCatalog(got, generatedLimits())
		if err != nil || !v.Equal(decoded) {
			t.Fatal(decoded, err)
		}
	}
}

func TestGeneratedStrictDecodeZeroOutput(t *testing.T) {
	valid := unhex(t, sampleZeroHex)
	replace := func(old, replacement string) []byte {
		t.Helper()
		if strings.Count(sampleZeroHex, old) != 1 {
			t.Fatalf("ambiguous replacement %s", old)
		}
		return unhex(t, strings.Replace(sampleZeroHex, old, replacement, 1))
	}
	cases := map[string][]byte{
		"trailing":                        append(bytes.Clone(valid), 0),
		"trailing after populated result": append(unhex(t, sampleExtremaHex), 0),
		"null":                            {0xf6}, "tag": {0xc0, 0xa0}, "indefinite": {0xbf, 0xff}, "float": {0xfb, 0, 0, 0, 0, 0, 0, 0, 0},
		"missing":            unhex(t, "a0"),
		"unknown immediate":  unhex(t, "a16178"),
		"nonminimal map":     append([]byte{0xb8, 0x0b}, valid[1:]...),
		"nonminimal integer": replace("62693800", "6269381800"),
		"nonminimal string":  replace("656c6162656c60", "656c6162656c7800"),
		"wrong type":         replace("626f6bf4", "626f6b00"),
		"null optional":      append(unhex(t, strings.Replace(sampleZeroHex, "ab", "ac", 1)), unhex(t, "686f7074696f6e616cf6")...),
		"int8 high":          replace("62693800", "6269381880"),
		"int8 low":           replace("62693800", "6269383880"),
		"int16 high":         replace("6369313600", "63693136198000"),
		"int16 low":          replace("6369313600", "63693136398000"),
		"int32 low":          replace("6369333200", "636933323a80000000"),
		"nonminimal bool":    replace("626f6bf4", "626f6bf814"),
		"undefined simple":   replace("626f6bf4", "626f6bf7"),
		"int32 high":         replace("6369333200", "636933321a80000000"),
		"int64 high":         replace("6369363400", "636936341b8000000000000000"),
		"int64 low":          replace("6369363400", "636936343b8000000000000000"),
		"uint negative":      replace("62753800", "62753820"),
		"uint8 high":         replace("62753800", "627538190100"),
		"uint16 high":        replace("6375313600", "637531361a00010000"),
		"uint32 high":        replace("6375333200", "637533321b0000000100000000"),
		"short fixed":        replace("65666978656443000000", "656669786564420000"),
		"long fixed":         replace("65666978656443000000", "6566697865644400000000"),
		"invalid utf8":       replace("656c6162656c60", "656c6162656c61ff"),
		"null bytes":         replace("65666978656443000000", "656669786564f6"),
		"duplicate":          replace("626f6bf4", "62693800"),
		"unordered":          replace("62693800626f6bf4", "626f6bf462693800"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := fixture.DecodeSample(data, generatedLimits())
			if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, fixture.Sample{}) {
				t.Fatalf("got %#v, %v", got, err)
			}
		})
	}
	for i := 0; i < len(valid); i++ {
		got, err := fixture.DecodeSample(valid[:i], generatedLimits())
		if err == nil || !reflect.DeepEqual(got, fixture.Sample{}) {
			t.Fatalf("truncate %d: %#v %v", i, got, err)
		}
	}
	// Struct keys are schema names, data-map entries are not. Reject duplicate or
	// unordered data keys and null elements without retaining a partial Catalog.
	for _, middle := range []string{"a2617a80616180", "a2616180616180", "a1616181f6", "a1616181a16464617461f6"} {
		data := unhex(t, "a4656e616d65738067656e7472696573"+middle+"677061796c6f61644068726571756972656440")
		got, err := fixture.DecodeCatalog(data, generatedLimits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, fixture.Catalog{}) {
			t.Fatalf("catalog %x: %#v %v", data, got, err)
		}
	}
}

func richCatalog() fixture.Catalog {
	required := value.Blob{3, 4}
	optional := []string{"p"}
	n := int64(42)
	return fixture.Catalog{
		Required: &required, Optional: &optional, Payload: value.Blob{5, 6}, Names: []string{"one", "two"},
		Entries: map[value.Key][]*value.Detail{"x": {{Data: value.Blob{7, 8}}}},
		Cache:   map[string]*fixture.CacheEntry{"cache": {Bytes: []byte{9}, Lists: map[string][]*int64{"n": {&n}}}},
	}
}

func TestGeneratedWholeValueMethods(t *testing.T) {
	v := richCatalog()
	w := v.Clone()
	z := richCatalog()
	if !v.Equal(v) || !v.Equal(w) || !w.Equal(v) || !w.Equal(z) || !v.Equal(z) {
		t.Fatal("equality reflexivity, symmetry or transitivity")
	}
	// Mutate every mutable descendant in one clone and compare the source to its
	// concrete fixture values, never to another Clone result.
	(*w.Required)[0] = 99
	(*w.Optional)[0] = "changed"
	w.Payload[0] = 99
	w.Names[0] = "changed"
	w.Entries["x"][0].Data[0] = 99
	w.Entries["x"] = append(w.Entries["x"], nil)
	w.Entries["other"] = nil
	w.Cache["cache"].Bytes[0] = 99
	w.Cache["other"] = nil
	*w.Cache["cache"].Lists["n"][0] = 99
	w.Cache["cache"].Lists["other"] = nil
	if (*v.Required)[0] != 3 || (*v.Optional)[0] != "p" || v.Payload[0] != 5 || v.Names[0] != "one" ||
		v.Entries["x"][0].Data[0] != 7 || len(v.Entries["x"]) != 1 || v.Entries["other"] != nil ||
		v.Cache["cache"].Bytes[0] != 9 || v.Cache["other"] != nil || *v.Cache["cache"].Lists["n"][0] != 42 || v.Cache["cache"].Lists["other"] != nil {
		t.Fatal("clone mutation aliased source")
	}
	if _, exists := v.Entries["other"]; exists {
		t.Fatal("clone entries map aliases source")
	}
	if _, exists := v.Cache["other"]; exists {
		t.Fatal("clone excluded map aliases source")
	}
	if _, exists := v.Cache["cache"].Lists["other"]; exists {
		t.Fatal("clone nested map aliases source")
	}
	if v.Equal(w) {
		t.Fatal("equality ignored mutable descendant")
	}
	// Source mutation must leave a separately created clone unchanged.
	independent := v.Clone()
	(*v.Required)[0] = 88
	(*v.Optional)[0] = "source changed"
	v.Payload[0] = 88
	v.Names[0] = "source changed"
	v.Entries["x"][0].Data[0] = 88
	v.Entries["x"] = append(v.Entries["x"], nil)
	v.Cache["cache"].Bytes[0] = 88
	*v.Cache["cache"].Lists["n"][0] = 88
	if (*independent.Required)[0] != 3 || (*independent.Optional)[0] != "p" || independent.Payload[0] != 5 || independent.Names[0] != "one" ||
		independent.Entries["x"][0].Data[0] != 7 || len(independent.Entries["x"]) != 1 ||
		independent.Cache["cache"].Bytes[0] != 9 || *independent.Cache["cache"].Lists["n"][0] != 42 {
		t.Fatal("source mutation aliased clone")
	}
	// Two independently produced clones cannot share mutable descendants.
	left, right := independent.Clone(), independent.Clone()
	left.Payload[0] = 77
	left.Names[0] = "left"
	(*left.Optional)[0] = "left"
	left.Entries["x"][0].Data[0] = 77
	left.Cache["cache"].Bytes[0] = 77
	*left.Cache["cache"].Lists["n"][0] = 77
	if right.Payload[0] != 5 || right.Names[0] != "one" || (*right.Optional)[0] != "p" ||
		right.Entries["x"][0].Data[0] != 7 || right.Cache["cache"].Bytes[0] != 9 || *right.Cache["cache"].Lists["n"][0] != 42 {
		t.Fatal("clones share mutable descendants")
	}
	a := fixture.Catalog{}
	b := fixture.Catalog{Entries: map[value.Key][]*value.Detail{}, Names: []string{}, Payload: value.Blob{}, Cache: map[string]*fixture.CacheEntry{}}
	if !a.Equal(b) || !b.Equal(a) {
		t.Fatal("nil/empty collection equality")
	}
	ac, bc := a.Clone(), b.Clone()
	if ac.Entries != nil || ac.Names != nil || ac.Payload != nil || ac.Cache != nil || bc.Entries == nil || bc.Names == nil || bc.Payload == nil || bc.Cache == nil {
		t.Fatal("clone nil/empty representation")
	}
	b.Required = new(value.Blob)
	if a.Equal(b) {
		t.Fatal("nil pointer equals present-empty")
	}
	bc = b.Clone()
	if bc.Required == nil || *bc.Required != nil || bc.Required == b.Required {
		t.Fatal("clone pointer-to-nil collection")
	}
	a.Entries = map[value.Key][]*value.Detail{"x": nil}
	b = a.Clone()
	b.Entries["x"] = []*value.Detail{}
	if !a.Equal(b) {
		t.Fatal("nested nil/empty collection equality")
	}
	b.Entries["x"] = []*value.Detail{nil}
	a.Entries["x"] = []*value.Detail{{}}
	if a.Equal(b) {
		t.Fatal("nested pointer presence ignored")
	}
	a.Cache = map[string]*fixture.CacheEntry{"x": nil}
	b = a.Clone()
	delete(b.Cache, "x")
	b.Cache["y"] = nil
	if a.Equal(b) {
		t.Fatal("map membership ignored for nil values")
	}
	a = richCatalog()
	b = a.Clone()
	*b.Cache["cache"].Lists["n"][0]++
	if a.Equal(b) {
		t.Fatal("excluded field equality ignored")
	}
	// Whole-value methods also work on values invalid for persistence. Excluded
	// fields, invalid UTF-8, nil required pointers and nil map elements are not hooks.
	a = fixture.Catalog{Names: []string{"\xff"}, Entries: map[value.Key][]*value.Detail{"x": {nil}}}
	if !a.Equal(a.Clone()) {
		t.Fatal("invalid-but-supported value clone")
	}
	if fixture.CheckCatalog(a, generatedLimits()) == nil {
		t.Fatal("invalid codec value accepted")
	}
	n := int64(0)
	s := fixture.Sample{}
	other := fixture.Sample{Optional: &n}
	if s.Equal(other) || !other.Equal(other.Clone()) {
		t.Fatal("optional scalar presence")
	}
}

func TestGeneratedOwningRoundtripAndSemantics(t *testing.T) {
	v := richCatalog()
	v.Cache = nil
	data, err := fixture.EncodeCatalog(v, generatedLimits())
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(data)
	v.Payload[0] = 99
	v.Entries["x"][0].Data[0] = 99
	if !bytes.Equal(data, original) {
		t.Fatal("encoded bytes alias DTO")
	}
	got, err := fixture.DecodeCatalog(data, generatedLimits())
	if err != nil {
		t.Fatal(err)
	}
	baseline := got.Clone()
	for i := range data {
		data[i] = 0
	}
	if !got.Equal(baseline) {
		t.Fatal("decoded values borrow input")
	}
	got.Payload[0] = 88
	(*got.Required)[0] = 88
	got.Names[0] = "changed"
	got.Entries["x"][0].Data[0] = 88
	if baseline.Payload[0] != 5 || baseline.Entries["x"][0].Data[0] != 7 {
		t.Fatal("mutation ownership")
	}
	// Mutating an owning decode must also leave its still-live input unchanged.
	live := bytes.Clone(original)
	owned, err := fixture.DecodeCatalog(live, generatedLimits())
	if err != nil {
		t.Fatal(err)
	}
	owned.Payload[0] = 99
	(*owned.Required)[0] = 99
	owned.Entries["x"][0].Data[0] = 99
	if !bytes.Equal(live, original) {
		t.Fatal("decoded mutable storage aliases live input")
	}
	v = richCatalog()
	v.Cache = map[string]*fixture.CacheEntry{"excluded": {Bytes: []byte{1}}}
	data, err = fixture.EncodeCatalog(v, generatedLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err = fixture.DecodeCatalog(data, generatedLimits())
	if err != nil || got.Cache != nil {
		t.Fatal("excluded persistence", got, err)
	}
	// Structural success is deliberately independent from owner semantic rejection.
	structural, err := fixture.DecodeSample(unhex(t, sampleZeroHex), generatedLimits())
	if err != nil {
		t.Fatal(err)
	}
	semanticCalls := 0
	semanticErr := errors.New("owner requires OK")
	semantics := func(s fixture.Sample) error {
		semanticCalls++
		if !s.OK {
			return semanticErr
		}
		return nil
	}
	if !errors.Is(semantics(structural), semanticErr) {
		t.Fatal("owner semantics unexpectedly accepted")
	}
	ownerEncode := func(s fixture.Sample, l codec.Limits) ([]byte, error) {
		if err := fixture.CheckSample(s, l); err != nil {
			return nil, err
		}
		if err := semantics(s); err != nil {
			return nil, err
		}
		return fixture.EncodeSample(s, l)
	}
	before := semanticCalls
	if b, err := ownerEncode(fixture.Sample{Label: strings.Repeat("x", 33)}, generatedLimits()); b != nil || !errors.Is(err, codec.ErrLimitExceeded) || semanticCalls != before {
		t.Fatal("bounded checks must precede owner semantics", err)
	}
	if b, err := ownerEncode(fixture.Sample{}, generatedLimits()); b != nil || !errors.Is(err, semanticErr) || semanticCalls != before+1 {
		t.Fatal("structural success must not imply owner semantic success", err)
	}
	if b, err := ownerEncode(fixture.Sample{OK: true}, generatedLimits()); b == nil || err != nil {
		t.Fatal("explicit owner acceptance", err)
	}
	// Planted Sample.Validate panics if a generated operation invokes it.
	if err := fixture.CheckSample(structural, generatedLimits()); err != nil {
		t.Fatal(err)
	}
	_ = structural.Clone().Equal(structural)
}

func TestGeneratedConcurrentReadOnlyCalls(t *testing.T) {
	l := generatedLimits()
	v := richCatalog()
	v.Cache = nil
	expected, err := fixture.EncodeCatalog(v, l)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 20; j++ {
				if !v.Clone().Equal(v) {
					t.Error("shared-input value methods")
					return
				}
				if err := fixture.CheckCatalog(v, l); err != nil {
					t.Error(err)
					return
				}
				b, err := fixture.EncodeCatalog(v, l)
				if err != nil || !bytes.Equal(b, expected) {
					t.Error("encode", err)
					return
				}
				w, err := fixture.DecodeCatalog(b, l)
				if err != nil || !w.Equal(v) || !w.Clone().Equal(v) {
					t.Error("decode/value methods", err)
					return
				}
			}
		})
	}
	wg.Wait()
}
