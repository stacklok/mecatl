package lease

// AcquireFailure is a closed in-memory acquisition classification, not a
// persisted label. Values outside the declared constants normalize to
// AcquireInvalid in NewAcquireError; zero is the fail-closed permanent case.
type AcquireFailure uint8

const (
	// AcquireInvalid is invalid, unsupported, terminal, or otherwise permanent.
	// Stop opening; never fall back to unfenced storage or remint this attempt.
	AcquireInvalid AcquireFailure = iota
	// AcquireHeld means a definitively different healthy owner holds the group.
	// Fail promptly; this attempt's own uncertain grant is not a different owner.
	AcquireHeld
	// AcquireRetryable proves this attempt was not granted. A bounded retry may
	// use the same Attempt; it does not require or authorize a fresh Begin.
	AcquireRetryable
	// AcquireUncertain means grant existence is unknown, including cancellation
	// after possible grant. Reconcile only through this exact Attempt; cleanup
	// uncertainty must not be silently treated as no authority.
	AcquireUncertain
)

// AcquireError reports acquisition disposition without exposing private cause
// text. Its zero value is AcquireInvalid with no cause. Callers use errors.As to
// inspect Failure; unclassified errors stop rather than trigger retries.
// Every Try error accompanies nil Hold and zero Validity.
type AcquireError struct {
	failure AcquireFailure
	cause   error
}

// NewAcquireError constructs a safe error preserving cause for errors.Is/As.
// A nil cause is allowed. Unknown classifications normalize to AcquireInvalid.
// The cause may contain private data: never render or log its raw text.
func NewAcquireError(failure AcquireFailure, cause error) *AcquireError {
	if failure > AcquireUncertain {
		failure = AcquireInvalid
	}
	return &AcquireError{failure: failure, cause: cause}
}

// Failure returns the closed acquisition disposition.
func (e *AcquireError) Failure() AcquireFailure { return e.failure }

// Error returns fixed safe text and never formats the private cause.
func (e *AcquireError) Error() string {
	switch e.failure {
	case AcquireHeld:
		return "lease acquisition held elsewhere"
	case AcquireRetryable:
		return "lease acquisition retryable"
	case AcquireUncertain:
		return "lease acquisition uncertain"
	default:
		return "lease acquisition invalid"
	}
}

// Unwrap preserves the underlying cause for programmatic inspection. Its raw
// text is not safe for presentation merely because AcquireError.Error is safe.
func (e *AcquireError) Unwrap() error { return e.cause }
