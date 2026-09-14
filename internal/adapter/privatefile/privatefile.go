// Package privatefile updates small owner-private files by atomic replacement.
package privatefile

import (
	"context"
	"errors"
)

// CommitState describes how far a preserving update progressed.
type CommitState string

const (
	// CommitNoop means the requested value already held.
	CommitNoop CommitState = "no_op"
	// CommitNotApplied means replacement did not occur.
	CommitNotApplied CommitState = "not_applied"
	// CommitDurable means replacement and parent sync succeeded.
	CommitDurable CommitState = "durable"
	// CommitReplacementAppliedDurabilityUnknown means replacement occurred but a later durability step failed.
	CommitReplacementAppliedDurabilityUnknown CommitState = "replacement_applied_durability_unknown"
)

// ErrConfigurationChanged is the published retryable outcome for an accidental
// concurrent edit detected before replacement.
//
//nolint:revive,staticcheck // Exact CLI retry message is a published contract.
var ErrConfigurationChanged = errors.New("Configuration changed while this command was running; no changes were made. Review the file and retry.")

// Mutate returns the preserving replacement and whether the file is unchanged.
type Mutate func([]byte) (out []byte, noop bool, err error)

// Supported reports whether preserving private-file updates are implemented.
func Supported() bool { return supported() }

// Preflight validates the private parent and target, then calls validate for an
// existing target. conventionalPath may name the one target whose missing
// parent directory may later be created by Update.
func Preflight(path, conventionalPath string, maxBytes int64, validate func([]byte) error) error {
	return preflight(path, conventionalPath, maxBytes, validate)
}

// Update performs a bounded preserving update. Its second read is best-effort
// detection of accidental concurrent edits, not a CAS against a hostile same-UID
// process. conventionalPath may name the one target whose missing 0700 parent
// directory may be created.
func Update(ctx context.Context, path, conventionalPath string, maxBytes int64, mutate Mutate) (CommitState, error) {
	return update(ctx, path, conventionalPath, maxBytes, mutate)
}
