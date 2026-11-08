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
	if !reflect.DeepEqual(request.Recipe(), ConstructionRecipeView{}) {
		t.Fatal("zero recipe")
	}
	if got, ok := request.RetryOf(); ok || got != (ModelRequestID{}) {
		t.Fatal("zero retry")
	}
	if got, ok := response.Usage(); ok || got != (InvocationUsageView{}) {
		t.Fatal("zero response usage")
	}
	if got, ok := response.Private(); ok || got != (OpaqueProviderDataView{}) {
		t.Fatal("zero response private")
	}
	if got, ok := failure.Evidence(); ok || got != (ObservedEvidenceView{}) {
		t.Fatal("zero failure evidence")
	}
	if got, ok := ending.Capture(); ok || !reflect.DeepEqual(got, CaptureFailureEvidenceView{}) {
		t.Fatal("zero capture")
	}
	response.Items().All()(func(_ int, _ ResponseItemView) bool { t.Fatal("zero Items yielded"); return true })
	response.Calls()(func(RequestedCallView) bool { t.Fatal("zero Calls yielded"); return true })
	if response.Terminal() != "" || content.Kind() != "" || content.Parts().Len() != 0 || call.ID() != (RequestedCallID{}) || call.Name() != "" || call.Arguments().Copy() != nil || failure.Class() != "" {
		t.Fatal("zero scalars")
	}
	if got, ok := response.ProviderStopReason(); ok || got != "" {
		t.Fatal("zero reason")
	}
	if got, ok := response.Evidence(); ok || got != (ObservedEvidenceView{}) {
		t.Fatal("zero evidence")
	}
	if got, ok := failure.Explanation(); ok || got != "" {
		t.Fatal("zero explanation")
	}
	if got, ok := failure.Usage(); ok || got != (InvocationUsageView{}) {
		t.Fatal("zero usage")
	}
	if got, ok := ending.Explanation(); ok || got != "" {
		t.Fatal("zero ending detail")
	}
	if got, ok := item.Content(); ok || got != (ResponseContentView{}) {
		t.Fatal("zero content branch")
	}
	if got, ok := item.Call(); ok || !reflect.DeepEqual(got, RequestedCallDataView{}) {
		t.Fatal("zero call branch")
	}
	if content.Parts().Len() != 0 {
		t.Fatal("zero parts")
	}
	content.Parts().All()(func(int, ContentPartView) bool { t.Fatal("zero Parts yielded"); return true })
	for _, access := range []func(){func() { response.Items().At(-1) }, func() { response.Items().At(0) }, func() { response.CallAt(-1) }, func() { response.CallAt(0) }, func() { content.Parts().At(-1) }, func() { content.Parts().At(0) }} {
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
	response := ModelResponse{RequestID: id, Terminal: ResponseTerminalOther, ProviderStopReason: new(""), Evidence: &ObservedEvidence{Detail: new(""), Correlation: new("opaque")}, Usage: usage, Private: &OpaqueProviderData{Producer: "p", Format: "unknown", Version: 19, Data: []byte{0xff, 0, 1}}, Items: []ResponseItem{
		{Content: &ResponseContent{Kind: ResponseContentOutput, Parts: []ContentPart{{Text: new("")}, {Text: new("  exact\n")}, {Media: &Media{Kind: MediaKindImage, MIMEType: "image/unknown", Source: MediaSourceInline, InlineData: []byte{0xff, 0}}}}}},
		{Call: &RequestedCall{Name: "", Arguments: []byte{}}},
		{Content: &ResponseContent{Kind: ResponseContentReasoning}},
		{Call: &RequestedCall{Name: "unknown", Arguments: []byte{0xff, 0, '{'}}},
		{Content: &ResponseContent{Kind: ResponseContentRefusal, Parts: []ContentPart{}}},
	}}
	expected := response.Clone()
	r := interactionRecord(t)(newModelResponseRecord(response, s.policy.PayloadLimits))
	// Mutation controls: each operation changes its own representation, but must
	// not change the constructor's captured bytes or any accepted view.
	*response.ProviderStopReason = "changed"
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
	if n := testing.AllocsPerRun(100, func() {
		usage, ok := view.Usage()
		if !ok {
			panic("missing usage")
		}
		for _, counter := range []func() (int64, bool){usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite, usage.Reasoning} {
			if _, ok := counter(); !ok {
				panic("missing usage counter")
			}
		}
		raw, ok := usage.Raw()
		if !ok || raw.Producer() != "p" || raw.Format() != "usage" || raw.Version() != 1 {
			panic("missing raw usage")
		}
		evidence, ok := view.Evidence()
		if !ok {
			panic("missing evidence")
		}
		if _, ok := evidence.Detail(); !ok {
			panic("missing detail")
		}
		if _, ok := evidence.Correlation(); !ok {
			panic("missing correlation")
		}
		private, ok := view.Private()
		if !ok || private.Producer() != "p" || private.Format() != "unknown" || private.Version() != 19 {
			panic("missing private")
		}
	}); n != 0 {
		t.Fatalf("scalar evidence inspection allocated: %g", n)
	}
	if got, ok := view.ProviderStopReason(); !ok || got != "" {
		t.Fatal("explicit empty reason")
	}
	evidence, _ := view.Evidence()
	if detail, ok := evidence.Detail(); !ok || detail != "" {
		t.Fatal("evidence detail changed")
	}
	if correlation, ok := evidence.Correlation(); !ok || correlation != "opaque" {
		t.Fatal("evidence correlation changed")
	}
	usageView, _ := view.Usage()
	for _, counter := range []struct {
		name string
		get  func() (int64, bool)
		want int64
	}{
		{"input", usageView.Input, 0},
		{"output", usageView.Output, 1},
		{"cache read", usageView.CacheRead, 2},
		{"cache write", usageView.CacheWrite, 3},
		{"reasoning", usageView.Reasoning, 4},
	} {
		if got, ok := counter.get(); !ok || got != counter.want {
			t.Fatalf("usage %s = %d, %t", counter.name, got, ok)
		}
	}
	raw, ok := usageView.Raw()
	if !ok || raw.Producer() != "p" || raw.Format() != "usage" || raw.Version() != 1 || !bytes.Equal(raw.Data().Copy(), []byte{0xff, 0}) {
		t.Fatal("usage raw changed")
	}
	private, _ := view.Private()
	if private.Producer() != "p" || private.Format() != "unknown" || private.Version() != 19 || !bytes.Equal(private.Data().Copy(), expected.Private.Data) {
		t.Fatal("private changed")
	}
	data := raw.Data().Copy()
	clear(data)
	data = private.Data().Copy()
	clear(data)
	dto := record.Format()
	clear(dto.Payload)
	callValue, _ := view.Items().At(3).Call()
	clear(callValue.Arguments().Copy())
	clear(view.CallAt(1).Arguments().Copy())
	again, _ := view.Usage()
	raw, _ = again.Raw()
	if !bytes.Equal(raw.Data().Copy(), expected.Usage.Raw.Data) {
		t.Fatal("usage byte export aliases")
	}
	evidence, _ = view.Evidence()
	if detail, ok := evidence.Detail(); !ok || detail != "" {
		t.Fatal("evidence view changed")
	}
	private, _ = view.Private()
	if !bytes.Equal(private.Data().Copy(), expected.Private.Data) {
		t.Fatal("private byte export aliases")
	}
	if !bytes.Equal(view.CallAt(1).Arguments().Copy(), expected.Items[3].Call.Arguments) {
		t.Fatal("argument export aliases")
	}
	c, _ := view.Items().At(0).Content()
	if text, ok := c.Parts().At(0).Text(); !ok || text != "" {
		t.Fatal("empty text normalized")
	}
	if text, ok := c.Parts().At(1).Text(); !ok || text != "  exact\n" {
		t.Fatal("text normalized")
	}
	media, _ := c.Parts().At(2).Media()
	mediaData, _ := media.InlineData()
	clear(mediaData.Copy())
	mediaData, _ = media.InlineData()
	if !bytes.Equal(mediaData.Copy(), []byte{0xff, 0}) {
		t.Fatal("media export aliases")
	}
	if got, ok := view.Items().At(0).Call(); ok || !reflect.DeepEqual(got, RequestedCallDataView{}) {
		t.Fatal("content returned call")
	}
	if got, ok := view.Items().At(1).Content(); ok || got != (ResponseContentView{}) {
		t.Fatal("call returned content")
	}
	for i, want := range []ResponseContentKind{ResponseContentReasoning, ResponseContentRefusal} {
		empty, _ := view.Items().At(2 + i*2).Content()
		if empty.Kind() != want || empty.Parts().Len() != 0 {
			t.Fatal("empty content conflated")
		}
	}
	if got, ok := view.Items().At(1).Call(); !ok || len(got.Arguments().Copy()) != 0 {
		t.Fatal("empty call arguments")
	}
	// Invoke the sequence directly: a range-break-only check could conceal a
	// producer incorrectly invoking yield again after false.
	visits := 0
	view.Items().All()(func(_ int, _ ResponseItemView) bool { visits++; return false })
	if visits != 1 {
		t.Fatal("Items ignored false")
	}
	visits = 0
	view.Calls()(func(v RequestedCallView) bool {
		visits++
		if v.ID().ordinal != 1 {
			t.Fatal("call ordinal")
		}
		return false
	})
	if visits != 1 {
		t.Fatal("Calls ignored false")
	}
	visits = 0
	c.Parts().All()(func(_ int, _ ContentPartView) bool { visits++; return false })
	if visits != 1 {
		t.Fatal("Parts ignored false")
	}
	itemVisits, partVisits := 0, 0
	for range view.Items().All() {
		itemVisits++
	}
	for range c.Parts().All() {
		partVisits++
	}
	if itemVisits != 5 || len(slices.Collect(view.Calls())) != 2 || partVisits != 3 {
		t.Fatal("short circuit poisoned later traversal")
	}
	for _, access := range []func(){func() { view.Items().At(-1) }, func() { view.Items().At(5) }, func() { view.CallAt(-1) }, func() { view.CallAt(2) }, func() { c.Parts().At(-1) }, func() { c.Parts().At(3) }} {
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
