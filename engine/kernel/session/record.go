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

// Record is an owned, locally validated canonical session record for typed
// preparation. Typed constructors and DecodeRecord create usable records. Zero is
// unusable. Copies may share private immutable storage; no method
// exposes that storage. It proves neither transition legality nor acceptance.
// Its private originating scope survives conversion to local persistence ordinals
// so preparation can reject foreign public facts before constructing an envelope.
type Record struct {
	scope historyid.HistoryID
	value format.Record

	// value.Kind selects these constructor-established lifecycle scalars.
	// Input content exists only in value.
	userInput      InputID
	runStarted     sessionformat.RunStarted
	runInterrupted RunInterrupted
	// Constructor-established source metadata contains no borrowed content.
	messageCount   uint64
	effectiveInput InputID
	hasMessage     bool
	maxMessage     uint64
}

// Format returns a detached history persistence DTO. Mutating it does not change
// the Record. The DTO deliberately omits the originating history scope; it must
// be bound through a verified history chain when applied. The zero Record returns
// a zero, invalid DTO.
func (r Record) Format() format.Record { return r.value.Clone() }

// checkPayload rechecks the complete rich payload under the current policy.
// Bounded generated decoding is required: metadata alone cannot validate nested
// strings, collection counts, JSON, or variants under stricter limits.
func (r Record) checkPayload(limits codec.Limits) error {
	_, err := decodeAccepted(r.value, limits)
	return err
}

// supportedRecord dispatches exact kind/version pairs. In particular, opaque
// user-input v1 is not silently reinterpreted as rich authored input.
func supportedRecord(kind string, version int64) bool {
	switch kind {
	case sessionformat.KindUserInput:
		return version == sessionformat.UserInputVersion
	case sessionformat.KindEffectiveInput:
		return version == sessionformat.EffectiveInputVersion
	case sessionformat.KindInstructions:
		return version == sessionformat.InstructionsVersion
	case sessionformat.KindToolCatalog:
		return version == sessionformat.ToolCatalogVersion
	case sessionformat.KindModelBinding:
		return version == sessionformat.ModelBindingVersion
	case sessionformat.KindCreated, sessionformat.KindRunStarted, sessionformat.KindRunInterrupted:
		return version == sessionformat.Version
	default:
		return false
	}
}

// DecodeRecord binds a local persistence payload to an explicit nonzero history
// scope and owns a copy on success. This binding is not a membership proof; the
// caller supplies the scope, and Apply independently verifies chain ancestry.
// Generated bounded decoding validates each supported payload locally. Limits
// govern this single payload, not a commit or cumulative work.
// Keep the borrowed DTO stable during the call. Failure returns zero. Invalid
// scope returns ErrIdentity; unsupported kind/version returns ErrUnsupportedRecord;
// malformed data and operational limits use codec error categories. No error
// includes payload bytes.
func DecodeRecord(scope historyid.HistoryID, v format.Record, limits codec.Limits) (Record, error) {
	if err := (Created{HistoryID: scope}).Validate(); err != nil {
		return Record{}, ErrIdentity
	}
	if err := v.Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	if !supportedRecord(v.Kind, v.KindVersion) {
		return Record{}, ErrUnsupportedRecord
	}
	fact, err := decodeAccepted(v, limits)
	if err != nil {
		return Record{}, err
	}
	return fact.metadata(scope), nil
}
