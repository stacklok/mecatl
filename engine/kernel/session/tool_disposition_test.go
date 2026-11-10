package session

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func toolFactOutput() ToolOutput {
	return ToolOutput{Capture: ToolCaptureComplete, Parts: []ToolOutputPart{
		{Content: &ContentPart{Text: new(" exact text\n")}},
		{Content: &ContentPart{Media: &Media{Kind: MediaKindImage, Source: MediaSourceInline, MIMEType: "image/x", InlineData: []byte{1, 2, 3}}}},
		{JSON: new([]byte(" { \"z\":1e+02, \"z\":null } \n"))},
		{ResourceLink: &ToolResource{URI: "urn:link", Name: new(""), Title: new("title"), MIMEType: new("application/x")}},
		{Resource: &ToolEmbeddedResource{Resource: ToolResource{URI: "urn:text"}, Text: new("body")}},
		{Resource: &ToolEmbeddedResource{Resource: ToolResource{URI: "urn:blob"}, Blob: new([]byte{0xff, 0})}},
		{Resource: &ToolEmbeddedResource{Resource: ToolResource{URI: "urn:empty-text"}, Text: new("")}},
		{Resource: &ToolEmbeddedResource{Resource: ToolResource{URI: "urn:empty-blob"}, Blob: new([]byte(nil))}},
	}}
}

func TestToolDispositionMaterializationAndView(t *testing.T) {
	call := RequestedCallID{requestID: requestID(testHistoryID(t), 7, 9), ordinal: 3}
	for _, kind := range []string{"returned", "not invoked", "indeterminate"} {
		t.Run(kind, func(t *testing.T) {
			v := ToolDisposition{Diagnostic: new("")}
			switch kind {
			case "returned":
				v.Returned = &ToolReturned{Status: ToolReturnError, Output: toolFactOutput()}
			case "not invoked":
				v.NotInvoked = &ToolNotInvoked{Reason: ToolNotInvokedPreparationFailed}
			case "indeterminate":
				output := toolFactOutput()
				output.Capture = ToolCaptureTruncated
				v.Indeterminate = &ToolIndeterminate{Reason: ToolIndeterminateExecutorLost, Output: output}
			}
			r, err := newToolDispositionRecord(v, call, testLimits())
			interactionCheck(t, err)
			want := v.Clone()
			want.RunID, want.RequestID, want.CallID = 7, 9, 3
			wire, err := codec.Encode(want, testLimits())
			interactionCheck(t, err)
			if r.value.Kind != sessionformat.KindToolDisposition || r.value.KindVersion != 1 || !bytes.Equal(wire, r.value.Payload) || !r.toolDisposition.Equal(want) {
				t.Fatal("materialized disposition differs from persisted DTO")
			}
			record := RecordView{scope: testHistoryID(t), record: r}
			view, ok := record.ToolDisposition()
			if !ok || view.CallID() != call {
				t.Fatal("disposition identity")
			}
			if _, ok := record.ToolInvocation(); ok {
				t.Fatal("wrong-kind invocation")
			}
			if text, ok := view.Diagnostic(); !ok || text != "" {
				t.Fatal("present-empty diagnostic")
			}
			returned, ret := view.Returned()
			notInvoked, not := view.NotInvoked()
			indeterminate, ind := view.Indeterminate()
			if ret != (v.Returned != nil) || not != (v.NotInvoked != nil) || ind != (v.Indeterminate != nil) {
				t.Fatal("branch presence")
			}
			if not {
				if notInvoked.Reason() != ToolNotInvokedPreparationFailed {
					t.Fatal("not invoked reason")
				}
				v.NotInvoked.Reason = ToolNotInvokedOther
			} else {
				var output ToolOutputView
				var source *ToolOutput
				if ret {
					if returned.Status() != ToolReturnError {
						t.Fatal("returned status")
					}
					output, source = returned.Output(), &v.Returned.Output
				} else {
					if indeterminate.Reason() != ToolIndeterminateExecutorLost {
						t.Fatal("indeterminate reason")
					}
					output, source = indeterminate.Output(), &v.Indeterminate.Output
				}
				checkToolFactOutputView(t, output, *source)
				*source.Parts[0].Content.Text = "changed"
				source.Parts[1].Content.Media.InlineData[0] = 9
				(*source.Parts[2].JSON)[0] = 'x'
				source.Parts[3].ResourceLink.URI = "urn:changed"
				*source.Parts[4].Resource.Text = "changed"
				(*source.Parts[5].Resource.Blob)[0] = 0
			}
			*v.Diagnostic = "changed"
			if v.RunID != 0 || v.RequestID != 0 || v.CallID != 0 || !r.toolDisposition.Equal(want) {
				t.Fatal("constructor mutated input identity or retained caller aliases")
			}
			decoded, err := decodeMaterialized(record.Format(), testLimits())
			interactionCheck(t, err)
			if !decoded.toolDisposition.Equal(want) {
				t.Fatal("storage decode changed complete disposition")
			}
		})
	}
}

func checkToolFactOutputView(t *testing.T, output ToolOutputView, want ToolOutput) {
	t.Helper()
	if output.Capture() != want.Capture || output.Parts().Len() != 8 {
		t.Fatal("output fields")
	}
	for i, part := range output.Parts().All() {
		content, c := part.Content()
		j, jsonPresent := part.JSON()
		link, l := part.ResourceLink()
		resource, r := part.Resource()
		if c != (want.Parts[i].Content != nil) || jsonPresent != (want.Parts[i].JSON != nil) || l != (want.Parts[i].ResourceLink != nil) || r != (want.Parts[i].Resource != nil) {
			t.Fatal("output branch presence")
		}
		switch i {
		case 0:
			if text, ok := content.Text(); !ok || text != *want.Parts[i].Content.Text {
				t.Fatal("exact text")
			}
		case 1:
			media, ok := content.Media()
			if !ok || media.Kind() != MediaKindImage || media.Source() != MediaSourceInline || media.MIMEType() != "image/x" || !bytes.Equal(media.InlineData().Copy(), []byte{1, 2, 3}) {
				t.Fatal("media content")
			}
		case 2:
			detached := j.Copy()
			if !bytes.Equal(detached, *want.Parts[i].JSON) {
				t.Fatal("exact JSON normalized")
			}
			detached[0] = 'x'
			again, _ := output.Parts().At(i).JSON()
			if !bytes.Equal(again.Copy(), *want.Parts[i].JSON) {
				t.Fatal("JSON Copy exposed backing")
			}
		case 3:
			name, n := link.Name()
			title, tt := link.Title()
			mime, m := link.MIMEType()
			if link.URI() != "urn:link" || !n || name != "" || !tt || title != "title" || !m || mime != "application/x" {
				t.Fatal("resource labels")
			}
		case 4, 6:
			text, ok := resource.Text()
			if !ok || text != *want.Parts[i].Resource.Text || resource.Resource().URI() != want.Parts[i].Resource.Resource.URI {
				t.Fatal("embedded text")
			}
			if _, ok := resource.Blob(); ok {
				t.Fatal("text has blob")
			}
		case 5, 7:
			blob, ok := resource.Blob()
			if !ok || !bytes.Equal(blob.Copy(), *want.Parts[i].Resource.Blob) || resource.Resource().URI() != want.Parts[i].Resource.Resource.URI {
				t.Fatal("embedded blob/present empty")
			}
			if _, ok := resource.Text(); ok {
				t.Fatal("blob has text")
			}
			if i == 5 {
				detached := blob.Copy()
				detached[0] = 0
				if !bytes.Equal(blob.Copy(), *want.Parts[i].Resource.Blob) {
					t.Fatal("Blob Copy exposed backing")
				}
			}
		}
	}
	if n := testing.AllocsPerRun(100, func() {
		for i := 0; i < output.Parts().Len(); i++ {
			part := output.Parts().At(i)
			part.Content()
			part.JSON()
			part.ResourceLink()
			part.Resource()
		}
	}); n != 0 {
		t.Fatalf("direct output navigation allocated: %g", n)
	}
}

func TestToolOutputZeroAndEmptyViews(t *testing.T) {
	var part ToolOutputPartView
	if v, ok := part.Content(); ok || v != (ContentPartView{}) {
		t.Fatal("zero content")
	}
	if v, ok := part.JSON(); ok || v.Copy() != nil {
		t.Fatal("zero JSON")
	}
	if v, ok := part.ResourceLink(); ok || v.URI() != "" {
		t.Fatal("zero resource link")
	}
	resource, ok := part.Resource()
	if ok || resource.Resource().URI() != "" {
		t.Fatal("zero embedded resource")
	}
	if v, ok := resource.Text(); ok || v != "" {
		t.Fatal("zero text")
	}
	if v, ok := resource.Blob(); ok || v.Copy() != nil {
		t.Fatal("zero blob")
	}
	for _, label := range []func() (string, bool){resource.Resource().Name, resource.Resource().Title, resource.Resource().MIMEType} {
		if text, ok := label(); ok || text != "" {
			t.Fatal("zero resource label")
		}
	}
	call := RequestedCallID{requestID: requestID(testHistoryID(t), 1, 1), ordinal: 1}
	for _, v := range []ToolDisposition{
		{Returned: &ToolReturned{Status: ToolReturnSuccess, Output: ToolOutput{Capture: ToolCaptureComplete}}},
		{Indeterminate: &ToolIndeterminate{Reason: ToolIndeterminateTimeout, Output: ToolOutput{Capture: ToolCaptureOmitted}}},
	} {
		r, err := newToolDispositionRecord(v, call, testLimits())
		interactionCheck(t, err)
		view, _ := (RecordView{scope: testHistoryID(t), record: r}).ToolDisposition()
		if text, ok := view.Diagnostic(); ok || text != "" {
			t.Fatal("absent diagnostic became present empty")
		}
		if ret, ok := view.Returned(); ok {
			if ret.Output().Capture() != ToolCaptureComplete || ret.Output().Parts().Len() != 0 {
				t.Fatal("complete empty output")
			}
		} else {
			ind, ok := view.Indeterminate()
			if !ok || ind.Output().Capture() != ToolCaptureOmitted || ind.Output().Parts().Len() != 0 {
				t.Fatal("omitted empty output")
			}
		}
	}
}

func TestToolDispositionMaterializationBounds(t *testing.T) {
	call := RequestedCallID{requestID: requestID(testHistoryID(t), 1, 1), ordinal: 1}
	for _, tc := range []struct {
		name string
		v    ToolDisposition
		want error
	}{
		{"empty union", ToolDisposition{}, codec.ErrInvalidData},
		{"two branches", ToolDisposition{Returned: &ToolReturned{Status: ToolReturnSuccess, Output: ToolOutput{Capture: ToolCaptureComplete}}, NotInvoked: &ToolNotInvoked{Reason: ToolNotInvokedOther}}, codec.ErrInvalidData},
		{"parts before child validation", ToolDisposition{Returned: &ToolReturned{Status: ToolReturnSuccess, Output: ToolOutput{Capture: ToolCaptureComplete, Parts: make([]ToolOutputPart, 17)}}}, codec.ErrLimitExceeded},
		{"JSON size before syntax", ToolDisposition{Returned: &ToolReturned{Status: ToolReturnSuccess, Output: ToolOutput{Capture: ToolCaptureComplete, Parts: []ToolOutputPart{{JSON: new(make([]byte, 1025))}}}}}, codec.ErrLimitExceeded},
		{"JSON syntax", ToolDisposition{Returned: &ToolReturned{Status: ToolReturnSuccess, Output: ToolOutput{Capture: ToolCaptureComplete, Parts: []ToolOutputPart{{JSON: new([]byte("{"))}}}}}, codec.ErrInvalidData},
		{"complete indeterminate", ToolDisposition{Indeterminate: &ToolIndeterminate{Reason: ToolIndeterminateOther, Output: ToolOutput{Capture: ToolCaptureComplete}}}, codec.ErrInvalidData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newToolDispositionRecord(tc.v, call, testLimits())
			if r != nil || !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}
