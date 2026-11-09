package format_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestToolEvidenceRoundtrip(t *testing.T) {
	v := format.ToolEvidence{Detail: ptr(""), Correlation: ptr("diagnostic"), Metadata: &format.OpaqueToolData{Producer: "p", Format: "f", Version: 1, Data: []byte{0xff, 0}}}
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
	*v.Correlation = "changed"
	v.Metadata.Data[0] = 0
	clear(wire)
	if !got.Equal(before) {
		t.Fatal("evidence aliases")
	}
	if (format.ToolEvidence{}).Equal(format.ToolEvidence{Detail: ptr("")}) {
		t.Fatal("lost present empty")
	}
	if err := (format.ToolEvidence{}).Validate(); err != nil {
		t.Fatal(err)
	}
	if !(format.OpaqueToolData{}).Equal(format.OpaqueToolData{Data: []byte{}}) {
		t.Fatal("nil/empty opaque differs")
	}
}

func TestToolEvidenceMalformedAndCaps(t *testing.T) {
	for _, field := range []string{"detail", "correlation", "producer", "format", "data"} {
		t.Run(field, func(t *testing.T) {
			for _, over := range []int{0, 1} {
				v := format.ToolEvidence{Metadata: &format.OpaqueToolData{Producer: "p", Format: "f", Version: 1}}
				switch field {
				case "detail":
					v.Detail = ptr(strings.Repeat("d", 16384+over))
				case "correlation":
					v.Correlation = ptr(strings.Repeat("c", 1024+over))
				case "producer":
					v.Metadata.Producer = strings.Repeat("p", 1024+over)
				case "format":
					v.Metadata.Format = strings.Repeat("f", 1024+over)
				case "data":
					v.Metadata.Data = make([]byte, 65536+over)
				}
				out, err := codec.Encode(v, toolLimits())
				if over == 0 {
					if err != nil || v.Validate() != nil {
						t.Fatalf("exact cap: %v", err)
					}
					if got, err := codec.Decode[format.ToolEvidence](out, toolLimits()); err != nil || !got.Equal(v) {
						t.Fatalf("roundtrip: %v", err)
					}
				} else {
					if v.Validate() == nil || !errors.Is(err, codec.ErrLimitExceeded) || out != nil {
						t.Fatalf("one over: %v", err)
					}
					if got, err := codec.Decode[format.ToolEvidence](v.AppendCBOR(nil), toolLimits()); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, format.ToolEvidence{}) {
						t.Fatalf("decode cap: %v", err)
					}
				}
			}
		})
	}
	for _, metadata := range []format.OpaqueToolData{{}, {Producer: "p", Format: "f"}, {Producer: "p", Format: "f", Version: -1}, {Producer: "", Format: "f", Version: 1}, {Producer: "p", Format: "", Version: 1}, {Producer: "\xff", Format: "f", Version: 1}, {Producer: "p", Format: "\xff", Version: 1}} {
		v := format.ToolEvidence{Metadata: &metadata}
		if v.Validate() == nil {
			t.Fatal("invalid metadata validated")
		}
		if out, err := codec.Encode(v, toolLimits()); !errors.Is(err, codec.ErrInvalidData) || out != nil {
			t.Fatalf("encode: %v", err)
		}
		if got, err := codec.Decode[format.ToolEvidence](v.AppendCBOR(nil), toolLimits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ToolEvidence{}) {
			t.Fatalf("decode: %v", err)
		}
	}
	for _, v := range []format.ToolEvidence{{Detail: ptr("\xff")}, {Correlation: ptr("\xff")}} {
		if v.Validate() == nil {
			t.Fatal("invalid evidence UTF-8 accepted")
		}
	}
}
