package lease

import "context"

// Attempt retains the private stable identity of one acquisition. The caller
// serializes Try and Abandon, with at most one call in flight, including when a
// dependency ignores cancellation. An attempt must not become a new acquisition
// after expiry, abandonment, release, supersession, or its retry horizon ending.
// Implementations may bound adjudication retention, but must refuse forgotten
// requests rather than reinterpret them as fresh authority.
type Attempt interface {
	// Try blocks while acquiring or reconciling this exact attempt. A retry
	// after a lost acknowledgment returns the same still-live incarnation,
	// never a replacement generation. A known different healthy incumbent
	// returns AcquireHeld promptly; this attempt's uncertain grant is not Held.
	//
	// Success returns a nonnil Hold for the requested group and nonzero current
	// Validity, transferring cleanup responsibility to its sole group owner.
	// Repeated success reconciles that same transfer; it does not create another
	// owner. Positive known grant success is not changed into rejection solely
	// because the caller's wait was canceled late.
	//
	// Every error returns nil Hold and zero Validity. AcquireError classifies
	// invalid/permanent, held, definitely-not-granted retryable, and uncertain
	// results independently of cancellation. Unclassified errors stop opening.
	// A terminal attempt returns AcquireInvalid and never remints authority.
	Try(context.Context) (Hold, Validity, error)

	// Abandon terminally closes this attempt before successful transfer and
	// performs best-effort exact-attempt cleanup. It is idempotent and cannot
	// affect another attempt or a successor. Nil proves that no delayed request
	// can subsequently establish this attempt; error or timeout leaves cleanup
	// uncertain. Retrying cleanup is allowed, reacquiring is not.
	// After successful transfer it does not release the Hold: use Hold.Release.
	Abandon(context.Context) error
}
