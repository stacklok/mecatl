package format

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

func TestContentPartValidationLayers(t *testing.T) {
	v := ContentPart{Media: &Media{}}
	if v.validateLocal() != nil || v.Validate() == nil {
		t.Fatal("immediate union check must not validate media; public validation must")
	}
	v.Text = new("")
	if err := v.Validate(); err == nil || err.Error() != "content part requires exactly one text or media branch" {
		t.Fatalf("public union error must precede malformed child: %v", err)
	}
}

func TestInvocationUsageValidationLayers(t *testing.T) {
	v := InvocationUsage{Raw: &OpaqueProviderData{}}
	if v.validateLocal() != nil || v.Validate() == nil {
		t.Fatal("immediate counter check must not validate raw evidence; public validation must")
	}
	v.Input = new(int64(-1))
	if err := v.Validate(); err == nil || err.Error() != "usage count must be nonnegative" {
		t.Fatalf("public counter error must precede malformed raw metadata: %v", err)
	}
}

func TestResponseContentValidationLayers(t *testing.T) {
	v := ResponseContent{Kind: ResponseContentOutput, Parts: []ContentPart{{Media: &Media{}}}}
	if v.validateLocal() != nil || v.Validate() == nil {
		t.Fatal("immediate kind check must not validate parts; public validation must")
	}
	v.Kind = "unknown"
	if err := v.Validate(); err == nil || err.Error() != "unknown response content kind" {
		t.Fatalf("public kind error must precede malformed part: %v", err)
	}
}

func TestResponseItemValidationLayers(t *testing.T) {
	for _, v := range []ResponseItem{
		{Content: &ResponseContent{Kind: ResponseContentOutput, Parts: []ContentPart{{}}}},
		{Call: &RequestedCall{Name: "\xff"}},
	} {
		if v.validateLocal() != nil || v.Validate() == nil {
			t.Fatal("immediate union check must not validate children; public validation must")
		}
	}
	v := ResponseItem{Content: &ResponseContent{}, Call: &RequestedCall{Name: "\xff"}}
	if err := v.Validate(); err == nil || err.Error() != "response item requires exactly one branch" {
		t.Fatalf("public union error must precede malformed child: %v", err)
	}
}

func TestCanonicalPayloadValidationLayers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		local  func() error
		public func() error
	}{
		{"input", (UserInput{RunID: 1, ID: 1, Parts: []ContentPart{{}}}).validateLocal, (UserInput{RunID: 1, ID: 1, Parts: []ContentPart{{}}}).Validate},
		{"source", (ContentSource{Description: "source", WorkspaceFile: &WorkspaceFileSource{}}).validateLocal, (ContentSource{Description: "source", WorkspaceFile: &WorkspaceFileSource{}}).Validate},
		{"item", (InputItem{Inline: &AttributedInput{}}).validateLocal, (InputItem{Inline: &AttributedInput{}}).Validate},
		{"attributed", (AttributedInput{Actor: "open/future", Purpose: PromptContext}).validateLocal, (AttributedInput{Actor: "open/future", Purpose: PromptContext}).Validate},
		{"effective", (EffectiveInput{RunID: 1, Inputs: []InputItem{{}}}).validateLocal, (EffectiveInput{RunID: 1, Inputs: []InputItem{{}}}).Validate},
		{"catalog", (ToolCatalog{Tools: []ToolDefinition{{Name: "name"}}}).validateLocal, (ToolCatalog{Tools: []ToolDefinition{{Name: "name"}}}).Validate},
		{"request", (ModelRequest{RunID: 1, ID: 1}).validateLocal, (ModelRequest{RunID: 1, ID: 1}).Validate},
		{"response", (ModelResponse{RunID: 1, RequestID: 1, Terminal: ResponseTerminalNormal, Usage: &InvocationUsage{Input: new(int64(-1))}}).validateLocal, (ModelResponse{RunID: 1, RequestID: 1, Terminal: ResponseTerminalNormal, Usage: &InvocationUsage{Input: new(int64(-1))}}).Validate},
		{"failure", (InvocationFailure{RunID: 1, RequestID: 1, Class: InvocationFailureOther, Evidence: &ObservedEvidence{Detail: new("\xff")}}).validateLocal, (InvocationFailure{RunID: 1, RequestID: 1, Class: InvocationFailureOther, Evidence: &ObservedEvidence{Detail: new("\xff")}}).Validate},
		{"capture", (CaptureFailureEvidence{RequestID: 1, Usage: &InvocationUsage{Raw: &OpaqueProviderData{}}}).validateLocal, (CaptureFailureEvidence{RequestID: 1, Usage: &InvocationUsage{Raw: &OpaqueProviderData{}}}).Validate},
		{"ending", (RunEnded{RunID: 1, Reason: RunEndedCaptureFailed, Capture: &CaptureFailureEvidence{}}).validateLocal, (RunEnded{RunID: 1, Reason: RunEndedCaptureFailed, Capture: &CaptureFailureEvidence{}}).Validate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.local() != nil || tc.public() == nil {
				t.Fatal("immediate codec hook must accept local fields; public validation must reject invalid children")
			}
		})
	}
	if (InstructionBlock{}).Validate() == nil || (Instructions{Blocks: []InstructionBlock{{}}}).Validate() == nil {
		t.Fatal("instruction validation omitted provenance")
	}
}

func TestSharedPayloadCodecBoundsPrecedeParentValidation(t *testing.T) {
	l := codec.Limits{MaxBytes: 1 << 20, MaxStringBytes: 128, MaxByteStringBytes: 4, MaxArrayElements: 32, MaxMapPairs: 32, MaxDepth: 32, MaxNodes: 1024, MaxCollectionElements: 1024}
	// Each parent is malformed, but an earlier child byte limit must win on both
	// codec paths. Recursive public validation deliberately reports the parent first.
	media := &Media{Kind: MediaKindImage, Source: MediaSourceInline, MIMEType: "x", InlineData: make([]byte, 5)}
	part := ContentPart{Text: new(""), Media: media}
	content := ResponseContent{Kind: "unknown", Parts: []ContentPart{{Media: media}}}
	item := ResponseItem{Call: &RequestedCall{Arguments: make([]byte, 5)}, Content: &ResponseContent{Kind: ResponseContentOutput}}
	usage := InvocationUsage{Input: new(int64(-1)), Raw: &OpaqueProviderData{Producer: "p", Format: "f", Version: 1, Data: make([]byte, 5)}}
	for _, tc := range []struct {
		name   string
		value  codec.Marshaler
		decode func([]byte) error
	}{
		{"part", part, func(b []byte) error { _, err := codec.Decode[ContentPart](b, l); return err }},
		{"content", content, func(b []byte) error { _, err := codec.Decode[ResponseContent](b, l); return err }},
		{"item", item, func(b []byte) error { _, err := codec.Decode[ResponseItem](b, l); return err }},
		{"usage", usage, func(b []byte) error { _, err := codec.Decode[InvocationUsage](b, l); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out, err := codec.Encode(tc.value, l); out != nil || !errors.Is(err, codec.ErrLimitExceeded) {
				t.Fatalf("bounded encode: %x, %v", out, err)
			}
			if err := tc.decode(tc.value.AppendCBOR(nil)); !errors.Is(err, codec.ErrLimitExceeded) {
				t.Fatalf("bounded decode: %v", err)
			}
			wide := l
			wide.MaxByteStringBytes = 5
			if out, err := codec.Encode(tc.value, wide); out != nil || !errors.Is(err, codec.ErrInvalidData) {
				t.Fatalf("raised limit must expose invalid parent: %x, %v", out, err)
			}
		})
	}
}
