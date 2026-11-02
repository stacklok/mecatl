package history

// Status is the outcome of observing one proposal against a linear history.
type Status uint8

const (
	// StatusNotObserved says Resolve did not observe acceptance of the proposal.
	// It is not a final rejection and is never returned by Append.
	StatusNotObserved Status = iota
	// StatusAccepted says the echoed proposal is accepted in the observed history.
	StatusAccepted
	// StatusConflict says this proposal is not accepted at the relevant observation.
	StatusConflict
	// StatusRetryExpired says the retry window deliberately makes no historical
	// acceptance adjudication, even if a backend retains older data.
	StatusRetryExpired
)

// Resolution is an observation made atomically with a successful Append or
// Resolve operation. Proposal is always the echoed request; Head, status, and
// retry window all arise from the same linearized observation. On an operation
// error, the zero Resolution is not an authoritative verdict.
type Resolution struct {
	// Status is the outcome of the observation.
	Status Status
	// Proposal echoes the proposal passed to Append or Resolve.
	Proposal CommitRef
	// Head is the accepted head at the observation, or zero for an unknown history.
	Head CommitRef
}
