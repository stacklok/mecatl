package session

import (
	"iter"

	"github.com/stacklok/mecatl/engine/kernel/history"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

// CommitView pins the accepted prefix and proposed prefix of one SessionCommit.
// Capture, copying and indexing cost O(1), without decoding or copying payloads.
// Records are immutable but proposals confer no acceptance or execution authority.
// Captured answers remain readable after additions, submission or discard, including
// uncertain or failed submission. Handles retain backing for their lifetime and
// may retain extra capacity. Zero is unscoped and empty. Indexed reads panic outside
// [0, RecordCount()). Readers may run concurrently with later owner-serialized writes.
type CommitView struct {
	origin    *SessionCommit
	sessionID sessionid.SessionID
	activeRun RunID
	head      history.CommitRef
	accepted  []*materializedRecord
	proposed  []*materializedRecord
}

// View captures the complete ordered source at this writable original batch.
// It requires no active run or request eligibility. Closed, frozen and copied
// batch handles fail with a zero view; existing captures remain readable.
func (b *SessionCommit) View() (CommitView, error) {
	if err := b.check(true); err != nil {
		return CommitView{}, err
	}
	v := b.owner.state.view
	return CommitView{origin: b, sessionID: v.sessionID, activeRun: b.admission.candidate.activeRun,
		head: v.head, accepted: v.records, proposed: b.records}, nil
}

// SessionID returns the captured Session scope, including before creation.
func (v CommitView) SessionID() sessionid.SessionID { return v.sessionID }

// ActiveRun returns the captured candidate run, or zero while idle.
func (v CommitView) ActiveRun() RunID { return v.activeRun }

// RecordCount returns the number of accepted and proposed chronological facts.
func (v CommitView) RecordCount() int { return len(v.accepted) + len(v.proposed) }

// RecordAt returns a scoped immutable record; out-of-range indices panic.
func (v CommitView) RecordAt(i int) RecordView {
	if i < len(v.accepted) {
		return RecordView{scope: v.sessionID, record: v.accepted[i]}
	}
	return RecordView{scope: v.sessionID, record: v.proposed[i-len(v.accepted)]}
}

// Records returns a repeatable, early-stoppable sequence pinned to both prefixes.
// Traversal costs O(records yielded); zero yields nothing.
func (v CommitView) Records() iter.Seq[RecordView] {
	return func(yield func(RecordView) bool) {
		for i := 0; i < v.RecordCount(); i++ {
			if !yield(v.RecordAt(i)) {
				return
			}
		}
	}
}
