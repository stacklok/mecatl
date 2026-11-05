package format_test

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
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

func TestRecordReadCBORPreservesReceiver(t *testing.T) {
	valid := []byte("\xa3\x61k\x61x\x61p\x42\xff\x00\x61v\x01")
	limited := testLimits()
	limited.MaxByteStringBytes = 1
	for _, test := range []struct {
		wire     []byte
		limits   codec.Limits
		category error
	}{
		{valid[:len(valid)-1], testLimits(), codec.ErrInvalidData},
		{[]byte("\xa3\x61k\x61x\x61p\x42\xff\x00\x61v\x00"), testLimits(), codec.ErrInvalidData},
		{valid, limited, codec.ErrLimitExceeded},
	} {
		r := format.Record{Kind: "previous", KindVersion: 7, Payload: []byte("previous payload")}
		before := r.Clone()
		wire := bytes.Clone(test.wire)
		reader, err := codec.NewReader(wire, test.limits)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, test.category) {
			t.Fatalf("ReadCBOR: %v", err)
		}
		if !reflect.DeepEqual(r, before) || !bytes.Equal(wire, test.wire) {
			t.Fatal("failure changed receiver or input")
		}
		got, err := codec.Decode[format.Record](wire, test.limits)
		if !errors.Is(err, test.category) || !reflect.DeepEqual(got, format.Record{}) {
			t.Fatalf("root failure returned partial record: %v", err)
		}
	}
	var r format.Record
	reader, err := codec.NewReader(valid, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReadCBOR(&reader, 0, math.MaxInt); err != nil {
		t.Fatal(err)
	}
	clear(valid)
	if r.Kind != "x" || r.KindVersion != 1 || !bytes.Equal(r.Payload, []byte{0xff, 0}) {
		t.Fatal("successful read borrowed input")
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
