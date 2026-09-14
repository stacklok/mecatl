package authfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/stacklok/mecatl/internal/adapter/privatefile"
)

//nolint:revive,staticcheck // Exact CLI retry message is a published contract.
var errConfigurationChanged = errors.New("Configuration changed while this command was running; no changes were made. Review the file and retry.")

// APIKeyUpdate is one targeted auth.yaml mutation. A nil APIKey removes only
// Provider's api_key field.
type APIKeyUpdate struct {
	Provider string
	APIKey   *string
}

// CommitState describes exactly how far a preserving file update progressed.
type CommitState = privatefile.CommitState

const (
	// CommitNoop means the requested value already held.
	CommitNoop = privatefile.CommitNoop
	// CommitNotApplied means replacement did not occur.
	CommitNotApplied = privatefile.CommitNotApplied
	// CommitDurable means replacement and parent sync succeeded.
	CommitDurable = privatefile.CommitDurable
	// CommitReplacementAppliedDurabilityUnknown means replacement occurred but a later durability step failed.
	CommitReplacementAppliedDurabilityUnknown = privatefile.CommitReplacementAppliedDurabilityUnknown
)

// CanonicalPath returns a physical target path without requiring its leaf to
// exist. Its parent must exist so aliases are resolved before any prompt.
func CanonicalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is unavailable")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("path is unavailable")
	}
	physical, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(physical), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("path is unavailable")
	}
	parent, parentErr := filepath.EvalSymlinks(filepath.Dir(abs))
	if parentErr != nil {
		return "", errors.New("path parent is unavailable")
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

// UpdateSupported reports whether this platform supports preserving private-file updates.
func UpdateSupported() bool { return updateSupported() }

// PreflightAPIKeyUpdateTarget validates an existing target with the same
// private-file and document checks used by UpdateAPIKey, without creating files.
func PreflightAPIKeyUpdateTarget(path string) error { return preflightAPIKeyUpdateTarget(path) }

// UpdateAPIKey performs a bounded, preserving update.
func UpdateAPIKey(ctx context.Context, path string, update APIKeyUpdate) (CommitState, error) {
	return updateAPIKey(ctx, path, update)
}
