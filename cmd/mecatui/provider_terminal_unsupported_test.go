//go:build !linux && !darwin

package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
)

func TestProviderReviewUnsupportedTerminalGuidance(t *testing.T) {
	c := testProviderCommands()
	c.terminal.readAPIKey = readHiddenProviderAPIKey
	c.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		t.Fatal("unsupported terminal wrote credentials")
		return authfile.CommitNotApplied, nil
	}
	err := c.runAPIKey(context.Background(), providerCredentialResolution(providerActionLogin, "openai"), "unused", io.Discard, io.Discard)
	if err == nil {
		t.Fatal("unsupported entry succeeded")
	}
	for _, want := range []string{"No key was read or saved", "OPENAI_API_KEY", "credential_store.api_key.file", "owner-only", "https://mecatl.dev/docs/"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q: %v", want, err)
		}
	}
}
