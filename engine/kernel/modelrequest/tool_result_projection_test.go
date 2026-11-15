package modelrequest

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestConstructToolNotInvokedNotices(t *testing.T) {
	for _, tc := range []struct {
		reason session.ToolNotInvokedReason
		notice string
	}{
		{session.ToolNotInvokedUserDenied, "User denied tool execution"},
		{session.ToolNotInvokedPolicyRejected, "Tool execution was denied by policy"},
		{session.ToolNotInvokedUnavailable, "Requested tool was unavailable"},
		{session.ToolNotInvokedPreparationFailed, "Tool invocation could not be prepared"},
		{session.ToolNotInvokedInfrastructureFailed, "Tool was not invoked because of an infrastructure failure"},
		{session.ToolNotInvokedCancelled, "Tool invocation was cancelled before execution"},
		{session.ToolNotInvokedOther, "Tool was not invoked"},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			f := newConstructionFixture(t)
			response := f.response([]session.ResponseItem{{Call: &session.RequestedCall{ProviderCallID: "provider"}}}, nil)
			requireConstruction(t, f.b.AddToolDisposition(response.CallAt(0).ID(), session.ToolDisposition{NotInvoked: &session.ToolNotInvoked{Reason: tc.reason}, Diagnostic: new("private MUST NOT RENDER")}))
			result := f.build(format.ParametersV1{}).Request().Format().Conversation[1].ToolResult
			if result.Outcome != format.ToolOutcomeNotInvoked || len(result.Parts) != 1 || *result.Parts[0].Text != tc.notice {
				t.Fatal("not-invoked notice or invented capture")
			}
			requireConstruction(t, f.b.Discard())
		})
	}
}

func TestConstructToolOutcomeCaptureNotices(t *testing.T) {
	for _, outcome := range []string{"success", "error", "indeterminate"} {
		for _, capture := range []session.ToolCaptureState{session.ToolCaptureComplete, session.ToolCaptureOmitted, session.ToolCaptureTruncated} {
			if outcome == "indeterminate" && capture == session.ToolCaptureComplete {
				continue
			}
			for _, content := range []string{"none", "empty", "whitespace", "text"} {
				if capture == session.ToolCaptureOmitted && content != "none" {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/%s", outcome, capture, content), func(t *testing.T) {
					f := newConstructionFixture(t)
					response := f.response([]session.ResponseItem{{Call: &session.RequestedCall{ProviderCallID: "provider"}}}, nil)
					call := response.CallAt(0).ID()
					requireConstruction(t, f.b.AddToolInvocation(call, session.ToolInvocation{Name: "effective"}))
					f.submit()
					output := session.ToolOutput{Capture: capture}
					text := ""
					switch content {
					case "whitespace":
						text = " \n"
					case "text":
						text = "exact output"
					}
					if content != "none" {
						output.Parts = []session.ToolOutputPart{{Content: &session.ContentPart{Text: new(text)}}}
					}
					disposition := session.ToolDisposition{Diagnostic: new("private MUST NOT RENDER")}
					var want []session.ContentPart
					wantOutcome := format.ToolOutcomeReturnedSuccess
					if outcome == "indeterminate" {
						disposition.Indeterminate = &session.ToolIndeterminate{Reason: session.ToolIndeterminateTimeout, Output: output}
						wantOutcome = format.ToolOutcomeIndeterminate
						want = textParts("Tool outcome is unknown; side effects may have occurred.")
					} else {
						status := session.ToolReturnSuccess
						if outcome == "error" {
							status = session.ToolReturnError
							wantOutcome = format.ToolOutcomeReturnedError
							want = textParts("Tool returned an error.")
						}
						disposition.Returned = &session.ToolReturned{Status: status, Output: output}
					}
					switch capture {
					case session.ToolCaptureComplete:
						if text == "" {
							want = append(want, textParts("Tool returned no output.")...)
						}
					case session.ToolCaptureOmitted:
						want = append(want, textParts("Tool output was not retained.")...)
					case session.ToolCaptureTruncated:
						want = append(want, textParts("Tool output is incomplete.")...)
					}
					if text != "" {
						want = append(want, textParts(text)...)
					}
					requireConstruction(t, f.b.AddToolDisposition(call, disposition))
					got := f.build(format.ParametersV1{}).Request().Format().Conversation[1].ToolResult
					if got.Outcome != wantOutcome || !(format.ContentBlock{Parts: got.Parts}).Equal(format.ContentBlock{Parts: want}) {
						t.Fatalf("outcome/capture rendering: %#v", got)
					}
					requireConstruction(t, f.b.Discard())
				})
			}
		}
	}
}

func TestConstructToolResourcesAndTypedMedia(t *testing.T) {
	f := newConstructionFixture(t)
	response := f.response([]session.ResponseItem{{Call: &session.RequestedCall{ProviderCallID: "provider"}}}, nil)
	call := response.CallAt(0).ID()
	requireConstruction(t, f.b.AddToolInvocation(call, session.ToolInvocation{Name: "effective"}))
	f.submit()
	resource := session.ToolResource{URI: "https://example.invalid/a?x=\"quoted\"", Name: new("line\nname"), Title: new(""), MIMEType: new("image/png")}
	empty := []byte{}
	media := session.ContentPart{Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceInline, MIMEType: "image/png", InlineData: []byte{1, 2, 3}}}
	output := session.ToolOutput{Capture: session.ToolCaptureComplete, Parts: []session.ToolOutputPart{
		{Content: &session.ContentPart{Text: new("")}},
		{JSON: new([]byte(" \n {\"z\": 2, \"a\":1} \t"))},
		{Content: &media},
		{ResourceLink: &resource},
		{Resource: &session.ToolEmbeddedResource{Resource: resource, Text: new("exact body\n\u2028")}},
		{Resource: &session.ToolEmbeddedResource{Resource: resource, Blob: new([]byte{0xff, 0, 42})}},
		{Resource: &session.ToolEmbeddedResource{Resource: session.ToolResource{URI: "urn:empty"}, Blob: &empty}},
		{Resource: &session.ToolEmbeddedResource{Resource: session.ToolResource{URI: "urn:empty-text"}, Text: new("")}},
	}}
	requireConstruction(t, f.b.AddToolDisposition(call, session.ToolDisposition{Returned: &session.ToolReturned{Status: session.ToolReturnSuccess, Output: output}}))
	metadata := "Resource URI: \"https://example.invalid/a?x=\\\"quoted\\\"\"\nName: \"line\\nname\"\nTitle: \"\"\nMIME type: \"image/png\""
	want := textParts(" \n {\"z\": 2, \"a\":1} \t")
	want = append(want, media)
	want = append(want, textParts(metadata, metadata, "exact body\n\u2028", metadata, "Binary body not included in this request.", "Resource URI: \"urn:empty\"", "Binary body not included in this request.", "Resource URI: \"urn:empty-text\"")...)
	built := f.build(format.ParametersV1{})
	result := built.Request().Format().Conversation[1].ToolResult
	if result.Outcome != format.ToolOutcomeReturnedSuccess || !(format.ContentBlock{Parts: result.Parts}).Equal(format.ContentBlock{Parts: want}) {
		t.Fatalf("resource projection mismatch: %#v", result)
	}
	f.submit()
	loaded, err := session.Load(t.Context(), f.h, f.run.HistoryID(), f.policy)
	requireConstruction(t, err)
	again, err := Construct(loaded.Snapshot(), format.ParametersV1{}, testLimits())
	requireConstruction(t, err)
	if again.Request().CID() != built.Request().CID() {
		t.Fatal("resource rendering changed after Load")
	}
	requireConstruction(t, f.b.Discard())
}

func TestConstructBulkCloseoutExplicitDispositionWins(t *testing.T) {
	f := newConstructionFixture(t)
	response := f.response([]session.ResponseItem{{Call: &session.RequestedCall{ProviderCallID: "explicit"}}, {Call: &session.RequestedCall{ProviderCallID: "invoked"}}, {Call: &session.RequestedCall{ProviderCallID: "not invoked"}}}, nil)
	requireConstruction(t, f.b.AddToolInvocation(response.CallAt(1).ID(), session.ToolInvocation{Name: "effective"}))
	requireConstruction(t, f.b.AddToolDisposition(response.CallAt(0).ID(), session.ToolDisposition{NotInvoked: &session.ToolNotInvoked{Reason: session.ToolNotInvokedUserDenied}}))
	requireConstruction(t, f.b.AddRunEnded(f.run, session.RunEnded{Reason: session.RunEndedLimit, BulkToolCloseout: true, Explanation: new("private closeout")}))
	var err error
	f.run, err = f.b.StartRun()
	requireConstruction(t, err)
	got := f.build(format.ParametersV1{}).Request().Format().Conversation
	for i, want := range []struct {
		outcome format.ToolOutcome
		notice  string
	}{
		{format.ToolOutcomeNotInvoked, "User denied tool execution"},
		{format.ToolOutcomeIndeterminate, "Tool outcome is unknown; side effects may have occurred."},
		{format.ToolOutcomeNotInvoked, "Tool was not invoked"},
	} {
		result := got[i+1].ToolResult
		if result.Outcome != want.outcome || len(result.Parts) != 1 || *result.Parts[0].Text != want.notice {
			t.Fatal("bulk closeout invented capture or overwrote explicit disposition")
		}
	}
	f.submit()
	loaded, err := session.Load(t.Context(), f.h, f.run.HistoryID(), f.policy)
	requireConstruction(t, err)
	if _, err := Construct(loaded.Snapshot(), format.ParametersV1{}, testLimits()); err != nil {
		t.Fatal(err)
	}
	requireConstruction(t, f.b.Discard())
}

func TestConstructBoundsExcludeUnrelatedHistory(t *testing.T) {
	f := newConstructionFixture(t)
	_, err := f.b.AddUserInput(f.run, session.UserInput{Parts: textParts(strings.Repeat("unselected", 50000))})
	requireConstruction(t, err)
	requireConstruction(t, f.b.AddEffectiveInput(f.run, session.EffectiveInput{Inputs: []session.InputItem{inline(session.PromptContext, "selected")}}))
	// The output limit is not a cap on retained inputs, reasoning, private data,
	// evidence, provenance or the number of unrelated source records.
	f.response([]session.ResponseItem{{Content: &session.ResponseContent{Kind: session.ResponseContentReasoning, Parts: textParts(strings.Repeat("reasoning", 50000))}}}, &session.OpaqueProviderData{Producer: "producer", Format: "format", Version: 1, Data: bytes.Repeat([]byte{42}, 500000)})
	limits := testLimits()
	limits.MaxBytes = 4096
	limits.MaxStringBytes = 128
	limits.MaxByteStringBytes = 128
	built, err := Construct(f.view(), format.ParametersV1{}, limits)
	requireConstruction(t, err)
	if len(built.Request().Format().Conversation) != 1 {
		t.Fatal("unrelated history rendered")
	}
	requireConstruction(t, f.b.Discard())
}

func TestConstructBoundsAmplificationBeforePayloadCopies(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size, count int
		change      func(*codec.Limits)
	}{
		{"repeated large input", 256 << 10, 200, func(l *codec.Limits) { l.MaxBytes = 1 << 20 }},
		{"conversation count", 1, 2000, func(l *codec.Limits) { l.MaxArrayElements = 100 }},
		{"aggregate nodes", 1, 2000, func(l *codec.Limits) { l.MaxNodes = 100 }},
		{"aggregate declarations", 1, 2000, func(l *codec.Limits) { l.MaxCollectionElements = 100 }},
		{"inline framing", 1, 1, func(l *codec.Limits) { l.MaxArrayElements = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConstructionFixture(t)
			parts := textParts(strings.Repeat("x", tc.size))
			if tc.name == "repeated large input" {
				// Each selected leaf fits the output policy; only the repeated
				// projection exceeds it. No media copy may precede sizing.
				parts = []session.ContentPart{{Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceInline, MIMEType: "image/png", InlineData: bytes.Repeat([]byte{42}, tc.size)}}}
			}
			u, err := f.b.AddUserInput(f.run, session.UserInput{Parts: parts})
			requireConstruction(t, err)
			input := session.EffectiveInput{}
			for range tc.count {
				input.Inputs = append(input.Inputs, session.InputItem{UserInputID: new(u.RunRelative())})
			}
			if tc.name == "inline framing" {
				input.Inputs = []session.InputItem{inline(session.PromptInstructions, "x")}
			}
			requireConstruction(t, f.b.AddEffectiveInput(f.run, input))
			limits := testLimits()
			limits.MaxArrayElements = 4000
			tc.change(&limits)
			view := f.view()
			assertConstructionFailure(t, view, limits, codec.ErrLimitExceeded)
			measurement := testing.Benchmark(func(b *testing.B) {
				for b.Loop() {
					result, err := Construct(view, format.ParametersV1{}, limits)
					if !errors.Is(err, codec.ErrLimitExceeded) || result.Request().CID() != (contentid.CID{}) {
						b.Fatal("expected bounded zero failure")
					}
				}
			})
			if allocated := measurement.AllocedBytesPerOp(); allocated > 128<<10 {
				t.Fatalf("rejected amplified projection allocated %d bytes", allocated)
			}
			requireConstruction(t, f.b.Discard())
		})
	}
}

func TestConstructLimitsAgreeWithGeneratedOutputBudget(t *testing.T) {
	f := newConstructionFixture(t)
	requireConstruction(t, f.b.ReplaceInstructions(f.run.HistoryID(), session.Instructions{Blocks: []session.InstructionBlock{instruction("root")}}))
	requireConstruction(t, f.b.AddEffectiveInput(f.run, session.EffectiveInput{Inputs: []session.InputItem{inline(session.PromptInstructions, "selected")}}))
	response := f.response([]session.ResponseItem{{Call: &session.RequestedCall{ProviderCallID: "original", Arguments: []byte{0xff, 0, '{'}}}}, &session.OpaqueProviderData{Producer: "producer", Format: "format", Version: 1, Data: []byte{1, 2}})
	requireConstruction(t, f.b.AddToolInvocation(response.CallAt(0).ID(), session.ToolInvocation{Name: "effective"}))
	f.submit()
	requireConstruction(t, f.b.AddToolDisposition(response.CallAt(0).ID(), session.ToolDisposition{Returned: &session.ToolReturned{Status: session.ToolReturnSuccess, Output: session.ToolOutput{Capture: session.ToolCaptureComplete, Parts: []session.ToolOutputPart{{ResourceLink: &session.ToolResource{URI: "urn:resource", Name: new("quoted\nname")}}, {Content: &session.ContentPart{Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceInline, MIMEType: "image/png", InlineData: []byte{1, 2, 3}}}}}}}}))
	parameters := format.ParametersV1{PrivateReplay: []format.PrivateReplaySelector{{SourceAPI: "api", SourceModel: "model", Producer: "producer", Format: "format", Version: 1}}}
	full := f.build(parameters)
	view := f.view()
	for _, field := range []string{"bytes", "string", "byte string", "array", "map", "depth", "nodes", "declarations"} {
		t.Run(field, func(t *testing.T) {
			maximum := 160
			if field == "bytes" {
				maximum = 4096
			}
			for bound := 1; bound <= maximum; bound++ {
				limits := testLimits()
				switch field {
				case "bytes":
					limits.MaxBytes = bound
				case "string":
					limits.MaxStringBytes = bound
				case "byte string":
					limits.MaxByteStringBytes = bound
				case "array":
					limits.MaxArrayElements = bound
				case "map":
					limits.MaxMapPairs = bound
				case "depth":
					limits.MaxDepth = bound
				case "nodes":
					limits.MaxNodes = bound
				case "declarations":
					limits.MaxCollectionElements = bound
				}
				wantErr := codec.Check(full.Request().Format(), limits)
				if err := codec.Check(parameters, limits); err != nil {
					wantErr = err
				}
				got, err := Construct(view, parameters, limits)
				if (err == nil) != (wantErr == nil) {
					t.Fatalf("cap %d: Construct %v, generated budget %v", bound, err, wantErr)
				}
				if err == nil && got.Request().CID() != full.Request().CID() {
					t.Fatal("raising limits changed successful bytes")
				}
				if err != nil && (got.Request().CID() != (contentid.CID{}) || !errors.Is(err, codec.ErrLimitExceeded)) {
					t.Fatal("nonzero failed output or unexpected error")
				}
			}
		})
	}
	requireConstruction(t, f.b.Discard())
}
