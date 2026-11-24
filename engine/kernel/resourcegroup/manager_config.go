package resourcegroup

import (
	"context"
	"errors"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/groupstorage"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
)

// ErrInvalidConfig reports missing dependencies or invalid manager bounds.
var ErrInvalidConfig = errors.New("invalid resource group manager config")

// Clock supplies the explicit, deterministic time domain shared with storage.
// Implementations support concurrent Now and Wait calls. Wait returns nil only
// once until is reached, or returns on cancellation, and retains no ctx afterward.
// There is no wall-clock default.
type Clock interface {
	// Now returns the current time in this clock's domain.
	Now() time.Time
	// Wait blocks until until is reached or ctx is canceled.
	Wait(ctx context.Context, until time.Time) error
}

// ManagerConfig fixes dependencies and lifecycle bounds, with no defaults.
// Intended construction copies it, borrowing dependencies through all outstanding
// work, including uncooperative work outliving failed opening or bounded shutdown.
// A report does not end that obligation. Validate never calls dependencies; a
// nonnil Clock interface must not contain a nil concrete pointer.
type ManagerConfig struct {
	// ResolveStorage is required and addresses an existing durable group without
	// claiming ownership or constructing resources. The Manager verifies the
	// returned GroupID before claiming. Facets borrow host-owned connections;
	// resolving does not transfer a separately closable connection to the Manager.
	ResolveStorage func(context.Context, groupid.GroupID) (groupstorage.Backend, error)
	// Clock is required and supplies explicit time and cancellation-aware waits.
	Clock Clock
	// LeaseDuration is the positive TTL requested by claims and renewals.
	LeaseDuration time.Duration
	// OpeningTimeout bounds shared opening, retries, and failed-opening cleanup,
	// independently of any individual waiter's cancellation.
	OpeningTimeout time.Duration
	// AttemptTimeout bounds each storage attempt without extending other bounds.
	// Cancellation does not prove an uncooperative operation has stopped.
	AttemptTimeout time.Duration
	// RetryInterval is the positive delay between failed attempts. Retrying must
	// not overlap an unresolved serialized storage operation.
	RetryInterval time.Duration
	// RenewInterval is the positive renewal delay, less than LeaseDuration.
	RenewInterval time.Duration
	// DrainTimeout bounds cooperative draining before emergency cleanup.
	DrainTimeout time.Duration
	// ShutdownTimeout bounds all shutdown coordination, including grace, and
	// exceeds DrainTimeout. Repeated triggers never reset or extend budgets.
	ShutdownTimeout time.Duration
}

// Validate checks required dependencies, positive durations, RenewInterval below
// LeaseDuration, and ShutdownTimeout above DrainTimeout. It executes no injected
// code and imposes no other duration relationships.
func (c ManagerConfig) Validate() error {
	if c.ResolveStorage == nil || c.Clock == nil ||
		c.LeaseDuration <= 0 || c.OpeningTimeout <= 0 || c.AttemptTimeout <= 0 ||
		c.RetryInterval <= 0 || c.RenewInterval <= 0 || c.DrainTimeout <= 0 ||
		c.ShutdownTimeout <= 0 || c.RenewInterval >= c.LeaseDuration ||
		c.ShutdownTimeout <= c.DrainTimeout {
		return ErrInvalidConfig
	}
	return nil
}
