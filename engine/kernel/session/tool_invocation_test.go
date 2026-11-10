package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
	"github.com/stacklok/mecatl/engine/kernel/value"
)

func TestToolInvocationMaterializationAndView(t *testing.T) {
	call := RequestedCallID{requestID: requestID(testHistoryID(t), 7, 9), ordinal: 3}
	items := []value.Value{value.Int64(math.MinInt64), value.Uint64(math.MaxUint64), value.Null()}
	nested := value.Map{"items": value.Array(items), "empty": value.Object(nil)}
	v := ToolInvocation{Name: " effective tool ", Arguments: value.Map{"nested": value.Object(nested), "sibling": value.String("unchanged")}, Prerequisites: []uint64{2, 1}}
	r, err := newToolInvocationRecord(v, call, testLimits())
	interactionCheck(t, err)
	want := v.Clone()
	want.RunID, want.RequestID, want.CallID = 7, 9, 3
	wire, err := codec.Encode(want, testLimits())
	interactionCheck(t, err)
	if r.value.Kind != sessionformat.KindToolInvocation || r.value.KindVersion != 2 || !bytes.Equal(wire, r.value.Payload) || !r.toolInvocation.Equal(want) {
		t.Fatal("materialized invocation differs from complete persisted DTO")
	}
	view, ok := (RecordView{scope: testHistoryID(t), record: r}).ToolInvocation()
	if !ok || view.CallID() != call || view.Name() != v.Name || view.Arguments().Len() != 2 {
		t.Fatal("invocation identity or fields")
	}
	for i, prerequisite := range view.Prerequisites().All() {
		if prerequisite.RequestID() != call.RequestID() || prerequisite.RequestRelative() != want.Prerequisites[i] {
			t.Fatal("prerequisite lost enclosing scope or order")
		}
	}
	if got := view.Prerequisites().At(1); got.RequestRelative() != 1 {
		t.Fatal("indexed prerequisite")
	}
	child, ok := view.Arguments().Get("nested")
	if !ok {
		t.Fatal("nested arguments")
	}
	object, ok := child.Object()
	if !ok || object.Len() != 2 {
		t.Fatal("nested object")
	}
	arrayValue, _ := object.Get("items")
	array, ok := arrayValue.Array()
	if !ok || array.Len() != 3 {
		t.Fatal("nested array")
	}
	if n, ok := array.At(0).Int64(); !ok || n != math.MinInt64 {
		t.Fatal("signed integer narrowed")
	}
	if n, ok := array.At(1).Uint64(); !ok || n != math.MaxUint64 {
		t.Fatal("unsigned integer narrowed")
	}
	if array.At(2).Kind() != value.NullKind {
		t.Fatal("null lost")
	}
	before, err := json.Marshal(view.Arguments())
	interactionCheck(t, err)
	items[0] = value.Bool(false)
	nested["empty"] = value.Null()
	v.Arguments["sibling"] = value.Null()
	v.Prerequisites[0] = 99
	if v.RunID != 0 || v.RequestID != 0 || v.CallID != 0 || v.Equal(ToolInvocation{Name: want.Name, Arguments: want.Arguments, Prerequisites: want.Prerequisites}) {
		t.Fatal("identity mutation or ineffective caller mutation control")
	}
	after, err := json.Marshal(view.Arguments())
	interactionCheck(t, err)
	if !bytes.Equal(before, after) || !r.toolInvocation.Equal(want) || view.Prerequisites().At(0).RequestRelative() != 2 {
		t.Fatal("caller mutated retained backing")
	}
	// Scalar and direct-child reads do not allocate or traverse sibling payloads.
	if n := testing.AllocsPerRun(100, func() {
		if view.CallID() != call || view.Arguments().Len() != 2 || view.Name() == "" {
			panic("bad invocation view")
		}
	}); n != 0 {
		t.Fatalf("scalar/argument capture allocated: %g", n)
	}
	// Requested bytes stay separate and exact, even when not JSON and when
	// effective intent has a different name and semantic argument object.
	requested := []byte{0xff, 0, '{', ' '}
	responseRecord, err := newModelResponseRecord(ModelResponse{Terminal: ResponseTerminalNormal, Items: []ResponseItem{{Call: &RequestedCall{Name: "requested", Arguments: requested}}}}, call.RequestID(), testLimits())
	interactionCheck(t, err)
	response, _ := (RecordView{scope: testHistoryID(t), record: responseRecord}).ModelResponse()
	original := response.CallAt(0)
	clear(requested)
	if original.Name() != "requested" || !bytes.Equal(original.Arguments().Copy(), []byte{0xff, 0, '{', ' '}) || view.Name() != " effective tool " {
		t.Fatal("effective intent replaced or normalized requested bytes")
	}
	for _, args := range []value.Map{nil, {}} {
		r, err := newToolInvocationRecord(ToolInvocation{Name: "x", Arguments: args}, call, testLimits())
		interactionCheck(t, err)
		view, _ := (RecordView{scope: testHistoryID(t), record: r}).ToolInvocation()
		j, err := json.Marshal(view.Arguments())
		if err != nil || string(j) != "{}" {
			t.Fatalf("empty effective object: %s, %v", j, err)
		}
	}
}

func TestToolInvocationMaterializationBounds(t *testing.T) {
	call := RequestedCallID{requestID: requestID(testHistoryID(t), 1, 1), ordinal: 3}
	cycle := value.Map{}
	cycle["self"] = value.Object(cycle)
	for _, tc := range []struct {
		name   string
		v      ToolInvocation
		limits codec.Limits
		want   error
	}{
		{"invalid limits", ToolInvocation{Name: "x"}, codec.Limits{}, codec.ErrInvalidLimits},
		{"oversized name before syntax", ToolInvocation{Name: string(bytes.Repeat([]byte{0xff}, 129))}, testLimits(), codec.ErrLimitExceeded},
		{"oversized prerequisites before scan", ToolInvocation{Name: "x", Prerequisites: make([]uint64, 17)}, testLimits(), codec.ErrLimitExceeded},
		{"forward prerequisite", ToolInvocation{Name: "x", Prerequisites: []uint64{4}}, testLimits(), codec.ErrInvalidData},
		{"duplicate prerequisite", ToolInvocation{Name: "x", Prerequisites: []uint64{1, 1}}, testLimits(), codec.ErrInvalidData},
		{"invalid argument", ToolInvocation{Name: "x", Arguments: value.Map{"x": {}}}, testLimits(), codec.ErrInvalidData},
		{"cyclic argument", ToolInvocation{Name: "x", Arguments: cycle}, testLimits(), codec.ErrLimitExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newToolInvocationRecord(tc.v, call, tc.limits)
			if r != nil || !errors.Is(err, tc.want) {
				t.Fatalf("materialization: %v, want %v", err, tc.want)
			}
		})
	}
}

func TestToolFactZeroAndWrongKindViews(t *testing.T) {
	created := interactionRecord(t)(newCreatedRecord(Created{}, testLimits()))
	for _, record := range []RecordView{{}, {scope: testHistoryID(t), record: created}} {
		invocation, ok := record.ToolInvocation()
		if ok || invocation.CallID() != (RequestedCallID{}) || invocation.Name() != "" || invocation.Arguments().Len() != 0 || invocation.Prerequisites().Len() != 0 {
			t.Fatal("zero or wrong-kind invocation")
		}
		invocation.Prerequisites().All()(func(int, RequestedCallID) bool { t.Fatal("zero prerequisites yielded"); return true })
		disposition, ok := record.ToolDisposition()
		if ok || disposition.CallID() != (RequestedCallID{}) {
			t.Fatal("zero or wrong-kind disposition")
		}
		if v, ok := disposition.Returned(); ok || v != (ToolReturnedView{}) || v.Status() != "" || v.Output().Parts().Len() != 0 {
			t.Fatal("zero returned")
		}
		if v, ok := disposition.NotInvoked(); ok || v != (ToolNotInvokedView{}) || v.Reason() != "" {
			t.Fatal("zero not invoked")
		}
		if v, ok := disposition.Indeterminate(); ok || v != (ToolIndeterminateView{}) || v.Reason() != "" || v.Output().Capture() != "" {
			t.Fatal("zero indeterminate")
		}
		if text, ok := disposition.Diagnostic(); ok || text != "" {
			t.Fatal("zero diagnostic")
		}
	}
}
