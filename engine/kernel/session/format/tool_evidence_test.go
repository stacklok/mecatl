package format_test

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestToolEvidenceRoundtrip(t *testing.T) {
	v := format.ToolEvidence{Detail: ptr("")}
	before := v.Clone()
	wire, err := codec.Encode(v, toolLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.ToolEvidence](wire, toolLimits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	*v.Detail = "changed"
	clear(wire)
	if !got.Equal(before) {
		t.Fatal("evidence aliases")
	}
	if (format.ToolEvidence{}).Equal(format.ToolEvidence{Detail: ptr("")}) {
		t.Fatal("lost present empty diagnostic")
	}
	if err := (format.ToolEvidence{}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestToolEvidenceMalformedAndCaps(t *testing.T) {
	for _, over := range []int{0, 1} {
		v := format.ToolEvidence{Detail: ptr(strings.Repeat("d", 16384+over))}
		out, err := codec.Encode(v, toolLimits())
		if over == 0 {
			if err != nil || v.Validate() != nil {
				t.Fatalf("exact cap: %v", err)
			}
			if got, err := codec.Decode[format.ToolEvidence](out, toolLimits()); err != nil || !got.Equal(v) {
				t.Fatalf("roundtrip: %v", err)
			}
		} else {
			rejectToolEvidence(t, v, codec.ErrLimitExceeded)
		}
	}
	rejectToolEvidence(t, format.ToolEvidence{Detail: ptr("\xff")}, codec.ErrInvalidData)
}

// rejectToolEvidence checks both codec boundaries and transactional receiver failure.
func rejectToolEvidence(t *testing.T, v format.ToolEvidence, kind error) {
	t.Helper()
	if v.Validate() == nil {
		t.Fatal("invalid diagnostic validated")
	}
	if out, err := codec.Encode(v, toolLimits()); !errors.Is(err, kind) || out != nil {
		t.Fatalf("encode: %v", err)
	}
	wire := v.AppendCBOR(nil)
	if got, err := codec.Decode[format.ToolEvidence](wire, toolLimits()); !errors.Is(err, kind) || !reflect.DeepEqual(got, format.ToolEvidence{}) {
		t.Fatalf("decode: %v", err)
	}
	receiver := format.ToolEvidence{Detail: ptr("retained")}
	before := receiver.Clone()
	r, err := codec.NewReader(wire, toolLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, kind) || !receiver.Equal(before) {
		t.Fatalf("receiver changed: %v", err)
	}
}
