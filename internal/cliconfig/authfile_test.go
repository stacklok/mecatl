package cliconfig

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/app"
)

// writeAuthFile points XDG_CONFIG_HOME at a fresh temp dir and writes
// mecatl/auth.yaml under it with the given contents, returning the file path.
// Using the real environment (via t.Setenv) rather than a fake ResolveEnv
// matches this package's existing convention (see TestApplyMapsAllSixFields
// and friends, which drive the provider-key env vars the same way) — Apply
// resolves the conventional default through xdgconfig.OSEnv, so redirecting
// XDG_CONFIG_HOME is what actually exercises that path. The parsing/schema
// specifics (strict decode, unknown providers, permissions, the secret-leak
// regression) live at the internal/adapter/authfile level; these tests only
// prove Apply's WIRING — precedence and flag plumbing.
func writeAuthFile(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	authDir := filepath.Join(dir, "mecatl")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(authDir, "auth.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write auth.yaml: %v", err)
	}
	return path
}

func clearProviderEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envOpenAIKey, "")
	t.Setenv(envOpenRouterKey, "")
	t.Setenv(envAnthropicKey, "")
	t.Setenv(envOpenCodeKey, "")
}

// TestRegisterProviderFlagsRegistersAuthFile proves --auth-file is registered
// alongside the three base-URL flags.
func TestRegisterProviderFlagsRegistersAuthFile(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	_ = RegisterProviderFlags(fs, ProviderFlagHelp{})
	f := fs.Lookup("auth-file")
	if f == nil {
		t.Fatal("flag --auth-file not registered")
	}
	if f.Usage == "" {
		t.Error("flag --auth-file has empty help")
	}
}

// TestApplyFillsFromAuthFileWhenEnvUnset proves an auth.yaml entry fills a
// still-empty provider credential at the conventional default path, with no
// --auth-file flag needed, and reports no warning.
func TestApplyFillsFromAuthFileWhenEnvUnset(t *testing.T) {
	clearProviderEnv(t)
	writeAuthFile(t, `
providers:
  anthropic:
    api_key: sk-ant-from-file
`)
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cfg app.Config
	keys := pf.Apply(&cfg)

	if cfg.AnthropicKey != "sk-ant-from-file" {
		t.Errorf("AnthropicKey = %q, want sk-ant-from-file", cfg.AnthropicKey)
	}
	if keys.Anthropic != "sk-ant-from-file" {
		t.Errorf("keys.Anthropic = %q, want sk-ant-from-file", keys.Anthropic)
	}
	if keys.AuthFileWarning != "" {
		t.Errorf("AuthFileWarning = %q, want empty (a valid file is not a warning)", keys.AuthFileWarning)
	}
	// A provider the file doesn't mention stays empty, exactly like an unset
	// env var — auth.yaml is additive, never a blanket "any provider works".
	if cfg.OpenAIKey != "" {
		t.Errorf("OpenAIKey = %q, want empty (auth.yaml has no openai entry)", cfg.OpenAIKey)
	}
}

// TestApplyEnvWinsOverAuthFile proves an environment credential is never
// overwritten by auth.yaml — the byte-identical-default invariant: a
// deployment that only ever used env vars behaves identically whether or not
// an auth.yaml happens to exist alongside it.
func TestApplyEnvWinsOverAuthFile(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv(envAnthropicKey, "sk-ant-from-env")
	writeAuthFile(t, `
providers:
  anthropic:
    api_key: sk-ant-from-file
`)
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cfg app.Config
	keys := pf.Apply(&cfg)

	if cfg.AnthropicKey != "sk-ant-from-env" {
		t.Errorf("AnthropicKey = %q, want sk-ant-from-env (env must win)", cfg.AnthropicKey)
	}
	if keys.AuthFileWarning != "" {
		t.Errorf("AuthFileWarning = %q, want empty", keys.AuthFileWarning)
	}
}

// TestApplyMissingConventionalAuthFileIsSilent proves the common case — no
// auth.yaml exists yet, but an environment credential is present, so the missing
// conventional file produces no warning.
func TestApplyMissingConventionalAuthFileIsSilent(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv(envOpenAIKey, "sk-from-env")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // real dir, but no mecatl/auth.yaml inside it

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cfg app.Config
	keys := pf.Apply(&cfg)

	if keys.AuthFileWarning != "" {
		t.Errorf("AuthFileWarning = %q, want empty (a missing conventional file is not an error)", keys.AuthFileWarning)
	}
	if !keys.Any() || cfg.OpenAIKey != "sk-from-env" {
		t.Errorf("environment credential should remain usable: keys=%+v cfg.OpenAIKey=%q", keys, cfg.OpenAIKey)
	}
}

func TestApplyMissingConventionalAuthFileIsSilentWithoutEnv(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	keys := pf.Apply(&app.Config{})
	if keys.AuthFileWarning != "" {
		t.Fatalf("missing conventional auth file should be silent in shared resolution: %q", keys.AuthFileWarning)
	}
}

// TestApplyExplicitAuthFileFlagIsUsed proves --auth-file overrides the
// conventional default path.
func TestApplyExplicitAuthFileFlagIsUsed(t *testing.T) {
	clearProviderEnv(t)
	// Point XDG_CONFIG_HOME somewhere with NO auth.yaml, so only the explicit
	// flag path can possibly supply a key.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	explicitDir := t.TempDir()
	explicitPath := filepath.Join(explicitDir, "custom-auth.yaml")
	if err := os.WriteFile(explicitPath, []byte("providers:\n  anthropic:\n    api_key: sk-ant-explicit\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse([]string{"--auth-file", explicitPath}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cfg app.Config
	keys := pf.Apply(&cfg)

	if cfg.AnthropicKey != "sk-ant-explicit" {
		t.Errorf("AnthropicKey = %q, want sk-ant-explicit", cfg.AnthropicKey)
	}
	if keys.AuthFileWarning != "" {
		t.Errorf("AuthFileWarning = %q, want empty", keys.AuthFileWarning)
	}
}

// TestApplyMissingExplicitAuthFileWarns proves an explicit --auth-file
// pointed at a nonexistent path IS reported — the operator named that exact
// path, so silence would hide a typo.
func TestApplyMissingExplicitAuthFileWarns(t *testing.T) {
	clearProviderEnv(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse([]string{"--auth-file", missing}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cfg app.Config
	keys := pf.Apply(&cfg)

	if keys.AuthFileWarning == "" {
		t.Fatal("AuthFileWarning should be non-empty for a missing explicit --auth-file path")
	}
	if !strings.Contains(keys.AuthFileWarning, missing) {
		t.Errorf("AuthFileWarning = %q, want it to name the path %q", keys.AuthFileWarning, missing)
	}
}

// TestApplyAllFourProvidersFillFromFile proves cmp.Or is wired for all four
// provider fields, not just Anthropic.
func TestApplyAllFourProvidersFillFromFile(t *testing.T) {
	clearProviderEnv(t)
	writeAuthFile(t, `
providers:
  anthropic:
    api_key: sk-ant
  openai:
    api_key: sk-oai
  openrouter:
    api_key: sk-or
  opencode:
    api_key: sk-oc
`)
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cfg app.Config
	keys := pf.Apply(&cfg)

	if keys.Anthropic != "sk-ant" || keys.OpenAI != "sk-oai" || keys.OpenRouter != "sk-or" || keys.OpenCode != "sk-oc" {
		t.Errorf("keys mismatch: %+v", keys)
	}
	if cfg.AnthropicKey != "sk-ant" || cfg.OpenAIKey != "sk-oai" || cfg.OpenRouterKey != "sk-or" || cfg.OpenCodeKey != "sk-oc" {
		t.Errorf("cfg mismatch: %+v", cfg)
	}
}
