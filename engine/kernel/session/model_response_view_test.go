package session

import (
	"bytes"
	"reflect"
	"slices"
	"testing"
)

func TestModelResponseViewEmptyAndFailureOutputs(t *testing.T) {
	var response ModelResponseView
	var content ResponseContentView
	var item ResponseItemView
	var call RequestedCallView
	var failure InvocationFailureView
	var ending RunEndedView
	var request ModelRequestView
	if !reflect.DeepEqual(request.Recipe(), ConstructionRecipe{}) {
		t.Fatal("zero recipe")
	}
	if got, ok := request.RetryOf(); ok || got != (ModelRequestID{}) {
		t.Fatal("zero retry")
	}
	if got, ok := response.Usage(); ok || !reflect.DeepEqual(got, InvocationUsage{}) {
		t.Fatal("zero response usage")
	}
	if got, ok := response.Private(); ok || !reflect.DeepEqual(got, OpaqueProviderData{}) {
		t.Fatal("zero response private")
	}
	if got, ok := failure.Evidence(); ok || !reflect.DeepEqual(got, ObservedEvidence{}) {
		t.Fatal("zero failure evidence")
	}
	if got, ok := ending.Capture(); ok || !reflect.DeepEqual(got, CaptureFailureEvidence{}) {
		t.Fatal("zero capture")
	}
	response.Items()(func(ResponseItemView) bool { t.Fatal("zero Items yielded"); return true })
	response.Calls()(func(RequestedCallView) bool { t.Fatal("zero Calls yielded"); return true })
	if response.Terminal() != "" || content.Kind() != "" || content.PartCount() != 0 || call.ID() != (RequestedCallID{}) || call.Name() != "" || call.Arguments() != nil || failure.Class() != "" {
		t.Fatal("zero scalars")
	}
	if got, ok := response.ProviderReason(); ok || got != "" {
		t.Fatal("zero reason")
	}
	if got, ok := response.Evidence(); ok || !reflect.DeepEqual(got, ObservedEvidence{}) {
		t.Fatal("zero evidence")
	}
	if got, ok := failure.Explanation(); ok || got != "" {
		t.Fatal("zero explanation")
	}
	if got, ok := failure.Usage(); ok || !reflect.DeepEqual(got, InvocationUsage{}) {
		t.Fatal("zero usage")
	}
	if got, ok := ending.Explanation(); ok || got != "" {
		t.Fatal("zero ending detail")
	}
	if got, ok := item.Content(); ok || got != (ResponseContentView{}) {
		t.Fatal("zero content branch")
	}
	if got, ok := item.Call(); ok || !reflect.DeepEqual(got, RequestedCall{}) {
		t.Fatal("zero call branch")
	}
	if slices.Collect(content.Parts()) != nil {
		t.Fatal("zero parts")
	}
	for _, access := range []func(){func() { response.ItemAt(-1) }, func() { response.ItemAt(0) }, func() { response.CallAt(-1) }, func() { response.CallAt(0) }, func() { content.PartAt(-1) }, func() { content.PartAt(0) }} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("zero out-of-range did not panic")
				}
			}()
			access()
		}()
	}
	var record RecordView
	if got, ok := record.ModelRequest(); ok || got != (ModelRequestView{}) {
		t.Fatal("zero request accessor")
	}
	if got, ok := record.ModelResponse(); ok || got != (ModelResponseView{}) {
		t.Fatal("zero response accessor")
	}
	if got, ok := record.InvocationFailure(); ok || got != (InvocationFailureView{}) {
		t.Fatal("zero failure accessor")
	}
	if got, ok := record.RunEnded(); ok || got != (RunEndedView{}) {
		t.Fatal("zero ending accessor")
	}
}

func TestModelResponseViewRepresentationOwnershipAndShortCircuit(t *testing.T) {
	s := interactionFixture(t, "pending")
	id := s.state.view.lifecycle.pending
	usage := &InvocationUsage{Input: new(int64(0)), Output: new(int64(1)), CacheRead: new(int64(2)), CacheWrite: new(int64(3)), Reasoning: new(int64(4)), Raw: &OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{0xff, 0}}}
	response := ModelResponse{RequestID: id, Terminal: ResponseTerminalOther, ProviderReason: new(""), Evidence: &ObservedEvidence{Detail: new(""), Correlation: new("opaque")}, Usage: usage, Private: &OpaqueProviderData{Producer: "p", Format: "unknown", Version: 19, Data: []byte{0xff, 0, 1}}, Items: []ResponseItem{
		{Content: &ResponseContent{Kind: ResponseContentOutput, Parts: []ContentPart{{Text: new("")}, {Text: new("  exact\n")}, {Media: &Media{Kind: MediaKindImage, MIMEType: "image/unknown", Source: MediaSourceInline, InlineData: []byte{0xff, 0}}}}}},
		{Call: &RequestedCall{Name: "", Arguments: []byte{}}},
		{Content: &ResponseContent{Kind: ResponseContentReasoning}},
		{Call: &RequestedCall{Name: "unknown", Arguments: []byte{0xff, 0, '{'}}},
		{Content: &ResponseContent{Kind: ResponseContentRefusal, Parts: []ContentPart{}}},
	}}
	expected := response.Clone()
	r := interactionRecord(t)(NewModelResponseRecord(response, s.policy.PayloadLimits))
	// Mutation controls: each operation changes its own representation, but must
	// not change the constructor's captured bytes or any accepted view.
	*response.ProviderReason = "changed"
	*response.Evidence.Detail = "changed"
	*response.Evidence.Correlation = "changed"
	*response.Items[0].Content.Parts[0].Text = "changed"
	response.Items[0].Content.Parts[2].Media.InlineData[0] = 0
	response.Items[3].Call.Arguments[0] = 0
	response.Private.Data[0] = 0
	for _, p := range []*int64{usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite, usage.Reasoning} {
		*p = 99
	}
	usage.Raw.Data[0] = 0
	if response.Equal(expected) {
		t.Fatal("mutation control did not change source")
	}
	interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.add(r, nil)) })
	record := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1)
	view, ok := record.ModelResponse()
	if !ok {
		t.Fatal("response")
	}
	if _, ok := record.ModelRequest(); ok {
		t.Fatal("wrong-kind request")
	}
	if _, ok := record.InvocationFailure(); ok {
		t.Fatal("wrong-kind failure")
	}
	if _, ok := record.RunEnded(); ok {
		t.Fatal("wrong-kind ending")
	}
	if got, ok := view.ProviderReason(); !ok || got != "" {
		t.Fatal("explicit empty reason")
	}
	evidence, _ := view.Evidence()
	if !evidence.Equal(*expected.Evidence) {
		t.Fatal("evidence changed")
	}
	*evidence.Detail = "mutated"
	*evidence.Correlation = "mutated"
	exported, _ := view.Usage()
	if !exported.Equal(*expected.Usage) {
		t.Fatal("usage changed")
	}
	for _, p := range []*int64{exported.Input, exported.Output, exported.CacheRead, exported.CacheWrite, exported.Reasoning} {
		*p = 99
	}
	clear(exported.Raw.Data)
	private, _ := view.Private()
	clear(private.Data)
	dto := record.Format()
	clear(dto.Payload)
	callValue, _ := view.ItemAt(3).Call()
	clear(callValue.Arguments)
	clear(view.CallAt(1).Arguments())
	again, _ := view.Usage()
	if !again.Equal(*expected.Usage) {
		t.Fatal("usage export aliases")
	}
	evidence, _ = view.Evidence()
	if !evidence.Equal(*expected.Evidence) {
		t.Fatal("evidence export aliases")
	}
	private, _ = view.Private()
	if !private.Equal(*expected.Private) {
		t.Fatal("private export aliases")
	}
	if !bytes.Equal(view.CallAt(1).Arguments(), expected.Items[3].Call.Arguments) {
		t.Fatal("argument export aliases")
	}
	c, _ := view.ItemAt(0).Content()
	if text, ok := c.PartAt(0).Text(); !ok || text != "" {
		t.Fatal("empty text normalized")
	}
	if text, ok := c.PartAt(1).Text(); !ok || text != "  exact\n" {
		t.Fatal("text normalized")
	}
	media, _ := c.PartAt(2).Media()
	data, _ := media.InlineData()
	clear(data)
	data, _ = media.InlineData()
	if !bytes.Equal(data, []byte{0xff, 0}) {
		t.Fatal("media export aliases")
	}
	if got, ok := view.ItemAt(0).Call(); ok || !reflect.DeepEqual(got, RequestedCall{}) {
		t.Fatal("content returned call")
	}
	if got, ok := view.ItemAt(1).Content(); ok || got != (ResponseContentView{}) {
		t.Fatal("call returned content")
	}
	for i, want := range []ResponseContentKind{ResponseContentReasoning, ResponseContentRefusal} {
		empty, _ := view.ItemAt(2 + i*2).Content()
		if empty.Kind() != want || empty.PartCount() != 0 {
			t.Fatal("empty content conflated")
		}
	}
	if got, ok := view.ItemAt(1).Call(); !ok || len(got.Arguments) != 0 {
		t.Fatal("empty call arguments")
	}
	// Invoke the sequence directly: a range-break-only check could conceal a
	// producer incorrectly invoking yield again after false.
	visits := 0
	view.Items()(func(ResponseItemView) bool { visits++; return false })
	if visits != 1 {
		t.Fatal("Items ignored false")
	}
	visits = 0
	view.Calls()(func(v RequestedCallView) bool {
		visits++
		if v.ID().Ordinal != 1 {
			t.Fatal("call ordinal")
		}
		return false
	})
	if visits != 1 {
		t.Fatal("Calls ignored false")
	}
	visits = 0
	c.Parts()(func(ContentPartView) bool { visits++; return false })
	if visits != 1 {
		t.Fatal("Parts ignored false")
	}
	if len(slices.Collect(view.Items())) != 5 || len(slices.Collect(view.Calls())) != 2 || len(slices.Collect(c.Parts())) != 3 {
		t.Fatal("short circuit poisoned later traversal")
	}
	for _, access := range []func(){func() { view.ItemAt(-1) }, func() { view.ItemAt(5) }, func() { view.CallAt(-1) }, func() { view.CallAt(2) }, func() { c.PartAt(-1) }, func() { c.PartAt(3) }} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("range check missing")
				}
			}()
			access()
		}()
	}
	interactionReplay(t, s)
}
