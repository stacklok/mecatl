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
// XDG_CONFIG_HOME is what actually exercises that path.
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
// auth.yaml exists yet, no --auth-file flag passed — produces no warning at
// all, matching every other "empty = auto, absent = skip" convention in this
// codebase (soul-file, skills-dir, ...).
func TestApplyMissingConventionalAuthFileIsSilent(t *testing.T) {
	clearProviderEnv(t)
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
	if keys.Any() {
		t.Error("Any() should be false: no env, no file")
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

// TestApplyMalformedAuthFileWarns proves invalid YAML at the auth-file path
// is reported (never silently ignored) and contributes no keys, regardless of
// whether the path was explicit or the conventional default.
func TestApplyMalformedAuthFileWarns(t *testing.T) {
	clearProviderEnv(t)
	writeAuthFile(t, "providers: [this is not a map]")

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cfg app.Config
	keys := pf.Apply(&cfg)

	if keys.AuthFileWarning == "" {
		t.Fatal("AuthFileWarning should be non-empty for malformed YAML")
	}
	if cfg.AnthropicKey != "" || keys.Any() {
		t.Errorf("a malformed file must contribute no keys; got %+v", keys)
	}
}

// TestApplyStrictDecodeRejectsUnknownField proves a typo'd field inside a
// provider entry (api_key misspelled) is a parse error rather than a
// silently-dropped credential — the strict (KnownFields) decode is exactly
// what should catch this in a credentials file.
func TestApplyStrictDecodeRejectsUnknownField(t *testing.T) {
	clearProviderEnv(t)
	writeAuthFile(t, `
providers:
  anthropic:
    apikey: sk-ant-typo
`)
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cfg app.Config
	keys := pf.Apply(&cfg)

	if keys.AuthFileWarning == "" {
		t.Fatal("a mistyped field name should produce a warning, not a silently-dropped credential")
	}
	if cfg.AnthropicKey != "" {
		t.Errorf("AnthropicKey = %q, want empty (the typo'd entry must not resolve)", cfg.AnthropicKey)
	}
}

// TestApplyUnknownProviderNameWarns proves a provider name outside the known
// set (anthropic/openai/openrouter/opencode) is reported, while a VALID
// sibling entry in the same file still applies — a typo in one entry
// shouldn't cost you the rest of the file.
func TestApplyUnknownProviderNameWarns(t *testing.T) {
	clearProviderEnv(t)
	writeAuthFile(t, `
providers:
  anthropic:
    api_key: sk-ant-good
  anthropik:
    api_key: sk-ant-typo
`)
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cfg app.Config
	keys := pf.Apply(&cfg)

	if cfg.AnthropicKey != "sk-ant-good" {
		t.Errorf("AnthropicKey = %q, want sk-ant-good (the valid entry must still apply)", cfg.AnthropicKey)
	}
	if keys.AuthFileWarning == "" || !strings.Contains(keys.AuthFileWarning, "anthropik") {
		t.Errorf("AuthFileWarning = %q, want it to name the unknown provider %q", keys.AuthFileWarning, "anthropik")
	}
}

// TestApplyEmptyAuthFileIsFine proves a present-but-empty auth.yaml (e.g. a
// freshly-touched file) parses cleanly with no warning and contributes no keys.
func TestApplyEmptyAuthFileIsFine(t *testing.T) {
	clearProviderEnv(t)
	writeAuthFile(t, "")

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cfg app.Config
	keys := pf.Apply(&cfg)

	if keys.AuthFileWarning != "" {
		t.Errorf("AuthFileWarning = %q, want empty", keys.AuthFileWarning)
	}
	if keys.Any() {
		t.Error("Any() should be false for an empty file and no env")
	}
}

// TestApplyOversizedAuthFileWarns proves the size cap rejects an
// implausibly-large auth.yaml before parsing (defense in depth, CWE-770).
func TestApplyOversizedAuthFileWarns(t *testing.T) {
	clearProviderEnv(t)
	huge := "providers:\n  anthropic:\n    api_key: " + strings.Repeat("x", maxAuthFileBytes+1) + "\n"
	writeAuthFile(t, huge)

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cfg app.Config
	keys := pf.Apply(&cfg)

	if keys.AuthFileWarning == "" {
		t.Fatal("an oversized auth.yaml should warn rather than parse silently")
	}
	if cfg.AnthropicKey != "" {
		t.Error("an oversized file must contribute no keys")
	}
}
