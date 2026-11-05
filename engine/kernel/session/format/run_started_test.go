package format_test

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func limits() codec.Limits {
	return codec.Limits{MaxBytes: 4096, MaxStringBytes: 128, MaxByteStringBytes: 1024,
		MaxArrayElements: 16, MaxMapPairs: 8, MaxDepth: 16, MaxNodes: 512, MaxCollectionElements: 128}
}

func TestRunStarted(t *testing.T) {
	v := format.RunStarted{ID: math.MaxUint64}
	cloned := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.RunStarted](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	v.ID = 1
	clear(wire)
	if !cloned.Equal(got) || cloned.ID != math.MaxUint64 {
		t.Fatal("clone or decode aliased inputs")
	}
}

func TestRunStartedInvalid(t *testing.T) {
	for _, v := range []format.RunStarted{
		{},
	} {
		if v.Validate() == nil {
			t.Fatal("invalid local DTO validated")
		}
		if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
			t.Fatalf("invalid encode: %v", err)
		}
		// Deliberately bypass prechecking to exercise independent read validation.
		wire := v.AppendCBOR(nil)
		got, err := codec.Decode[format.RunStarted](wire, limits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.RunStarted{}) {
			t.Fatalf("invalid decode: %v", err)
		}
		receiver := format.RunStarted{ID: 3}
		before := receiver.Clone()
		reader, err := codec.NewReader(wire, limits())
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || !receiver.Equal(before) {
			t.Fatalf("failed local validation changed receiver: %v", err)
		}
	}
}
