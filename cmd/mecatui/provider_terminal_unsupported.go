//go:build !linux && !darwin

package main

import (
	"context"
	"os"
)

func readProviderTerminalLine(ctx context.Context, _ *os.File, _ *os.File, _ string, _ bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", providerTerminalError("local provider terminal input is supported on Linux and macOS. No key was read or saved. Set the provider's API-key environment variable (for example OPENAI_API_KEY), or manually configure credential_store.api_key.file as an owner-only plaintext file; see https://mecatl.dev/docs/features/choose-models#set-up-a-local-provider . Never put a key in a command argument.")
}
