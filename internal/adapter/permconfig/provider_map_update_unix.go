package permconfig

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/internal/adapter/privatefile"
)

// UpdateProviderMap adds, replaces, or removes exactly one custom providers entry.
// It preserves unrelated settings mappings and uses an atomic, checked replacement.
// A nil update.Definition removes update.Provider.
func UpdateProviderMap(ctx context.Context, path string, update ProviderMapUpdate) (privatefile.CommitState, error) {
	if err := ValidateProviderID(update.Provider); err != nil {
		return privatefile.CommitNotApplied, fmt.Errorf("settings provider update: %w", err)
	}
	state, err := privatefile.Update(ctx, path, settingsPath(), maxConfigBytes, func(data []byte) ([]byte, bool, error) {
		return mutateProviderMap(data, update)
	})
	if err != nil {
		if errors.Is(err, privatefile.ErrConfigurationChanged) || errors.Is(err, errProviderConfigurationChanged) {
			return state, errProviderConfigurationChanged
		}
		return state, fmt.Errorf("settings provider update: %w", err)
	}
	return state, nil
}
