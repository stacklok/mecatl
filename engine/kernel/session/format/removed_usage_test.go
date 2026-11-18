package format_test

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestRemovedEmbeddedUsageFieldsRejectStrictly(t *testing.T) {
	// Each u member carries an empty map, formerly valid embedded usage. The
	// experimental Version 1 deliberately has no migration or permissive reader.
	t.Run("response", func(t *testing.T) {
		valid := format.ModelResponse{RunID: 1, RequestID: 1, Terminal: format.ResponseTerminalOther}
		wire := valid.AppendCBOR(nil)
		wire[0]++
		wire = append(wire, 0x61, 'u', 0xa0)
		rejectRemovedUsage(t, wire, valid)
	})
	t.Run("failure", func(t *testing.T) {
		valid := format.InvocationFailure{RunID: 1, RequestID: 1, Class: format.InvocationFailureOther}
		wire := valid.AppendCBOR(nil)
		wire[0]++
		wire = append(wire, 0x61, 'u', 0xa0)
		rejectRemovedUsage(t, wire, valid)
	})
	t.Run("capture", func(t *testing.T) {
		valid := format.CaptureFailureEvidence{RequestID: 1}
		wire := valid.AppendCBOR(nil)
		wire[0]++
		wire = append(wire, 0x61, 'u', 0xa0)
		rejectRemovedUsage(t, wire, valid)
		// The nested decoder must also reject when reached through its actual root.
		ending := []byte("\xa3\x61c")
		ending = append(ending, wire...)
		ending = append(ending, []byte("\x61e\x6ecapture-failed\x61r\x01")...)
		rejectRemovedUsage(t, ending, format.RunEnded{RunID: 1, Reason: format.RunEndedInterrupted})
	})
	t.Run("removed definition field", func(t *testing.T) {
		rejectRemovedUsage(t, []byte("\xa2\x61g\x69aggregate\x61p\x64main"), format.UsageDefinition{Purpose: format.UsagePurposeMain})
	})
}

func rejectRemovedUsage[T codec.Marshaler, P interface {
	*T
	codec.Unmarshaler
}](t *testing.T, wire []byte, valid T) {
	t.Helper()
	got, err := codec.Decode[T, P](wire, limits())
	var zero T
	if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, zero) {
		t.Fatalf("old embedded field decoded: %v", err)
	}
	before := valid.AppendCBOR(nil)
	reader, err := codec.NewReader(wire, limits())
	if err != nil {
		t.Fatal(err)
	}
	if err := P(&valid).ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(before, valid.AppendCBOR(nil)) {
		t.Fatalf("failed read changed receiver: %v", err)
	}
}
