package authfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

var testKnownProviders = []string{"anthropic", "openai", "openrouter", "opencode", "openai-codex"}

// fakeEnv builds a xdgconfig.ResolveEnv over a real temp directory (not a
// pure in-memory fake) so file-permission tests can exercise real os.Stat
// mode bits — the one part of this package that a mocked filesystem can't
// meaningfully stand in for.
func fakeEnv(home string) xdgconfig.ResolveEnv {
	return xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return home, nil },
		ReadFile:    os.ReadFile,
	}
}

func writeFile(t *testing.T, dir, contents string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, "auth.yaml")
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestDefaultPathIsSettingsYAMLSibling(t *testing.T) {
	home := t.TempDir()
	got := DefaultPath(fakeEnv(home))
	want := filepath.Join(home, ".config", "mecatl", "auth.yaml")
	if got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
}

func TestLoadFillsAPIKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "providers:\n  anthropic:\n    api_key: sk-ant-good\n", 0o600)
	f, warning := Load(filepath.Join(dir, "auth.yaml"), false, fakeEnv(dir), testKnownProviders)
	if warning != "" {
		t.Errorf("warning = %q, want empty", warning)
	}
	if got := f.APIKey("anthropic"); got != "sk-ant-good" {
		t.Errorf("APIKey(anthropic) = %q, want sk-ant-good", got)
	}
	if got := f.APIKey("openai"); got != "" {
		t.Errorf("APIKey(openai) = %q, want empty (file has no entry)", got)
	}
}

func TestAPIKeyCompatibility(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		apiKey   string
	}{
		{name: "anthropic", provider: "anthropic", apiKey: "sk-ant-compatible"},
		{name: "openai", provider: "openai", apiKey: "sk-openai-compatible"},
		{name: "openrouter", provider: "openrouter", apiKey: "sk-or-compatible"},
		{name: "opencode", provider: "opencode", apiKey: "sk-opencode-compatible"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			contents := "providers:\n  " + tt.provider + ":\n    api_key: " + tt.apiKey + "\n"
			path := writeFile(t, dir, contents, 0o600)
			f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
			if warning != "" {
				t.Fatalf("warning = %q, want empty", warning)
			}
			if got := f.APIKey(tt.provider); got != tt.apiKey {
				t.Errorf("APIKey(%s) = %q, want original key", tt.provider, got)
			}
		})
	}
}

func TestOpenAICodexOAuthSchema(t *testing.T) {
	t.Run("valid shape and copy-returning accessor", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFile(t, dir, "providers:\n  openai-codex:\n    oauth:\n      access_token: token-raw\n      account_id: account-raw\n      expires_at: 2026-08-04T18:30:00Z\n", 0o600)
		f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
		if warning != "" {
			t.Fatalf("warning = %q, want empty", warning)
		}
		got := f.OAuth("openai-codex")
		want := (OAuthEntry{AccessToken: "token-raw", AccountID: "account-raw", ExpiresAt: "2026-08-04T18:30:00Z"})
		if got != want {
			t.Fatalf("OAuth(openai-codex) = %#v, want %#v", got, want)
		}
		got.AccessToken = "mutated-copy"
		if again := f.OAuth("openai-codex"); again != want {
			t.Fatalf("OAuth accessor exposed mutable state: %#v", again)
		}
	})

	t.Run("structurally invalid file is rejected as a whole", func(t *testing.T) {
		tests := []struct {
			name, codex string
		}{
			{name: "unknown OAuth field", codex: "    oauth:\n      access_token: codex-secret\n      refresh_token: refresh-secret\n"},
			{name: "malformed OAuth nesting", codex: "    oauth: codex-secret\n"},
			{name: "non-string access token", codex: "    oauth:\n      access_token: 123\n"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				dir := t.TempDir()
				contents := "providers:\n  anthropic:\n    api_key: sk-ant-valid\n  openai-codex:\n" + tt.codex
				path := writeFile(t, dir, contents, 0o600)
				f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
				if warning == "" || f != nil {
					t.Fatalf("Load(invalid schema) = (%v, %q), want (nil, warning)", f, warning)
				}
			})
		}
	})

	t.Run("empty access token drops only Codex OAuth", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFile(t, dir, "providers:\n  anthropic:\n    api_key: sk-ant-survives\n  openai-codex:\n    oauth:\n      access_token: \"  \"\n      account_id: account-secret\n", 0o600)
		f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
		if warning == "" {
			t.Fatal("empty access token should produce a warning")
		}
		if got := f.APIKey("anthropic"); got != "sk-ant-survives" {
			t.Fatalf("APIKey(anthropic) = %q, want unrelated key preserved", got)
		}
		if got := f.OAuth("openai-codex"); got != (OAuthEntry{}) {
			t.Fatalf("OAuth(openai-codex) = %#v, want zero value", got)
		}
	})

	t.Run("OAuth is scoped to openai-codex and API key remains usable", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFile(t, dir, "providers:\n  openai:\n    api_key: sk-api-survives\n    oauth:\n      access_token: codex-secret\n", 0o600)
		f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
		if warning == "" {
			t.Fatal("mis-scoped OAuth should produce a warning")
		}
		if got := f.APIKey("openai"); got != "sk-api-survives" {
			t.Errorf("APIKey(openai) = %q, want existing API key preserved", got)
		}
		if got := f.OAuth("openai"); got != (OAuthEntry{}) {
			t.Errorf("OAuth(openai) = %#v, want zero value", got)
		}
	})

	t.Run("API key cannot enable openai-codex", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFile(t, dir, "providers:\n  openai-codex:\n    api_key: sk-wrong-billing-identity\n", 0o600)
		f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
		if warning == "" {
			t.Fatal("Codex API key should produce a warning")
		}
		if f != nil && f.APIKey("openai-codex") != "" {
			t.Fatal("API key must not enable the subscription provider")
		}
	})
}

func TestLoadRejectsSecondYAMLDocument(t *testing.T) {
	tests := []struct {
		name   string
		second string
	}{
		{name: "populated", second: "providers:\n  openai:\n    api_key: second-document-secret\n"},
		{name: "empty", second: ""},
		{name: "null", second: "null\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			contents := "providers:\n  anthropic:\n    api_key: first-document-secret\n---\n" + tt.second
			path := writeFile(t, dir, contents, 0o600)
			f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
			if f != nil || warning == "" {
				t.Fatalf("Load(multiple documents) = (%v, %q), want (nil, warning)", f, warning)
			}
			for _, secret := range []string{"first-document-secret", "second-document-secret"} {
				if strings.Contains(warning, secret) {
					t.Fatalf("warning leaked document content %q: %q", secret, warning)
				}
			}
		})
	}
}

func TestOAuthWarningsAreValueFree(t *testing.T) {
	const sentinel = "sk-oauth-SENTINEL-MUST-NOT-LEAK"
	tests := []struct {
		name     string
		contents string
	}{
		{
			name:     "mis-scoped OAuth",
			contents: "providers:\n  openai:\n    oauth:\n      access_token: " + sentinel + "\n",
		},
		{
			name:     "malformed nesting",
			contents: "providers:\n  openai-codex:\n    oauth: " + sentinel + "\n",
		},
		{
			name:     "unknown OAuth field",
			contents: "providers:\n  openai-codex:\n    oauth:\n      access_token: valid-token\n      " + sentinel + ": value-must-not-leak\n",
		},
		{
			name:     "Codex API key",
			contents: "providers:\n  openai-codex:\n    api_key: " + sentinel + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeFile(t, dir, tt.contents, 0o600)
			_, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
			if warning == "" {
				t.Fatal("invalid OAuth configuration should warn")
			}
			for n := 4; n <= len(sentinel); n++ {
				if strings.Contains(warning, sentinel[:n]) {
					t.Fatalf("warning leaked a %d-byte secret fragment in %q", n, warning)
				}
			}
		})
	}
}

func TestAPIKeyOnNilFile(t *testing.T) {
	var f *File
	if got := f.APIKey("anthropic"); got != "" {
		t.Errorf("nil File APIKey = %q, want empty", got)
	}
}

func TestLoadMissingConventionalFileIsSilent(t *testing.T) {
	dir := t.TempDir() // real dir, no auth.yaml inside it
	f, warning := Load(filepath.Join(dir, "auth.yaml"), false, fakeEnv(dir), testKnownProviders)
	if warning != "" {
		t.Errorf("warning = %q, want empty (missing conventional file is not an error)", warning)
	}
	if f != nil {
		t.Errorf("f = %+v, want nil", f)
	}
}

func TestLoadMissingExplicitFileWarns(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "auth.yaml")
	_, warning := Load(missing, true, fakeEnv(dir), testKnownProviders)
	if warning == "" {
		t.Fatal("warning should be non-empty for a missing EXPLICIT path")
	}
	if !strings.Contains(warning, missing) {
		t.Errorf("warning = %q, want it to name %q", warning, missing)
	}
}

func TestLoadEmptyPathIsNoop(t *testing.T) {
	f, warning := Load("", false, fakeEnv(t.TempDir()), testKnownProviders)
	if f != nil || warning != "" {
		t.Errorf("Load(\"\") = (%v, %q), want (nil, \"\")", f, warning)
	}
}

func TestLoadEmptyFileIsFine(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "", 0o600)
	f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
	if warning != "" {
		t.Errorf("warning = %q, want empty", warning)
	}
	if f.APIKey("anthropic") != "" {
		t.Error("an empty file must contribute no keys")
	}
}

func TestLoadOversizedFileWarns(t *testing.T) {
	dir := t.TempDir()
	huge := "providers:\n  anthropic:\n    api_key: " + strings.Repeat("x", maxFileBytes+1) + "\n"
	path := writeFile(t, dir, huge, 0o600)
	f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
	if warning == "" {
		t.Fatal("an oversized file should warn rather than parse silently")
	}
	if f != nil {
		t.Error("an oversized file must contribute no keys")
	}
}

func TestLoadUnknownProviderNameWarnsButValidEntryApplies(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "providers:\n  anthropic:\n    api_key: sk-ant-good\n  anthropik:\n    api_key: sk-ant-typo\n", 0o600)
	f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
	if got := f.APIKey("anthropic"); got != "sk-ant-good" {
		t.Errorf("APIKey(anthropic) = %q, want sk-ant-good (the valid entry must still apply)", got)
	}
	if warning == "" {
		t.Fatal("warning should be non-empty for an unknown provider name")
	}
	// The unknown provider name must NOT appear verbatim in the warning — a
	// secret typed where the provider name belongs (inverted nesting) would
	// otherwise echo into the warning (CWE-532). The warning reports a COUNT,
	// never the name.
	if strings.Contains(warning, "anthropik") {
		t.Errorf("warning = %q, must NOT contain the unknown provider name %q", warning, "anthropik")
	}
	if !strings.Contains(warning, "1 unknown provider") {
		t.Errorf("warning = %q, want it to report the count (1 unknown provider)", warning)
	}
	// The warning must still list the known providers so the operator can
	// self-correct without guessing which names are valid.
	for _, known := range testKnownProviders {
		if !strings.Contains(warning, known) {
			t.Errorf("warning = %q, want it to list known provider %q", warning, known)
		}
	}
}

// TestLoadStrictDecodeRejectsUnknownField proves a typo'd field inside a
// provider entry (api_key misspelled) is a parse error rather than a
// silently-dropped credential.
func TestLoadStrictDecodeRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "providers:\n  anthropic:\n    apikey: sk-ant-typo\n", 0o600)
	f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
	if warning == "" {
		t.Fatal("a mistyped field name should produce a warning, not a silently-dropped credential")
	}
	if f != nil {
		t.Error("a malformed file must contribute no keys")
	}
}

// TestLoadMalformedYAMLNeverLeaksFileContent is the security-review
// regression test: a structural type mismatch (a scalar where a mapping is
// expected) must never let the underlying YAML decoder's rendered error —
// which can otherwise echo the raw source text of the offending node — leak
// a fragment of a real secret into the warning string. Reproduces the exact
// shape that triggered it: forgetting the "api_key:" nesting.
func TestLoadMalformedYAMLNeverLeaksFileContent(t *testing.T) {
	const realSecret = "sk-ant-api03-THIS-MUST-NEVER-APPEAR-IN-A-WARNING"
	dir := t.TempDir()
	path := writeFile(t, dir, "providers:\n  anthropic: "+realSecret+"\n", 0o600)
	f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
	if warning == "" {
		t.Fatal("a structural type mismatch should produce a warning")
	}
	if f != nil {
		t.Error("a malformed file must contribute no keys")
	}
	if strings.Contains(warning, realSecret) {
		t.Fatalf("warning leaked the real secret verbatim: %q", warning)
	}
	// Also check any prefix fragment of the secret (the decoder truncates
	// long values but still echoes a short leading fragment) — the whole
	// point of the fix is that NO fragment of file content reaches here.
	for n := 4; n <= len(realSecret); n++ {
		if strings.Contains(warning, realSecret[:n]) {
			t.Fatalf("warning leaked a %d-byte fragment of the real secret: %q (in %q)", n, realSecret[:n], warning)
		}
	}
}

// TestLoadUnknownProviderNameNeverLeaksSecretShapedName is the
// security-review regression test for the value-free unknown-provider warning:
// a provider name that looks like a real secret (sk-ant-api03-...) must never
// appear in the warning string — not verbatim, and not as a substring
// fragment. This is the inverted-nesting analog of
// TestLoadMalformedYAMLNeverLeaksFileContent: that test covers structural
// mismatch (a scalar where a mapping is expected), while this one covers
// arbitrary YAML keys that are file content and must never be echoed.
func TestLoadUnknownProviderNameNeverLeaksSecretShapedName(t *testing.T) {
	const secretName = "sk-ant-api03-THIS-MUST-NEVER-APPEAR-IN-A-WARNING"
	contents := "providers:\n  anthropic:\n    api_key: sk-ant-good\n  " + secretName + ":\n    api_key: x\n"
	dir := t.TempDir()
	path := writeFile(t, dir, contents, 0o600)
	f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
	if warning == "" {
		t.Fatal("warning should be non-empty for an unknown provider name")
	}
	// The valid entry must still apply.
	if got := f.APIKey("anthropic"); got != "sk-ant-good" {
		t.Errorf("APIKey(anthropic) = %q, want sk-ant-good (the valid entry must still apply)", got)
	}
	// No fragment — the whole point is that NO user-supplied key string
	// reaches the warning. Check every substring prefix from 4 bytes to the
	// full length, matching the existing
	// TestLoadMalformedYAMLNeverLeaksFileContent pattern.
	if strings.Contains(warning, secretName) {
		t.Fatalf("warning leaked the secret-shaped provider name verbatim: %q", warning)
	}
	for n := 4; n <= len(secretName); n++ {
		if strings.Contains(warning, secretName[:n]) {
			t.Fatalf("warning leaked a %d-byte fragment of the secret-shaped name: %q (in %q)", n, secretName[:n], warning)
		}
	}
}

// TestLoadLoosePermissionsAndUnknownProviderBothWarned proves the
// accumulation fix: a file with a loose permission AND a content warning
// (unknown provider name) reports BOTH in the single returned warning string
// — the permission finding is not clobbered by the content warning.
func TestLoadLoosePermissionsAndUnknownProviderBothWarned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits don't apply on windows")
	}
	dir := t.TempDir()
	path := writeFile(t, dir, "providers:\n  anthropic:\n    api_key: sk-ant-good\n  anthropik:\n    api_key: sk-ant-typo\n", 0o644)
	f, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
	if warning == "" {
		t.Fatal("warning should be non-empty for both loose permissions and unknown provider")
	}
	// The valid entry must still apply.
	if got := f.APIKey("anthropic"); got != "sk-ant-good" {
		t.Errorf("APIKey(anthropic) = %q, want sk-ant-good (the valid entry must still apply)", got)
	}
	if !strings.Contains(warning, "0644") {
		t.Errorf("warning = %q, want it to report the loose mode 0644", warning)
	}
	if !strings.Contains(warning, "1 unknown provider") {
		t.Errorf("warning = %q, want it to report the count (1 unknown provider)", warning)
	}
}

func TestCheckPermissionsWarnsOnLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits don't apply on windows")
	}
	dir := t.TempDir()
	path := writeFile(t, dir, "providers:\n  anthropic:\n    api_key: sk-ant-good\n", 0o644)
	_, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
	if warning == "" || !strings.Contains(warning, "0644") {
		t.Errorf("warning = %q, want it to name the loose mode 0644", warning)
	}
}

func TestCheckPermissionsSilentOnStrictPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits don't apply on windows")
	}
	dir := t.TempDir()
	path := writeFile(t, dir, "providers:\n  anthropic:\n    api_key: sk-ant-good\n", 0o600)
	_, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
	if warning != "" {
		t.Errorf("warning = %q, want empty for a strictly-permissioned 0600 file", warning)
	}
}
