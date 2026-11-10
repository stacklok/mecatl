package value_test

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/value"
)

func TestParseJSONObjectCanonical(t *testing.T) {
	data := []byte(`{"min":-9223372036854775808,"maxsigned":9223372036854775807,"max":18446744073709551615,"exact":9007199254740993,"zero":-0,"array":[null,true,false,"",[],{},-1],"nested":{"😀":"\ud83d\ude00","replacement":"�\ufffd","escaped":"\\uD800\""}}`)
	want := value.Map{
		"min": value.Int64(math.MinInt64), "maxsigned": value.Int64(math.MaxInt64),
		"max": value.Uint64(math.MaxUint64), "exact": value.Uint64(9007199254740993), "zero": value.Uint64(0),
		"array":  value.Array([]value.Value{value.Null(), value.Bool(true), value.Bool(false), value.String(""), value.Array(nil), value.Object(nil), value.Int64(-1)}),
		"nested": value.Object(value.Map{"😀": value.String("😀"), "replacement": value.String("��"), "escaped": value.String(`\uD800"`)}),
	}
	got, err := value.ParseJSONObject(data, limits())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatal("JSON/native semantic mismatch")
	}
	encoded, err := codec.Encode(got, limits())
	if err != nil {
		t.Fatal(err)
	}
	native, err := codec.Encode(want, limits())
	if err != nil || !bytes.Equal(encoded, native) {
		t.Fatalf("JSON/native canonical mismatch: %x != %x: %v", encoded, native, err)
	}
	view, err := got.Admit(limits())
	if err != nil {
		t.Fatal(err)
	}
	for i := range data {
		data[i] = 'x'
	}
	if !got.Equal(want) {
		t.Fatal("output aliases JSON input")
	}
	got["exact"] = value.Null()
	clear(got)
	x, ok := view.Get("exact")
	if n, valid := x.Uint64(); !ok || !valid || n != 9007199254740993 {
		t.Fatal("admitted view aliases authoring storage")
	}
	for _, input := range []string{`{}`, " \n{}\t"} {
		got, err := value.ParseJSONObject([]byte(input), limits())
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("empty object: %v, %v", got, err)
		}
	}
}

func TestParseJSONObjectDecodedKeyMembership(t *testing.T) {
	for _, input := range []string{
		`{"\ud83d\ude00":1,"\uD83D\uDE01":2,"�":3,"\ufffdx":4}`,
		`{"😀":1,"😁":2,"\uFFFD":3,"�x":4}`,
	} {
		got, err := value.ParseJSONObject([]byte(input), limits())
		if err != nil {
			t.Fatal(err)
		}
		want := value.Map{"😀": value.Uint64(1), "😁": value.Uint64(2), "�": value.Uint64(3), "�x": value.Uint64(4)}
		if !got.Equal(want) {
			t.Fatalf("decoded membership differs: %v", got)
		}
	}
}

func TestParseJSONObjectRejects(t *testing.T) {
	for _, input := range []string{
		``, `null`, `[]`, `1`, `"object"`, `true`, `{`, `{"a":`, `{"a":0,}`, `{"a" 0}`, `{"a":01}`, `{"a":+1}`,
		`{} {}`, `{} null`, `{}x`, `{"a":0} trailing`, `{"a":NaN}`, `{"a":1.0}`, `{"a":1e3}`, `{"a":1E0}`, `{"a":-0.0}`,
		`{"a":18446744073709551616}`, `{"a":-9223372036854775809}`,
		`{"a":0,"a":1}`, `{"a":0,"\u0061":1}`, `{"x":[{"a":0,"\u0061":1}]}`, `{"x":{"a":{"b":0,"b":1}}}`,
		`{"😀":0,"\ud83d\ude00":1}`, `{"\ud800":0,"�":1}`, `{"\ud800":0,"\udfff":1}`, "{\"\xff\":0,\"�\":1}",
		`{"a":"\u123"}`, `{"a":"\uZZZZ"}`, `{"a":"\x00"}`, "{\"a\":\"\n\"}",
	} {
		t.Run(input, func(t *testing.T) {
			got, err := value.ParseJSONObject([]byte(input), limits())
			if err == nil || got != nil {
				t.Fatalf("published %v, error %v", got, err)
			}
		})
	}
}

func TestParseJSONObjectLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		set   func(*codec.Limits, int)
		exact int
	}{
		{"string", `{"a":"\u0061\u0062"}`, func(l *codec.Limits, n int) { l.MaxStringBytes = n }, 2},
		{"key", `{"\u0061\u0062":0}`, func(l *codec.Limits, n int) { l.MaxStringBytes = n }, 2},
		{"array", `{"a":[0,0]}`, func(l *codec.Limits, n int) { l.MaxArrayElements = n }, 2},
		{"map", `{"a":{"b":0,"c":0}}`, func(l *codec.Limits, n int) { l.MaxMapPairs = n }, 2},
		{"depth", `{"a":[0]}`, func(l *codec.Limits, n int) { l.MaxDepth = n }, 2},
		{"nodes", `{"a":[0,0]}`, func(l *codec.Limits, n int) { l.MaxNodes = n }, 5},
		{"elements", `{"a":[0,0]}`, func(l *codec.Limits, n int) { l.MaxCollectionElements = n }, 3},
		{"size", `{"a":[0,0]}`, func(l *codec.Limits, n int) { l.MaxBytes = n }, 46},
		{"empty header", `{}`, func(l *codec.Limits, n int) { l.MaxBytes = n }, 9},
		{"whole input", strings.Repeat(" ", 50) + `{}`, func(l *codec.Limits, n int) { l.MaxBytes = n }, 52},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := limits()
			tc.set(&l, tc.exact)
			got, err := value.ParseJSONObject([]byte(tc.input), l)
			if err != nil {
				t.Fatalf("exact limit: %v", err)
			}
			if err := codec.Check(got, l); err != nil {
				t.Fatalf("native accounting differs: %v", err)
			}
			tc.set(&l, tc.exact-1)
			got, err = value.ParseJSONObject([]byte(tc.input), l)
			if !errors.Is(err, codec.ErrLimitExceeded) || got != nil {
				t.Fatalf("over limit: %v, %v", got, err)
			}
		})
	}
	// Invalid policy wins over byte rejection; byte rejection wins over malformed
	// Unicode/JSON (the decoder must not be constructed for oversized inputs).
	l := limits()
	l.MaxNodes = 0
	l.MaxBytes = 1
	if got, err := value.ParseJSONObject([]byte("\xff\xff"), l); got != nil || !errors.Is(err, codec.ErrInvalidLimits) {
		t.Fatalf("policy-first: %v, %v", got, err)
	}
	l.MaxNodes = 1
	if got, err := value.ParseJSONObject([]byte("\xff\xff"), l); got != nil || !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("size-first: %v, %v", got, err)
	}
	// Machine-sized bounds must not overflow incremental accounting.
	l = codec.Limits{MaxBytes: math.MaxInt, MaxStringBytes: math.MaxInt, MaxByteStringBytes: math.MaxInt, MaxArrayElements: math.MaxInt, MaxMapPairs: math.MaxInt, MaxDepth: math.MaxInt, MaxNodes: math.MaxInt, MaxCollectionElements: math.MaxInt}
	if _, err := value.ParseJSONObject([]byte(`{"a":[0,{"b":true}]}`), l); err != nil {
		t.Fatal(err)
	}
}

func TestParseJSONObjectLocalDepth(t *testing.T) {
	for _, leaf := range []string{`0`, `[]`, `{}`} {
		for _, depth := range []int{128, 129} {
			input := `{"a":` + strings.Repeat(`[`, depth-1) + leaf + strings.Repeat(`]`, depth-1) + `}`
			got, err := value.ParseJSONObject([]byte(input), limits())
			if depth == 129 {
				if got != nil || !errors.Is(err, value.ErrDepthExceeded) {
					t.Fatalf("depth 129: %v, %v", got, err)
				}
				continue
			}
			if err != nil || !got.Equal(got.Clone()) {
				t.Fatalf("depth 128: %v", err)
			}
			if _, err := got.Admit(limits()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Invalid children prove that incremental limits reject before reading the next
// child, rather than relying only on the final whole-value codec.Check.
func TestParseJSONObjectIncrementalRejection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		set   func(*codec.Limits)
	}{
		{"array", `{"a":[0,!]}`, func(l *codec.Limits) { l.MaxArrayElements = 1 }},
		{"map", `{"a":0,"b":!}`, func(l *codec.Limits) { l.MaxMapPairs = 1 }},
		{"nodes", `{"a":[0,!]}`, func(l *codec.Limits) { l.MaxNodes = 4 }},
		{"elements", `{"a":[0,!]}`, func(l *codec.Limits) { l.MaxCollectionElements = 2 }},
		{"headers", `{"a":[0,!]}`, func(l *codec.Limits) { l.MaxBytes = 45 }},
		{"key bytes", `{"aa":!}`, func(l *codec.Limits) { l.MaxStringBytes = 1 }},
		{"depth", `{"a":[!]}`, func(l *codec.Limits) { l.MaxDepth = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := limits()
			tc.set(&l)
			got, err := value.ParseJSONObject([]byte(tc.input), l)
			if got != nil || !errors.Is(err, codec.ErrLimitExceeded) {
				t.Fatalf("incremental rejection: %v, %v", got, err)
			}
		})
	}
}

func FuzzParseJSONObject(f *testing.F) {
	for _, seed := range []string{`{}`, `{"a":[1,-1,null,"x",{"b":true}]}`, `{"n":18446744073709551615}`, `{"x":"\ud800"}`, `{"a":0,"\u0061":1}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := value.ParseJSONObject(data, limits())
		if err != nil {
			if got != nil {
				t.Fatal("partial publication")
			}
			return
		}
		encoded, err := codec.Encode(got, limits())
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := codec.Decode[value.Map](encoded, limits())
		if err != nil || !decoded.Equal(got) {
			t.Fatalf("roundtrip: %v", err)
		}
		reencoded, err := codec.Encode(decoded, limits())
		if err != nil || !bytes.Equal(encoded, reencoded) {
			t.Fatalf("unstable encoding: %v", err)
		}
	})
}

func BenchmarkParseJSONObject(b *testing.B) {
	data := []byte(`{"path":"README.md","limit":9007199254740993,"options":{"enabled":true,"extra":null},"items":[1,2,3]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := value.ParseJSONObject(data, limits()); err != nil {
			b.Fatal(err)
		}
	}
}
