package modelrequest

import (
	"errors"
	"iter"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

// Reconstruct rebuilds an accepted request from its retained recipe and exact
// original record prefix, including preparation earlier in the same commit. It
// uses the original run, not the snapshot's current active run or latest context.
// Session admission has verified the source anchor (including pre-genesis) and
// active run at that boundary; reconstruction grants no invocation authority.
// Lookup and construction scan the pinned records without backend I/O or lifecycle
// replay. Limits govern parameters and projected output, not unrelated history.
// Unsupported recipes, malformed data and CID mismatches return a zero Request;
// they do not affect ordinary session inspection.
func Reconstruct(snapshot session.Snapshot, requestID session.ModelRequestID, limits codec.Limits) (Request, error) {
	if _, err := codec.NewBudget(limits); err != nil {
		return Request{}, err
	}
	if requestID.Validate() != nil || requestID.RunID().SessionID() != snapshot.SessionID() {
		return Request{}, session.ErrIdentity
	}
	for i := 0; i < snapshot.RecordCount(); i++ {
		target, ok := snapshot.RecordAt(i).ModelRequest()
		if !ok || target.ID() != requestID {
			continue
		}
		recipe := target.Recipe()
		switch {
		case recipe.Algorithm() == "chronological" && recipe.Version() == 1:
			// ParametersV1's generated decoder rejects missing/unknown fields,
			// malformed values and trailing data; empty bytes are not defaults.
		default:
			return Request{}, errors.New("unsupported model request construction recipe")
		}
		if recipe.Check().Len() != 36 {
			return Request{}, codec.ErrInvalidData
		}
		expected, err := contentid.ParseBinary(recipe.Check().Copy())
		if err != nil {
			return Request{}, err
		}
		if recipe.Parameters().Len() > limits.MaxBytes {
			return Request{}, codec.ErrLimitExceeded
		}
		parameters, err := codec.Decode[format.ParametersV1](recipe.Parameters().Copy(), limits)
		if err != nil {
			return Request{}, err
		}
		built, err := Construct(requestPrefix{snapshot: snapshot, count: i, run: requestID.RunID()}, parameters, limits)
		if err != nil {
			return Request{}, err
		}
		if built.Request().CID() != expected {
			return Request{}, contentid.ErrMismatch
		}
		return built.Request(), nil
	}
	return Request{}, session.ErrIdentity
}

// requestPrefix borrows accepted backing strictly before the target. Admission
// already proved its anchor and original active run, including a nil genesis
// anchor. No historical lifecycle Snapshot or permanent record index is rebuilt.
// Capture is O(1); bounds exclude both the target and all subsequent records.
type requestPrefix struct {
	snapshot session.Snapshot
	count    int
	run      session.RunID
}

func (v requestPrefix) SessionID() sessionid.SessionID { return v.snapshot.SessionID() }
func (v requestPrefix) ActiveRun() session.RunID       { return v.run }
func (v requestPrefix) RecordCount() int               { return v.count }
func (v requestPrefix) RecordAt(i int) session.RecordView {
	if i < 0 || i >= v.count {
		panic("model request prefix index out of range")
	}
	return v.snapshot.RecordAt(i)
}
func (v requestPrefix) Records() iter.Seq[session.RecordView] {
	return func(yield func(session.RecordView) bool) {
		for i := 0; i < v.count; i++ {
			if !yield(v.RecordAt(i)) {
				return
			}
		}
	}
}
