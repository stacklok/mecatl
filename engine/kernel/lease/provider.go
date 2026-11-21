package lease

import groupid "github.com/stacklok/mecatl/engine/kernel/sessiongroup/id"

// Provider constructs fresh local acquisition attempts. Implementations support
// concurrent Begin calls; independent openings must never share attempt identity.
// No unsupported or unfenced fallback is permitted.
type Provider interface {
	// Begin performs no dependency or storage I/O and reserves no authority.
	// Each call creates a distinct attempt for a nonzero group ID. On error it
	// returns nil Attempt; invalid/unsupported requests are AcquireInvalid.
	// Never call Begin to retry an uncertain acquisition: retain its Attempt.
	Begin(groupid.GroupID) (Attempt, error)
}
