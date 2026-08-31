package workspacetrust

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// envWithSettings returns an injected env whose user-global settings.yaml
// (<config>/mecatl/settings.yaml) returns the given bytes. configDir is exported as
// $XDG_CONFIG_HOME so UserConfigDir resolves to it. Any other read fails (no home).
// This keeps the trust read fully offline — never the developer's real ~/.config.
func envWithSettings(configDir string, settings []byte) xdgconfig.ResolveEnv {
	want := filepath.Join(configDir, "mecatl", "settings.yaml")
	return xdgconfig.ResolveEnv{
		Getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return configDir
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile: func(p string) ([]byte, error) {
			if p == want {
				if settings == nil {
					return nil, errors.New("not found")
				}
				return settings, nil
			}
			return nil, errors.New("not found")
		},
	}
}

// mkdir creates a real directory under t.TempDir() and returns its realpath
// (EvalSymlinks resolves e.g. macOS /var→/private/var so the test compares the
// resolved form, matching the adapter's keying).
func realDir(t *testing.T, base, name string) string {
	t.Helper()
	p := filepath.Join(base, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("eval %s: %v", p, err)
	}
	return resolved
}

// TestIsDeclaredMatch asserts a workspace whose realpath is in trustedWorkspaces
// is reported declared-trusted.
func TestIsDeclaredMatch(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	cfg := t.TempDir()
	settings := []byte("trustedWorkspaces:\n  - " + ws + "\n")

	r := NewWithEnv(envWithSettings(cfg, settings))
	if !r.IsDeclared(ws) {
		t.Fatalf("IsDeclared(%q) = false, want true (declared in trustedWorkspaces)", ws)
	}
}

// TestIsDeclaredNoMatch asserts a workspace NOT in the list is not trusted.
func TestIsDeclaredNoMatch(t *testing.T) {
	base := t.TempDir()
	declared := realDir(t, base, "trusted")
	other := realDir(t, base, "untrusted")
	cfg := t.TempDir()
	settings := []byte("trustedWorkspaces:\n  - " + declared + "\n")

	r := NewWithEnv(envWithSettings(cfg, settings))
	if r.IsDeclared(other) {
		t.Fatalf("IsDeclared(%q) = true, want false (not declared)", other)
	}
}

// TestIsDeclaredEmptyWorkspace asserts an empty workspace is never trusted.
func TestIsDeclaredEmptyWorkspace(t *testing.T) {
	r := NewWithEnv(envWithSettings(t.TempDir(), []byte("trustedWorkspaces:\n  - /some/path\n")))
	if r.IsDeclared("") {
		t.Fatal("IsDeclared(\"\") = true, want false")
	}
}

// TestIsDeclaredNoSettings asserts a missing settings.yaml ⇒ not trusted (no key).
func TestIsDeclaredNoSettings(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	// nil settings ⇒ ReadFile returns "not found".
	r := NewWithEnv(envWithSettings(t.TempDir(), nil))
	if r.IsDeclared(ws) {
		t.Fatal("IsDeclared with no settings.yaml = true, want false (no declared trust)")
	}
}

// TestIsDeclaredMissingKey asserts a settings.yaml with permission keys but NO
// trustedWorkspaces key ⇒ not trusted (the two readers coexist on one file).
func TestIsDeclaredMissingKey(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	cfg := t.TempDir()
	settings := []byte("permissions:\n  allow:\n    - \"Bash(go test*)\"\n")

	r := NewWithEnv(envWithSettings(cfg, settings))
	if r.IsDeclared(ws) {
		t.Fatal("IsDeclared with no trustedWorkspaces key = true, want false")
	}
}

// TestIsDeclaredMalformedEntryIgnoredRestHonoured asserts a single bad entry (a
// path that does not resolve) is skipped while a valid entry still grants trust.
func TestIsDeclaredMalformedEntryIgnoredRestHonoured(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	cfg := t.TempDir()
	// First entry is a nonexistent path (EvalSymlinks fails ⇒ skipped); second is valid.
	settings := []byte("trustedWorkspaces:\n  - /nonexistent/path/that/does/not/resolve\n  - " + ws + "\n")

	r := NewWithEnv(envWithSettings(cfg, settings))
	if !r.IsDeclared(ws) {
		t.Fatal("IsDeclared = false, want true (a bad entry must not suppress a valid one)")
	}
}

// TestIsDeclaredBlankEntriesIgnored asserts empty/blank list entries are dropped
// and never spuriously match an empty/odd workspace.
func TestIsDeclaredBlankEntriesIgnored(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	cfg := t.TempDir()
	settings := []byte("trustedWorkspaces:\n  - \"\"\n  - \"   \"\n  - " + ws + "\n")

	r := NewWithEnv(envWithSettings(cfg, settings))
	if !r.IsDeclared(ws) {
		t.Fatal("IsDeclared = false, want true (blank entries ignored, valid one honoured)")
	}
}

// TestIsDeclaredUnparseable asserts a corrupt settings.yaml ⇒ not trusted
// (fail-safe: a corrupt config never GRANTS trust).
func TestIsDeclaredUnparseable(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	cfg := t.TempDir()
	const malformed = "trustedWorkspaces: [credential_key: super-secret-token # parser-looking: :\n  quote: \"never-log-me\"\n"
	var buf bytes.Buffer
	r := NewWithEnv(envWithSettings(cfg, []byte(malformed))).
		WithDiagnostics(slogdiag.New(&buf, false, port.LevelInfo))
	if r.IsDeclared(ws) {
		t.Fatal("IsDeclared with unparseable settings.yaml = true, want false (fail-safe)")
	}
	output := buf.String()
	if !strings.Contains(output, "settings.yaml unparseable") {
		t.Fatalf("missing fail-safe diagnostic: %s", output)
	}
	for _, forbidden := range []string{"credential_key", "super-secret-token", "parser-looking", "never-log-me", "quote:"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("diagnostic leaked YAML-derived content %q: %s", forbidden, output)
		}
	}
}

// TestIsDeclaredSymlinkAliasMatches asserts a symlinked ALIAS of a declared
// realpath resolves to trusted — both sides key on the resolved target, so an
// alias of a trusted dir is trusted (the same real dir).
func TestIsDeclaredSymlinkAliasMatches(t *testing.T) {
	base := t.TempDir()
	target := realDir(t, base, "real-repo")
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	cfg := t.TempDir()
	// Declare the REAL path; look up via the ALIAS.
	settings := []byte("trustedWorkspaces:\n  - " + target + "\n")

	r := NewWithEnv(envWithSettings(cfg, settings))
	if !r.IsDeclared(alias) {
		t.Fatalf("IsDeclared(alias=%q) = false, want true (alias of declared realpath)", alias)
	}
}

// TestIsDeclaredAliasCannotForgeOthersTrust asserts that declaring an ALIAS path
// does NOT grant trust to a DIFFERENT real directory: a symlink/alias cannot
// inherit or forge another workspace's trust entry (MUST-FIX 5.1). We declare an
// alias of dirA, then repoint the alias at dirB and confirm dirB is NOT trusted.
func TestIsDeclaredAliasCannotForgeOthersTrust(t *testing.T) {
	base := t.TempDir()
	dirA := realDir(t, base, "dirA")
	dirB := realDir(t, base, "dirB")
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(dirA, alias); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	cfg := t.TempDir()
	// Operator declared the ALIAS path (which currently resolves to dirA).
	settings := []byte("trustedWorkspaces:\n  - " + alias + "\n")
	r := NewWithEnv(envWithSettings(cfg, settings))

	// While the alias points at dirA, dirA is trusted; dirB is not.
	if !r.IsDeclared(dirA) {
		t.Fatal("dirA should be trusted (alias resolves to it)")
	}
	if r.IsDeclared(dirB) {
		t.Fatal("dirB trusted via an alias declaration it was never the target of — forged trust")
	}

	// Repoint the alias at dirB: the declared entry now resolves to dirB, and dirA
	// — whose trust came only through the moved alias — is no longer trusted.
	if err := os.Remove(alias); err != nil {
		t.Fatalf("remove alias: %v", err)
	}
	if err := os.Symlink(dirB, alias); err != nil {
		t.Fatalf("repoint alias: %v", err)
	}
	if r.IsDeclared(dirA) {
		t.Fatal("dirA still trusted after the alias was repointed away from it — stale/forged trust")
	}
	if !r.IsDeclared(dirB) {
		t.Fatal("dirB should be trusted after the alias was repointed at it")
	}
}

// TestIsDeclaredNilReader asserts a nil *Reader is safe (not trusted).
func TestIsDeclaredNilReader(t *testing.T) {
	var r *Reader
	if r.IsDeclared("/some/path") {
		t.Fatal("nil Reader.IsDeclared = true, want false")
	}
}

// TestIsDeclaredIgnoresProjectScopedSettings pins the SELF-TRUST EXCLUSION — the
// central threat for this feature. Trust is read ONLY from the USER-scoped
// settings.yaml; a project-tier file inside the workspace MUST NOT be able to
// self-declare its own trust. Here the user settings.yaml has NO trustedWorkspaces
// for ws, while a real <ws>/.mecatl/settings.yaml AND settings.local.yaml each
// declare `trustedWorkspaces: [<ws>]`. IsDeclared(ws) must stay false. This is a
// behavioural contract: a future refactor that pointed the reader at a project file
// would fail HERE (loudly), not silently open a self-trust bootstrap.
func TestIsDeclaredIgnoresProjectScopedSettings(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	cfg := t.TempDir()

	// USER-scoped settings.yaml: present, but declares trust for a DIFFERENT path
	// (so we exercise "user file read, but ws not in it" — not merely "no file").
	userSettings := []byte("trustedWorkspaces:\n  - " + base + "\n")

	// PROJECT-tier files INSIDE the workspace, each trying to self-declare trust.
	selfDeclare := []byte("trustedWorkspaces:\n  - " + ws + "\n")
	mecatlDir := filepath.Join(ws, ".mecatl")
	if err := os.MkdirAll(mecatlDir, 0o755); err != nil {
		t.Fatalf("mkdir .mecatl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mecatlDir, "settings.yaml"), selfDeclare, 0o644); err != nil {
		t.Fatalf("write project settings.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mecatlDir, "settings.local.yaml"), selfDeclare, 0o644); err != nil {
		t.Fatalf("write project settings.local.yaml: %v", err)
	}

	r := NewWithEnv(envWithSettings(cfg, userSettings))
	if r.IsDeclared(ws) {
		t.Fatal("IsDeclared(ws) = true — a project-tier .mecatl/settings.yaml self-declared trust; " +
			"trust must be read ONLY from user scope (self-trust bootstrap)")
	}
}

// TestIsDeclaredOversizedIgnored asserts the size cap's Warn-and-ignore path: a
// settings.yaml larger than maxConfigBytes (>1 MiB) of otherwise-valid
// trustedWorkspaces YAML is IGNORED (fail-safe), so IsDeclared(ws) == false. Without
// the cap this would still parse and GRANT trust — the test would catch a removed cap.
func TestIsDeclaredOversizedIgnored(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	cfg := t.TempDir()

	// A valid declaration for ws, padded past the cap with a giant YAML comment.
	var b []byte
	b = append(b, []byte("trustedWorkspaces:\n  - "+ws+"\n")...)
	pad := []byte("# padding to exceed the byte cap\n")
	for len(b) <= maxConfigBytes {
		b = append(b, pad...)
	}
	if len(b) <= maxConfigBytes {
		t.Fatalf("test setup: payload %d bytes did not exceed cap %d", len(b), maxConfigBytes)
	}

	r := NewWithEnv(envWithSettings(cfg, b))
	if r.IsDeclared(ws) {
		t.Fatal("IsDeclared(ws) = true for an oversized settings.yaml; the size cap must fail safe (ignore)")
	}
}
