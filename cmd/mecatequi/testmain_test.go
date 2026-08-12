package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/internal/testutil/testhome"
)

func TestMain(m *testing.M) {
	os.Exit(testhome.Run("mecatequi", m.Run))
}

func TestConventionalAuthFileIsIsolated(t *testing.T) {
	authPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "mecatl", "auth.yaml")
	if _, err := os.Stat(authPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("isolated conventional auth path must not exist: %v", err)
	}
	flags, err := parseFlags([]string{"--prompt", "test"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if flags.providerCredentials.HasOpenAICodex() || flags.providerCredentials.AuthFileWarning != "" {
		t.Fatal("ordinary mecatequi tests consulted conventional auth-file state")
	}
}
