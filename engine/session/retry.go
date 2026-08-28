package session

// RetryDisposition is the provider-neutral causal classification of a model
// stream failure. Its zero value is conservative: unknown is not safe to replay
// and is not presented as permanent.
type RetryDisposition uint8

// RetryDispositionUnknown is conservative; Retryable and Permanent are explicit.
const (
	RetryDispositionUnknown RetryDisposition = iota
	RetryDispositionRetryable
	RetryDispositionPermanent
)

// Valid reports whether d belongs to the closed retry vocabulary. Unknown is a
// valid conservative zero value for backward compatibility.
func (d RetryDisposition) Valid() bool {
	return d >= RetryDispositionUnknown && d <= RetryDispositionPermanent
}

// StreamProgress describes how far a streamed model attempt advanced
// semantically before its terminal outcome.
type StreamProgress uint8

// StreamProgressUnknown is conservative; later values mark semantic boundaries.
const (
	StreamProgressUnknown StreamProgress = iota
	StreamProgressPrecommit
	StreamProgressVisible
	StreamProgressComplete
)

// Valid reports whether p belongs to the closed progress vocabulary. Unknown is
// a valid conservative zero value for backward compatibility.
func (p StreamProgress) Valid() bool {
	return p >= StreamProgressUnknown && p <= StreamProgressComplete
}
