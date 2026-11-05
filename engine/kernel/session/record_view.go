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
	value       format.Record
	created     sessionformat.Created
	input       sessionformat.UserInput
	started     sessionformat.RunStarted
	interrupted sessionformat.RunInterrupted
}

// decodeAccepted decodes once and detaches encoded caller storage. Generated
// decoding owns Content; neither DTO nor encoded bytes escape without copying.
func decodeAccepted(v format.Record, limits codec.Limits) (*acceptedRecord, error) {
	if v.KindVersion != sessionformat.Version {
		return nil, ErrUnsupportedRecord
	}
	r := &acceptedRecord{}
	var err error
	switch v.Kind {
	case sessionformat.KindCreated:
		r.created, err = codec.Decode[sessionformat.Created](v.Payload, limits)
	case sessionformat.KindUserInput:
		r.input, err = codec.Decode[sessionformat.UserInput](v.Payload, limits)
	case sessionformat.KindRunStarted:
		r.started, err = codec.Decode[sessionformat.RunStarted](v.Payload, limits)
	case sessionformat.KindRunInterrupted:
		r.interrupted, err = codec.Decode[sessionformat.RunInterrupted](v.Payload, limits)
	default:
		return nil, ErrUnsupportedRecord
	}
	if err != nil {
		return nil, err
	}
	r.value = v.Clone()
	return r, nil
}

// metadata projects only the typed scalars needed by the shared lifecycle path.
// Its encoded bytes already belong to immutable private storage.
func (r *acceptedRecord) metadata(scope historyid.HistoryID) Record {
	return Record{scope: scope, value: r.value,
		userInput:  InputID{RunID: RunID{HistoryID: scope, Ordinal: r.input.RunID}, Ordinal: r.input.ID},
		inputBytes: len(r.input.Content), runStarted: r.started,
		runInterrupted: RunInterrupted{ID: RunID{HistoryID: scope, Ordinal: r.interrupted.ID}},
	}
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

// UserInput returns an immutable input handle only for the input kind.
func (v RecordView) UserInput() (InputView, bool) {
	if v.Kind() != sessionformat.KindUserInput {
		return InputView{}, false
	}
	return InputView(v), true
}

// RunStarted returns the scoped start fact only for the start kind.
func (v RecordView) RunStarted() (RunStarted, bool) {
	if v.Kind() != sessionformat.KindRunStarted {
		return RunStarted{}, false
	}
	return RunStarted{ID: RunID{HistoryID: v.scope, Ordinal: v.record.started.ID}}, true
}

// RunInterrupted returns the scoped interruption only for the interruption kind.
func (v RecordView) RunInterrupted() (RunInterrupted, bool) {
	if v.Kind() != sessionformat.KindRunInterrupted {
		return RunInterrupted{}, false
	}
	return RunInterrupted{ID: RunID{HistoryID: v.scope, Ordinal: v.record.interrupted.ID}}, true
}
