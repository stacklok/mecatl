package format_test

import (
	"errors"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestRunEndedReasonVocabulary(t *testing.T) {
	reasons := []format.RunEndedReason{format.RunEndedCompleted, format.RunEndedError, format.RunEndedLimit, format.RunEndedInterrupted, format.RunEndedCaptureFailed}
	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			if !utf8.ValidString(string(reason)) || len(reason) > format.MaxRunEndedReasonBytes || format.MaxRunEndedReasonBytes != 32 {
				t.Fatal("supported reason violates the 32-byte UTF-8 bound")
			}
			v := format.RunEnded{RunID: 1, Reason: reason}
			wire, err := codec.Encode(v, limits())
			if err != nil {
				t.Fatal(err)
			}
			got, err := codec.Decode[format.RunEnded](wire, limits())
			if err != nil || !got.Equal(v) {
				t.Fatalf("string reason roundtrip: %v", err)
			}
		})
	}
	for _, reason := range []string{"", "cancelled", "unknown", strings.Repeat("x", 32), strings.Repeat("x", 33), strings.Repeat("é", 17), string([]byte{0xff})} {
		v := format.RunEnded{RunID: 1, Reason: format.RunEndedReason(reason)}
		if v.Validate() == nil {
			t.Fatalf("invalid reason validated: %q", reason)
		}
		if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
			t.Fatalf("invalid reason encode: %v", err)
		}
		got, err := codec.Decode[format.RunEnded](v.AppendCBOR(nil), limits())
		if !errors.Is(err, codec.ErrInvalidData) || got != (format.RunEnded{}) {
			t.Fatalf("invalid reason decode: %v", err)
		}
	}
}

func TestRunEndedRejectsExperimentalNumericReasons(t *testing.T) {
	// The earlier experimental vocabulary used unsigned CBOR codes 1..6.
	// No numeric reason, including a formerly valid code, is converted to text.
	for reason := byte(0); reason <= 6; reason++ {
		wire := []byte{0xa3, 0x61, 'b', 0xf4, 0x61, 'e', reason, 0x61, 'r', 1}
		got, err := codec.Decode[format.RunEnded](wire, limits())
		if !errors.Is(err, codec.ErrInvalidData) || got != (format.RunEnded{}) {
			t.Fatalf("numeric reason %d decode: %v", reason, err)
		}
		receiver := format.RunEnded{RunID: 7, Reason: format.RunEndedInterrupted}
		before := receiver
		reader, err := codec.NewReader(wire, limits())
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || receiver != before {
			t.Fatalf("numeric reason %d changed receiver: %v", reason, err)
		}
	}
}

func TestRunEndedBulkToolCloseout(t *testing.T) {
	for _, reason := range []format.RunEndedReason{format.RunEndedCompleted, format.RunEndedError, format.RunEndedInterrupted, format.RunEndedLimit, format.RunEndedCaptureFailed} {
		for _, bulk := range []bool{false, true} {
			v := format.RunEnded{RunID: 1, Reason: reason, BulkToolCloseout: bulk}
			valid := !bulk || reason == format.RunEndedLimit || reason == format.RunEndedCaptureFailed
			wire, err := codec.Encode(v, limits())
			if valid {
				if err != nil || v.Validate() != nil {
					t.Fatalf("reason=%s bulk=%t: %v", reason, bulk, err)
				}
				got, err := codec.Decode[format.RunEnded](wire, limits())
				if err != nil || !got.Equal(v) || !v.Clone().Equal(v) {
					t.Fatalf("bulk roundtrip: %v", err)
				}
			} else {
				if wire != nil || !errors.Is(err, codec.ErrInvalidData) || v.Validate() == nil {
					t.Fatalf("invalid bulk reason=%s: %v", reason, err)
				}
				got, err := codec.Decode[format.RunEnded](v.AppendCBOR(nil), limits())
				if !errors.Is(err, codec.ErrInvalidData) || got != (format.RunEnded{}) {
					t.Fatalf("invalid bulk decode: %v", err)
				}
			}
		}
	}
	// A missing required false is not silently interpreted as ordinary termination.
	wire := []byte("\xa2\x61e\x65limit\x61r\x01")
	got, err := codec.Decode[format.RunEnded](wire, limits())
	if !errors.Is(err, codec.ErrInvalidData) || got != (format.RunEnded{}) {
		t.Fatalf("accepted missing bulk flag: %v", err)
	}
	receiver := format.RunEnded{RunID: 7, Reason: format.RunEndedLimit, BulkToolCloseout: true}
	before := receiver
	reader, err := codec.NewReader(wire, limits())
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || receiver != before {
		t.Fatalf("missing bulk flag changed receiver: %v", err)
	}
}

func TestRunEnded(t *testing.T) {
	for _, ordinal := range []uint64{1, math.MaxUint64} {
		v := format.RunEnded{RunID: ordinal, Reason: format.RunEndedInterrupted}
		wire, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Decode[format.RunEnded](wire, limits())
		if err != nil || !got.Equal(v.Clone()) {
			t.Fatalf("roundtrip: %v", err)
		}
	}
	v := format.RunEnded{}
	if v.Validate() == nil {
		t.Fatal("zero ordinal validated")
	}
	if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
		t.Fatalf("invalid encode: %v", err)
	}
	// Deliberately bypass prechecking so the generic decoder must discard the
	// syntactically valid, locally invalid temporary value.
	got, err := codec.Decode[format.RunEnded](v.AppendCBOR(nil), limits())
	if !errors.Is(err, codec.ErrInvalidData) || got != (format.RunEnded{}) {
		t.Fatalf("invalid decode returned nonzero value: %v", err)
	}
	receiver := format.RunEnded{RunID: 7, Reason: format.RunEndedInterrupted}
	before := receiver
	reader, err := codec.NewReader(v.AppendCBOR(nil), limits())
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || receiver != before {
		t.Fatalf("invalid local decode changed receiver: %v", err)
	}
}
