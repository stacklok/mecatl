package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "mecated-test-home-")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create isolated test home: %v\n", err)
		os.Exit(1)
	}
	configHome := filepath.Join(root, "config")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(configHome, 0o700); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create isolated test config: %v\n", err)
		_ = os.RemoveAll(root)
		os.Exit(1)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create isolated test HOME: %v\n", err)
		_ = os.RemoveAll(root)
		os.Exit(1)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", configHome); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "set isolated XDG_CONFIG_HOME: %v\n", err)
		_ = os.RemoveAll(root)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", home); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "set isolated HOME: %v\n", err)
		_ = os.RemoveAll(root)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
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
