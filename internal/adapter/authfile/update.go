package authfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// APIKeyUpdate is one targeted auth.yaml mutation. A nil APIKey removes only
// Provider's api_key field.
type APIKeyUpdate struct {
	Provider string
	APIKey   *string
}

// CommitState describes exactly how far a preserving file update progressed.
type CommitState string

const (
	// CommitNoop means the requested value already held and the target was not rewritten.
	CommitNoop CommitState = "no_op"
	// CommitNotApplied means replacement did not occur.
	CommitNotApplied CommitState = "not_applied"
	// CommitDurable means replacement and parent-directory sync both succeeded.
	CommitDurable CommitState = "durable"
	// CommitReplacementAppliedDurabilityUnknown means rename succeeded but a later durability step failed.
	CommitReplacementAppliedDurabilityUnknown CommitState = "replacement_applied_durability_unknown"
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

// UpdateSupported reports whether this platform can enforce the writer's
// descriptor-anchored ownership and durability contract.
func UpdateSupported() bool { return updateSupported() }

// PreflightAPIKeyUpdateTarget validates an existing target with the same
// descriptor and document checks used by UpdateAPIKey, without creating files.
func PreflightAPIKeyUpdateTarget(path string) error { return preflightAPIKeyUpdateTarget(path) }

// UpdateAPIKey performs a bounded, preserving, cooperative-lock update.
func UpdateAPIKey(ctx context.Context, path string, update APIKeyUpdate) (CommitState, error) {
	return updateAPIKey(ctx, path, update)
}
