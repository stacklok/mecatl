package format_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
)

func TestParametersRejectExtraFields(t *testing.T) {
	for _, tc := range []struct {
		name  string
		valid []byte
		extra []byte
	}{
		{"parameters", []byte{0xa1, 0x61, 'p', 0x80}, []byte{0xa2, 0x61, 'p', 0x80, 0x61, 'x', 0x00}},
		{"selector", []byte{0xa1, 0x61, 'p', 0x81, 0xa5,
			0x61, 'a', 0x61, 'a', 0x61, 'f', 0x61, 'f', 0x61, 'm', 0x61, 'm',
			0x61, 'p', 0x61, 'p', 0x61, 'v', 0x01},
			[]byte{0xa1, 0x61, 'p', 0x81, 0xa6,
				0x61, 'a', 0x61, 'a', 0x61, 'f', 0x61, 'f', 0x61, 'm', 0x61, 'm',
				0x61, 'p', 0x61, 'p', 0x61, 'v', 0x01, 0x61, 'x', 0x00}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := codec.Decode[format.ParametersV1](tc.valid, limits()); err != nil {
				t.Fatalf("control without extra field is invalid: %v", err)
			}
			got, err := codec.Decode[format.ParametersV1](tc.extra, limits())
			if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ParametersV1{}) {
				t.Fatalf("extra field did not return zero and invalid data: %v", err)
			}
		})
	}
}

func TestParametersCanonical(t *testing.T) {
	nilWire, err := codec.Encode(format.ParametersV1{}, limits())
	if err != nil {
		t.Fatal(err)
	}
	emptyWire, err := codec.Encode(format.ParametersV1{PrivateReplay: []format.PrivateReplaySelector{}}, limits())
	if err != nil || !bytes.Equal(nilWire, emptyWire) || !bytes.Equal(nilWire, []byte{0xa1, 0x61, 'p', 0x80}) {
		t.Fatalf("empty parameters: %x %v", nilWire, err)
	}
	selector := format.PrivateReplaySelector{SourceAPI: "a", SourceModel: "m", Producer: "p", Format: "f", Version: 1}
	v := format.ParametersV1{PrivateReplay: []format.PrivateReplaySelector{selector, selector}}
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.ParametersV1](wire, limits())
	if err != nil || !got.Equal(v) || v.Validate() != nil {
		t.Fatalf("parameters roundtrip: %v", err)
	}
	clone := v.Clone()
	v.PrivateReplay[0].SourceAPI = "changed"
	if !got.Equal(clone) {
		t.Fatal("clone aliases selectors")
	}
	for _, wire := range [][]byte{nil, {}, {0xa0}, {0xa1, 0x61, 'x', 0x80}, {0xa1, 0x61, 'p', 0xf6}, append(nilWire, 0)} {
		got, err := codec.Decode[format.ParametersV1](wire, limits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ParametersV1{}) {
			t.Fatalf("malformed parameters %x: %v", wire, err)
		}
	}
	for _, bad := range []format.PrivateReplaySelector{{SourceModel: "m", Producer: "p", Format: "f", Version: 1}, {SourceAPI: "a", Producer: "p", Format: "f", Version: 1}, {SourceAPI: "a", SourceModel: "m", Format: "f", Version: 1}, {SourceAPI: "a", SourceModel: "m", Producer: "p", Version: 1}, {SourceAPI: "a", SourceModel: "m", Producer: "p", Format: "f"}, {SourceAPI: string([]byte{0xff}), SourceModel: "m", Producer: "p", Format: "f", Version: 1}} {
		v := format.ParametersV1{PrivateReplay: []format.PrivateReplaySelector{bad}}
		if v.Validate() == nil {
			t.Fatal("bad selector validates")
		}
		if wire, err := codec.Encode(v, limits()); wire != nil || !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("bad selector: %v", err)
		}
	}
}
