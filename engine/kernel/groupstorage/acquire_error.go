package groupstorage

// AcquireFailure is a closed in-memory Claim classification, not a persisted
// label. Values outside the declared constants normalize to AcquireInvalid in
// NewAcquireError; zero is the fail-closed permanent case.
type AcquireFailure uint8

const (
	// AcquireInvalid is invalid, unsupported, terminal, or otherwise permanent.
	// Stop claiming; never fall back to unfenced storage or remint this acquisition.
	AcquireInvalid AcquireFailure = iota
	// AcquireHeld means a definitively different healthy owner holds the group.
	// Fail promptly; this acquisition's uncertain grant is not a different owner.
	AcquireHeld
	// AcquireRetryable proves this acquisition was not granted. A bounded retry
	// may use the same acquisition; it does not authorize a fresh identity.
	AcquireRetryable
	// AcquireUncertain means grant existence is unknown, including cancellation
	// after possible grant. Reconcile only through this exact acquisition.
	AcquireUncertain
)

// AcquireError reports Claim disposition without exposing private cause text. Its
// zero value is AcquireInvalid with no cause. Callers use errors.As to inspect
// Failure; unclassified errors stop rather than trigger retries.
type AcquireError struct {
	failure AcquireFailure
	cause   error
}

// NewAcquireError constructs a safe error preserving cause for errors.Is/As. A
// nil cause is allowed. Unknown classifications normalize to AcquireInvalid. The
// cause may contain private data and must never be rendered or logged raw.
func NewAcquireError(failure AcquireFailure, cause error) *AcquireError {
	if failure > AcquireUncertain {
		failure = AcquireInvalid
	}
	return &AcquireError{failure: failure, cause: cause}
}

// Failure returns the closed Claim disposition.
func (e *AcquireError) Failure() AcquireFailure { return e.failure }

// Error returns fixed safe text and never formats the private cause.
func (e *AcquireError) Error() string {
	switch e.failure {
	case AcquireHeld:
		return "group claim held elsewhere"
	case AcquireRetryable:
		return "group claim retryable"
	case AcquireUncertain:
		return "group claim uncertain"
	default:
		return "group claim invalid"
	}
}

// Unwrap preserves the underlying cause for programmatic inspection. Its raw
// text is not safe for presentation merely because AcquireError.Error is safe.
func (e *AcquireError) Unwrap() error { return e.cause }
