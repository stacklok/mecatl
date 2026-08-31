package workspacetrust

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// realConfigEnv returns an env rooted at a REAL temp config dir: $XDG_CONFIG_HOME
// points at configDir and ReadFile is the real os.ReadFile, so a registry written
// by osRegistryWrite under <configDir>/mecatl/ is read back by the same Reader. No
// home, so the only resolvable base is the temp dir — never the developer's
// ~/.config.
func realConfigEnv(configDir string) xdgconfig.ResolveEnv {
	return xdgconfig.ResolveEnv{
		Getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return configDir
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile:    os.ReadFile,
	}
}

func TestGoccyYAMLMigration_Scenario1_FailSafeDiagnosticsNeverEchoYAML(t *testing.T) {
	t.Parallel()

	const malformed = "version: [credential: super-secret-token # parser-looking: :\n  quote: \"never-log-me\"\n"
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	var buf bytes.Buffer
	r := NewWithEnv(envWithTrustYAML(t.TempDir(), []byte(malformed))).
		WithDiagnostics(slogdiag.New(&buf, false, port.LevelInfo))

	if remembered, drifted := r.Remembered(ws, "anchor"); remembered || drifted {
		t.Fatalf("Remembered(malformed registry) = (%v, %v), want (false, false)", remembered, drifted)
	}
	output := buf.String()
	if !strings.Contains(output, "trust.yaml unparseable") {
		t.Fatalf("missing fail-safe diagnostic: %s", output)
	}
	for _, forbidden := range []string{"credential", "super-secret-token", "parser-looking", "never-log-me", "quote:"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("diagnostic leaked YAML-derived content %q: %s", forbidden, output)
		}
	}
}

func TestGoccyYAMLMigration_Scenario5_WorkspaceTrustFailsSafeAndValueFree(t *testing.T) {
	t.Parallel()

	const malformed = "version: [credential: workspace-secret\n"
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	var buf bytes.Buffer
	r := NewWithEnv(envWithTrustYAML(t.TempDir(), []byte(malformed))).
		WithDiagnostics(slogdiag.New(&buf, false, port.LevelInfo))

	if remembered, _ := r.Remembered(ws, "anchor"); remembered {
		t.Fatal("malformed workspace-trust YAML granted remembered trust")
	}
	if output := buf.String(); strings.Contains(output, "workspace-secret") || strings.Contains(output, "credential") {
		t.Fatalf("workspace-trust diagnostic leaked YAML content: %s", output)
	}
}

// TestRegistryRoundTrip writes an entry then reads it back: the workspace is
// remembered, undrifted when the live anchor matches what was stored.
func TestRegistryRoundTrip(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	cfg := t.TempDir()
	r := NewWithEnv(realConfigEnv(cfg))

	const anchor = "deadbeef"
	if err := r.Remember(ws, anchor, time.Unix(1700000000, 0)); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	remembered, drifted := r.Remembered(ws, anchor)
	if !remembered || drifted {
		t.Fatalf("Remembered(matching anchor) = (%v,%v), want (true,false)", remembered, drifted)
	}

	// The file is owner-only and at the expected sibling path.
	path := filepath.Join(cfg, "mecatl", "trust.yaml")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat trust.yaml: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("trust.yaml mode = %v, want 0600", fi.Mode().Perm())
	}
}

// TestRegistryDriftOnHashMismatch asserts a remembered entry whose live anchor
// DIFFERS from the stored one reads as remembered+drifted (the drift signal).
func TestRegistryDriftOnHashMismatch(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	cfg := t.TempDir()
	r := NewWithEnv(realConfigEnv(cfg))

	if err := r.Remember(ws, "original-anchor", time.Unix(1700000000, 0)); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	remembered, drifted := r.Remembered(ws, "CHANGED-anchor")
	if !remembered || !drifted {
		t.Fatalf("Remembered(changed anchor) = (%v,%v), want (true,true) [drift]", remembered, drifted)
	}
}

// TestRegistryUpdateOverwrites asserts a second Remember for the same workspace
// OVERWRITES the entry (the new anchor matches; the old no longer does).
func TestRegistryUpdateOverwrites(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	cfg := t.TempDir()
	r := NewWithEnv(realConfigEnv(cfg))

	if err := r.Remember(ws, "v1", time.Unix(1, 0)); err != nil {
		t.Fatalf("Remember v1: %v", err)
	}
	if err := r.Remember(ws, "v2", time.Unix(2, 0)); err != nil {
		t.Fatalf("Remember v2: %v", err)
	}
	if _, drifted := r.Remembered(ws, "v2"); drifted {
		t.Fatal("after re-Remember, the new anchor must match (no drift)")
	}
	if _, drifted := r.Remembered(ws, "v1"); !drifted {
		t.Fatal("after re-Remember, the OLD anchor must no longer match (drift)")
	}
}

// TestRegistryUpdatePreservesOtherEntries asserts the RMW write keeps unrelated
// entries — remembering wsB must not forget wsA.
func TestRegistryUpdatePreservesOtherEntries(t *testing.T) {
	base := t.TempDir()
	wsA := realDir(t, base, "repoA")
	wsB := realDir(t, base, "repoB")
	cfg := t.TempDir()
	r := NewWithEnv(realConfigEnv(cfg))

	if err := r.Remember(wsA, "anchorA", time.Unix(1, 0)); err != nil {
		t.Fatalf("Remember A: %v", err)
	}
	if err := r.Remember(wsB, "anchorB", time.Unix(2, 0)); err != nil {
		t.Fatalf("Remember B: %v", err)
	}
	if remembered, _ := r.Remembered(wsA, "anchorA"); !remembered {
		t.Fatal("wsA was forgotten when wsB was remembered (RMW lost an entry)")
	}
	if remembered, _ := r.Remembered(wsB, "anchorB"); !remembered {
		t.Fatal("wsB not remembered")
	}
}

// TestRegistryRealpathKeyed asserts the entry is keyed by realpath: a symlinked
// ALIAS of the remembered workspace reads as the SAME entry (both sides resolve to
// the same real dir).
func TestRegistryRealpathKeyed(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(ws, alias); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	cfg := t.TempDir()
	r := NewWithEnv(realConfigEnv(cfg))

	if err := r.Remember(ws, "anchor", time.Unix(1, 0)); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if remembered, _ := r.Remembered(alias, "anchor"); !remembered {
		t.Fatal("an alias of the remembered realpath must read as the same entry")
	}
}

// TestRegistryWriteRefusesSymlink mirrors soulguard's symlink test: osRegistryWrite
// must refuse a trust.yaml whose path is a pre-planted SYMLINK (CWE-59 / O_NOFOLLOW),
// failing soft rather than writing THROUGH the link to an attacker target.
func TestRegistryWriteRefusesSymlink(t *testing.T) {
	cfg := t.TempDir()
	mecatlDir := filepath.Join(cfg, "mecatl")
	if err := os.MkdirAll(mecatlDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Plant a symlink at the registry path pointing at an attacker target.
	target := filepath.Join(t.TempDir(), "attacker-target")
	link := filepath.Join(mecatlDir, "trust.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	base := t.TempDir()
	ws := realDir(t, base, "repo")
	r := NewWithEnv(realConfigEnv(cfg))

	err := r.Remember(ws, "anchor", time.Unix(1, 0))
	if err == nil {
		t.Fatal("Remember through a symlinked trust.yaml succeeded; O_NOFOLLOW guard failed")
	}
	// The attacker target must NOT have been written.
	if _, serr := os.Stat(target); serr == nil {
		t.Fatal("the symlink target was written through; CWE-59 not prevented")
	}
}

// TestRegistryFailToUntrustedOnUnreadable asserts an unreadable trust.yaml ⇒ not
// remembered (fail-safe, no crash). We use an env whose ReadFile always errors.
func TestRegistryFailToUntrustedOnUnreadable(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	env := xdgconfig.ResolveEnv{
		Getenv:      func(k string) string { return map[string]string{"XDG_CONFIG_HOME": t.TempDir()}[k] },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile:    func(string) ([]byte, error) { return nil, errors.New("permission denied") },
	}
	r := NewWithEnv(env)
	if remembered, _ := r.Remembered(ws, "anchor"); remembered {
		t.Fatal("unreadable trust.yaml reported as remembered; must fail safe to untrusted")
	}
}

// TestRegistryFailToUntrustedOnUnparseable asserts a corrupt trust.yaml ⇒ not
// remembered (a corrupt registry never GRANTS trust).
func TestRegistryFailToUntrustedOnUnparseable(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	r := NewWithEnv(envWithTrustYAML(t.TempDir(), []byte("version: 1\nworkspaces: [unterminated\n : :")))
	if remembered, _ := r.Remembered(ws, "anchor"); remembered {
		t.Fatal("unparseable trust.yaml reported as remembered; must fail safe")
	}
}

// TestRegistryFailSafeWarnReachesInjectedSink pins BOTH the diagnostics wiring (the
// injected port.Diagnostics is actually consulted on the fail-safe path) AND the
// level (a silent WARN→Info downgrade fails the test). A corrupt trust.yaml must
// (a) fail safe to not-remembered AND (b) emit the "unparseable" line at WARN.
func TestRegistryFailSafeWarnReachesInjectedSink(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	var buf bytes.Buffer
	// minLevel=Info so an accidental WARN→Info downgrade would STILL be captured; the
	// assertion then pins level=WARN, so the downgrade fails the test.
	diag := slogdiag.New(&buf, false, port.LevelInfo)

	r := NewWithEnv(envWithTrustYAML(t.TempDir(), []byte("version: 1\nworkspaces: [unterminated\n : :"))).
		WithDiagnostics(diag)
	if remembered, _ := r.Remembered(ws, "anchor"); remembered {
		t.Fatal("unparseable trust.yaml reported as remembered; must fail safe")
	}

	out := buf.String()
	if !strings.Contains(out, "trust.yaml unparseable") {
		t.Fatalf("fail-safe unparseable line did not reach the injected sink; got: %s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("fail-safe unparseable line must be WARN (no silent downgrade); got: %s", out)
	}
}

// TestRegistryFailToUntrustedOnWrongVersion asserts a trust.yaml with an unknown
// schema version is ignored fail-safe (never mis-interpreted into a grant).
func TestRegistryFailToUntrustedOnWrongVersion(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	yamlBytes := []byte("version: 999\nworkspaces:\n  " + ws + ":\n    anchorSHA256: anchor\n")
	r := NewWithEnv(envWithTrustYAML(t.TempDir(), yamlBytes))
	if remembered, _ := r.Remembered(ws, "anchor"); remembered {
		t.Fatal("wrong-version trust.yaml reported as remembered; must fail safe")
	}
}

// TestRegistryFailToUntrustedOnOversized asserts an oversized trust.yaml is ignored
// fail-safe (no remembered trust), even if it otherwise parses.
func TestRegistryFailToUntrustedOnOversized(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	b := []byte("version: 1\nworkspaces:\n  " + ws + ":\n    anchorSHA256: anchor\n")
	pad := []byte("# pad\n")
	for len(b) <= maxRegistryBytes {
		b = append(b, pad...)
	}
	r := NewWithEnv(envWithTrustYAML(t.TempDir(), b))
	if remembered, _ := r.Remembered(ws, "anchor"); remembered {
		t.Fatal("oversized trust.yaml reported as remembered; the size cap must fail safe")
	}
}

// TestRegistryNotRememberedWhenAbsent asserts a missing trust.yaml ⇒ not remembered.
func TestRegistryNotRememberedWhenAbsent(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	r := NewWithEnv(envWithTrustYAML(t.TempDir(), nil)) // ReadFile returns "not found"
	if remembered, _ := r.Remembered(ws, "anchor"); remembered {
		t.Fatal("absent trust.yaml reported as remembered")
	}
}

// TestRememberRequiresWriteSeam asserts a Reader built read-only (NewWithEnvIO with
// nil seam) errors on Remember rather than silently no-op'ing.
func TestRememberRequiresWriteSeam(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	r := NewWithEnvIO(realConfigEnv(t.TempDir()), nil)
	if err := r.Remember(ws, "anchor", time.Unix(1, 0)); err == nil {
		t.Fatal("Remember with a nil write seam should error, not silently succeed")
	}
}

// TestRememberWriteSeamSpy asserts the injected write seam is the ONLY write path —
// the spy records the write. This is the seam mecated-never-writes assertions build
// on (a daemon Reader given a spy seam records zero writes when it only reads).
func TestRememberWriteSeamSpy(t *testing.T) {
	base := t.TempDir()
	ws := realDir(t, base, "repo")
	var writes int
	spy := func(string, []byte) error { writes++; return nil }
	r := NewWithEnvIO(realConfigEnv(t.TempDir()), spy)

	// A pure read path must NOT write.
	_, _ = r.Remembered(ws, "anchor")
	if writes != 0 {
		t.Fatalf("Remembered() triggered %d writes; a read must never write the registry", writes)
	}
	// Only Remember writes.
	if err := r.Remember(ws, "anchor", time.Unix(1, 0)); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if writes != 1 {
		t.Fatalf("Remember triggered %d writes, want exactly 1", writes)
	}
}

// envWithTrustYAML returns an injected env whose <config>/mecatl/trust.yaml returns
// the given bytes (nil ⇒ "not found"). Fully offline; never the real ~/.config.
func envWithTrustYAML(configDir string, trust []byte) xdgconfig.ResolveEnv {
	want := filepath.Join(configDir, "mecatl", "trust.yaml")
	return xdgconfig.ResolveEnv{
		Getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return configDir
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile: func(p string) ([]byte, error) {
			if p == want && trust != nil {
				return trust, nil
			}
			return nil, errors.New("not found")
		},
	}
}
