package format_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
	"github.com/stacklok/mecatl/engine/kernel/value"
)

// toolLimits deliberately exceed schema caps so tests exercise literal field bounds.
func toolLimits() codec.Limits {
	return codec.Limits{MaxBytes: 32 << 20, MaxStringBytes: 8 << 20, MaxByteStringBytes: 8 << 20, MaxArrayElements: 8192, MaxMapPairs: 32, MaxDepth: 32, MaxNodes: 100000, MaxCollectionElements: 100000}
}

func richToolInvocation() format.ToolInvocation {
	return format.ToolInvocation{RunID: 1, RequestID: 2, CallID: 3, Name: " effective tool ", Arguments: value.Map{"items": value.Array([]value.Value{value.Null(), value.Bool(true), value.Int64(math.MinInt64), value.Uint64(math.MaxUint64)}), "nested": value.Object(value.Map{"text": value.String("original")})}, Prerequisites: []uint64{5, 4}, Preparation: &format.ToolEvidence{Detail: ptr("prepared"), Correlation: ptr(""), Metadata: &format.OpaqueToolData{Producer: "p", Format: "f", Version: 1, Data: []byte{0xff}}}, Authorization: &format.ToolEvidence{Detail: ptr("authorized")}, Provenance: &format.ToolProvenance{Implementation: ptr(""), Version: ptr("v"), Runner: ptr("r"), Environment: ptr("e"), EnvironmentRevision: ptr("rev")}}
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
	v.Arguments["items"] = value.Null()
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
	if !(format.ToolInvocation{Arguments: nil}).Equal(format.ToolInvocation{Arguments: value.Map{}, Prerequisites: []uint64{}}) {
		t.Fatal("nil/empty differs")
	}
}

func TestToolInvocationStructuredArguments(t *testing.T) {
	items := []value.Value{value.String("original"), value.Null()}
	nested := value.Map{"items": value.Array(items)}
	v := richToolInvocation()
	v.Arguments["nested"] = value.Object(nested)
	before := v.Clone()
	wire, err := codec.Encode(v, toolLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.ToolInvocation](wire, toolLimits())
	if err != nil || !got.Equal(before) {
		t.Fatalf("structured roundtrip: %v", err)
	}
	jsonData, err := json.Marshal(v.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := value.ParseJSONObject(jsonData, toolLimits())
	if err != nil {
		t.Fatal(err)
	}
	fromJSON := v
	fromJSON.Arguments = parsed
	again, err := codec.Encode(fromJSON, toolLimits())
	if err != nil || !bytes.Equal(wire, again) {
		t.Fatalf("native/JSON canonical bytes differ: %v", err)
	}
	// Ordinary enclosing DTO fields dispatch the map marshaler too.
	projected, err := json.Marshal(v)
	var fields struct{ Arguments json.RawMessage }
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(projected, &fields); err != nil || !bytes.Equal(fields.Arguments, jsonData) {
		t.Fatalf("invocation argument projection: %s, %v", projected, err)
	}
	items[0] = value.Bool(false)
	nested["new"] = value.Null()
	v.Arguments["new"] = value.Null()
	clear(wire)
	if !got.Equal(before) || got.Equal(v) {
		t.Fatal("clone/decode did not detach nested argument backing")
	}
	borrowed := value.BorrowMap(got.Arguments)
	if out, err := json.Marshal(borrowed); err != nil || !bytes.Equal(out, jsonData) {
		t.Fatalf("decoded argument view changed: %s, %v", out, err)
	}
	for _, args := range []value.Map{nil, {}} {
		v := format.ToolInvocation{RunID: 1, RequestID: 2, CallID: 3, Name: "x", Arguments: args}
		wire, err := codec.Encode(v, toolLimits())
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Decode[format.ToolInvocation](wire, toolLimits())
		if err != nil || got.Arguments == nil || len(got.Arguments) != 0 {
			t.Fatalf("required empty object: %+v, %v", got.Arguments, err)
		}
	}
}

func TestToolInvocationArgumentValidation(t *testing.T) {
	for _, args := range []value.Map{
		{"invalid": {}}, {"text": value.String("\xff")}, {"\xff": value.Null()},
		{"nested": value.Array([]value.Value{value.Object(value.Map{"invalid": {}})})},
	} {
		v := richToolInvocation()
		v.Arguments = args
		if err := v.Validate(); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("direct argument validation: %v", err)
		}
		if out, err := codec.Encode(v, toolLimits()); out != nil || !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("invalid argument published: %x, %v", out, err)
		}
	}
	for _, depth := range []int{128, 129} {
		for _, leaf := range []value.Value{value.Null(), value.Array(nil), value.Object(nil)} {
			child := leaf
			for range depth - 1 {
				child = value.Array([]value.Value{child})
			}
			v := richToolInvocation()
			v.Arguments = value.Map{"deep": child}
			l := toolLimits()
			l.MaxDepth = 1024 // The argument root is nested inside the DTO wire root.
			out, err := codec.Encode(v, l)
			if depth == 128 {
				if err != nil || v.Validate() != nil {
					t.Fatalf("local depth 128: %v", err)
				}
				got, err := codec.Decode[format.ToolInvocation](out, l)
				if err != nil || !got.Equal(v) {
					t.Fatalf("local depth roundtrip: %v", err)
				}
			} else if out != nil || !errors.Is(err, value.ErrDepthExceeded) || !errors.Is(v.Validate(), value.ErrDepthExceeded) {
				t.Fatalf("local depth 129: %v", err)
			}
		}
	}
	cycle := value.Map{}
	cycle["self"] = value.Object(cycle)
	v := richToolInvocation()
	v.Arguments = cycle
	if err := v.Validate(); !errors.Is(err, value.ErrDepthExceeded) {
		t.Fatalf("cyclic arguments: %v", err)
	}
	l := toolLimits()
	l.MaxDepth = 1024
	if out, err := codec.Encode(v, l); out != nil || !errors.Is(err, value.ErrDepthExceeded) {
		t.Fatalf("cyclic arguments published: %x, %v", out, err)
	}
}

func TestToolInvocationArgumentCodecLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  value.Map
		limit func(*codec.Limits)
	}{
		{"string", value.Map{"p": value.String("ab")}, func(l *codec.Limits) { l.MaxStringBytes = 1 }},
		{"array", value.Map{"p": value.Array([]value.Value{value.Null(), value.Null()})}, func(l *codec.Limits) { l.MaxArrayElements = 1 }},
		{"wire depth", value.Map{"p": value.Array([]value.Value{value.Null()})}, func(l *codec.Limits) { l.MaxDepth = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := format.ToolInvocation{RunID: 1, RequestID: 2, CallID: 3, Name: "x", Arguments: tc.args}
			if err := v.Validate(); err != nil {
				t.Fatal("structural validation imposed operational policy", err)
			}
			wire, err := codec.Encode(v, toolLimits())
			if err != nil {
				t.Fatal(err)
			}
			l := toolLimits()
			tc.limit(&l)
			if out, err := codec.Encode(v, l); out != nil || !errors.Is(err, codec.ErrLimitExceeded) {
				t.Fatalf("argument limit bypassed on encode: %v", err)
			}
			if got, err := codec.Decode[format.ToolInvocation](wire, l); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, format.ToolInvocation{}) {
				t.Fatalf("argument limit bypassed on decode: %v", err)
			}
		})
	}
}

func TestToolInvocationArgumentSharedBudget(t *testing.T) {
	v := format.ToolInvocation{RunID: 1, RequestID: 2, CallID: 3, Name: "x"}
	args := value.Map{"p": value.Array([]value.Value{value.Null(), value.Null()})}
	l := toolLimits()
	l.MaxNodes = 13 // Six required DTO fields: root + six keys + six values.
	// Both components pass separately; only their shared traversal exceeds policy.
	if _, err := codec.Encode(v, l); err != nil {
		t.Fatal("empty invocation control", err)
	}
	if _, err := codec.Encode(args, l); err != nil {
		t.Fatal("argument-only control", err)
	}
	v.Arguments = args
	wire, err := codec.Encode(v, toolLimits())
	if err != nil {
		t.Fatal("unrestricted combined control", err)
	}
	if out, err := codec.Encode(v, l); out != nil || !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("aggregate encode accounting reset: %v", err)
	}
	if got, err := codec.Decode[format.ToolInvocation](wire, l); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, format.ToolInvocation{}) {
		t.Fatalf("aggregate decode accounting reset: %v", err)
	}
}

func TestToolInvocationRejectsNonObjectArguments(t *testing.T) {
	v := format.ToolInvocation{RunID: 1, RequestID: 2, CallID: 3, Name: "x"}
	wire, err := codec.Encode(v, toolLimits())
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte{0x61, 'a', 0xa0} // Required argument field containing an empty map.
	if bytes.Count(wire, marker) != 1 {
		t.Fatal("argument field negative control not found uniquely")
	}
	for _, raw := range [][]byte{{0x40}, {0xf6}, {0x80}, {0xa1, 0x61, 'x', 0x40}} {
		bad := bytes.Replace(wire, marker, append([]byte{0x61, 'a'}, raw...), 1)
		if got, err := codec.Decode[format.ToolInvocation](bad, toolLimits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ToolInvocation{}) {
			t.Fatalf("accepted non-object/unsupported arguments %x: %v", raw, err)
		}
		receiver := richToolInvocation()
		before := receiver.Clone()
		r, err := codec.NewReader(bad, toolLimits())
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || !receiver.Equal(before) {
			t.Fatalf("failed read changed receiver for %x: %v", raw, err)
		}
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
					v.Arguments = make(value.Map, 4096+over)
					for i := range 4096 + over {
						v.Arguments[strconv.Itoa(i)] = value.Null()
					}
				case "prerequisites":
					v.Prerequisites = make([]uint64, 4096+over)
					for i := range v.Prerequisites {
						v.Prerequisites[i] = uint64(i + 10)
					}
				case "provenance":
					v.Provenance.EnvironmentRevision = ptr(strings.Repeat("r", 1024+over))
				}
				l := toolLimits()
				l.MaxMapPairs = 8192 // Keep operational policy above the argument field cap.
				out, err := codec.Encode(v, l)
				if over == 0 {
					if err != nil || v.Validate() != nil {
						t.Fatalf("exact cap: %v", err)
					}
					got, err := codec.Decode[format.ToolInvocation](out, l)
					if err != nil || !got.Equal(v) {
						t.Fatalf("exact roundtrip: %v", err)
					}
				} else {
					if v.Validate() == nil || !errors.Is(err, codec.ErrLimitExceeded) || out != nil {
						t.Fatalf("one over: %v", err)
					}
					wire := v.AppendCBOR(nil)
					if got, err := codec.Decode[format.ToolInvocation](wire, l); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, format.ToolInvocation{}) {
						t.Fatalf("decode cap: %v", err)
					}
					receiver := richToolInvocation()
					before := receiver.Clone()
					r, err := codec.NewReader(wire, l)
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
