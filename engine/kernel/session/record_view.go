package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// acceptedRecord owns the encoded envelope record and exactly one decoded DTO.
// It is never mutated after staging; indexes reference this same backing fact.
type acceptedRecord struct {
	value        format.Record
	created      sessionformat.Created
	input        sessionformat.UserInput
	started      sessionformat.RunStarted
	ended        sessionformat.RunEnded
	request      sessionformat.ModelRequest
	response     sessionformat.ModelResponse
	failure      sessionformat.InvocationFailure
	instructions sessionformat.Instructions
	catalog      sessionformat.ToolCatalog
	binding      sessionformat.ModelBinding
	effective    sessionformat.EffectiveInput
}

// decodeAccepted decodes once and detaches encoded caller storage. Generated
// decoding owns nested content; neither DTO nor encoded bytes escape without copying.
func decodeAccepted(v format.Record, limits codec.Limits) (*acceptedRecord, error) {
	if !supportedRecord(v.Kind, v.KindVersion) {
		return nil, ErrUnsupportedRecord
	}
	r := &acceptedRecord{}
	var err error
	switch v.Kind {
	case sessionformat.KindCreated:
		r.created, err = codec.Decode[sessionformat.Created](v.Payload, limits)
	case sessionformat.KindUserInput:
		r.input, err = codec.Decode[sessionformat.UserInput](v.Payload, limits)
	case sessionformat.KindEffectiveInput:
		r.effective, err = codec.Decode[sessionformat.EffectiveInput](v.Payload, limits)
	case sessionformat.KindInstructions:
		r.instructions, err = codec.Decode[sessionformat.Instructions](v.Payload, limits)
	case sessionformat.KindToolCatalog:
		r.catalog, err = codec.Decode[sessionformat.ToolCatalog](v.Payload, limits)
	case sessionformat.KindModelBinding:
		r.binding, err = codec.Decode[sessionformat.ModelBinding](v.Payload, limits)
	case sessionformat.KindRunStarted:
		r.started, err = codec.Decode[sessionformat.RunStarted](v.Payload, limits)
	case sessionformat.KindRunEnded:
		r.ended, err = codec.Decode[sessionformat.RunEnded](v.Payload, limits)
	case sessionformat.KindModelRequest:
		r.request, err = codec.Decode[sessionformat.ModelRequest](v.Payload, limits)
	case sessionformat.KindModelResponse:
		r.response, err = codec.Decode[sessionformat.ModelResponse](v.Payload, limits)
	case sessionformat.KindInvocationFailure:
		r.failure, err = codec.Decode[sessionformat.InvocationFailure](v.Payload, limits)
	default:
		return nil, ErrUnsupportedRecord
	}
	if err != nil {
		return nil, err
	}
	r.value = v.Clone()
	return r, nil
}

// metadata projects the lifecycle scalars and effective contribution needed by
// the shared reducer. All backing storage is already private and immutable.
func (r *acceptedRecord) metadata(scope historyid.HistoryID) Record {
	out := Record{scope: scope, value: r.value,
		userInput:      UserInputID{runID: RunID{historyID: scope, ordinal: r.input.RunID}, ordinal: r.input.ID},
		runStarted:     r.started,
		runEnded:       endedFromFormat(scope, r.ended),
		modelRequest:   requestFromFormat(scope, r.request),
		effectiveInput: r.effective,
	}
	switch r.value.Kind {
	case sessionformat.KindModelResponse:
		out.outcome = requestID(scope, r.response.RunID, r.response.RequestID)
		for _, item := range r.response.Items {
			if item.Call != nil {
				out.calls++
			}
		}
	case sessionformat.KindInvocationFailure:
		out.outcome = requestID(scope, r.failure.RunID, r.failure.RequestID)
	}
	return out
}

// RecordView is an immutable accepted fact. Zero has no kind; all typed accesses
// return false. Wrong-kind access is distinct from a valid zero-field Created DTO.
type RecordView struct {
	scope  historyid.HistoryID
	record *acceptedRecord
}

// Kind returns the record kind, or empty for zero.
func (v RecordView) Kind() string {
	if v.record == nil {
		return ""
	}
	return v.record.value.Kind
}

// KindVersion returns the payload version, or zero for zero.
func (v RecordView) KindVersion() int64 {
	if v.record == nil {
		return 0
	}
	return v.record.value.KindVersion
}

// Format exports a detached history record DTO, or zero for zero.
func (v RecordView) Format() format.Record {
	if v.record == nil {
		return format.Record{}
	}
	return v.record.value.Clone()
}

// Created returns the scoped creation fact only for the creation kind.
func (v RecordView) Created() (Created, bool) {
	if v.Kind() != sessionformat.KindCreated {
		return Created{}, false
	}
	return Created{HistoryID: v.scope}, true
}

// UserInput returns an immutable user-submission handle only for that kind.
func (v RecordView) UserInput() (UserInputView, bool) {
	if v.Kind() != sessionformat.KindUserInput {
		return UserInputView{}, false
	}
	return UserInputView{scope: v.scope, value: &v.record.input}, true
}

// RunStarted returns the scoped start fact only for the start kind.
func (v RecordView) RunStarted() (RunStarted, bool) {
	if v.Kind() != sessionformat.KindRunStarted {
		return RunStarted{}, false
	}
	return RunStarted{ID: RunID{historyID: v.scope, ordinal: v.record.started.ID}}, true
}

// RunEnded returns an immutable ending handle only for the ending kind.
func (v RecordView) RunEnded() (RunEndedView, bool) {
	if v.record == nil || v.record.value.Kind != sessionformat.KindRunEnded {
		return RunEndedView{}, false
	}
	return RunEndedView{scope: v.scope, value: &v.record.ended}, true
}

// ModelRequest returns an immutable invocation commitment handle for that kind.
func (v RecordView) ModelRequest() (ModelRequestView, bool) {
	if v.record == nil || v.record.value.Kind != sessionformat.KindModelRequest {
		return ModelRequestView{}, false
	}
	return ModelRequestView{scope: v.scope, value: &v.record.request}, true
}

// ModelResponse returns an immutable complete-response handle for that kind.
func (v RecordView) ModelResponse() (ModelResponseView, bool) {
	if v.record == nil || v.record.value.Kind != sessionformat.KindModelResponse {
		return ModelResponseView{}, false
	}
	return ModelResponseView{scope: v.scope, value: &v.record.response}, true
}

// InvocationFailure returns an immutable observed-failure handle for that kind.
func (v RecordView) InvocationFailure() (InvocationFailureView, bool) {
	if v.record == nil || v.record.value.Kind != sessionformat.KindInvocationFailure {
		return InvocationFailureView{}, false
	}
	return InvocationFailureView{scope: v.scope, value: &v.record.failure}, true
}

// Instructions returns a replacement handle only for the instructions kind.
func (v RecordView) Instructions() (InstructionsView, bool) {
	if v.Kind() != sessionformat.KindInstructions {
		return InstructionsView{}, false
	}
	return InstructionsView{scope: v.scope, value: &v.record.instructions}, true
}

// ToolCatalog returns a replacement handle only for the catalog kind.
func (v RecordView) ToolCatalog() (ToolCatalogView, bool) {
	if v.Kind() != sessionformat.KindToolCatalog {
		return ToolCatalogView{}, false
	}
	return ToolCatalogView{scope: v.scope, value: &v.record.catalog}, true
}

// ModelBinding returns a replacement handle only for the binding kind.
func (v RecordView) ModelBinding() (ModelBindingView, bool) {
	if v.Kind() != sessionformat.KindModelBinding {
		return ModelBindingView{}, false
	}
	return ModelBindingView{scope: v.scope, value: &v.record.binding}, true
}

// EffectiveInput returns an incremental-input handle only for that kind.
func (v RecordView) EffectiveInput() (EffectiveInputView, bool) {
	if v.Kind() != sessionformat.KindEffectiveInput {
		return EffectiveInputView{}, false
	}
	return EffectiveInputView{scope: v.scope, value: &v.record.effective}, true
}
