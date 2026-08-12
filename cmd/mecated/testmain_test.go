package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/internal/testutil/testhome"
)

func TestMain(m *testing.M) {
	os.Exit(testhome.Run("mecated", m.Run))
}

func TestConventionalAuthFileIsIsolated(t *testing.T) {
	authPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "mecatl", "auth.yaml")
	if _, err := os.Stat(authPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("isolated conventional auth path must not exist: %v", err)
	}
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if cfg.providerCredentials.HasOpenAICodex() || cfg.providerCredentials.AuthFileWarning != "" {
		t.Fatal("ordinary mecated tests consulted conventional auth-file state")
	}
}
