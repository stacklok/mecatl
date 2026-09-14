package permconfig

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/privatefile"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func settingsPath() string {
	return filepath.Join(xdgconfig.UserConfigDir(xdgconfig.OSEnv), UserSettingsRelPath)
}

func preflightDefaultsUpdateTarget(path string) error {
	return privatefile.Preflight(path, settingsPath(), maxConfigBytes, func(data []byte) error {
		_, _, err := mutateDefaults(data, DefaultUpdate{Provider: "openai", Model: "preflight"})
		return err
	})
}

// UpdateDefaults preserves the settings AST while changing only
// models.default_provider and models.default.
func UpdateDefaults(ctx context.Context, path string, update DefaultUpdate) (privatefile.CommitState, error) {
	if !providerIDPattern.MatchString(update.Provider) || strings.TrimSpace(update.Model) == "" {
		return privatefile.CommitNotApplied, errors.New("settings default update: invalid provider or model")
	}
	state, err := privatefile.Update(ctx, path, settingsPath(), maxConfigBytes, func(data []byte) ([]byte, bool, error) {
		return mutateDefaults(data, update)
	})
	if err != nil {
		if errors.Is(err, privatefile.ErrConfigurationChanged) {
			return state, err
		}
		return state, fmt.Errorf("settings default update: %w", err)
	}
	return state, nil
}
