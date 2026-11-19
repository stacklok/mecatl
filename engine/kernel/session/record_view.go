package session

import (
	"time"

	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// RecordView is an immutable accepted or proposed fact, not acceptance proof.
// Its backing remains readable for the view's lifetime regardless of batch closure.
// Zero has no kind; all typed accesses
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

// Timestamp returns the UTC record-admission time in O(1), without copying
// payloads. Zero views return the zero time, false; a present timestamp at the
// Unix epoch is distinct from absence. Negative values and backwards clock jumps
// are valid. This is diagnostic recording time, not action timing or an ordering
// guarantee.
func (v RecordView) Timestamp() (time.Time, bool) {
	if v.record == nil {
		return time.Time{}, false
	}
	return time.Unix(0, v.record.value.Timestamp).UTC(), true
}

// Commit returns the enclosing accepted commit's immutable metadata in O(1),
// without copying payloads. Zero and proposed records return zero, false. Captured
// proposed views remain absent even after their batch is accepted; fresh accepted
// views obtained through Submit or Load expose the metadata.
func (v RecordView) Commit() (CommitMetadata, bool) {
	if v.record == nil || v.record.commit == nil {
		return CommitMetadata{}, false
	}
	return *v.record.commit, true
}

// MutableClone returns a deeply detached mutable copy of the record DTO, retaining
// its timestamp but not enclosing history scope or accepted commit metadata.
// A zero view returns a zero DTO. Mutating the result cannot change accepted
// backing or other clones.
func (v RecordView) MutableClone() format.Record {
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

// ToolInvocation returns an immutable scoped intent handle only for that kind.
func (v RecordView) ToolInvocation() (ToolInvocationView, bool) {
	if v.Kind() != sessionformat.KindToolInvocation {
		return ToolInvocationView{}, false
	}
	return ToolInvocationView{scope: v.scope, value: &v.record.toolInvocation}, true
}

// ToolDisposition returns an immutable scoped outcome handle only for that kind.
func (v RecordView) ToolDisposition() (ToolDispositionView, bool) {
	if v.Kind() != sessionformat.KindToolDisposition {
		return ToolDispositionView{}, false
	}
	return ToolDispositionView{scope: v.scope, value: &v.record.toolDisposition}, true
}

// Usage returns a scoped immutable accounting record only for the usage kind.
// A replacement has no Definition; this accessor performs no historical join.
func (v RecordView) Usage() (UsageView, bool) {
	if v.Kind() != sessionformat.KindUsage {
		return UsageView{}, false
	}
	return UsageView{scope: v.scope, value: &v.record.usage}, true
}

// InvocationFailure returns an immutable observed-failure handle for that kind.
func (v RecordView) InvocationFailure() (InvocationFailureView, bool) {
	if v.record == nil || v.record.value.Kind != sessionformat.KindInvocationFailure {
		return InvocationFailureView{}, false
	}
	return InvocationFailureView{scope: v.scope, value: &v.record.failure}, true
}

// UserInstructions returns a prepared user-guidance replacement handle only for
// the user-instructions kind.
func (v RecordView) UserInstructions() (UserInstructionsView, bool) {
	if v.Kind() != sessionformat.KindUserInstructions {
		return UserInstructionsView{}, false
	}
	return UserInstructionsView{scope: v.scope, value: &v.record.userInstructions}, true
}

// RootInstructions returns a harness root-contract replacement handle only for
// the root-instructions kind.
func (v RecordView) RootInstructions() (RootInstructionsView, bool) {
	if v.Kind() != sessionformat.KindRootInstructions {
		return RootInstructionsView{}, false
	}
	return RootInstructionsView{scope: v.scope, value: &v.record.rootInstructions}, true
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
