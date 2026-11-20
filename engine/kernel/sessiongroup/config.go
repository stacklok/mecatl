package sessiongroup

import (
	"context"
	"errors"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/lease"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

// ErrInvalidConfig reports a missing, nonpositive, or contradictory group
// configuration. Its text is safe to present; Validate never invokes injected
// Provider or Clock methods.
var ErrInvalidConfig = errors.New("invalid session group config")

// Clock supplies the one explicit, deterministic time domain for a group and
// its provider. Implementations must make Wait return nil only after until is
// reached in that domain, or return when ctx is canceled. Wait must not retain
// ctx after returning. Group code may call Now and Wait concurrently, so clock
// implementations synchronize any shared state. The package supplies no
// wall-clock default.
type Clock interface {
	// Now returns the current time in this clock's domain.
	Now() time.Time
	// Wait blocks until until is reached or ctx is canceled.
	Wait(ctx context.Context, until time.Time) error
}

// Config fixes the dependencies and bounds used by one future group opening.
// Its layout is provisional: it groups lease/lifecycle policy with the inputs
// needed to construct passive member Sessions. Revisit that grouping when private
// runtime construction supplies a concrete consumer; it is not a general runtime
// capability bundle or a live-update API. Model, tool, and workspace configuration
// do not belong here merely because a runtime may eventually use them.
//
// It is copied during construction. Caller-owned Provider and Clock implementations
// must remain usable through completion of every outstanding group call, including
// failed opening, bounded close, and operations that remain uncooperative after a
// report returns. A report neither transfers nor ends that obligation. Every duration
// and bound is required and positive. Config has no defaults.
//
// Validation checks values only; it does not call Provider, Clock, or any other
// injected dependency. It cannot detect a nonnil interface containing a nil
// concrete pointer, so callers must not supply one.
type Config struct {
	// Provider constructs the group's lease attempt and supplies its fenced
	// storage authority. It is required and must be nonnil.
	Provider lease.Provider
	// Clock supplies explicit current time and cancellation-aware scheduled
	// waits. It is required and must be nonnil.
	Clock Clock
	// HistoryPolicy supplies required per-member history resource bounds. It is
	// copied into each private History; the limits apply independently to every
	// member, not as a group aggregate.
	HistoryPolicy history.Policy
	// SessionPolicy supplies required per-member session resource bounds. It is
	// copied into each private Session; the limits apply independently to every
	// member, not as a group aggregate. Member authoring uses Clock.Now.
	SessionPolicy session.Policy
	// InstanceID is the required nonzero diagnostic writer identity shared by
	// private member Sessions. It proves neither authority nor uniqueness.
	InstanceID uint64
	// OpeningTimeout bounds all opening work, including acquisition, retry
	// waits, and failed-opening cleanup. A caller's earlier deadline wins.
	OpeningTimeout time.Duration
	// AttemptTimeout bounds each acquisition, reconciliation, or renewal call;
	// it never extends OpeningTimeout, established validity, or CloseTimeout.
	// Cancellation is not proof that an uncooperative call has stopped.
	AttemptTimeout time.Duration
	// RetryInterval is the positive delay before retrying acquisition,
	// uncertainty reconciliation, or a failed renewal while validity remains
	// provable. Retries never overlap an unresolved provider operation.
	RetryInterval time.Duration
	// RenewInterval is the positive delay between successful maintenance
	// renewal attempts. Validity deadlines remain independently enforced.
	RenewInterval time.Duration
	// DrainTimeout bounds the graceful interval after close rejects new local
	// admission and before release is attempted.
	DrainTimeout time.Duration
	// CloseTimeout bounds the single group-owned close operation, including
	// draining and release, from its first invocation. Close caller contexts
	// bound only observation, not shutdown. It must exceed DrainTimeout to
	// reserve time for release; uncooperative operations may outlive the report.
	CloseTimeout time.Duration
	// MaxLoadedSessions bounds the number of resident member runtimes retained
	// at once. Loading another Session is refused at capacity, without changing
	// permanent membership or authority over unloaded members. A runtime with
	// unresolved obligations cannot be unloaded to make space.
	MaxLoadedSessions int
}

// Validate reports whether Config contains all required dependencies and
// positive bounded values. It does not execute injected code. A valid Config
// has valid private member History and Session policies and reserves release
// time by requiring CloseTimeout to exceed DrainTimeout.
func (c Config) Validate() error {
	if c.Provider == nil || c.Clock == nil || c.InstanceID == 0 ||
		c.HistoryPolicy.Validate() != nil || c.SessionPolicy.Validate() != nil ||
		c.OpeningTimeout <= 0 || c.AttemptTimeout <= 0 ||
		c.RetryInterval <= 0 || c.RenewInterval <= 0 ||
		c.DrainTimeout <= 0 || c.CloseTimeout <= c.DrainTimeout ||
		c.MaxLoadedSessions <= 0 {
		return ErrInvalidConfig
	}
	return nil
}
