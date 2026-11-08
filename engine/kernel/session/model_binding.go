package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ModelBinding retains declared logical preparation choices, not observed upstream
// identity. ReplaceModelBinding takes history scope separately. Assignment shares
// exact JSON Settings; Clone detaches it. Zero is invalid.
type ModelBinding = sessionformat.ModelBinding

// newModelBindingRecord encodes an owned replacement under explicit limits.
// Failure returns nil. Construction proves no lifecycle admission.
func newModelBindingRecord(v ModelBinding, limits codec.Limits) (*materializedRecord, error) {
	b := newValueBudget(limits)
	b.text(v.Route)
	b.text(v.API)
	b.text(v.Model)
	b.bytes(len(v.Settings))
	if b.err != nil {
		return nil, b.err
	}
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindModelBinding, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// ModelBindingView retains immutable accepted backing storage, including its
// record. Capture and navigation cost O(1); zero has empty fields. Settings
// borrows retained backing; Copy detaches only those exact JSON bytes.
type ModelBindingView struct {
	scope historyid.HistoryID
	value *sessionformat.ModelBinding
}

// HistoryID returns retained scope, or zero for zero.
func (v ModelBindingView) HistoryID() historyid.HistoryID { return v.scope }
