package value_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/value"
)

func TestParseJSONRoots(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  value.Value
	}{
		{`null`, value.Null()}, {`true`, value.Bool(true)}, {`false`, value.Bool(false)},
		{`""`, value.String("")}, {`"text"`, value.String("text")},
		{`-9223372036854775808`, value.Int64(math.MinInt64)},
		{`9223372036854775807`, value.Int64(math.MaxInt64)},
		{`18446744073709551615`, value.Uint64(math.MaxUint64)},
		{`9007199254740993`, value.Uint64(9007199254740993)}, {`-0`, value.Uint64(0)},
		{`[]`, value.Array(nil)}, {`{}`, value.Object(nil)},
		{`[null,true,-1,{"a":"x"}]`, value.Array([]value.Value{value.Null(), value.Bool(true), value.Int64(-1), value.Object(value.Map{"a": value.String("x")})})},
	} {
		t.Run(tc.input, func(t *testing.T) {
			data := []byte(" \n" + tc.input + "\t")
			got, err := value.ParseJSON(data, limits())
			if err != nil || !got.Equal(tc.want) {
				t.Fatalf("parse: %v, %v", got, err)
			}
			for i := range data {
				data[i] = 'x'
			}
			if !got.Equal(tc.want) {
				t.Fatal("output aliases input")
			}
			checkJSONCBORRoundtrip(t, got)
		})
	}
	for _, input := range []string{"", `1.0`, `1e0`, `-0.0`, `18446744073709551616`, `-9223372036854775809`, `01`, `+1`, `[0,]`, `[`, `true false`, `null!`, `"x" 1`, `{"a":0,"\u0061":1}`, `{"\ud800":0,"\udc00":1}`} {
		got, err := value.ParseJSON([]byte(input), limits())
		if err == nil || value.Borrow(got).Kind() != value.InvalidKind {
			t.Fatalf("%q published a value: %v", input, err)
		}
	}
}

func TestParseJSONUnicodeMatchesStandardLibrary(t *testing.T) {
	for _, input := range []string{
		`"\ud800"`, `"\udfff"`, `"\ud800\ud800"`, `"\udc00\ud800"`, `"\ud800x"`, `"\ud800\\udc00"`,
		`"\ud83d\ude00"`, `"�\ufffd"`, "\"\xff\xc0\x80\xed\xa0\x80\"",
	} {
		var want string
		if err := json.Unmarshal([]byte(input), &want); err != nil {
			t.Fatal(err)
		}
		got, err := value.ParseJSON([]byte(input), limits())
		if err != nil || !got.Equal(value.String(want)) {
			t.Fatalf("%q: stdlib %q, parse error %v", input, want, err)
		}
		// Replacement applies equally to object keys and values.
		object, err := value.ParseJSONObject([]byte(`{`+input+`:`+input+`}`), limits())
		if err != nil || !object.Equal(value.Map{want: value.String(want)}) {
			t.Fatalf("object %q: %v", input, err)
		}
	}
	l := limits()
	l.MaxStringBytes = 2 // A one-byte invalid sequence expands to three bytes.
	got, err := value.ParseJSON([]byte("\"\xff\""), l)
	if !errors.Is(err, codec.ErrLimitExceeded) || value.Borrow(got).Kind() != value.InvalidKind {
		t.Fatalf("decoded replacement size not accounted: %v", err)
	}
}

func TestParseJSONRootLimits(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		set         func(*codec.Limits, int)
		exact       int
	}{
		{"scalar header", `null`, func(l *codec.Limits, n int) { l.MaxBytes = n }, 9},
		{"string bytes", `"ab"`, func(l *codec.Limits, n int) { l.MaxBytes = n }, 11},
		{"string length", `"ab"`, func(l *codec.Limits, n int) { l.MaxStringBytes = n }, 2},
		{"array count", `[0,0]`, func(l *codec.Limits, n int) { l.MaxArrayElements = n }, 2},
		{"map count", `{"a":0,"b":0}`, func(l *codec.Limits, n int) { l.MaxMapPairs = n }, 2},
		{"nodes", `[0,0]`, func(l *codec.Limits, n int) { l.MaxNodes = n }, 3},
		{"elements", `[0,0]`, func(l *codec.Limits, n int) { l.MaxCollectionElements = n }, 2},
		{"depth", `[[0]]`, func(l *codec.Limits, n int) { l.MaxDepth = n }, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := limits()
			tc.set(&l, tc.exact)
			got, err := value.ParseJSON([]byte(tc.input), l)
			if err != nil {
				t.Fatal(err)
			}
			if err := codec.Check(got, l); err != nil {
				t.Fatal(err)
			}
			tc.set(&l, tc.exact-1)
			got, err = value.ParseJSON([]byte(tc.input), l)
			if !errors.Is(err, codec.ErrLimitExceeded) || value.Borrow(got).Kind() != value.InvalidKind {
				t.Fatalf("over limit: %v", err)
			}
		})
	}
	l := limits()
	l.MaxNodes, l.MaxBytes = 0, 1
	if _, err := value.ParseJSON([]byte("\xff\xff"), l); !errors.Is(err, codec.ErrInvalidLimits) {
		t.Fatalf("policy first: %v", err)
	}
	l.MaxNodes = 1
	if _, err := value.ParseJSON([]byte("\xff\xff"), l); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("size first: %v", err)
	}
	for _, tc := range []struct {
		input string
		set   func(*codec.Limits)
	}{
		{`[0,!]`, func(l *codec.Limits) { l.MaxArrayElements = 1 }},
		{`[0,!]`, func(l *codec.Limits) { l.MaxNodes = 2 }},
		{`[0,!]`, func(l *codec.Limits) { l.MaxCollectionElements = 1 }},
		{`[0,!]`, func(l *codec.Limits) { l.MaxBytes = 26 }},
		{`[[!]]`, func(l *codec.Limits) { l.MaxDepth = 1 }},
	} {
		l := limits()
		tc.set(&l)
		got, err := value.ParseJSON([]byte(tc.input), l)
		if !errors.Is(err, codec.ErrLimitExceeded) || value.Borrow(got).Kind() != value.InvalidKind {
			t.Fatalf("poison child %s: %v", tc.input, err)
		}
	}
}

func TestParseJSONLocalDepth(t *testing.T) {
	for _, leaf := range []string{`0`, `[]`, `{}`} {
		for _, depth := range []int{128, 129} {
			input := strings.Repeat(`[`, depth) + leaf + strings.Repeat(`]`, depth)
			got, err := value.ParseJSON([]byte(input), limits())
			if depth == 129 {
				if !errors.Is(err, value.ErrDepthExceeded) || value.Borrow(got).Kind() != value.InvalidKind {
					t.Fatalf("depth 129: %v", err)
				}
			} else if err != nil {
				t.Fatalf("depth 128: %v", err)
			} else {
				checkJSONCBORRoundtrip(t, got)
			}
		}
	}
	// The local ceiling rejects before decoding a poison child at depth 129.
	input := strings.Repeat(`[`, 129) + `!` + strings.Repeat(`]`, 129)
	if _, err := value.ParseJSON([]byte(input), limits()); !errors.Is(err, value.ErrDepthExceeded) {
		t.Fatalf("local depth poison: %v", err)
	}
}

// checkMarshalForms proves value-receiver dispatch for pointers, nonaddressable
// map elements and enclosing structs, not just direct calls to MarshalJSON.
func checkMarshalForms[T json.Marshaler](t *testing.T, v T, want string) {
	t.Helper()
	for _, tc := range []struct {
		input any
		want  string
	}{
		{v, want}, {&v, want},
		{map[string]T{"field": v}, `{"field":` + want + `}`},
		{struct{ Field T }{v}, `{"Field":` + want + `}`},
		{&struct{ Field T }{v}, `{"Field":` + want + `}`},
	} {
		got, err := json.Marshal(tc.input)
		if err != nil || string(got) != tc.want {
			t.Fatalf("%T: got %s, want %s: %v", tc.input, got, tc.want, err)
		}
	}
	got, err := v.MarshalJSON()
	if err != nil || string(got) != want {
		t.Fatalf("direct: %s, %v", got, err)
	}
}

func TestMarshalJSONNative(t *testing.T) {
	for _, tc := range []struct {
		v    value.Value
		want string
	}{
		{value.Null(), `null`}, {value.Bool(true), `true`}, {value.Bool(false), `false`},
		{value.String(""), `""`},
		{value.String("\"\\\b\f\n\r\t\x00<>&\u2028\u2029😀"), `"\"\\\b\f\n\r\t\u0000\u003c\u003e\u0026\u2028\u2029😀"`},
		{value.Int64(math.MinInt64), `-9223372036854775808`}, {value.Int64(-1), `-1`},
		{value.Int64(0), `0`}, {value.Int64(math.MaxInt64), `9223372036854775807`},
		{value.Uint64(math.MaxUint64), `18446744073709551615`}, {value.Uint64(9007199254740993), `9007199254740993`},
		{value.Array(nil), `[]`}, {value.Array([]value.Value{}), `[]`},
		{value.Object(nil), `{}`}, {value.Object(value.Map{}), `{}`},
		{value.Array([]value.Value{value.Null(), value.Bool(true), value.Object(value.Map{"a": value.Array(nil)})}), `[null,true,{"a":[]}]`},
	} {
		t.Run(tc.want, func(t *testing.T) {
			checkMarshalForms(t, tc.v, tc.want)
			checkMarshalForms(t, value.Borrow(tc.v), tc.want)
			checkJSONCBORRoundtrip(t, tc.v)
		})
	}
	m := value.Map{"z": value.Null(), "aa": value.String("<"), "a": value.Bool(true), "\"\n<": value.Uint64(1)}
	before := m.Clone()
	want := `{"\"\n\u003c":1,"a":true,"aa":"\u003c","z":null}`
	checkMarshalForms(t, m, want)
	checkMarshalForms(t, value.BorrowMap(m), want)
	checkMarshalForms(t, value.Object(m), want)
	checkMarshalForms(t, value.Borrow(value.Object(m)), want)
	if !m.Equal(before) {
		t.Fatal("marshal mutated backing")
	}
	checkMarshalForms(t, value.Map(nil), `{}`)
	checkMarshalForms(t, value.Map{}, `{}`)
	checkMarshalForms(t, value.MapView{}, `{}`)
	checkMarshalForms(t, value.BorrowMap(value.Map{}), `{}`)
	// Repeated shared subtrees are legal and encoded per occurrence.
	shared := value.Object(m)
	v := value.Array([]value.Value{shared, shared})
	checkMarshalForms(t, v, `[`+want+`,`+want+`]`)
	// Encoding is not admission: no MaxBytes, MaxNodes or string policy applies.
	large := value.String(strings.Repeat("x", limits().MaxBytes+1))
	if out, err := large.MarshalJSON(); err != nil || len(out) != limits().MaxBytes+3 {
		t.Fatalf("operational byte limit applied: %v", err)
	}
}

func TestMarshalJSONWithoutAdmissionBudgets(t *testing.T) {
	// This tree exceeds node, collection-element and immediate-array policies,
	// but remains shallow and valid for conventional JSON marshaling.
	items := make([]value.Value, limits().MaxNodes+1)
	for i := range items {
		items[i] = value.Null()
	}
	m := value.Map{"items": value.Array(items)}
	if err := codec.Check(m, limits()); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("negative control did not exceed admission limits: %v", err)
	}
	want := `{"items":[` + strings.Repeat("null,", len(items)-1) + `null]}`
	checkMarshalForms(t, m, want)
	checkMarshalForms(t, value.BorrowMap(m), want)
	checkMarshalForms(t, value.Object(m), want)
	checkMarshalForms(t, value.Borrow(value.Object(m)), want)
}

func TestMarshalJSONOutputOwnership(t *testing.T) {
	m := value.Map{"items": value.Array([]value.Value{value.String("original")})}
	before := m.Clone()
	accepted, err := m.Admit(limits())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"items":["original"]}`
	for _, input := range []json.Marshaler{m, value.Object(m), value.BorrowMap(m), value.Borrow(value.Object(m)), accepted} {
		first, err := input.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		second, err := input.MarshalJSON()
		if err != nil || string(first) != want || string(second) != want {
			t.Fatalf("retained output changed: %q, %q: %v", first, second, err)
		}
		for i := range first {
			first[i] = 'x'
		}
		if string(second) != want || !m.Equal(before) {
			t.Fatal("output mutation escaped its buffer")
		}
		checkMarshalForms(t, input, want)
		checkMarshalForms(t, accepted, want)
	}
}

func checkMarshalError(t *testing.T, v json.Marshaler, want error) {
	t.Helper()
	if out, err := v.MarshalJSON(); out != nil || !errors.Is(err, want) {
		t.Fatalf("%T: direct output %q, error %v; want %v", v, out, err, want)
	}
	if out, err := json.Marshal(v); out != nil || !errors.Is(err, want) {
		t.Fatalf("%T: wrapped output %q, error %v; want %v", v, out, err, want)
	}
}

func TestMarshalJSONInvalid(t *testing.T) {
	checkMarshalError(t, value.Value{}, codec.ErrInvalidData)
	checkMarshalError(t, value.View{}, codec.ErrInvalidData)
	for _, v := range []value.Value{value.String("\xff"), value.Array([]value.Value{{}}), value.Object(value.Map{"ok": {}}), value.Object(value.Map{"\xff": value.Null()})} {
		checkMarshalError(t, v, codec.ErrInvalidData)
		checkMarshalError(t, value.Borrow(v), codec.ErrInvalidData)
	}
	for _, m := range []value.Map{{"ok": {}}, {"\xff": value.Null()}} {
		checkMarshalError(t, m, codec.ErrInvalidData)
		checkMarshalError(t, value.BorrowMap(m), codec.ErrInvalidData)
	}
}

func TestMarshalJSONDepthAndCycles(t *testing.T) {
	for _, leaf := range []value.Value{value.Null(), value.Bool(true), value.String("leaf"), value.Int64(math.MinInt64), value.Uint64(math.MaxUint64), value.Array(nil), value.Object(nil)} {
		v := leaf
		var rootMap value.Map
		// Alternate arrays and maps so no public method may restart depth.
		for i := range 128 {
			if i%2 == 0 {
				v = value.Array([]value.Value{v})
			} else {
				rootMap = value.Map{"child": v}
				v = value.Object(rootMap)
			}
		}
		checkJSONCBORRoundtrip(t, v)
		out, err := v.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		checkMarshalForms(t, v, string(out))
		checkMarshalForms(t, value.Borrow(v), string(out))
		m := value.Map{"child": v} // Adds a 129th descendant edge.
		checkMarshalError(t, value.Array([]value.Value{v}), value.ErrDepthExceeded)
		checkMarshalError(t, value.Object(m), value.ErrDepthExceeded)
		checkMarshalError(t, value.Borrow(value.Object(m)), value.ErrDepthExceeded)
		checkMarshalError(t, m, value.ErrDepthExceeded)
		checkMarshalError(t, value.BorrowMap(m), value.ErrDepthExceeded)
		// The root at depth 128 is a map: all four receivers share the boundary.
		checkMarshalForms(t, rootMap, string(out))
		checkMarshalForms(t, value.BorrowMap(rootMap), string(out))
	}
	m := value.Map{}
	m["cycle"] = value.Object(m)
	checkMarshalError(t, m, value.ErrDepthExceeded)
	checkMarshalError(t, value.BorrowMap(m), value.ErrDepthExceeded)
	checkMarshalError(t, value.Object(m), value.ErrDepthExceeded)
	checkMarshalError(t, value.Borrow(value.Object(m)), value.ErrDepthExceeded)
	a := make([]value.Value, 1)
	a[0] = value.Array(a)
	checkMarshalError(t, value.Array(a), value.ErrDepthExceeded)
	checkMarshalError(t, value.Borrow(value.Array(a)), value.ErrDepthExceeded)
	mixed := value.Map{"a": value.Array(a)}
	a[0] = value.Object(mixed)
	checkMarshalError(t, mixed, value.ErrDepthExceeded)
}

func checkJSONCBORRoundtrip(t *testing.T, v value.Value) {
	t.Helper()
	out, err := v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := value.ParseJSON(out, limits())
	if err != nil || !parsed.Equal(v) {
		t.Fatalf("JSON roundtrip: %s: %v", out, err)
	}
	encoded, err := codec.Encode(parsed, limits())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode[value.Value](encoded, limits())
	if err != nil || !decoded.Equal(v) {
		t.Fatalf("CBOR roundtrip: %v", err)
	}
	again, err := decoded.MarshalJSON()
	if err != nil || !bytes.Equal(out, again) {
		t.Fatalf("unstable JSON roundtrip: %s: %v", again, err)
	}
}

func FuzzJSONRoundtrip(f *testing.F) {
	for _, seed := range []string{`null`, `true`, `"\ud800"`, `"\udc00"`, "\"\xff\"", `-0`, `-9223372036854775808`, `18446744073709551615`, `[]`, `{}`, `[null,true,1,-1,"x",{"a":[]}]`, `{"a":0,"\u0061":1}`, `1e0`, strings.Repeat(`[`, 129) + `0` + strings.Repeat(`]`, 129)} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		l := limits()
		l.MaxBytes = 4096
		got, err := value.ParseJSON(data, l)
		if err != nil {
			if value.Borrow(got).Kind() != value.InvalidKind {
				t.Fatal("partial publication")
			}
		} else {
			checkJSONCBORRoundtrip(t, got)
		}
		// Native strings reject malformed UTF-8 rather than replacing it. These
		// paths also exercise marshal errors independently of parser success.
		if len(data) > l.MaxBytes {
			return
		}
		s := string(data)
		for _, v := range []json.Marshaler{value.String(s), value.Borrow(value.String(s)), value.Map{s: value.Null()}, value.BorrowMap(value.Map{s: value.Null()})} {
			out, err := v.MarshalJSON()
			if utf8.Valid(data) {
				if err != nil || !json.Valid(out) {
					t.Fatalf("valid native text: %q, %v", out, err)
				}
			} else if out != nil || !errors.Is(err, codec.ErrInvalidData) {
				t.Fatalf("invalid native text: %q, %v", out, err)
			}
		}
	})
}
