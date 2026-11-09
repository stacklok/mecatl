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

// toolLimits deliberately exceed schema caps so tests exercise literal field bounds.
func toolLimits() codec.Limits {
	return codec.Limits{MaxBytes: 32 << 20, MaxStringBytes: 8 << 20, MaxByteStringBytes: 8 << 20, MaxArrayElements: 8192, MaxMapPairs: 32, MaxDepth: 32, MaxNodes: 100000, MaxCollectionElements: 100000}
}

func richToolInvocation() format.ToolInvocation {
	return format.ToolInvocation{RunID: 1, RequestID: 2, CallID: 3, Name: " effective tool ", Arguments: []byte{0xff, 0}, Prerequisites: []uint64{5, 4}, Preparation: &format.ToolEvidence{Detail: ptr("prepared"), Correlation: ptr(""), Metadata: &format.OpaqueToolData{Producer: "p", Format: "f", Version: 1, Data: []byte{0xff}}}, Authorization: &format.ToolEvidence{Detail: ptr("authorized")}, Provenance: &format.ToolProvenance{Implementation: ptr(""), Version: ptr("v"), Runner: ptr("r"), Environment: ptr("e"), EnvironmentRevision: ptr("rev")}}
}

func TestToolInvocationRoundtrip(t *testing.T) {
	v := richToolInvocation()
	before := v.Clone()
	wire, err := codec.Encode(v, toolLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.ToolInvocation](wire, toolLimits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	v.Arguments[0] = 0
	v.Prerequisites[0] = 8
	*v.Preparation.Detail = "changed"
	v.Preparation.Metadata.Data[0] = 0
	*v.Authorization.Detail = "changed"
	*v.Provenance.Implementation = "changed"
	clear(wire)
	if !got.Equal(before) {
		t.Fatal("clone/decode retained mutable aliases")
	}
	v = before.Clone()
	v.Prerequisites[0], v.Prerequisites[1] = v.Prerequisites[1], v.Prerequisites[0]
	if v.Equal(before) {
		t.Fatal("prerequisite storage order lost")
	}
	if !(format.ToolProvenance{EnvironmentRevision: ptr("")}).Equal((format.ToolProvenance{EnvironmentRevision: ptr("")}).Clone()) {
		t.Fatal("partial provenance clone")
	}
	if err := (format.ToolProvenance{EnvironmentRevision: ptr("")}).Validate(); err != nil {
		t.Fatal(err)
	}
	if (format.ToolProvenance{}).Equal(format.ToolProvenance{Runner: ptr("")}) {
		t.Fatal("lost label presence")
	}
	if !(format.ToolInvocation{Arguments: nil}).Equal(format.ToolInvocation{Arguments: []byte{}, Prerequisites: []uint64{}}) {
		t.Fatal("nil/empty differs")
	}
}

func TestToolInvocationMalformed(t *testing.T) {
	for name, edit := range map[string]func(*format.ToolInvocation){
		"run":               func(v *format.ToolInvocation) { v.RunID = 0 },
		"request":           func(v *format.ToolInvocation) { v.RequestID = 0 },
		"call":              func(v *format.ToolInvocation) { v.CallID = 0 },
		"name":              func(v *format.ToolInvocation) { v.Name = "" },
		"utf8":              func(v *format.ToolInvocation) { v.Name = "\xff" },
		"zero prerequisite": func(v *format.ToolInvocation) { v.Prerequisites = []uint64{0} },
		"self":              func(v *format.ToolInvocation) { v.Prerequisites = []uint64{v.CallID} },
		"duplicate":         func(v *format.ToolInvocation) { v.Prerequisites = []uint64{4, 4} },
		"preparation":       func(v *format.ToolInvocation) { v.Preparation.Detail = ptr("\xff") },
		"authorization":     func(v *format.ToolInvocation) { v.Authorization.Correlation = ptr("\xff") },
		"provenance":        func(v *format.ToolInvocation) { v.Provenance.Runner = ptr("\xff") },
	} {
		t.Run(name, func(t *testing.T) {
			v := richToolInvocation()
			edit(&v)
			if v.Validate() == nil {
				t.Fatal("direct validation accepted invalid invocation")
			}
			if out, err := codec.Encode(v, toolLimits()); !errors.Is(err, codec.ErrInvalidData) || out != nil {
				t.Fatalf("encode: %v", err)
			}
			wire := v.AppendCBOR(nil)
			if got, err := codec.Decode[format.ToolInvocation](wire, toolLimits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ToolInvocation{}) {
				t.Fatalf("decode: %v", err)
			}
			receiver := richToolInvocation()
			before := receiver.Clone()
			r, err := codec.NewReader(wire, toolLimits())
			if err != nil {
				t.Fatal(err)
			}
			if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || !receiver.Equal(before) {
				t.Fatalf("receiver changed: %v", err)
			}
		})
	}
}

func TestToolInvocationValidateRejectsOversizedPrerequisitesBeforeAllocation(t *testing.T) {
	v := format.ToolInvocation{RunID: 1, RequestID: 2, CallID: 3, Name: "tool", Prerequisites: make([]uint64, 4097)}
	for i := range v.Prerequisites {
		v.Prerequisites[i] = uint64(i + 10)
	}
	var err error
	allocs := testing.AllocsPerRun(100, func() {
		err = v.Validate()
	})
	if err == nil {
		t.Fatal("oversized prerequisites directly validated")
	}
	if allocs > 1 {
		t.Fatalf("oversized prerequisites allocated %.1f times", allocs)
	}
}

func TestToolInvocationSchemaCaps(t *testing.T) {
	for _, field := range []string{"name", "arguments", "prerequisites", "provenance"} {
		t.Run(field, func(t *testing.T) {
			for _, over := range []int{0, 1} {
				v := richToolInvocation()
				switch field {
				case "name":
					v.Name = strings.Repeat("n", 4096+over)
				case "arguments":
					v.Arguments = make([]byte, 4194304+over)
				case "prerequisites":
					v.Prerequisites = make([]uint64, 4096+over)
					for i := range v.Prerequisites {
						v.Prerequisites[i] = uint64(i + 10)
					}
				case "provenance":
					v.Provenance.EnvironmentRevision = ptr(strings.Repeat("r", 1024+over))
				}
				out, err := codec.Encode(v, toolLimits())
				if over == 0 {
					if err != nil || v.Validate() != nil {
						t.Fatalf("exact cap: %v", err)
					}
					got, err := codec.Decode[format.ToolInvocation](out, toolLimits())
					if err != nil || !got.Equal(v) {
						t.Fatalf("exact roundtrip: %v", err)
					}
				} else {
					if v.Validate() == nil || !errors.Is(err, codec.ErrLimitExceeded) || out != nil {
						t.Fatalf("one over: %v", err)
					}
					wire := v.AppendCBOR(nil)
					if got, err := codec.Decode[format.ToolInvocation](wire, toolLimits()); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, format.ToolInvocation{}) {
						t.Fatalf("decode cap: %v", err)
					}
					receiver := richToolInvocation()
					before := receiver.Clone()
					r, err := codec.NewReader(wire, toolLimits())
					if err != nil {
						t.Fatal(err)
					}
					if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, codec.ErrLimitExceeded) || !receiver.Equal(before) {
						t.Fatalf("read cap: %v", err)
					}
				}
			}
		})
	}
}
