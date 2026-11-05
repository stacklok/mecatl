package format_test

import (
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestRunInterrupted(t *testing.T) {
	for _, ordinal := range []uint64{1, math.MaxUint64} {
		v := format.RunInterrupted{ID: ordinal}
		wire, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Decode[format.RunInterrupted](wire, limits())
		if err != nil || !got.Equal(v.Clone()) {
			t.Fatalf("roundtrip: %v", err)
		}
	}
	v := format.RunInterrupted{}
	if v.Validate() == nil {
		t.Fatal("zero ordinal validated")
	}
	if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
		t.Fatalf("invalid encode: %v", err)
	}
	// Deliberately bypass prechecking so the generic decoder must discard the
	// syntactically valid, locally invalid temporary value.
	got, err := codec.Decode[format.RunInterrupted](v.AppendCBOR(nil), limits())
	if !errors.Is(err, codec.ErrInvalidData) || got != (format.RunInterrupted{}) {
		t.Fatalf("invalid decode returned nonzero value: %v", err)
	}
	receiver := format.RunInterrupted{ID: 7}
	before := receiver
	reader, err := codec.NewReader(v.AppendCBOR(nil), limits())
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || receiver != before {
		t.Fatalf("invalid local decode changed receiver: %v", err)
	}
}
