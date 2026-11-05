package codec_test

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
)

// Independent framing and fixed CIDv1/DAG-CBOR/SHA-256 profile with a zero digest.
// The digest is opaque: a syntactically valid link need not identify known data.
const generatedCIDHex = "d82a582500017112200000000000000000000000000000000000000000000000000000000000000000"
const generatedLinkHex = "a16176" + generatedCIDHex
const generatedLinksHex = "a3616240616ea06172" + generatedCIDHex
const generatedNestedLinksHex = "a4616240616ea1617882" + generatedCIDHex + generatedCIDHex + "616f" + generatedCIDHex + "6172" + generatedCIDHex

func generatedCID(t *testing.T) contentid.CID {
	t.Helper()
	v, err := contentid.ParseBinary(unhex(t, generatedCIDHex)[5:])
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestGeneratedCIDGoldenAndOwnership(t *testing.T) {
	c := generatedCID(t)
	l := generatedLimits()
	v := fixture.Link{Value: c}
	want := unhex(t, generatedLinkHex)
	if err := codec.Check[fixture.Link](v, l); err != nil {
		t.Fatal(err)
	}
	got, err := codec.Encode[fixture.Link](v, l)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("golden %x, %v; want %x", got, err, want)
	}
	identity := contentid.Sum(want)
	decoded, err := codec.Decode[fixture.Link](want, l)
	if err != nil || !v.Equal(decoded) || contentid.Verify(got, identity) != nil {
		t.Fatal("exact byte identity/decoding", err)
	}
	reencoded, err := codec.Encode[fixture.Link](decoded, l)
	if err != nil || !bytes.Equal(reencoded, want) {
		t.Fatal("reencoding changed canonical input", err)
	}
	clear(want)
	if decoded.Value != c || !bytes.Equal(got, reencoded) {
		t.Fatal("decoded CID or encoded output borrows input")
	}

	for _, tt := range []struct {
		v    fixture.Links
		wire string
	}{
		{fixture.Links{Required: &c}, generatedLinksHex},
		{fixture.Links{Required: &c, Optional: &c, Nested: map[string][]*fixture.CIDAlias{"x": {&c, &c}}}, generatedNestedLinksHex},
		// Link-like bytes remain an opaque byte string, not a schema link.
		{fixture.Links{Required: &c, Opaque: unhex(t, generatedCIDHex)}, "a361625829" + generatedCIDHex + "616ea06172" + generatedCIDHex},
	} {
		b, err := codec.Encode[fixture.Links](tt.v, l)
		if err != nil || !bytes.Equal(b, unhex(t, tt.wire)) || codec.Check[fixture.Links](tt.v, l) != nil {
			t.Fatalf("container golden %x, %v; want %s", b, err, tt.wire)
		}
		w, err := codec.Decode[fixture.Links](b, l)
		if err != nil || !w.Equal(tt.v) || w.Nested == nil || w.Opaque == nil {
			t.Fatal("container roundtrip/normalization", err)
		}
	}
}

func TestGeneratedCIDPresenceCloneAndExcluded(t *testing.T) {
	c, z := generatedCID(t), contentid.CID{}
	l := generatedLimits()
	if err := codec.Check[fixture.Link](fixture.Link{}, l); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal("required zero", err)
	}
	if b, err := codec.Encode[fixture.Link](fixture.Link{}, l); b != nil || !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal("required zero encode", err)
	}
	for _, v := range []fixture.Links{
		{}, {Required: &z}, {Required: &c, Optional: &z},
		{Required: &c, Nested: map[string][]*fixture.CIDAlias{"x": {nil}}},
		{Required: &c, Nested: map[string][]*fixture.CIDAlias{"x": {&z}}},
	} {
		if err := codec.Check[fixture.Links](v, l); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatal("invalid presence accepted by Check", err)
		}
		if b, err := codec.Encode[fixture.Links](v, l); b != nil || !errors.Is(err, codec.ErrInvalidData) {
			t.Fatal("invalid presence accepted by Encode", err)
		}
		if !v.Equal(v.Clone()) {
			t.Fatal("Clone/Equal validated invalid values")
		}
	}
	v := fixture.Links{Required: &c, Optional: &c, Nested: map[string][]*fixture.CIDAlias{"x": {&c}}, Excluded: c, Cache: &z}
	w := v.Clone()
	if !v.Equal(w) || w.Required == v.Required || w.Optional == v.Optional || w.Cache == v.Cache || w.Nested["x"][0] == v.Nested["x"][0] {
		t.Fatal("whole-value pointer clone")
	}
	*w.Optional, *w.Cache, *w.Nested["x"][0] = z, c, z
	if *v.Optional != c || *v.Cache != z || *v.Nested["x"][0] != c || v.Equal(w) {
		t.Fatal("clone aliases original or Equal loses state")
	}
	w = v.Clone()
	w.Excluded = z
	if v.Equal(w) {
		t.Fatal("Equal ignored excluded CID")
	}
	w = v.Clone()
	w.Cache = nil
	if v.Equal(w) {
		t.Fatal("Equal ignored excluded present zero pointer")
	}
	wire, err := codec.Encode[fixture.Links](v, l)
	if err != nil {
		t.Fatal(err)
	}
	w, err = codec.Decode[fixture.Links](wire, l)
	if err != nil || w.Excluded != z || w.Cache != nil {
		t.Fatal("excluded CID persisted", err)
	}
	v.Excluded, v.Cache = z, nil
	if !v.Equal(w) {
		t.Fatal("persisted fields changed")
	}
	var zero fixture.Links
	if !zero.Equal(zero.Clone()) || zero.Clone().Optional != nil {
		t.Fatal("zero-value clone/equal")
	}
}

func TestGeneratedCIDMalformedInputs(t *testing.T) {
	link := unhex(t, generatedCIDHex)
	cases := map[string][]byte{
		"wrong tag":                append([]byte{0xd8, 41}, link[2:]...),
		"nonminimal tag16":         append([]byte{0xd9, 0, 42}, link[2:]...),
		"nonminimal tag32":         append([]byte{0xda, 0, 0, 0, 42}, link[2:]...),
		"nonminimal tag64":         append([]byte{0xdb, 0, 0, 0, 0, 0, 0, 0, 42}, link[2:]...),
		"stacked tag":              append([]byte{0xd8, 42}, link...),
		"nonminimal bytes16":       append([]byte{0xd8, 42, 0x59, 0, 37}, link[4:]...),
		"nonminimal bytes32":       append([]byte{0xd8, 42, 0x5a, 0, 0, 0, 37}, link[4:]...),
		"nonminimal bytes64":       append([]byte{0xd8, 42, 0x5b, 0, 0, 0, 0, 0, 0, 0, 37}, link[4:]...),
		"wrong prefix":             append([]byte{0xd8, 42, 0x58, 37, 1}, link[5:]...),
		"short length":             append([]byte{0xd8, 42, 0x58, 36}, link[4:]...),
		"long length":              append(append([]byte{0xd8, 42, 0x58, 38}, link[4:]...), 0),
		"huge length":              {0xd8, 42, 0x5b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"indefinite bytes":         append(append([]byte{0xd8, 42, 0x5f, 0x58, 37}, link[4:]...), 0xff),
		"nonminimal binary varint": append([]byte{0xd8, 42, 0x58, 38, 0, 0x81, 0}, link[6:]...),
		"text":                     append([]byte{0xd8, 42, 0x78, 37}, link[4:]...),
		"raw bytes":                link[2:], "null": {0xf6}, "integer": {0}, "array": {0x80}, "map": {0xa0}, "bool": {0xf4},
		"tagged null": {0xd8, 42, 0xf6}, "float": {0xf9, 0, 0},
	}
	for i, name := range []string{"version", "codec", "hash", "digest length"} {
		bad := bytes.Clone(link)
		bad[5+i]++
		cases[name] = bad
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			data := append(unhex(t, "a16176"), bad...)
			v, err := codec.Decode[fixture.Link](data, generatedLimits())
			if !errors.Is(err, codec.ErrInvalidData) || v != (fixture.Link{}) {
				t.Fatalf("malformed link accepted or partial output: %v", err)
			}
		})
	}
	wire := unhex(t, generatedLinkHex)
	for end := range len(wire) {
		v, err := codec.Decode[fixture.Link](wire[:end], generatedLimits())
		if !errors.Is(err, codec.ErrInvalidData) || v != (fixture.Link{}) {
			t.Fatalf("truncated prefix %d: %v", end, err)
		}
	}
	if v, err := codec.Decode[fixture.Link](append(bytes.Clone(wire), 0), generatedLimits()); !errors.Is(err, codec.ErrInvalidData) || v != (fixture.Link{}) {
		t.Fatal("trailing bytes", err)
	}
	for _, wire := range []string{
		"a4616240616ea0616ff66172" + generatedCIDHex,                // present null optional
		"a3616240616ea06172f6",                                      // required null pointer
		"a3616240616ea1617881f66172" + generatedCIDHex,              // null element
		"a36162" + generatedCIDHex + "616ea06172" + generatedCIDHex, // tag at opaque bytes
		"a3616240616e" + generatedCIDHex + "6172" + generatedCIDHex, // tag at map
	} {
		v, err := codec.Decode[fixture.Links](unhex(t, wire), generatedLimits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(v, fixture.Links{}) {
			t.Fatal("null/non-CID tag accepted or partial output", err)
		}
	}
}

func TestGeneratedCIDPolicyParity(t *testing.T) {
	c := generatedCID(t)
	v := fixture.Link{Value: c}
	wire := unhex(t, generatedLinkHex)
	// One map, one one-byte key, and the two-node link: 4*9+1+37=74.
	// Only the enclosing map pair charges a collection declaration.
	boundary := codec.Limits{MaxBytes: 74, MaxStringBytes: 1, MaxByteStringBytes: 37, MaxArrayElements: 1, MaxMapPairs: 1, MaxDepth: 2, MaxNodes: 4, MaxCollectionElements: 1}
	for _, tt := range []struct {
		name string
		edit func(*codec.Limits)
		want error
	}{
		{"exact", func(*codec.Limits) {}, nil},
		{"size", func(l *codec.Limits) { l.MaxBytes-- }, codec.ErrLimitExceeded},
		{"nodes", func(l *codec.Limits) { l.MaxNodes-- }, codec.ErrLimitExceeded},
		{"depth", func(l *codec.Limits) { l.MaxDepth-- }, codec.ErrLimitExceeded},
		{"byte cap includes prefix", func(l *codec.Limits) { l.MaxByteStringBytes-- }, codec.ErrLimitExceeded},
		{"invalid limits", func(l *codec.Limits) { l.MaxDepth = 0 }, codec.ErrInvalidLimits},
		{"extreme positive", func(l *codec.Limits) {
			*l = codec.Limits{MaxBytes: math.MaxInt, MaxStringBytes: math.MaxInt, MaxByteStringBytes: math.MaxInt, MaxArrayElements: math.MaxInt, MaxMapPairs: math.MaxInt, MaxDepth: math.MaxInt, MaxNodes: math.MaxInt, MaxCollectionElements: math.MaxInt}
		}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := boundary
			tt.edit(&l)
			check := codec.Check[fixture.Link](v, l)
			b, enc := codec.Encode[fixture.Link](v, l)
			w, dec := codec.Decode[fixture.Link](wire, l)
			for _, err := range []error{check, enc, dec} {
				if !errors.Is(err, tt.want) {
					t.Fatalf("policy disagreement: check=%v encode=%v decode=%v", check, enc, dec)
				}
			}
			if tt.want != nil && (b != nil || w != (fixture.Link{})) {
				t.Fatal("failure output is not zero")
			}
			if tt.want == nil && (!bytes.Equal(b, wire) || !v.Equal(w)) {
				t.Fatal("boundary changed value/bytes")
			}
			if tt.want == codec.ErrInvalidLimits {
				if _, err := codec.Decode[fixture.Link](nil, l); !errors.Is(err, codec.ErrInvalidLimits) {
					t.Fatal("policy not checked before input", err)
				}
			}
		})
	}

	// Four repeated links in a map, nested array and pointers: 17 nodes,
	// 5 one-byte keys, 4*37 content bytes, 7 collection declarations, depth 4.
	nested := fixture.Links{Required: &c, Optional: &c, Nested: map[string][]*fixture.CIDAlias{"x": {&c, &c}}}
	l := codec.Limits{MaxBytes: 306, MaxStringBytes: 1, MaxByteStringBytes: 37, MaxArrayElements: 2, MaxMapPairs: 4, MaxDepth: 4, MaxNodes: 17, MaxCollectionElements: 7}
	for i, edit := range []func(*codec.Limits){
		func(*codec.Limits) {},
		func(l *codec.Limits) { l.MaxBytes-- }, func(l *codec.Limits) { l.MaxNodes-- },
		func(l *codec.Limits) { l.MaxDepth-- }, func(l *codec.Limits) { l.MaxCollectionElements-- },
		func(l *codec.Limits) { l.MaxArrayElements-- }, func(l *codec.Limits) { l.MaxMapPairs-- },
	} {
		p := l
		edit(&p)
		check := codec.Check[fixture.Links](nested, p)
		b, enc := codec.Encode[fixture.Links](nested, p)
		w, dec := codec.Decode[fixture.Links](unhex(t, generatedNestedLinksHex), p)
		want := error(nil)
		if i != 0 {
			want = codec.ErrLimitExceeded
		}
		if !errors.Is(check, want) || !errors.Is(enc, want) || !errors.Is(dec, want) {
			t.Fatalf("aggregate parity: %+v: %v %v %v", p, check, enc, dec)
		}
		if want != nil && (b != nil || !reflect.DeepEqual(w, fixture.Links{})) {
			t.Fatal("aggregate failure output")
		}
	}
}

func TestGeneratedCIDRejectsBeforeCopy(t *testing.T) {
	// Supply all declared bytes and permissive globals, so the exact CID length
	// check, rather than truncation or resource policy, must prevent a copy.
	const size = 1 << 18
	link := append([]byte{0xd8, 42, 0x5a, 0, 4, 0, 0}, make([]byte, size)...)
	wire := append(unhex(t, "a16176"), link...)
	l := generatedLimits()
	l.MaxByteStringBytes = size
	t.Run("Reader", func(t *testing.T) {
		requireAllocatedBytes(t, 0, func() error {
			r, err := codec.NewReader(link, l)
			if err != nil {
				return err
			}
			v, err := r.CIDLink(0)
			if v != ([36]byte{}) {
				return errors.New("partial raw CID output")
			}
			return err
		}, codec.ErrInvalidData)
	})
	t.Run("Generated", func(t *testing.T) {
		requireAllocatedBytes(t, 2048, func() error {
			v, err := codec.Decode[fixture.Link](wire, l)
			if v != (fixture.Link{}) {
				return errors.New("partial decoded output")
			}
			return err
		}, codec.ErrInvalidData)
	})
}

func FuzzLinkPersistence(f *testing.F) {
	seedQualification(f, generatedLinkHex,
		"a16176d82a5bffffffffffffffff", "a16176d82a5826008100711220", "a16176d82a58250002711220")
	f.Fuzz(func(t *testing.T, data []byte, mode uint8) {
		fuzzQualification(t, data, mode, codec.Decode[fixture.Link], codec.Check[fixture.Link], codec.Encode[fixture.Link], fixture.Link.Clone, fixture.Link.Equal)
	})
}

func FuzzLinksPersistence(f *testing.F) {
	seedQualification(f, generatedLinksHex, generatedNestedLinksHex,
		"a3616240616ea161789bffffffffffffffff6172"+generatedCIDHex,
		"a36162d82a5825616ea06172"+generatedCIDHex)
	f.Fuzz(func(t *testing.T, data []byte, mode uint8) {
		fuzzQualification(t, data, mode, codec.Decode[fixture.Links], codec.Check[fixture.Links], codec.Encode[fixture.Links], fixture.Links.Clone, fixture.Links.Equal)
	})
}
