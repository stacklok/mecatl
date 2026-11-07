package session

import (
	"bytes"
	"iter"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ResponseContentKind selects one canonical content meaning. Empty and unknown
// values are rejected.
type ResponseContentKind string

const (
	// ResponseContentOutput retains ordinary model output.
	ResponseContentOutput ResponseContentKind = sessionformat.ResponseContentOutput
	// ResponseContentReasoning retains readable model reasoning.
	ResponseContentReasoning ResponseContentKind = sessionformat.ResponseContentReasoning
	// ResponseContentRefusal retains a model refusal.
	ResponseContentRefusal ResponseContentKind = sessionformat.ResponseContentRefusal
)

// ResponseTerminalClass is the closed normalized terminal provider class. Empty
// and unknown values are rejected, unlike the provider-owned stop reason.
type ResponseTerminalClass string

const (
	// ResponseTerminalNormal identifies a normal terminal response.
	ResponseTerminalNormal ResponseTerminalClass = sessionformat.ResponseTerminalNormal
	// ResponseTerminalLimit identifies a provider limit terminal response.
	ResponseTerminalLimit ResponseTerminalClass = sessionformat.ResponseTerminalLimit
	// ResponseTerminalRefusal identifies a provider refusal terminal response.
	ResponseTerminalRefusal ResponseTerminalClass = sessionformat.ResponseTerminalRefusal
	// ResponseTerminalError identifies a provider error terminal response.
	ResponseTerminalError ResponseTerminalClass = sessionformat.ResponseTerminalError
	// ResponseTerminalCancelled identifies a provider cancellation terminal response.
	ResponseTerminalCancelled ResponseTerminalClass = sessionformat.ResponseTerminalCancelled
	// ResponseTerminalOther identifies another terminal response.
	ResponseTerminalOther ResponseTerminalClass = sessionformat.ResponseTerminalOther
)

// RequestedCall is one complete requested call. Name and Arguments are retained
// exactly: empty/unknown names and invalid/empty JSON are valid. Clone detaches bytes.
type RequestedCall struct {
	Name      string
	Arguments []byte
}

// Validate checks UTF-8 call-name syntax only.
func (v RequestedCall) Validate() error { return v.toFormat().Validate() }

// Clone detaches exact argument bytes.
func (v RequestedCall) Clone() RequestedCall { v.Arguments = bytes.Clone(v.Arguments); return v }

// Equal compares the exact call name and arguments.
func (v RequestedCall) Equal(other RequestedCall) bool { return v.toFormat().Equal(other.toFormat()) }
func (v RequestedCall) toFormat() sessionformat.RequestedCall {
	return sessionformat.RequestedCall{Name: v.Name, Arguments: v.Arguments}
}

// ResponseContent is an explicitly present output, reasoning, or refusal item.
// Parts preserve order and may be empty. Assignment borrows parts; Clone detaches them.
type ResponseContent struct {
	Kind  ResponseContentKind
	Parts []ContentPart
}

// Validate checks content kind and all ordered parts.
func (v ResponseContent) Validate() error {
	if err := v.toFormat().Validate(); err != nil {
		return err
	}
	return validateParts(v.Parts)
}

// Clone detaches ordered content parts.
func (v ResponseContent) Clone() ResponseContent {
	return responseContentFromFormat(v.toFormat().Clone())
}

// Equal compares content kind and ordered parts.
func (v ResponseContent) Equal(other ResponseContent) bool {
	return v.toFormat().Equal(other.toFormat())
}
func (v ResponseContent) toFormat() sessionformat.ResponseContent {
	return sessionformat.ResponseContent{Kind: string(v.Kind), Parts: partsToFormat(v.Parts)}
}
func responseContentFromFormat(v sessionformat.ResponseContent) ResponseContent {
	return ResponseContent{Kind: ResponseContentKind(v.Kind), Parts: partsFromFormat(v.Parts)}
}

// ResponseItem preserves response order and has exactly one content or call branch.
type ResponseItem struct {
	Content *ResponseContent
	Call    *RequestedCall
}

// Validate checks branch exclusivity and the selected branch.
func (v ResponseItem) Validate() error {
	if err := v.toFormat().Validate(); err != nil {
		return err
	}
	if v.Content != nil {
		return v.Content.Validate()
	}
	return v.Call.Validate()
}

// Clone detaches the selected branch.
func (v ResponseItem) Clone() ResponseItem {
	out := v
	if v.Content != nil {
		x := v.Content.Clone()
		out.Content = &x
	}
	if v.Call != nil {
		x := v.Call.Clone()
		out.Call = &x
	}
	return out
}

// Equal compares branch presence and exact branch values.
func (v ResponseItem) Equal(other ResponseItem) bool { return v.toFormat().Equal(other.toFormat()) }
func (v ResponseItem) toFormat() sessionformat.ResponseItem {
	out := sessionformat.ResponseItem{}
	if v.Content != nil {
		x := v.Content.toFormat()
		out.Content = &x
	}
	if v.Call != nil {
		x := v.Call.toFormat()
		out.Call = &x
	}
	return out
}

// ModelResponse is one atomic terminal response for RequestID. Items may be empty;
// Private is one response-wide provider envelope. Assignment borrows nested storage;
// Clone detaches it. ProviderStopReason nil is absent and distinct from explicit empty.
type ModelResponse struct {
	RequestID ModelRequestID
	Terminal  ResponseTerminalClass
	// ProviderStopReason retains the provider's original stop/finish reason, not
	// reasoning content or a reasoning summary. Any UTF-8 value is allowed; nil
	// is absent and differs from an explicitly empty string.
	ProviderStopReason *string
	Items              []ResponseItem
	Evidence           *ObservedEvidence
	Usage              *InvocationUsage
	Private            *OpaqueProviderData
}

// Validate checks local response structure without acceptance lookup.
func (v ModelResponse) Validate() error {
	if err := v.RequestID.Validate(); err != nil {
		return err
	}
	if err := v.toFormat().Validate(); err != nil {
		return err
	}
	for _, item := range v.Items {
		if err := item.Validate(); err != nil {
			return err
		}
	}
	if v.Evidence != nil {
		if err := v.Evidence.Validate(); err != nil {
			return err
		}
	}
	if v.Usage != nil {
		if err := v.Usage.Validate(); err != nil {
			return err
		}
	}
	if v.Private != nil {
		return v.Private.Validate()
	}
	return nil
}

// Clone detaches every mutable response field.
func (v ModelResponse) Clone() ModelResponse {
	out := v
	if v.ProviderStopReason != nil {
		s := *v.ProviderStopReason
		out.ProviderStopReason = &s
	}
	if v.Items != nil {
		out.Items = make([]ResponseItem, len(v.Items))
		for i := range v.Items {
			out.Items[i] = v.Items[i].Clone()
		}
	}
	if v.Evidence != nil {
		x := v.Evidence.Clone()
		out.Evidence = &x
	}
	if v.Usage != nil {
		x := v.Usage.Clone()
		out.Usage = &x
	}
	if v.Private != nil {
		x := v.Private.Clone()
		out.Private = &x
	}
	return out
}

// Equal compares the full scoped request identity and retained response values.
func (v ModelResponse) Equal(other ModelResponse) bool {
	return v.RequestID == other.RequestID && v.toFormat().Equal(other.toFormat())
}
func (v ModelResponse) toFormat() sessionformat.ModelResponse {
	out := sessionformat.ModelResponse{RunID: v.RequestID.runID.ordinal, RequestID: v.RequestID.requestOrdinal, Terminal: string(v.Terminal), ProviderStopReason: v.ProviderStopReason, Items: responseItemsToFormat(v.Items), Evidence: evidenceToFormat(v.Evidence), Usage: usageToFormat(v.Usage), Private: opaqueToFormat(v.Private)}
	return out
}
func responseItemsToFormat(v []ResponseItem) []sessionformat.ResponseItem {
	if v == nil {
		return nil
	}
	out := make([]sessionformat.ResponseItem, len(v))
	for i := range v {
		out[i] = v[i].toFormat()
	}
	return out
}
func (v ModelResponse) checkSize(b *valueBudget) {
	b.text(string(v.Terminal))
	checkEvidenceSize(v.Evidence, b)
	checkUsageSize(v.Usage, b)
	checkOpaqueSize(v.Private, b)
	if v.ProviderStopReason != nil {
		b.pointer()
		b.text(*v.ProviderStopReason)
	}
	b.collection(len(v.Items))
	if b.err != nil {
		return
	}
	for _, item := range v.Items {
		if item.Content != nil {
			b.pointer()
			b.text(string(item.Content.Kind))
			checkPartsSize(item.Content.Parts, b)
		}
		if item.Call != nil {
			b.pointer()
			b.text(item.Call.Name)
			b.bytes(len(item.Call.Arguments))
		}
	}
}

// NewModelResponseRecord owns a complete response, never a truncated subset.
// Sizing precedes item/part conversion and allocation; failure returns zero.
func NewModelResponseRecord(v ModelResponse, limits codec.Limits) (Record, error) {
	if err := v.RequestID.Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	v.checkSize(&b)
	if b.err != nil {
		return Record{}, b.err
	}
	payload, err := codec.Encode(v.toFormat(), limits)
	if err != nil {
		return Record{}, err
	}
	var calls uint64
	for _, item := range v.Items {
		if item.Call != nil {
			calls++
		}
	}
	return Record{scope: v.RequestID.runID.historyID, outcome: v.RequestID, calls: calls, value: format.Record{Kind: sessionformat.KindModelResponse, KindVersion: sessionformat.Version, Payload: payload}}, nil
}

// ModelResponseView is an immutable complete response. Zero has no identity or
// items. Item navigation is constant-time; call lookup scans response order.
// Evidence and byte exports detach storage.
type ModelResponseView struct {
	scope historyid.HistoryID
	value *sessionformat.ModelResponse
}

// RequestID returns the full accepted request reference, or zero for zero.
func (v ModelResponseView) RequestID() ModelRequestID {
	if v.value == nil {
		return ModelRequestID{}
	}
	return requestID(v.scope, v.value.RunID, v.value.RequestID)
}

// Terminal returns the normalized provider terminal class, or empty for zero.
func (v ModelResponseView) Terminal() ResponseTerminalClass {
	if v.value == nil {
		return ""
	}
	return ResponseTerminalClass(v.value.Terminal)
}

// ProviderStopReason returns the original provider-owned stop/finish reason, not
// reasoning content. It preserves absence versus explicitly empty.
func (v ModelResponseView) ProviderStopReason() (string, bool) {
	if v.value == nil || v.value.ProviderStopReason == nil {
		return "", false
	}
	return *v.value.ProviderStopReason, true
}

// Evidence returns detached safe observed evidence, preserving optional presence.
func (v ModelResponseView) Evidence() (ObservedEvidence, bool) {
	if v.value == nil || v.value.Evidence == nil {
		return ObservedEvidence{}, false
	}
	return evidenceFromFormat(v.value.Evidence).Clone(), true
}

// Usage returns detached reported counters and raw usage-only evidence.
func (v ModelResponseView) Usage() (InvocationUsage, bool) {
	if v.value == nil || v.value.Usage == nil {
		return InvocationUsage{}, false
	}
	return usageFromFormat(v.value.Usage).Clone(), true
}

// Private returns detached response-wide private evidence, not interpretation.
func (v ModelResponseView) Private() (OpaqueProviderData, bool) {
	if v.value == nil || v.value.Private == nil {
		return OpaqueProviderData{}, false
	}
	return opaqueFromFormat(v.value.Private).Clone(), true
}

// ItemCount returns the ordered canonical item count, or zero for zero.
func (v ModelResponseView) ItemCount() int {
	if v.value == nil {
		return 0
	}
	return len(v.value.Items)
}

// ItemAt returns a pinned item; it panics outside [0, ItemCount()).
func (v ModelResponseView) ItemAt(i int) ResponseItemView {
	return ResponseItemView{value: &v.value.Items[i]}
}

// Items yields pinned canonical items in response order, including empty content.
func (v ModelResponseView) Items() iter.Seq[ResponseItemView] {
	return func(yield func(ResponseItemView) bool) {
		for i := 0; i < v.ItemCount(); i++ {
			if !yield(v.ItemAt(i)) {
				return
			}
		}
	}
}

// CallCount derives the number of complete calls, independent of content items.
// It scans the response; no redundant call identities are stored in DTOs.
func (v ModelResponseView) CallCount() int {
	n := 0
	for i := 0; i < v.ItemCount(); i++ {
		if v.value.Items[i].Call != nil {
			n++
		}
	}
	return n
}

// CallAt returns the ith call with its derived contiguous call identity. It scans
// response order and panics outside [0, CallCount()).
func (v ModelResponseView) CallAt(i int) RequestedCallView {
	if i >= 0 {
		n := 0
		for j := 0; j < v.ItemCount(); j++ {
			if c := v.value.Items[j].Call; c != nil {
				if n == i {
					return RequestedCallView{id: RequestedCallID{requestID: v.RequestID(), ordinal: uint64(n) + 1}, value: c}
				}
				n++
			}
		}
	}
	panic("call index out of range")
}

// Calls yields complete calls with IDs derived from call order, not item order.
func (v ModelResponseView) Calls() iter.Seq[RequestedCallView] {
	return func(yield func(RequestedCallView) bool) {
		var ordinal uint64
		for i := 0; i < v.ItemCount(); i++ {
			if c := v.value.Items[i].Call; c != nil {
				ordinal++
				if !yield(RequestedCallView{id: RequestedCallID{requestID: v.RequestID(), ordinal: ordinal}, value: c}) {
					return
				}
			}
		}
	}
}

// ResponseItemView is a pinned union handle. Zero has neither branch.
type ResponseItemView struct{ value *sessionformat.ResponseItem }

// Content returns a pinned content branch only when present.
func (v ResponseItemView) Content() (ResponseContentView, bool) {
	if v.value == nil || v.value.Content == nil {
		return ResponseContentView{}, false
	}
	return ResponseContentView{value: v.value.Content}, true
}

// Call returns detached call data only when present. Scoped identities are exposed
// by ModelResponseView.Calls, whose ordinal counts calls rather than items.
func (v ResponseItemView) Call() (RequestedCall, bool) {
	if v.value == nil || v.value.Call == nil {
		return RequestedCall{}, false
	}
	return RequestedCall{Name: v.value.Call.Name, Arguments: bytes.Clone(v.value.Call.Arguments)}, true
}

// ResponseContentView is immutable content with ordered parts. Zero is empty.
type ResponseContentView struct {
	value *sessionformat.ResponseContent
}

// Kind returns the content meaning, or empty for zero.
func (v ResponseContentView) Kind() ResponseContentKind {
	if v.value == nil {
		return ""
	}
	return ResponseContentKind(v.value.Kind)
}

// PartCount returns the ordered part count, or zero for zero.
func (v ResponseContentView) PartCount() int {
	if v.value == nil {
		return 0
	}
	return len(v.value.Parts)
}

// PartAt returns a pinned part; it panics outside [0, PartCount()).
func (v ResponseContentView) PartAt(i int) ContentPartView {
	return ContentPartView{value: &v.value.Parts[i]}
}

// Parts yields pinned parts in canonical order.
func (v ResponseContentView) Parts() iter.Seq[ContentPartView] {
	return func(yield func(ContentPartView) bool) {
		for i := 0; i < v.PartCount(); i++ {
			if !yield(v.PartAt(i)) {
				return
			}
		}
	}
}

// RequestedCallView is an immutable complete-call occurrence. Zero has zero ID.
type RequestedCallView struct {
	id    RequestedCallID
	value *sessionformat.RequestedCall
}

// ID returns the scoped call identity, derived from accepted call order.
func (v RequestedCallView) ID() RequestedCallID { return v.id }

// Name returns exact UTF-8 name data, including empty/unknown names.
func (v RequestedCallView) Name() string {
	if v.value == nil {
		return ""
	}
	return v.value.Name
}

// Arguments returns detached exact argument bytes, including invalid JSON.
func (v RequestedCallView) Arguments() []byte {
	if v.value == nil {
		return nil
	}
	return bytes.Clone(v.value.Arguments)
}
