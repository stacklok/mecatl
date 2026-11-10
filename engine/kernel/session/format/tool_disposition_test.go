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

func TestToolDispositionBranches(t *testing.T) {
	var branches []format.ToolDisposition
	for _, status := range []format.ToolReturnStatus{format.ToolReturnSuccess, format.ToolReturnError} {
		branches = append(branches, format.ToolDisposition{Returned: &format.ToolReturned{Status: status, Output: richToolOutput()}})
	}
	for _, reason := range []format.ToolNotInvokedReason{format.ToolNotInvokedPolicyRejected, format.ToolNotInvokedPreparationFailed, format.ToolNotInvokedCancelled, format.ToolNotInvokedOther} {
		branches = append(branches, format.ToolDisposition{NotInvoked: &format.ToolNotInvoked{Reason: reason}})
	}
	for _, reason := range []format.ToolIndeterminateReason{format.ToolIndeterminateTransport, format.ToolIndeterminateTimeout, format.ToolIndeterminateExecutorLost, format.ToolIndeterminateCancelled, format.ToolIndeterminateOther} {
		for _, capture := range []format.ToolCaptureState{format.ToolCaptureTruncated, format.ToolCaptureOmitted} {
			output := format.ToolOutput{Capture: capture}
			if capture == format.ToolCaptureTruncated {
				output = richToolOutput()
				output.Capture = capture
			}
			branches = append(branches, format.ToolDisposition{Indeterminate: &format.ToolIndeterminate{Reason: reason, Output: output}})
		}
	}
	for i, v := range branches {
		v.RunID = 1
		v.RequestID = 2
		v.CallID = 3
		v.Diagnostic = ptr("")
		if err := v.Validate(); err != nil {
			t.Fatalf("branch %d: %v", i, err)
		}
		before := v.Clone()
		wire, err := codec.Encode(v, toolLimits())
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Decode[format.ToolDisposition](wire, toolLimits())
		if err != nil {
			t.Fatalf("branch %d roundtrip: %v", i, err)
		}
		if got.RunID != v.RunID || got.RequestID != v.RequestID || got.CallID != v.CallID {
			t.Fatalf("branch %d IDs: got (%d, %d, %d), want (%d, %d, %d)", i, got.RunID, got.RequestID, got.CallID, v.RunID, v.RequestID, v.CallID)
		}
		switch {
		case v.Returned != nil:
			if got.Returned == nil || got.NotInvoked != nil || got.Indeterminate != nil || got.Returned.Status != v.Returned.Status {
				t.Fatalf("branch %d returned branch changed", i)
			}
		case v.NotInvoked != nil:
			if got.Returned != nil || got.NotInvoked == nil || got.Indeterminate != nil || got.NotInvoked.Reason != v.NotInvoked.Reason {
				t.Fatalf("branch %d not-invoked branch changed", i)
			}
		case v.Indeterminate != nil:
			if got.Returned != nil || got.NotInvoked != nil || got.Indeterminate == nil || got.Indeterminate.Reason != v.Indeterminate.Reason {
				t.Fatalf("branch %d indeterminate branch changed", i)
			}
		}
		if !got.Equal(v) {
			t.Fatalf("branch %d roundtrip value changed", i)
		}
		*v.Diagnostic = "changed"
		if v.Returned != nil {
			*v.Returned.Output.Parts[0].Content.Text = "changed"
		}
		if v.NotInvoked != nil {
			v.NotInvoked.Reason = "changed"
		}
		if v.Indeterminate != nil {
			v.Indeterminate.Output.Capture = "changed"
			if len(v.Indeterminate.Output.Parts) != 0 {
				*v.Indeterminate.Output.Parts[0].Content.Text = "changed"
				v.Indeterminate.Output.Parts[1].Content.Media.InlineData[0] = 0
				(*v.Indeterminate.Output.Parts[4].JSON)[0] = 'x'
			}
		}
		clear(wire)
		if !got.Equal(before) {
			t.Fatalf("branch %d aliases", i)
		}
	}
}

func TestToolDispositionEqualDistinguishesIdentityAndMeaning(t *testing.T) {
	returned := format.ToolDisposition{RunID: 1, RequestID: 2, CallID: 3, Returned: &format.ToolReturned{Status: format.ToolReturnSuccess, Output: richToolOutput()}}
	notInvoked := format.ToolDisposition{RunID: 1, RequestID: 2, CallID: 3, NotInvoked: &format.ToolNotInvoked{Reason: format.ToolNotInvokedOther}}
	indeterminateOutput := richToolOutput()
	indeterminateOutput.Capture = format.ToolCaptureTruncated
	indeterminate := format.ToolDisposition{RunID: 1, RequestID: 2, CallID: 3, Indeterminate: &format.ToolIndeterminate{Reason: format.ToolIndeterminateOther, Output: indeterminateOutput}}
	cases := []struct {
		name string
		base format.ToolDisposition
		edit func(*format.ToolDisposition)
	}{
		{"run ID", returned, func(v *format.ToolDisposition) { v.RunID++ }},
		{"request ID", returned, func(v *format.ToolDisposition) { v.RequestID++ }},
		{"call ID", returned, func(v *format.ToolDisposition) { v.CallID++ }},
		{"returned status", returned, func(v *format.ToolDisposition) { v.Returned.Status = format.ToolReturnError }},
		{"not-invoked reason", notInvoked, func(v *format.ToolDisposition) { v.NotInvoked.Reason = format.ToolNotInvokedCancelled }},
		{"indeterminate reason", indeterminate, func(v *format.ToolDisposition) { v.Indeterminate.Reason = format.ToolIndeterminateTimeout }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			other := tc.base.Clone()
			tc.edit(&other)
			if tc.base.Equal(other) || other.Equal(tc.base) {
				t.Fatal("Equal ignored a persisted identity or disposition meaning")
			}
		})
	}
}

func TestToolDispositionMalformed(t *testing.T) {
	returned := &format.ToolReturned{Status: format.ToolReturnSuccess, Output: format.ToolOutput{Capture: format.ToolCaptureComplete}}
	notInvoked := &format.ToolNotInvoked{Reason: format.ToolNotInvokedOther}
	indeterminate := &format.ToolIndeterminate{Reason: format.ToolIndeterminateOther, Output: format.ToolOutput{Capture: format.ToolCaptureOmitted}}
	badUnion := format.ToolOutput{Capture: format.ToolCaptureTruncated, Parts: []format.ToolOutputPart{{Content: &format.ContentPart{Text: ptr("")}, JSON: ptr([]byte("true"))}}}
	cases := []format.ToolDisposition{
		{}, {Returned: returned, NotInvoked: notInvoked}, {Returned: returned, Indeterminate: indeterminate}, {NotInvoked: notInvoked, Indeterminate: indeterminate}, {Returned: returned, NotInvoked: notInvoked, Indeterminate: indeterminate},
		{Returned: &format.ToolReturned{Status: format.ToolReturnSuccess}},
		{Returned: returned, Diagnostic: ptr("\xff")},
		{Indeterminate: &format.ToolIndeterminate{Reason: format.ToolIndeterminateOther, Output: badUnion}},
	}
	for _, unknown := range []string{"", "unknown"} {
		cases = append(cases,
			format.ToolDisposition{Returned: &format.ToolReturned{Status: format.ToolReturnStatus(unknown), Output: returned.Output}},
			format.ToolDisposition{NotInvoked: &format.ToolNotInvoked{Reason: format.ToolNotInvokedReason(unknown)}},
			format.ToolDisposition{Indeterminate: &format.ToolIndeterminate{Reason: format.ToolIndeterminateReason(unknown), Output: indeterminate.Output}},
		)
	}
	for _, capture := range []format.ToolCaptureState{"", format.ToolCaptureComplete, "unknown"} {
		cases = append(cases, format.ToolDisposition{Indeterminate: &format.ToolIndeterminate{Reason: format.ToolIndeterminateOther, Output: format.ToolOutput{Capture: capture}}})
	}
	for i := range 3 {
		v := format.ToolDisposition{RunID: 1, RequestID: 2, CallID: 3, Returned: returned}
		switch i {
		case 0:
			v.RunID = 0
		case 1:
			v.RequestID = 0
		case 2:
			v.CallID = 0
		}
		cases = append(cases, v)
	}
	for i, v := range cases {
		// Only the final three cases intentionally retain a zero ID.
		if i < len(cases)-3 {
			v.RunID = 1
			v.RequestID = 2
			v.CallID = 3
		}
		if v.Validate() == nil {
			t.Fatalf("case %d directly validated", i)
		}
		if out, err := codec.Encode(v, toolLimits()); !errors.Is(err, codec.ErrInvalidData) || out != nil {
			t.Fatalf("case %d encode: %v", i, err)
		}
		wire := v.AppendCBOR(nil)
		if got, err := codec.Decode[format.ToolDisposition](wire, toolLimits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ToolDisposition{}) {
			t.Fatalf("case %d decode: %v", i, err)
		}
		receiver := format.ToolDisposition{RunID: 1, RequestID: 2, CallID: 3, Returned: ptr(returned.Clone())}
		before := receiver.Clone()
		r, err := codec.NewReader(wire, toolLimits())
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || !receiver.Equal(before) {
			t.Fatalf("case %d receiver changed: %v", i, err)
		}
	}
}

func TestToolDispositionDiagnostic(t *testing.T) {
	base := format.ToolDisposition{RunID: 1, RequestID: 2, CallID: 3, NotInvoked: &format.ToolNotInvoked{Reason: format.ToolNotInvokedPreparationFailed}}
	presentEmpty := base.Clone()
	presentEmpty.Diagnostic = ptr("")
	if base.Equal(presentEmpty) {
		t.Fatal("lost present-empty diagnostic")
	}
	for _, tc := range []struct {
		name string
		text *string
		kind error
	}{
		{"absent", nil, nil},
		{"empty", ptr(""), nil},
		{"explanation", ptr("required argument 'path' is missing"), nil},
		{"exact cap", ptr(strings.Repeat("d", 16384)), nil},
		{"over cap", ptr(strings.Repeat("d", 16385)), codec.ErrLimitExceeded},
		{"invalid UTF-8", ptr("\xff"), codec.ErrInvalidData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := base.Clone()
			v.Diagnostic = tc.text
			out, err := codec.Encode(v, toolLimits())
			if tc.kind == nil {
				if err != nil || v.Validate() != nil {
					t.Fatalf("valid diagnostic: %v", err)
				}
				before := v.Clone()
				got, err := codec.Decode[format.ToolDisposition](out, toolLimits())
				if err != nil || !got.Equal(v) {
					t.Fatalf("roundtrip: %v", err)
				}
				if v.Diagnostic != nil {
					*v.Diagnostic = "changed"
				}
				clear(out)
				if !got.Equal(before) {
					t.Fatal("diagnostic aliases input or encoded storage")
				}
				return
			}
			if v.Validate() == nil || !errors.Is(err, tc.kind) || out != nil {
				t.Fatalf("invalid diagnostic encode: %v", err)
			}
			wire := v.AppendCBOR(nil)
			if got, err := codec.Decode[format.ToolDisposition](wire, toolLimits()); !errors.Is(err, tc.kind) || !reflect.DeepEqual(got, format.ToolDisposition{}) {
				t.Fatalf("decode: %v", err)
			}
			receiver := base.Clone()
			receiver.Diagnostic = ptr("retained")
			before := receiver.Clone()
			r, err := codec.NewReader(wire, toolLimits())
			if err != nil {
				t.Fatal(err)
			}
			if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, tc.kind) || !receiver.Equal(before) {
				t.Fatalf("receiver changed: %v", err)
			}
		})
	}
}
