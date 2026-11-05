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

// Record is an owned, locally validated canonical lifecycle record for typed
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
	// inputBytes is payload accounting, not retained input content. It permits
	// generated policy checks under a stricter policy without decoding Content.
	inputBytes int
}

// Format returns a detached history persistence DTO. Mutating it does not change
// the Record. The DTO deliberately omits the originating history scope; it must
// be bound through a verified history chain when applied. The zero Record returns
// a zero, invalid DTO.
func (r Record) Format() format.Record { return r.value.Clone() }

// checkPayload rechecks constructor-owned facts under the session's policy.
// Content is opaque: generated input checks inspect only its length, so an
// equally sized view of the owned payload avoids decoding or retaining it twice.
func (r Record) checkPayload(limits codec.Limits) error {
	if r.value.KindVersion != sessionformat.Version {
		return ErrUnsupportedRecord
	}
	switch r.value.Kind {
	case sessionformat.KindCreated:
		return codec.Check(sessionformat.Created{}, limits)
	case sessionformat.KindUserInput:
		return codec.Check(sessionformat.UserInput{RunID: r.userInput.RunID.Ordinal, ID: r.userInput.Ordinal, Content: r.value.Payload[:r.inputBytes]}, limits)
	case sessionformat.KindRunStarted:
		return codec.Check(r.runStarted, limits)
	case sessionformat.KindRunInterrupted:
		return codec.Check(sessionformat.RunInterrupted{ID: r.runInterrupted.ID.Ordinal}, limits)
	default:
		return ErrUnsupportedRecord
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
	if v.KindVersion != sessionformat.Version {
		return Record{}, ErrUnsupportedRecord
	}
	fact, err := decodeAccepted(v, limits)
	if err != nil {
		return Record{}, err
	}
	return fact.metadata(scope), nil
}
