package authfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

var testKnownProviders = []string{"anthropic", "openai", "openrouter", "opencode"}

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
	if warning == "" || !strings.Contains(warning, "anthropik") {
		t.Errorf("warning = %q, want it to name the unknown provider %q", warning, "anthropik")
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
