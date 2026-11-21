package lease

import "time"

// Validity is conservative evidence in the owning group's explicit local clock
// domain, not a transferable grant or storage authority. The provider accounts
// for transport delay and uncertainty; a raw foreign wall-clock expiry is not
// sufficient. The initial memory proof shares one controlled provider/group clock.
// Evidence is usable only while current and the local group remains live; a late
// renewal result cannot revive retired admission or undo an accepted append.
type Validity struct {
	// Until is the exclusive local admission deadline. At or after Until no
	// admission is justified by this evidence. Zero is unusable. The owner must
	// independently monitor it even when Hold.Done notification is delayed.
	Until time.Time
}
