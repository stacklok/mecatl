//go:build !linux && !darwin

package permconfig

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
)

var providerMapUpdateTestHook func(string) error

// UpdateProviderMap is unavailable where secure descriptor-anchored replacement
// is not implemented.
func UpdateProviderMap(context.Context, string, ProviderMapUpdate) (authfile.CommitState, error) {
	return authfile.CommitNotApplied, errors.New("settings provider mutation is unsupported on this platform")
}
