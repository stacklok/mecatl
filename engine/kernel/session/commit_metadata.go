package session

import (
	"time"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

// CommitMetadata is immutable diagnostic metadata for an accepted enclosing
// commit, not a captured batch source (CommitView). Copying and scalar reads cost
// O(1), retain no record payloads, and remain valid after later writes or reloads.
// Zero is absent; obtain presence from RecordView.Commit.
//
// Timestamps describe submission preparation, not backend receipt, durability,
// action duration or ordering. Instance IDs distinguish caller-chosen writer
// clock domains; they prove neither authority nor global uniqueness.
type CommitMetadata struct {
	ref        history.CommitRef
	timestamp  int64
	instanceID uint64
}

// Ref returns the accepted enclosing commit reference, or zero for zero.
func (v CommitMetadata) Ref() history.CommitRef { return v.ref }

// Timestamp returns the UTC preparation time. Zero metadata returns the zero time.
// A present timestamp at the Unix epoch is distinct from absent metadata; use
// RecordView.Commit for accepted-metadata presence.
func (v CommitMetadata) Timestamp() time.Time {
	if v == (CommitMetadata{}) {
		return time.Time{}
	}
	return time.Unix(0, v.timestamp).UTC()
}

// InstanceID returns the writer's nonzero diagnostic instance ID, or zero for zero.
func (v CommitMetadata) InstanceID() uint64 { return v.instanceID }
