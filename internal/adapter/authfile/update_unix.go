package authfile

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/stacklok/mecatl/internal/adapter/privatefile"
	"github.com/stacklok/mecatl/internal/adapter/providerid"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func updateSupported() bool { return privatefile.Supported() }

func preflightAPIKeyUpdateTarget(path string) error {
	return privatefile.Preflight(path, DefaultPath(xdgconfig.OSEnv), maxFileBytes, validateAuthDocument)
}

func updateAPIKey(ctx context.Context, path string, update APIKeyUpdate) (CommitState, error) {
	operation := "set"
	if update.APIKey == nil {
		operation = "remove"
	}
	if !providerid.Valid(update.Provider) {
		return CommitNotApplied, errors.New("auth update for provider: invalid provider id")
	}
	switch update.Provider {
	case "mock", "openai-codex", "toolhive":
		return CommitNotApplied, fmt.Errorf("auth %s for provider %s: provider does not use API keys", operation, update.Provider)
	}
	if update.APIKey != nil && !utf8.ValidString(*update.APIKey) {
		return CommitNotApplied, fmt.Errorf("auth %s for provider %s: API key is not valid UTF-8", operation, update.Provider)
	}
	state, err := privatefile.Update(ctx, path, DefaultPath(xdgconfig.OSEnv), maxFileBytes, func(data []byte) ([]byte, bool, error) {
		return mutateAuth(data, update)
	})
	if err != nil {
		if errors.Is(err, privatefile.ErrConfigurationChanged) {
			return state, errConfigurationChanged
		}
		return state, fmt.Errorf("auth %s for provider %s: %w", operation, update.Provider, err)
	}
	return state, nil
}
