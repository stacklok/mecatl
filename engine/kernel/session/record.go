package session

import (
	"errors"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ErrUnsupportedRecord identifies a kind/version this package cannot interpret.
// History may preserve such records as opaque payloads.
var ErrUnsupportedRecord = errors.New("unsupported session record kind or version")

// proposedRecord is a scoped proposal handle over an owned, locally validated
// canonical fact. Typed constructors and decodeRecord create usable proposals;
// zero is unusable. Copies share immutable materialized storage, never caller
// slices. Local validity proves neither transition legality nor acceptance.
// Originating scope survives conversion to local persistence ordinals so
// preparation can reject foreign public facts before constructing an envelope.
type proposedRecord struct {
	scope  historyid.HistoryID
	record *materializedRecord
}

// materializedRecord is the common immutable backing for proposals, admission,
// and accepted views/indexes. value owns canonical encoded bytes; exactly the
// complete DTO selected by value.Kind is valid, and all other variants are zero.
// Field names match their kinds (input is UserInput, started/ended are run facts,
// catalog is ToolCatalog, binding is ModelBinding, effective is EffectiveInput).
// All nested storage is privately owned; consumers must not mutate or expose it.
// Local decoding validates payloads, not scope membership, lifecycle or acceptance:
// proposal/view handles supply history scope and admission supplies source/state.
// Keep variants by value for now; evaluate alternative layouts with measurements
// later, without changing public contracts or persistence.
type materializedRecord struct {
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

// decodeMaterialized detaches encoded caller storage and decodes the selected
// complete variant under explicit limits. Generated decoding owns nested content.
// Failure returns nil; success alone establishes neither scope nor acceptance.
func decodeMaterialized(v format.Record, limits codec.Limits) (*materializedRecord, error) {
	if !supportedRecord(v.Kind, v.KindVersion) {
		return nil, ErrUnsupportedRecord
	}
	r := &materializedRecord{}
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

// format returns a detached persistence DTO without originating history scope.
// The zero proposal returns a zero, invalid DTO.
func (r proposedRecord) format() format.Record {
	if r.record == nil {
		return format.Record{}
	}
	return r.record.value.Clone()
}

// checkPayload rechecks the complete rich payload under the current policy.
// Bounded generated decoding revalidates nested strings, collection counts,
// JSON, and variants even when construction used looser limits.
func (r proposedRecord) checkPayload(limits codec.Limits) error {
	if r.record == nil {
		return codec.ErrInvalidData
	}
	_, err := decodeMaterialized(r.record.value, limits)
	return err
}

// supportedRecord dispatches the closed kind set at the single supported version.
func supportedRecord(kind string, version int64) bool {
	if version != sessionformat.Version {
		return false
	}
	switch kind {
	case sessionformat.KindCreated, sessionformat.KindUserInput, sessionformat.KindRunStarted,
		sessionformat.KindRunEnded, sessionformat.KindModelRequest, sessionformat.KindModelResponse, sessionformat.KindInvocationFailure, sessionformat.KindEffectiveInput, sessionformat.KindInstructions,
		sessionformat.KindToolCatalog, sessionformat.KindModelBinding:
		return true
	default:
		return false
	}
}

// decodeRecord binds a local persistence payload to an explicit nonzero history
// scope and owns a copy on success. This binding is not a membership proof;
// private verified application independently checks chain ancestry.
// Generated bounded decoding validates each supported payload locally. Limits
// govern this single payload, not a commit or cumulative work.
// Keep the borrowed DTO stable during the call. Failure returns zero. Invalid
// scope returns ErrIdentity; unsupported kind/version returns ErrUnsupportedRecord;
// malformed data and operational limits use codec error categories. No error
// includes payload bytes.
func decodeRecord(scope historyid.HistoryID, v format.Record, limits codec.Limits) (proposedRecord, error) {
	if err := (Created{HistoryID: scope}).Validate(); err != nil {
		return proposedRecord{}, ErrIdentity
	}
	if err := v.Validate(); err != nil {
		return proposedRecord{}, codec.ErrInvalidData
	}
	if !supportedRecord(v.Kind, v.KindVersion) {
		return proposedRecord{}, ErrUnsupportedRecord
	}
	fact, err := decodeMaterialized(v, limits)
	if err != nil {
		return proposedRecord{}, err
	}
	return proposedRecord{scope: scope, record: fact}, nil
}
