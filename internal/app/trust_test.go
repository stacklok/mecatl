package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/workspacetrust"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// withTrustEnv swaps the package-level trustEnv for the duration of a test (so the
// declarative-trust read runs against a faked XDG/home — never the developer's
// real ~/.config) and restores it on cleanup.
func withTrustEnv(t *testing.T, env xdgconfig.ResolveEnv) {
	t.Helper()
	prev := trustEnv
	trustEnv = env
	t.Cleanup(func() { trustEnv = prev })
}

// isolateUserConfig points the real-env user-config resolution (xdgconfig.OSEnv)
// at an empty temp dir for the duration of a test, so a test that exercises the
// CONVENTIONAL user-global settings.yaml read (permconfig.New{Conventional:true},
// or app.Build with PermissionsConventional) never sees the developer's real
// ~/.config/mecatl/settings.yaml. Sets both XDG_CONFIG_HOME and HOME (UserConfigDir
// falls back to ~/.config when XDG_CONFIG_HOME is unset). t.Setenv forbids
// t.Parallel, which none of these tests use.
func isolateUserConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// isolatedPermConfigEnv returns an environment for conventional permission-config
// discovery that reads only from a temporary XDG config directory.
func isolatedPermConfigEnv(t *testing.T) *xdgconfig.ResolveEnv {
	t.Helper()
	configDir := t.TempDir()
	return &xdgconfig.ResolveEnv{
		Getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return configDir
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile:    os.ReadFile,
	}
}

// trustSettingsEnv returns an injected env whose user-global settings.yaml returns
// the given bytes, with $XDG_CONFIG_HOME pointed at configDir.
func trustSettingsEnv(configDir string, settings []byte) xdgconfig.ResolveEnv {
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
			if p == want && settings != nil {
				return settings, nil
			}
			return nil, errors.New("not found")
		},
	}
}

// realWS creates a real workspace dir and returns its realpath.
func realWS(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	return resolved
}

// TestResolveTrustFlagWins asserts --trust-project ⇒ Trusted, Source=flag,
// regardless of (and without consulting) the declarative list.
func TestResolveTrustFlagWins(t *testing.T) {
	withTrustEnv(t, trustSettingsEnv(t.TempDir(), nil)) // no declarations
	d := resolveTrust(Config{Workspace: realWS(t), TrustProject: true})
	if !d.Trusted || d.Source != TrustFlag {
		t.Fatalf("resolveTrust(--trust-project) = %+v, want Trusted with Source=flag", d)
	}
	if d.Drifted {
		t.Fatal("Drifted must stay false in Phase 1")
	}
}

// TestResolveTrustDeclared asserts a workspace whose realpath is in
// trustedWorkspaces ⇒ Trusted, Source=declared, with NO flag.
func TestResolveTrustDeclared(t *testing.T) {
	ws := realWS(t)
	cfg := t.TempDir()
	settings := []byte("trustedWorkspaces:\n  - " + ws + "\n")
	withTrustEnv(t, trustSettingsEnv(cfg, settings))

	d := resolveTrust(Config{Workspace: ws}) // no --trust-project
	if !d.Trusted || d.Source != TrustDeclared {
		t.Fatalf("resolveTrust(declared) = %+v, want Trusted with Source=declared", d)
	}
}

// TestResolveTrustNone asserts a non-declared workspace with no flag ⇒ untrusted.
func TestResolveTrustNone(t *testing.T) {
	ws := realWS(t)
	cfg := t.TempDir()
	// Declare a DIFFERENT path.
	settings := []byte("trustedWorkspaces:\n  - " + filepath.Dir(ws) + "\n")
	withTrustEnv(t, trustSettingsEnv(cfg, settings))

	d := resolveTrust(Config{Workspace: ws})
	if d.Trusted || d.Source != TrustNone {
		t.Fatalf("resolveTrust(not declared, no flag) = %+v, want untrusted with Source=none", d)
	}
}

// TestResolveTrustFlagWinsSourceOverDeclared asserts that when BOTH the flag and a
// declaration apply, the flag is the reported Source (highest precedence), still
// Trusted.
func TestResolveTrustFlagWinsSourceOverDeclared(t *testing.T) {
	ws := realWS(t)
	cfg := t.TempDir()
	settings := []byte("trustedWorkspaces:\n  - " + ws + "\n")
	withTrustEnv(t, trustSettingsEnv(cfg, settings))

	d := resolveTrust(Config{Workspace: ws, TrustProject: true})
	if !d.Trusted || d.Source != TrustFlag {
		t.Fatalf("resolveTrust(flag+declared) = %+v, want Trusted with Source=flag", d)
	}
}

// TestResolveTrustMalformedEntryFailSafe asserts a malformed settings.yaml ⇒
// untrusted (no grant), and a single bad entry does not suppress a valid one.
func TestResolveTrustMalformedEntryFailSafe(t *testing.T) {
	ws := realWS(t)
	cfg := t.TempDir()

	// Unparseable ⇒ untrusted (fail-safe).
	withTrustEnv(t, trustSettingsEnv(cfg, []byte("trustedWorkspaces: [unterminated\n : :")))
	if d := resolveTrust(Config{Workspace: ws}); d.Trusted {
		t.Fatalf("unparseable settings.yaml granted trust: %+v", d)
	}

	// Bad entry first, valid entry second ⇒ still trusted (declared).
	settings := []byte("trustedWorkspaces:\n  - /nonexistent/does/not/resolve\n  - " + ws + "\n")
	withTrustEnv(t, trustSettingsEnv(cfg, settings))
	if d := resolveTrust(Config{Workspace: ws}); !d.Trusted || d.Source != TrustDeclared {
		t.Fatalf("bad entry suppressed a valid declaration: %+v", d)
	}
}

// TestResolveTrustEmptyWorkspaceNoDeclared asserts an empty workspace is never
// declared-trusted (only the flag can trust an empty-workspace run).
func TestResolveTrustEmptyWorkspaceNoDeclared(t *testing.T) {
	withTrustEnv(t, trustSettingsEnv(t.TempDir(), []byte("trustedWorkspaces:\n  - /x\n")))
	if d := resolveTrust(Config{Workspace: ""}); d.Trusted {
		t.Fatalf("empty workspace declared-trusted: %+v", d)
	}
}

// TestDeclaredTrustFeedsSoulGate proves the folded decision reaches the soul
// provenance gate: declared trust admits project steering exactly as the explicit
// flag does. Build performs this same fold before selecting the soul.
func TestDeclaredTrustFeedsSoulGate(t *testing.T) {
	ws := realWS(t)
	writeProjectSoul(t, ws, "You are a project persona.")
	xdg := t.TempDir() // no user soul
	fakeSoulEnv(t, xdg)

	cfgDir := t.TempDir()
	settings := []byte("trustedWorkspaces:\n  - " + ws + "\n")
	withTrustEnv(t, trustSettingsEnv(cfgDir, settings))

	cfg := Config{Workspace: ws} // NO --trust-project
	d := resolveTrust(cfg)
	if !d.Trusted || d.Source != TrustDeclared {
		t.Fatalf("precondition: declared workspace should resolve trusted, got %+v", d)
	}
	cfg.TrustProject = d.Trusted // the Build-time fold

	src, meta := selectSoulSource(cfg, newFakeIO().io(), nil)
	if src == nil {
		t.Fatal("a declared-trusted project soul (no user soul) must load via the folded decision")
	}
	if !meta.Present || meta.Provenance != soulProject || !meta.Trusted {
		t.Fatalf("meta = %+v, want Present project trusted (declared trust honoured by the soul gate)", meta)
	}
}

// TestNonDeclaredDropsSoul is the negative companion: a non-declared workspace,
// no flag ⇒ the project soul is withheld (untrusted), confirming the fold does not
// spuriously grant.
func TestNonDeclaredDropsSoul(t *testing.T) {
	ws := realWS(t)
	writeProjectSoul(t, ws, "You are a project persona.")
	xdg := t.TempDir()
	fakeSoulEnv(t, xdg)
	withTrustEnv(t, trustSettingsEnv(t.TempDir(), nil)) // no declarations

	cfg := Config{Workspace: ws}
	cfg.TrustProject = resolveTrust(cfg).Trusted // false
	src, meta := selectSoulSource(cfg, newFakeIO().io(), nil)
	if src != nil || meta.Present {
		t.Fatalf("non-declared, no-flag project soul must be withheld, got src=%v meta=%+v", src, meta)
	}
}

// TestResolveTrustMonotonicPositiveDenyHonoured is the MUST-FIX 5.5 invariant at
// the composition→permconfig boundary: a trusted workspace folds to
// TrustProject=true, but a project DENY rule is STILL honoured — trust grants
// admission of ALLOWs only; it never overrides a Deny. This mirrors permconfig's
// TestResolveProjectDenyKeptWhenUntrusted from the GRANTED side.
//
// FOLD-SOURCE PARITY: the invariant is asserted under BOTH trust sources —
// TrustDeclared (the settings.yaml list) and TrustFlag (--trust-project) — proving
// the fold collapses to the SAME effective bool regardless of where trust came
// from, so neither source can be a Deny-overriding back door.
func TestResolveTrustMonotonicPositiveDenyHonoured(t *testing.T) {
	// The permconfig.New{Conventional:true} below reads the CONVENTIONAL user-global
	// settings.yaml via xdgconfig.OSEnv (separate from the trustEnv seam above) —
	// isolate it or the developer's real ~/.config allow rules leak into the fold.
	isolateUserConfig(t)
	cases := []struct {
		name       string
		wantSource TrustSource
		// cfg builds the Config for a fresh workspace ws; declared installs the
		// user-scope trustedWorkspaces declaration when the source is TrustDeclared.
		setup func(t *testing.T, ws string) Config
	}{
		{
			name:       "declared",
			wantSource: TrustDeclared,
			setup: func(t *testing.T, ws string) Config {
				t.Helper()
				cfgDir := t.TempDir()
				withTrustEnv(t, trustSettingsEnv(cfgDir, []byte("trustedWorkspaces:\n  - "+ws+"\n")))
				return Config{Workspace: ws} // NO --trust-project
			},
		},
		{
			name:       "flag",
			wantSource: TrustFlag,
			setup: func(t *testing.T, ws string) Config {
				t.Helper()
				withTrustEnv(t, trustSettingsEnv(t.TempDir(), nil)) // no declarations
				return Config{Workspace: ws, TrustProject: true}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := realWS(t)
			cfg := tc.setup(t, ws)

			d := resolveTrust(cfg)
			if !d.Trusted || d.Source != tc.wantSource {
				t.Fatalf("precondition: want Trusted via %v, got %+v", tc.wantSource, d)
			}

			// Build the permconfig resolver the SAME way buildEngine does, feeding the
			// folded decision. The project ships a DENY; it must survive being trusted.
			settingsYAML := "permissions:\n  deny:\n    - \"Bash(some-blocked-cmd:*)\"\n"
			if err := os.MkdirAll(filepath.Join(ws, ".mecatl"), 0o755); err != nil {
				t.Fatalf("mkdir .mecatl: %v", err)
			}
			if err := os.WriteFile(filepath.Join(ws, ".mecatl", "settings.yaml"), []byte(settingsYAML), 0o644); err != nil {
				t.Fatalf("write project settings: %v", err)
			}

			resolver := permconfig.New(permconfig.Options{
				Conventional: true,
				TrustProject: d.Trusted, // the FOLDED decision (true via either source)
			})
			if resolver == nil {
				t.Fatal("resolver should be non-nil with Conventional on")
			}
			wsReader, err := osfs.NewWorkspace(ws)
			if err != nil {
				t.Fatalf("open ws reader: %v", err)
			}
			rules := resolver.Resolve(context.Background(), wsReader)

			found := false
			for _, r := range rules {
				if r.Tool == "Bash" && r.Effect == governance.Deny {
					found = true
				}
				if r.Effect == governance.Allow && r.Tool == "Bash" {
					t.Fatalf("trust must not turn a project DENY into an ALLOW: %+v", r)
				}
			}
			if !found {
				t.Fatalf("project DENY dropped under %v trust (monotonic-positive violated): %+v", tc.wantSource, rules)
			}
		})
	}
}

// realConfigTrustEnv returns an env rooted at a REAL temp config dir (XDG_CONFIG_HOME
// = configDir, real os.ReadFile), so the fold can read a trust.yaml that the same
// workspacetrust.Reader wrote — fully offline, never the developer's ~/.config. Used
// by the remembered-trust fold tests.
func realConfigTrustEnv(configDir string) xdgconfig.ResolveEnv {
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

// writeAnchorSurface writes a minimal project identity surface (a soul) so the
// workspace has a non-trivial, stable anchor to remember and later drift.
func writeAnchorSurface(t *testing.T, ws, persona string) {
	t.Helper()
	dir := filepath.Join(ws, ".mecatl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .mecatl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "soul.md"), []byte(persona), 0o644); err != nil {
		t.Fatalf("write project soul: %v", err)
	}
}

// TestResolveTrustRememberedMatch (R2.2) asserts a workspace with a trust.yaml entry
// whose anchor MATCHES the live identity anchor ⇒ Trusted, Source=remembered,
// Drifted=false — when there is no flag and no declaration.
func TestResolveTrustRememberedMatch(t *testing.T) {
	ws := realWS(t)
	writeAnchorSurface(t, ws, "remembered persona")
	cfg := t.TempDir()
	withTrustEnv(t, realConfigTrustEnv(cfg))

	// Remember the workspace at its CURRENT anchor (what the 2c prompt will do).
	reader := workspacetrust.NewWithEnv(realConfigTrustEnv(cfg))
	if err := reader.Remember(ws, reader.AnchorHash(ws), time.Unix(1700000000, 0)); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	d := resolveTrust(Config{Workspace: ws}) // no flag, no declaration
	if !d.Trusted || d.Source != TrustRemembered || d.Drifted {
		t.Fatalf("resolveTrust(remembered, matching anchor) = %+v, want Trusted Source=remembered Drifted=false", d)
	}
}

// TestResolveTrustRememberedDriftFailsSafe (R2.2 + R2.8 + MUST-FIX 5.4) asserts a
// trust.yaml entry whose anchor MISMATCHES the live anchor (the project's identity
// surface changed since it was trusted) ⇒ Trusted=FALSE, Drifted=true. mecated has
// no prompt, so a drifted entry must fail safe to untrusted (2c re-prompts).
func TestResolveTrustRememberedDriftFailsSafe(t *testing.T) {
	ws := realWS(t)
	writeAnchorSurface(t, ws, "original persona")
	cfg := t.TempDir()
	withTrustEnv(t, realConfigTrustEnv(cfg))

	reader := workspacetrust.NewWithEnv(realConfigTrustEnv(cfg))
	if err := reader.Remember(ws, reader.AnchorHash(ws), time.Unix(1700000000, 0)); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// Tamper the identity surface AFTER remembering — the live anchor now differs.
	if err := os.WriteFile(filepath.Join(ws, ".mecatl", "soul.md"), []byte("MALICIOUS persona"), 0o644); err != nil {
		t.Fatalf("tamper soul: %v", err)
	}

	d := resolveTrust(Config{Workspace: ws})
	if d.Trusted || !d.Drifted {
		t.Fatalf("resolveTrust(drifted) = %+v, want Trusted=false Drifted=true (fail-safe)", d)
	}
}

// TestResolveTrustFlagBeatsRemembered asserts --trust-project short-circuits before
// the registry is consulted: the SOURCE is flag even when a remembered entry exists.
func TestResolveTrustFlagBeatsRemembered(t *testing.T) {
	ws := realWS(t)
	writeAnchorSurface(t, ws, "persona")
	cfg := t.TempDir()
	withTrustEnv(t, realConfigTrustEnv(cfg))
	reader := workspacetrust.NewWithEnv(realConfigTrustEnv(cfg))
	if err := reader.Remember(ws, reader.AnchorHash(ws), time.Unix(1, 0)); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	d := resolveTrust(Config{Workspace: ws, TrustProject: true})
	if !d.Trusted || d.Source != TrustFlag {
		t.Fatalf("flag must win over remembered: got %+v, want Source=flag", d)
	}
}

// TestResolveTrustDeclaredBeatsRemembered asserts a declarative trustedWorkspaces
// match takes precedence over a remembered entry (Source=declared). Here BOTH the
// settings.yaml declaration AND a trust.yaml entry exist for ws; declared wins.
func TestResolveTrustDeclaredBeatsRemembered(t *testing.T) {
	ws := realWS(t)
	writeAnchorSurface(t, ws, "persona")
	cfg := t.TempDir()

	// Write the declaration into the SAME config dir's settings.yaml, and remember
	// the workspace in that dir's trust.yaml. The fold reads both from realConfigTrustEnv.
	mecatlDir := filepath.Join(cfg, "mecatl")
	if err := os.MkdirAll(mecatlDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mecatlDir, "settings.yaml"), []byte("trustedWorkspaces:\n  - "+ws+"\n"), 0o644); err != nil {
		t.Fatalf("write settings.yaml: %v", err)
	}
	withTrustEnv(t, realConfigTrustEnv(cfg))
	reader := workspacetrust.NewWithEnv(realConfigTrustEnv(cfg))
	if err := reader.Remember(ws, reader.AnchorHash(ws), time.Unix(1, 0)); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	d := resolveTrust(Config{Workspace: ws})
	if !d.Trusted || d.Source != TrustDeclared {
		t.Fatalf("declared must win over remembered: got %+v, want Source=declared", d)
	}
}

// TestResolveTrustRememberedMonotonicDenyHonoured (MUST-FIX 5.5) asserts a
// remembered-trusted workspace still honours a project DENY — remembered trust, like
// flag/declared, grants ADMISSION only and never overrides a Deny.
func TestResolveTrustRememberedMonotonicDenyHonoured(t *testing.T) {
	// permconfig.New{Conventional:true} below reads the CONVENTIONAL user-global
	// settings.yaml via xdgconfig.OSEnv — isolate it or the developer's real
	// ~/.config allow rules leak into the fold.
	isolateUserConfig(t)
	ws := realWS(t)
	writeAnchorSurface(t, ws, "persona")
	cfg := t.TempDir()
	withTrustEnv(t, realConfigTrustEnv(cfg))
	reader := workspacetrust.NewWithEnv(realConfigTrustEnv(cfg))
	if err := reader.Remember(ws, reader.AnchorHash(ws), time.Unix(1, 0)); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	d := resolveTrust(Config{Workspace: ws})
	if !d.Trusted || d.Source != TrustRemembered {
		t.Fatalf("precondition: want remembered-trusted, got %+v", d)
	}

	// The project ships a DENY; remembered trust must not turn it into an ALLOW.
	settingsYAML := "permissions:\n  deny:\n    - \"Bash(some-blocked-cmd:*)\"\n"
	if err := os.WriteFile(filepath.Join(ws, ".mecatl", "settings.yaml"), []byte(settingsYAML), 0o644); err != nil {
		t.Fatalf("write project settings: %v", err)
	}
	resolver := permconfig.New(permconfig.Options{Conventional: true, TrustProject: d.Trusted})
	if resolver == nil {
		t.Fatal("resolver nil")
	}
	wsReader, err := osfs.NewWorkspace(ws)
	if err != nil {
		t.Fatalf("open ws reader: %v", err)
	}
	for _, r := range resolver.Resolve(context.Background(), wsReader) {
		if r.Effect == governance.Allow && r.Tool == "Bash" {
			t.Fatalf("remembered trust turned a project DENY into an ALLOW: %+v", r)
		}
	}
}

// TestResolveTrustNeverWritesRegistry (FIX 3 — mecated declarative, no write on the
// fold path) pins that resolveTrust (the path Build runs, and the daemon's only
// trust path) NEVER creates or modifies trust.yaml. Only Phase 2c's mecatui prompt
// writes the registry. We remember a workspace (a legitimate prior write), snapshot
// trust.yaml's mtime+size, run resolveTrust repeatedly, and assert the file is
// byte-for-byte untouched (no create, no mtime bump).
func TestResolveTrustNeverWritesRegistry(t *testing.T) {
	ws := realWS(t)
	writeAnchorSurface(t, ws, "persona")
	cfg := t.TempDir()
	withTrustEnv(t, realConfigTrustEnv(cfg))

	// A legitimate prior write (simulating a past 2c approval).
	reader := workspacetrust.NewWithEnv(realConfigTrustEnv(cfg))
	if err := reader.Remember(ws, reader.AnchorHash(ws), time.Unix(1700000000, 0)); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	path := filepath.Join(cfg, "mecatl", "trust.yaml")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat trust.yaml: %v", err)
	}

	// Resolve several times (remembered + matching ⇒ trusted). None may write.
	for i := 0; i < 3; i++ {
		if d := resolveTrust(Config{Workspace: ws}); !d.Trusted || d.Source != TrustRemembered {
			t.Fatalf("precondition: want remembered-trusted on iter %d, got %+v", i, d)
		}
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("trust.yaml vanished after resolveTrust: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("resolveTrust modified trust.yaml (mtime %v→%v, size %d→%d); the fold/daemon path must NEVER write",
			before.ModTime(), after.ModTime(), before.Size(), after.Size())
	}
}

// TestNarrateTrustWarnsOnDrift asserts a Drifted decision narrates at Warn (the
// re-gated-to-untrusted alarm), not the quiet Info line.
func TestNarrateTrustWarnsOnDrift(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelInfo)

	narrateTrust(diag, TrustDecision{Trusted: false, Source: TrustNone, Drifted: true}, "/some/ws")
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "DRIFTED") {
		t.Fatalf("drifted decision must narrate at WARN with a drift message; got: %s", out)
	}
}

// TestNarrateTrustLogsDecision covers R1.4: the composition narrates the trust
// decision so the why-trusted story is visible in the log (mirroring the soul
// narration). We swap the default slog logger for a buffer and assert the source +
// trusted fields are emitted. It is a thin logging assertion, not over-engineered.
func TestNarrateTrustLogsDecision(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelInfo)

	narrateTrust(diag, TrustDecision{Trusted: true, Source: TrustDeclared}, "/some/ws")

	out := buf.String()
	for _, want := range []string{"workspace trust", "trusted=true", "source=declared", "/some/ws"} {
		if !strings.Contains(out, want) {
			t.Fatalf("narrateTrust log missing %q; got: %s", want, out)
		}
	}
}

// osBackedTrustEnv returns a ResolveEnv whose XDG config dir is configDir and whose
// reads hit the real filesystem (os.ReadFile) — so the REAL registry write seam
// (osRegistryWrite, used by workspacetrust.NewWithEnv) writes trust.yaml under
// configDir and a subsequent read sees it. Fully offline (a temp dir, never the
// developer's ~/.config).
func osBackedTrustEnv(configDir string) xdgconfig.ResolveEnv {
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

// realWSWithSoul creates a real workspace dir carrying a project soul (an authority
// member + a stable identity anchor) and returns its realpath.
func realWSWithSoul(t *testing.T) string {
	t.Helper()
	ws := realWS(t)
	if err := os.MkdirAll(filepath.Join(ws, ".mecatl"), 0o755); err != nil {
		t.Fatalf("mkdir .mecatl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".mecatl", "soul.md"), []byte("project persona"), 0o644); err != nil {
		t.Fatalf("write soul: %v", err)
	}
	return ws
}

// TestResolveTrustExportedDelegates asserts the EXPORTED ResolveTrust returns the
// same decision as the unexported resolveTrust (the prompt and Build never disagree).
func TestResolveTrustExportedDelegates(t *testing.T) {
	ws := realWS(t)
	cfg := t.TempDir()
	settings := []byte("trustedWorkspaces:\n  - " + ws + "\n")
	withTrustEnv(t, trustSettingsEnv(cfg, settings))

	got := ResolveTrust(Config{Workspace: ws})
	want := resolveTrust(Config{Workspace: ws})
	if got != want {
		t.Fatalf("ResolveTrust=%+v != resolveTrust=%+v", got, want)
	}
	if !got.Trusted || got.Source != TrustDeclared {
		t.Fatalf("ResolveTrust(declared) = %+v, want Trusted/declared", got)
	}
}

// TestHasProjectAuthorityComposition asserts the composition wrapper detects a
// project soul and skips an empty workspace.
func TestHasProjectAuthorityComposition(t *testing.T) {
	withTrustEnv(t, trustSettingsEnv(t.TempDir(), nil))
	if HasProjectAuthority(Config{Workspace: ""}) {
		t.Fatal("empty workspace reported authority")
	}
	if HasProjectAuthority(Config{Workspace: realWS(t)}) {
		t.Fatal("empty repo reported authority")
	}
	if !HasProjectAuthority(Config{Workspace: realWSWithSoul(t)}) {
		t.Fatal("repo with a soul did NOT report authority")
	}
}

// TestHasProjectAuthorityCompositionPerType lifts the adapter's per-type authority
// cases UP through the composition wrapper (app.HasProjectAuthority), so the two
// can't silently diverge: it covers each authority member type (soul / agent /
// command / skill / .mecatl allow-rule / .claude allow-rule) and the two
// NOT-authority cases (deny-only, empty-allow). It writes to a real workspace (the
// probe walks the real tree); trustEnv is faked only so no real ~/.config is read.
func TestHasProjectAuthorityCompositionPerType(t *testing.T) {
	cases := []struct {
		name string
		rel  string
		body string
		want bool
	}{
		{"soul", ".mecatl/soul.md", "project persona", true},
		{"agent", ".claude/agents/reviewer.md", "agent body", true},
		{"command", ".mecatl/commands/deploy.md", "command body", true},
		{"skill", ".mecatl/skills/x/SKILL.md", "skill body", true},
		{"mecatl-allow", ".mecatl/settings.yaml", "permissions:\n  allow:\n    - Read\n", true},
		{"claude-allow", ".claude/settings.json", `{"permissions":{"allow":["Read"]}}`, true},
		{"deny-only", ".mecatl/settings.yaml", "permissions:\n  deny:\n    - Bash\n", false},
		{"empty-allow", ".mecatl/settings.yaml", "permissions:\n  allow: []\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTrustEnv(t, trustSettingsEnv(t.TempDir(), nil))
			ws := realWS(t)
			p := filepath.Join(ws, tc.rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := HasProjectAuthority(Config{Workspace: ws}); got != tc.want {
				t.Fatalf("HasProjectAuthority(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestRememberTrustRoundTripFeedsResolve is the no-double-resolution proof: after the
// prompt persists via RememberTrust, a fresh ResolveTrust returns TrustRemembered for
// the SAME workspace+anchor — Build will NOT re-prompt or re-resolve to a different
// answer. It also proves the persisted anchor matches the live anchor (no drift right
// after a grant), and that the trustedAt clock is the injected one.
func TestRememberTrustRoundTripFeedsResolve(t *testing.T) {
	ws := realWSWithSoul(t)
	cfg := t.TempDir()
	withTrustEnv(t, osBackedTrustEnv(cfg))

	// Pre-state: not trusted, but authority present ⇒ the prompt would fire.
	pre := ResolveTrust(Config{Workspace: ws})
	if pre.Trusted || pre.Drifted {
		t.Fatalf("pre-Remember: %+v, want untrusted/undrifted (TrustNone)", pre)
	}
	if !HasProjectAuthority(Config{Workspace: ws}) {
		t.Fatal("pre-Remember: expected authority present")
	}

	at := time.Date(2026, 6, 4, 9, 30, 0, 0, time.UTC)
	if err := RememberTrust(Config{Workspace: ws}, at); err != nil {
		t.Fatalf("RememberTrust: %v", err)
	}

	// Post-state: the SAME fold now returns TrustRemembered — the outcome feeds the
	// next resolution (no re-prompt). This is exactly what Build computes.
	post := ResolveTrust(Config{Workspace: ws})
	if !post.Trusted || post.Source != TrustRemembered || post.Drifted {
		t.Fatalf("post-Remember: %+v, want Trusted/remembered/undrifted", post)
	}
}

// TestRememberTrustThenDriftReResolvesDrifted asserts that editing the identity
// surface AFTER a Remember makes the next ResolveTrust report Drifted (untrusted) —
// the signal mecatui turns into a re-prompt. Re-persisting then clears it.
func TestRememberTrustThenDriftReResolvesDrifted(t *testing.T) {
	ws := realWSWithSoul(t)
	cfg := t.TempDir()
	withTrustEnv(t, osBackedTrustEnv(cfg))

	at := time.Date(2026, 6, 4, 9, 30, 0, 0, time.UTC)
	if err := RememberTrust(Config{Workspace: ws}, at); err != nil {
		t.Fatalf("RememberTrust: %v", err)
	}
	if d := ResolveTrust(Config{Workspace: ws}); !d.Trusted {
		t.Fatalf("after Remember: %+v, want trusted", d)
	}

	// Edit the soul ⇒ the live anchor drifts from the remembered one.
	if err := os.WriteFile(filepath.Join(ws, ".mecatl", "soul.md"), []byte("MALICIOUS rewrite"), 0o644); err != nil {
		t.Fatalf("rewrite soul: %v", err)
	}
	drift := ResolveTrust(Config{Workspace: ws})
	if drift.Trusted || !drift.Drifted {
		t.Fatalf("after edit: %+v, want untrusted+Drifted (re-prompt signal)", drift)
	}

	// Re-bless: persist the new anchor ⇒ trusted again, no drift.
	if err := RememberTrust(Config{Workspace: ws}, at); err != nil {
		t.Fatalf("re-RememberTrust: %v", err)
	}
	if d := ResolveTrust(Config{Workspace: ws}); !d.Trusted || d.Drifted {
		t.Fatalf("after re-bless: %+v, want trusted/undrifted", d)
	}
}

// TestRememberTrustEmptyWorkspaceNoop asserts RememberTrust with no workspace is a
// no-op (no error, nothing written) — a degraded composition never aborts.
func TestRememberTrustEmptyWorkspaceNoop(t *testing.T) {
	withTrustEnv(t, osBackedTrustEnv(t.TempDir()))
	if err := RememberTrust(Config{Workspace: ""}, time.Now()); err != nil {
		t.Fatalf("RememberTrust(empty) = %v, want nil no-op", err)
	}
}
