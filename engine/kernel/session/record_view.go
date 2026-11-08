package session

import (
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// RecordView is an immutable accepted fact. Zero has no kind; all typed accesses
// return false. Wrong-kind access is distinct from a valid zero-field Created DTO.
type RecordView struct {
	scope  historyid.HistoryID
	record *materializedRecord
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
func (v RecordView) Created() (CreatedView, bool) {
	if v.Kind() != sessionformat.KindCreated {
		return CreatedView{}, false
	}
	return CreatedView{scope: v.scope, value: &v.record.created}, true
}

// UserInput returns an immutable user-submission handle only for that kind.
func (v RecordView) UserInput() (UserInputView, bool) {
	if v.Kind() != sessionformat.KindUserInput {
		return UserInputView{}, false
	}
	return UserInputView{scope: v.scope, value: &v.record.input}, true
}

// RunStarted returns the scoped start fact only for the start kind.
func (v RecordView) RunStarted() (RunStartedView, bool) {
	if v.Kind() != sessionformat.KindRunStarted {
		return RunStartedView{}, false
	}
	return RunStartedView{scope: v.scope, value: &v.record.started}, true
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
