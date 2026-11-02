package format_test

import (
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

func TestRecordClone(t *testing.T) {
	for _, payload := range [][]byte{nil, {}, {0, 0xff, 'x'}} {
		source := format.Record{Kind: "unknown.kind", KindVersion: -1, Payload: payload}
		cloned := source.Clone()
		if !reflect.DeepEqual(cloned, source) {
			t.Fatalf("clone = %#v; want %#v", cloned, source)
		}
		if len(payload) == 0 {
			continue
		}
		source.Payload[0] = 1
		if cloned.Payload[0] != 0 {
			t.Fatal("source payload mutation changed clone")
		}
		cloned.Payload[1] = 2
		if source.Payload[1] != 0xff {
			t.Fatal("cloned payload mutation changed source")
		}
	}
}

func TestRecordEqual(t *testing.T) {
	base := format.Record{Kind: "unknown.kind", KindVersion: 7, Payload: []byte{0, 0xff}}
	for name, other := range map[string]format.Record{
		"kind":    {Kind: "other", KindVersion: 7, Payload: []byte{0, 0xff}},
		"version": {Kind: "unknown.kind", KindVersion: 8, Payload: []byte{0, 0xff}},
		"payload": {Kind: "unknown.kind", KindVersion: 7, Payload: []byte{0, 0xfe}},
		"length":  {Kind: "unknown.kind", KindVersion: 7, Payload: []byte{0}},
	} {
		t.Run(name, func(t *testing.T) {
			if base.Equal(other) || other.Equal(base) {
				t.Fatal("different records compared equal")
			}
		})
	}
	if other := (format.Record{Kind: base.Kind, KindVersion: 7, Payload: []byte{0, 0xff}}); !base.Equal(other) || !other.Equal(base) {
		t.Fatal("independently allocated equal records compared unequal")
	}
	if !(format.Record{}).Equal(format.Record{Payload: []byte{}}) || !(format.Record{Payload: []byte{}}).Equal(format.Record{}) {
		t.Fatal("nil and empty payloads must compare equal, without validation")
	}
	// Opaque payload equality is byte equality, not decoded JSON equivalence.
	if (format.Record{Payload: []byte(`{"a":1}`)}).Equal(format.Record{Payload: []byte(`{ "a": 1 }`)}) {
		t.Fatal("differently spelled opaque payloads compared equal")
	}
}

func TestRecordValidate(t *testing.T) {
	for _, payload := range [][]byte{nil, {}, {0xff, 0, 0x9f}, []byte(`{ "not": json }`)} {
		r := format.Record{Kind: "unknown / 🐈", KindVersion: math.MaxInt64, Payload: payload}
		before := r.Clone()
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, r) {
			t.Fatal("validation mutated record")
		}
	}
	for _, r := range []format.Record{{Kind: "x"}, {Kind: "x", KindVersion: -1}, {KindVersion: 1}, {Kind: "\xff", KindVersion: 1}} {
		if err := r.Validate(); err == nil {
			t.Fatal("invalid record accepted")
		}
	}
}
