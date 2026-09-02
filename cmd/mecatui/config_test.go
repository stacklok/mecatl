package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/buildinfo"
	"github.com/stacklok/mecatl/internal/testutil/codextest"
)

func TestVersionInvocationIsExact(t *testing.T) {
	if !buildinfo.IsVersion([]string{"mecatui", "--version"}) {
		t.Fatal("exact --version was not recognized")
	}
	var buf bytes.Buffer
	buildinfo.PrintVersion(&buf, "mecatui")
	if got := buf.String(); got != "mecatui "+buildinfo.BuildID+"\n" {
		t.Errorf("PrintVersion output = %q, want %q", got, "mecatui "+buildinfo.BuildID+"\n")
	}
	for _, args := range [][]string{{"--version", "--mock"}, {"-version"}} {
		if _, _, err := parseTransportFlags(modeLocal, io.Discard, args); err == nil {
			t.Errorf("parseTransportFlags(%v) accepted a non-exact version invocation", args)
		}
	}
}

// TestContextWindowOverrideFlagWiring pins the embedded-only flag's parse, config
// mapping, and connect-mode rejection.
func TestContextWindowOverrideFlagWiring(t *testing.T) {
	cfg, err := parseFlags([]string{"--context-window-override", "321000"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.contextWindowOverride != 321000 {
		t.Fatalf("parsed override = %d, want 321000", cfg.contextWindowOverride)
	}
	if got := embeddedConfig(cfg, port.NopDiagnostics{}).ContextWindowOverride; got != 321000 {
		t.Fatalf("embedded override = %d, want 321000", got)
	}
	if _, _, err := parseTransportFlags(modeConnect, io.Discard, []string{"--context-window-override", "1"}); err == nil {
		t.Fatal("connect mode accepted embedded-only context-window override")
	}
}

// TestEmbeddedConfigEnablesAgentDefs asserts the embedded server enables conventional
// agent-definition discovery (consistent with EnableTeams/EnableParallel; inert until a
// <name>.md exists under a conventional dir).
func TestEmbeddedConfigEnablesAgentDefs(t *testing.T) {
	ac := embeddedConfig(config{workspace: "/ws", model: "m", mock: true}, port.NopDiagnostics{})
	if ac.ServerImplementation != mecatuiServerImplementation {
		t.Errorf("embeddedConfig ServerImplementation = %q, want %q", ac.ServerImplementation, mecatuiServerImplementation)
	}
	if !ac.AgentsConventional {
		t.Error("embeddedConfig AgentsConventional = false, want true")
	}
	if !ac.EnableTeams || !ac.EnableParallel {
		t.Errorf("embeddedConfig should also keep teams/fork on (teams=%v fork=%v)", ac.EnableTeams, ac.EnableParallel)
	}
}

// TestEmbeddedConfigPermissionPosture asserts the TUI discovers the conventional
// per-project permission config and imports Claude-Code settings (issue #13), but
// that project TRUST is DEFAULT FALSE (WORKSPACE-TRUST Phase 0): unified with
// mecated, a project's ALLOW rules + project soul are gated behind --trust-project.
func TestEmbeddedConfigPermissionPosture(t *testing.T) {
	ac := embeddedConfig(config{workspace: "/ws", model: "m", mock: true}, port.NopDiagnostics{})
	if !ac.PermissionsConventional {
		t.Error("embeddedConfig PermissionsConventional = false, want true")
	}
	if !ac.ImportClaudePermissions {
		t.Error("embeddedConfig ImportClaudePermissions = false, want true")
	}
	if ac.TrustProject {
		t.Error("embeddedConfig TrustProject = true by default, want false (Phase 0: untrusted unless --trust-project)")
	}
}

// TestEmbeddedConfigMapsTrustProject asserts the --trust-project flag flows through
// to app.Config.TrustProject: off by default, true when the flag is set.
func TestEmbeddedConfigMapsTrustProject(t *testing.T) {
	on := embeddedConfig(config{workspace: "/ws", model: "m", mock: true, trustProject: true}, port.NopDiagnostics{})
	if !on.TrustProject {
		t.Error("embeddedConfig.TrustProject = false with --trust-project, want true")
	}
	off := embeddedConfig(config{workspace: "/ws", model: "m", mock: true}, port.NopDiagnostics{})
	if off.TrustProject {
		t.Error("embeddedConfig.TrustProject = true with flag off, want false")
	}
}

// TestEmbeddedConfigHeadlessIsFalse asserts the mecatui embedded server is
// INTERACTIVE (Headless false): the posture ladder grants ingestion at auto/yolo
// (the dev default ingests the operator's own CLAUDE.md). The former suppressor
// was REMOVED (issue #359 redesign); the ingestion axis is now a
// positive grant raised by applyPosture in app.Build, not a cmd field.
func TestEmbeddedConfigHeadlessIsFalse(t *testing.T) {
	off := embeddedConfig(config{workspace: "/ws", model: "m", mock: true}, port.NopDiagnostics{})
	if off.Headless {
		t.Error("embeddedConfig.Headless = true, want false (mecatui is interactive; the ladder grants ingestion at auto/yolo)")
	}
}

// TestEmbeddedConfigMapsSubagentModel asserts --subagent-model flows through to
// app.Config.SubagentModel (the def-less child-default model, issue #35).
func TestEmbeddedConfigMapsSubagentModel(t *testing.T) {
	ac := embeddedConfig(config{workspace: "/ws", model: "m", subagentModel: "gpt-5-mini", mock: true}, port.NopDiagnostics{})
	if ac.SubagentModel != "gpt-5-mini" {
		t.Errorf("embeddedConfig.SubagentModel = %q, want %q", ac.SubagentModel, "gpt-5-mini")
	}
}

// TestEmbeddedConfigInteractive is the mecatui-embedded coherence fix (issue #31):
// mecatui IS the interactive client (a human sits at the approval modal), so the
// embedded server must run INTERACTIVE — a subagent/team-member/branch child's
// unresolved permission ask SURFACES to the modal rather than being auto-denied
// (or LLM-adjudicated). The wire path is pinned by
// server.TestGRPCConverseApproveSurfacedChildAsk; this pins the embedded posture.
func TestEmbeddedConfigInteractive(t *testing.T) {
	ac := embeddedConfig(config{workspace: "/ws", model: "m", mock: true}, port.NopDiagnostics{})
	if !ac.Interactive {
		t.Fatalf("embeddedConfig.Interactive = false, want true (mecatui is the interactive client; child asks must surface to the modal, not auto-deny)")
	}
}

// TestParseFlagsServerDefaultModel asserts the issue-#21 deployment-default
// flags (embedded server only) parse into the config — empty by default — and
// embeddedConfig threads them onto app.Config.DefaultProvider/DefaultModel,
// mirroring mecated's mapping.
func TestParseFlagsServerDefaultModel(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if def.defaultProvider != "" || def.defaultModel != "" {
		t.Errorf("defaults = (%q, %q), want both empty (no configured deployment default)", def.defaultProvider, def.defaultModel)
	}

	cfg, err := parseFlags([]string{
		"--default-provider", "openrouter",
		"--default-model", "openai/gpt-5-mini",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.defaultProvider != "openrouter" || cfg.defaultModel != "openai/gpt-5-mini" {
		t.Errorf("parsed Default* = (%q, %q), want (openrouter, openai/gpt-5-mini)", cfg.defaultProvider, cfg.defaultModel)
	}

	ac := embeddedConfig(config{workspace: "/ws", mock: true,
		defaultProvider: "openrouter", defaultModel: "openai/gpt-5-mini"}, port.NopDiagnostics{})
	if ac.DefaultProvider != "openrouter" || ac.DefaultModel != "openai/gpt-5-mini" {
		t.Errorf("app.Config Default* = (%q, %q), want the config fields threaded through", ac.DefaultProvider, ac.DefaultModel)
	}
}

// TestParseFlagsTrustProject asserts --trust-project defaults to false and flips
// true when set — unified with mecated's default-off posture.
func TestParseFlagsTrustProject(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if def.trustProject {
		t.Error("trustProject default = true, want false")
	}

	cfg, err := parseFlags([]string{"-trust-project"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !cfg.trustProject {
		t.Error("trustProject = false, want true (flag set)")
	}
}

// TestParseFlagsDefaults asserts an empty workspace resolves to an absolute path
// (cwd).
func TestParseFlagsDefaults(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !filepath.IsAbs(cfg.workspace) {
		t.Errorf("workspace = %q, want absolute", cfg.workspace)
	}
}

// TestListenerScopedWorkspaceAuthority_Scenario3_RemoteConnectSendsEmptyWorkspace pins the remote client privacy default: no client cwd reaches CreateSession.
func TestListenerScopedWorkspaceAuthority_Scenario3_RemoteConnectSendsEmptyWorkspace(t *testing.T) {
	cfg, err := parseRunConfig(invocationResolution{mode: modeConnect, address: "203.0.113.10:8080"})
	if err != nil {
		t.Fatalf("parse run config: %v", err)
	}
	if cfg.workspace != "" {
		t.Fatalf("remote CreateSession workspace = %q, want empty", cfg.workspace)
	}
}

// TestServerOwnedSessionPlacement_RemoteConnectNeverResolvesCWD pins that every
// connect target, including loopback, leaves local placement to the server.
func TestServerOwnedSessionPlacement_RemoteConnectNeverResolvesCWD(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "203.0.113.10:8080"} {
		cfg, err := parseRunConfig(invocationResolution{mode: modeConnect, address: addr})
		if err != nil {
			t.Fatalf("parse connect %s: %v", addr, err)
		}
		if cfg.workspace != "" {
			t.Fatalf("connect %s resolved local workspace %q", addr, cfg.workspace)
		}
	}
}

func TestServerOwnedSessionPlacement_EmbeddedWorkspaceConfiguresComposition(t *testing.T) {
	want, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseRunConfig(invocationResolution{mode: modeLocal, remaining: []string{"--mock"}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.workspace != want || !filepath.IsAbs(cfg.workspace) {
		t.Fatalf("embedded workspace = %q, want %q", cfg.workspace, want)
	}
}

// TestListenerScopedWorkspaceAuthority_Scenario3_RemoteExplicitWorkspaceIsRejectedLocally pins rejection before workspace resolution or session creation.
func TestListenerScopedWorkspaceAuthority_Scenario3_RemoteExplicitWorkspaceIsRejectedLocally(t *testing.T) {
	const explicitWorkspace = "must-not-resolve"
	_, parsed, err := parseTransportFlags(modeConnect, io.Discard, []string{"--workspace", explicitWorkspace})
	if err != nil {
		t.Fatalf("parse transport flags: %v", err)
	}
	if parsed.workspace != explicitWorkspace {
		t.Fatalf("parsed workspace = %q, want unresolved input %q", parsed.workspace, explicitWorkspace)
	}
	_, err = parseRunConfig(invocationResolution{
		mode: modeConnect, address: "203.0.113.10:8080",
		remaining: []string{"--workspace", explicitWorkspace},
	})
	if err == nil {
		t.Fatal("remote explicit workspace was accepted")
	} else if !strings.Contains(err.Error(), "--workspace") || !strings.Contains(err.Error(), "embedded") {
		t.Fatalf("rejection = %q, want clear remote --workspace error", err)
	}
	if parsed.workspace != explicitWorkspace {
		t.Fatalf("rejected workspace = %q, want unresolved input %q", parsed.workspace, explicitWorkspace)
	}
}

// TestParseFlagsWorkspaceAbs asserts a relative --workspace is made absolute.
func TestParseFlagsWorkspaceAbs(t *testing.T) {
	cfg, err := parseFlags([]string{"-workspace", "rel/dir"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !filepath.IsAbs(cfg.workspace) {
		t.Errorf("workspace = %q, want absolute", cfg.workspace)
	}
}

// TestParseFlagsNoAltScreen asserts the inline opt-out is off by default and is
// set by either --no-alt-screen or its --inline alias.
func TestParseFlagsNoAltScreen(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.noAltScreen {
		t.Error("noAltScreen = true by default, want false (full-screen alt screen)")
	}
	for _, flag := range []string{"-no-alt-screen", "-inline"} {
		cfg, err := parseFlags([]string{flag})
		if err != nil {
			t.Fatalf("parseFlags(%q): %v", flag, err)
		}
		if !cfg.noAltScreen {
			t.Errorf("%s did not set noAltScreen", flag)
		}
	}
}

// TestParseFlagsNoMouse asserts the native-selection escape hatch: off by default,
// set by --no-mouse, and set by MECATUI_NO_MOUSE=1/true (with the flag winning).
func TestParseFlagsNoMouse(t *testing.T) {
	t.Setenv("MECATUI_NO_MOUSE", "") // isolate from the ambient environment
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.noMouse {
		t.Error("noMouse = true by default, want false (mouse captured: wheel + in-app selection)")
	}

	cfg, err = parseFlags([]string{"-no-mouse"})
	if err != nil {
		t.Fatalf("parseFlags(-no-mouse): %v", err)
	}
	if !cfg.noMouse {
		t.Error("-no-mouse did not set noMouse")
	}

	for _, v := range []string{"1", "true"} {
		t.Setenv("MECATUI_NO_MOUSE", v)
		cfg, err := parseFlags(nil)
		if err != nil {
			t.Fatalf("parseFlags (MECATUI_NO_MOUSE=%q): %v", v, err)
		}
		if !cfg.noMouse {
			t.Errorf("MECATUI_NO_MOUSE=%q did not set noMouse", v)
		}
	}

	// A non-truthy env value must NOT enable it.
	t.Setenv("MECATUI_NO_MOUSE", "0")
	cfg, err = parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags (MECATUI_NO_MOUSE=0): %v", err)
	}
	if cfg.noMouse {
		t.Error("MECATUI_NO_MOUSE=0 set noMouse, want false")
	}
}

// TestParseFlagsThemeResolution asserts cfg.theme (the light-theme
// auto-detect gate's input, ADR 0280 — resolveThemeAutoDetect treats an empty
// cfg.theme as "no explicit theme") stays empty with none given, and picks up
// a theme name from either --theme or the MECATUI_THEME fallback.
func TestParseFlagsThemeResolution(t *testing.T) {
	t.Setenv("MECATUI_THEME", "") // isolate from the ambient environment

	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.theme != "" {
		t.Errorf("cfg.theme = %q with no theme given, want empty", cfg.theme)
	}

	cfg, err = parseFlags([]string{"-theme", "solar"})
	if err != nil {
		t.Fatalf("parseFlags(-theme solar): %v", err)
	}
	if cfg.theme != "solar" {
		t.Errorf("cfg.theme = %q, want %q", cfg.theme, "solar")
	}

	t.Setenv("MECATUI_THEME", "mono")
	cfg, err = parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags (MECATUI_THEME=mono): %v", err)
	}
	if cfg.theme != "mono" {
		t.Errorf("cfg.theme = %q, want %q", cfg.theme, "mono")
	}
}

// TestParseFlagsTerminalTitle asserts the dynamic terminal title is ON by
// default, turned off by --terminal-title=off/false/0, left on by
// on/true/1/"" (explicitly or defaulted), turned off by
// MECATUI_NO_TERMINAL_TITLE=1/true (with the flag winning), and that an invalid
// value fails fast.
func TestParseFlagsTerminalTitle(t *testing.T) {
	t.Setenv("MECATUI_NO_TERMINAL_TITLE", "") // isolate from the ambient environment

	// Default: on.
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.terminalTitleOff {
		t.Error("terminalTitleOff = true by default, want false (dynamic title on)")
	}

	// off/false/0 disable it.
	for _, v := range []string{"off", "false", "0"} {
		cfg, err := parseFlags([]string{"--terminal-title", v})
		if err != nil {
			t.Fatalf("parseFlags(--terminal-title %q): %v", v, err)
		}
		if !cfg.terminalTitleOff {
			t.Errorf("--terminal-title=%q did not set terminalTitleOff", v)
		}
	}

	// on/true/1/"" (explicit) leave it on.
	for _, v := range []string{"on", "true", "1", ""} {
		args := []string{"--terminal-title", v}
		if v == "" {
			// "" can't be passed as a flag value on the CLI; the default "" path is
			// already covered by parseFlags(nil) above. Skip the explicit-"" case.
			continue
		}
		cfg, err := parseFlags(args)
		if err != nil {
			t.Fatalf("parseFlags(--terminal-title %q): %v", v, err)
		}
		if cfg.terminalTitleOff {
			t.Errorf("--terminal-title=%q set terminalTitleOff, want false (on)", v)
		}
	}

	// Env fallback: MECATUI_NO_TERMINAL_TITLE=1/true disables it (flag not passed).
	for _, v := range []string{"1", "true"} {
		t.Setenv("MECATUI_NO_TERMINAL_TITLE", v)
		cfg, err := parseFlags(nil)
		if err != nil {
			t.Fatalf("parseFlags (MECATUI_NO_TERMINAL_TITLE=%q): %v", v, err)
		}
		if !cfg.terminalTitleOff {
			t.Errorf("MECATUI_NO_TERMINAL_TITLE=%q did not set terminalTitleOff", v)
		}
	}

	// A non-truthy env value must NOT disable it.
	t.Setenv("MECATUI_NO_TERMINAL_TITLE", "0")
	cfg, err = parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags (MECATUI_NO_TERMINAL_TITLE=0): %v", err)
	}
	if cfg.terminalTitleOff {
		t.Error("MECATUI_NO_TERMINAL_TITLE=0 set terminalTitleOff, want false")
	}

	// The flag wins over the env: --terminal-title=on with the env set keeps it on.
	t.Setenv("MECATUI_NO_TERMINAL_TITLE", "1")
	cfg, err = parseFlags([]string{"--terminal-title", "on"})
	if err != nil {
		t.Fatalf("parseFlags(--terminal-title on with env): %v", err)
	}
	if cfg.terminalTitleOff {
		t.Error("--terminal-title=on should win over MECATUI_NO_TERMINAL_TITLE=1")
	}

	// An invalid value fails fast.
	if _, err := parseFlags([]string{"--terminal-title", "maybe"}); err == nil {
		t.Error("--terminal-title=maybe should fail parseFlags (invalid value)")
	}
}

// TestParseFlagsMemoryDefaults asserts the memory flags default to off/empty;
// the per-project default PATH is computed later in embeddedConfig, not here.
func TestParseFlagsMemoryDefaults(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.memoryDir != "" {
		t.Errorf("memoryDir = %q, want \"\" (default computed in embeddedConfig)", cfg.memoryDir)
	}
	if cfg.noMemory {
		t.Error("noMemory = true by default, want false (memory on)")
	}
}

// TestParseFlagsMemoryFlags asserts --memory-dir and --no-memory map onto the
// config fields.
func TestParseFlagsMemoryFlags(t *testing.T) {
	cfg, err := parseFlags([]string{"-memory-dir", "/tmp/mem", "-no-memory"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.memoryDir != "/tmp/mem" {
		t.Errorf("memoryDir = %q, want /tmp/mem", cfg.memoryDir)
	}
	if !cfg.noMemory {
		t.Error("--no-memory did not set noMemory")
	}
}

// TestParseFlagsStoreDefaults asserts the session-store flags default to
// off/empty; the per-workspace default PATH is computed later in embeddedConfig
// (resolveStoreDir), not here (issue #79).
func TestParseFlagsStoreDefaults(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.storeDir != "" {
		t.Errorf("storeDir = %q, want \"\" (default computed in embeddedConfig)", cfg.storeDir)
	}
	if cfg.noStore {
		t.Error("noStore = true by default, want false (durable store on)")
	}
}

// TestParseFlagsStoreFlags asserts --store-dir and --no-store map onto the config
// fields (issue #79).
func TestParseFlagsStoreFlags(t *testing.T) {
	cfg, err := parseFlags([]string{"-store-dir", "/tmp/store", "-no-store"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.storeDir != "/tmp/store" {
		t.Errorf("storeDir = %q, want /tmp/store", cfg.storeDir)
	}
	if !cfg.noStore {
		t.Error("--no-store did not set noStore")
	}
}

// TestDefaultStoreDir is a pure table test of defaultStoreDir: it reads no
// globals (the stateBase is injected), so it needs no env dance and is safe to
// run in parallel. Covers the path-slug encoding (the SAME scheme as
// defaultMemoryDir), per-workspace disambiguation, and both degraded guards
// (empty base, empty workspace) (issue #79).
func TestDefaultStoreDir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		stateBase, ws string
		want          string
	}{
		{
			name:      "path slug under state home",
			stateBase: "/xdg/state",
			ws:        "/var/home/jaosorior/Development/stacklok/mecatl",
			want:      filepath.Join("/xdg/state", "mecatui", "sessions", "-var-home-jaosorior-Development-stacklok-mecatl"),
		},
		{
			name:      "local-state fallback base (resolved by the caller)",
			stateBase: "/home/tester/.local/state",
			ws:        "/ws/proj",
			want:      filepath.Join("/home/tester/.local/state", "mecatui", "sessions", "-ws-proj"),
		},
		{
			name:      "distinct parents, same basename -> distinct leaves (a)",
			stateBase: "/xdg/state",
			ws:        "/a/proj",
			want:      filepath.Join("/xdg/state", "mecatui", "sessions", "-a-proj"),
		},
		{
			name:      "distinct parents, same basename -> distinct leaves (b)",
			stateBase: "/xdg/state",
			ws:        "/b/proj",
			want:      filepath.Join("/xdg/state", "mecatui", "sessions", "-b-proj"),
		},
		{
			name:      "empty state home degrades to disabled",
			stateBase: "",
			ws:        "/ws/proj",
			want:      "",
		},
		{
			name:      "empty workspace degrades to disabled",
			stateBase: "/xdg/state",
			ws:        "",
			want:      "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := defaultStoreDir(tc.stateBase, tc.ws)
			if got != tc.want {
				t.Errorf("defaultStoreDir(%q, %q) = %q, want %q", tc.stateBase, tc.ws, got, tc.want)
			}
			if tc.want != "" && !strings.HasPrefix(filepath.Base(got), "-") {
				t.Errorf("leaf %q should preserve the leading separator as a leading '-'", filepath.Base(got))
			}
		})
	}

	// Determinism: the same inputs always produce the same path.
	if a, b := defaultStoreDir("/xdg/state", "/a/proj"), defaultStoreDir("/xdg/state", "/a/proj"); a != b {
		t.Errorf("defaultStoreDir is not deterministic: %q != %q", a, b)
	}
}

// TestResolveStoreDirPrecedence covers the precedence table on the real
// resolveStoreDir path (which reads the XDG state base via
// xdgconfig.UserStateDir, hence t.Setenv XDG_STATE_HOME — UserStateDir reads the
// env directly, so no xdg.Reload is needed): --no-store wins, then an explicit
// --store-dir, then the computed per-workspace default under the resolved state
// base (issue #79).
func TestResolveStoreDirPrecedence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")

	// --no-store wins even over an explicit --store-dir.
	if got := resolveStoreDir(config{workspace: "/ws", storeDir: "/x", noStore: true}); got != "" {
		t.Errorf("--no-store should win: got %q, want \"\"", got)
	}
	// explicit --store-dir overrides the default.
	if got := resolveStoreDir(config{workspace: "/ws", storeDir: "/x"}); got != "/x" {
		t.Errorf("explicit --store-dir: got %q, want /x", got)
	}
	// neither -> computed default under the resolved XDG state base.
	got := resolveStoreDir(config{workspace: "/var/home/ozz/dev/mecatl"})
	want := filepath.Join("/xdg/state", "mecatui", "sessions", "-var-home-ozz-dev-mecatl")
	if got != want {
		t.Errorf("default: got %q, want %q", got, want)
	}
}

// TestEmbeddedConfigStore asserts the embedded server defaults the durable store
// ON (a non-empty per-workspace StoreDir) with destructive main retention
// explicitly disabled, and that --no-store yields an empty StoreDir.
func TestEmbeddedConfigStore(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")

	on := embeddedConfig(config{workspace: "/var/home/ozz/dev/mecatl", model: "m", mock: true}, port.NopDiagnostics{})
	wantStore := filepath.Join("/xdg/state", "mecatui", "sessions", "-var-home-ozz-dev-mecatl")
	if on.StoreDir != wantStore {
		t.Errorf("default StoreDir = %q, want %q", on.StoreDir, wantStore)
	}
	if on.MainRetention != 0 {
		t.Errorf("MainRetention = %v, want disabled", on.MainRetention)
	}
	if on.MainRetentionMaxTotal != 0 {
		t.Errorf("MainRetentionMaxTotal = %d, want disabled", on.MainRetentionMaxTotal)
	}

	off := embeddedConfig(config{workspace: "/var/home/ozz/dev/mecatl", model: "m", mock: true, noStore: true}, port.NopDiagnostics{})
	if off.StoreDir != "" {
		t.Errorf("--no-store StoreDir = %q, want \"\" (in-memory fallback)", off.StoreDir)
	}
}

// TestParseFlagsCommandDefaults asserts slash commands are on by default (no flags
// set) — the resolution to "enabled with the conventional dirs" happens in
// embeddedConfig, so the raw config fields are empty/false here.
func TestParseFlagsCommandDefaults(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.commandsDir != "" {
		t.Errorf("commandsDir = %q, want \"\" (default resolved in embeddedConfig)", cfg.commandsDir)
	}
	if cfg.noCommands {
		t.Error("noCommands = true by default, want false (commands on)")
	}
}

// TestParseFlagsCommandFlags asserts --commands-dir and --no-commands map onto the
// config fields.
func TestParseFlagsCommandFlags(t *testing.T) {
	cfg, err := parseFlags([]string{"-commands-dir", "/tmp/cmds", "-no-commands"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.commandsDir != "/tmp/cmds" {
		t.Errorf("commandsDir = %q, want /tmp/cmds", cfg.commandsDir)
	}
	if !cfg.noCommands {
		t.Error("--no-commands did not set noCommands")
	}
}

// TestResolveCommands covers the slash-command precedence: --no-commands disables
// (wins over an explicit dir); an explicit --commands-dir overrides; otherwise
// commands are on with the conventional dirs (empty dir, enabled).
func TestResolveCommands(t *testing.T) {
	t.Parallel()
	// --no-commands wins, even with a dir set.
	if dir, enable := resolveCommands(config{commandsDir: "/x", noCommands: true}); dir != "" || enable {
		t.Errorf("no-commands: got (%q, %v), want (\"\", false)", dir, enable)
	}
	// explicit dir overrides.
	if dir, enable := resolveCommands(config{commandsDir: "/x"}); dir != "/x" || !enable {
		t.Errorf("commands-dir: got (%q, %v), want (\"/x\", true)", dir, enable)
	}
	// default: on with the conventional dirs (empty dir).
	if dir, enable := resolveCommands(config{}); dir != "" || !enable {
		t.Errorf("default: got (%q, %v), want (\"\", true)", dir, enable)
	}
}

// TestEmbeddedConfigCommands asserts embeddedConfig turns slash commands ON by
// default and that --no-commands turns them fully off.
func TestEmbeddedConfigCommands(t *testing.T) {
	on := embeddedConfig(config{workspace: "/ws", model: "m", mock: true}, port.NopDiagnostics{})
	if !on.EnableCommands || on.CommandsDir != "" {
		t.Errorf("default: EnableCommands=%v CommandsDir=%q, want (true, \"\")", on.EnableCommands, on.CommandsDir)
	}
	off := embeddedConfig(config{workspace: "/ws", model: "m", mock: true, noCommands: true}, port.NopDiagnostics{})
	if off.EnableCommands || off.CommandsDir != "" {
		t.Errorf("--no-commands: EnableCommands=%v CommandsDir=%q, want (false, \"\")", off.EnableCommands, off.CommandsDir)
	}
}

// TestParseFlagsSkillDefaults asserts skill discovery is on by default (no flags) —
// the resolution to conventional discovery happens in embeddedConfig, so the raw
// config fields are empty/false here.
func TestParseFlagsSkillDefaults(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.skillsDir != "" {
		t.Errorf("skillsDir = %q, want \"\" (default resolved in embeddedConfig)", cfg.skillsDir)
	}
	if cfg.noSkills {
		t.Error("noSkills = true by default, want false (skills on)")
	}
}

// TestParseFlagsSkillFlags asserts --skills-dir and --no-skills map onto the config.
func TestParseFlagsSkillFlags(t *testing.T) {
	cfg, err := parseFlags([]string{"-skills-dir", "/tmp/skills", "-no-skills"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.skillsDir != "/tmp/skills" {
		t.Errorf("skillsDir = %q, want /tmp/skills", cfg.skillsDir)
	}
	if !cfg.noSkills {
		t.Error("--no-skills did not set noSkills")
	}
}

// TestResolveSkills covers the skill precedence: --no-skills disables (wins over an
// explicit dir); an explicit --skills-dir scopes to that one dir (no conventional);
// otherwise conventional discovery is on (nil dirs, true).
func TestResolveSkills(t *testing.T) {
	t.Parallel()
	// --no-skills wins, even with a dir set.
	if dirs, conv := resolveSkills(config{skillsDir: "/x", noSkills: true}); dirs != nil || conv {
		t.Errorf("no-skills: got (%v, %v), want (nil, false)", dirs, conv)
	}
	// explicit dir scopes discovery, no conventional.
	if dirs, conv := resolveSkills(config{skillsDir: "/x"}); len(dirs) != 1 || dirs[0] != "/x" || conv {
		t.Errorf("skills-dir: got (%v, %v), want ([/x], false)", dirs, conv)
	}
	// default: conventional discovery on, no explicit dirs.
	if dirs, conv := resolveSkills(config{}); dirs != nil || !conv {
		t.Errorf("default: got (%v, %v), want (nil, true)", dirs, conv)
	}
}

// TestEmbeddedConfigSkills asserts embeddedConfig turns conventional skill discovery
// ON by default and that --no-skills turns it off (no dirs, no conventional).
func TestEmbeddedConfigSkills(t *testing.T) {
	on := embeddedConfig(config{workspace: "/ws", model: "m", mock: true}, port.NopDiagnostics{})
	if !on.SkillsConventional || on.SkillsDirs != nil {
		t.Errorf("default: SkillsConventional=%v SkillsDirs=%v, want (true, nil)", on.SkillsConventional, on.SkillsDirs)
	}
	off := embeddedConfig(config{workspace: "/ws", model: "m", mock: true, noSkills: true}, port.NopDiagnostics{})
	if off.SkillsConventional || off.SkillsDirs != nil {
		t.Errorf("--no-skills: SkillsConventional=%v SkillsDirs=%v, want (false, nil)", off.SkillsConventional, off.SkillsDirs)
	}
}

// TestDefaultMemoryDir is a pure table test of defaultMemoryDir: it reads no
// globals (the dataHome base is injected), so it needs no env/xdg.Reload dance and
// is safe to run in parallel. Covers the path-slug encoding, per-project
// disambiguation, determinism, and both degraded guards (empty base, empty
// workspace).
func TestDefaultMemoryDir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		dataHome, ws string
		want         string
	}{
		{
			name:     "path slug under data home",
			dataHome: "/xdg/data",
			ws:       "/var/home/jaosorior/Development/stacklok/mecatl",
			want:     filepath.Join("/xdg/data", "mecatui", "memory", "-var-home-jaosorior-Development-stacklok-mecatl"),
		},
		{
			name:     "local-share fallback base (resolved by the caller)",
			dataHome: "/home/tester/.local/share",
			ws:       "/ws/proj",
			want:     filepath.Join("/home/tester/.local/share", "mecatui", "memory", "-ws-proj"),
		},
		{
			name:     "distinct parents, same basename -> distinct leaves (a)",
			dataHome: "/xdg/data",
			ws:       "/a/proj",
			want:     filepath.Join("/xdg/data", "mecatui", "memory", "-a-proj"),
		},
		{
			name:     "distinct parents, same basename -> distinct leaves (b)",
			dataHome: "/xdg/data",
			ws:       "/b/proj",
			want:     filepath.Join("/xdg/data", "mecatui", "memory", "-b-proj"),
		},
		{
			name:     "empty data home degrades to disabled",
			dataHome: "",
			ws:       "/ws/proj",
			want:     "",
		},
		{
			name:     "empty workspace degrades to disabled",
			dataHome: "/xdg/data",
			ws:       "",
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := defaultMemoryDir(tc.dataHome, tc.ws)
			if got != tc.want {
				t.Errorf("defaultMemoryDir(%q, %q) = %q, want %q", tc.dataHome, tc.ws, got, tc.want)
			}
			// The leaf must preserve the leading separator as a leading '-'.
			if tc.want != "" && !strings.HasPrefix(filepath.Base(got), "-") {
				t.Errorf("leaf %q should preserve the leading separator as a leading '-'", filepath.Base(got))
			}
		})
	}

	// Determinism: the same inputs always produce the same path.
	a1 := defaultMemoryDir("/xdg/data", "/a/proj")
	a2 := defaultMemoryDir("/xdg/data", "/a/proj")
	if a1 != a2 {
		t.Errorf("defaultMemoryDir is not deterministic: %q != %q", a1, a2)
	}
}

// setDataHome points adrg/xdg's DataHome at dir for the duration of the test.
// adrg/xdg snapshots the environment at package init, so a bare t.Setenv is NOT
// reflected — Reload() must re-read the env. The cleanup re-reads it again so the
// global does not leak a synthetic path into a later test. Only the
// resolveMemoryDir integration test needs this; defaultMemoryDir is pure.
func setDataHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", dir)
	xdg.Reload()
	t.Cleanup(xdg.Reload)
}

// TestResolveMemoryDirPrecedence covers the precedence table on the real
// resolveMemoryDir path (which legitimately reads the xdg.DataHome global, hence
// setDataHome): --no-memory wins, then an explicit --memory-dir, then the computed
// per-project default under the resolved data base.
func TestResolveMemoryDirPrecedence(t *testing.T) {
	setDataHome(t, "/xdg/data")

	// --no-memory wins even over an explicit --memory-dir.
	if got := resolveMemoryDir(config{workspace: "/ws", memoryDir: "/x", noMemory: true}); got != "" {
		t.Errorf("--no-memory should win: got %q, want \"\"", got)
	}
	// explicit --memory-dir overrides the default.
	if got := resolveMemoryDir(config{workspace: "/ws", memoryDir: "/x"}); got != "/x" {
		t.Errorf("explicit --memory-dir: got %q, want /x", got)
	}
	// neither -> computed default under the resolved XDG data base.
	got := resolveMemoryDir(config{workspace: "/var/home/ozz/dev/mecatl"})
	want := filepath.Join("/xdg/data", "mecatui", "memory", "-var-home-ozz-dev-mecatl")
	if got != want {
		t.Errorf("default: got %q, want %q", got, want)
	}
}

// TestConnectExplicitAuthTokenParses is the by-name connect-side counterpart of
// TestRejectRemoteOnlyFlagsInBare: an EXPLICIT --auth-token in connect mode
// parses successfully and lands on the config — an accidental applicability
// flip (connect rejecting --auth-token) would stay green without it.
func TestConnectExplicitAuthTokenParses(t *testing.T) {
	_, cfg, err := parseTransportFlags(modeConnect, &bytes.Buffer{},
		[]string{"--auth-token", "explicit-tok", "--workspace", "/abs"})
	if err != nil {
		t.Fatalf("parseTransportFlags(connect, --auth-token explicit-tok): %v", err)
	}
	if cfg.authToken != "explicit-tok" {
		t.Errorf("authToken = %q, want explicit-tok", cfg.authToken)
	}
}

// TestParseFlagsAuthEnv asserts MECATL_AUTH_TOKEN is picked up when the flag is
// unset. --auth-token is a remote-only flag, so the test parses in connect mode.
func TestParseFlagsAuthEnv(t *testing.T) {
	t.Setenv("MECATL_AUTH_TOKEN", "tok-123")
	_, cfg, err := parseTransportFlags(modeConnect, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatalf("parseTransportFlags(connect): %v", err)
	}
	if cfg.authToken != "tok-123" {
		t.Errorf("authToken = %q, want tok-123", cfg.authToken)
	}
}

// TestValidateMode rejects unknown modes and accepts the three valid ones. The
// configs set mock so the embedded-provider check (validated last) passes and the
// test stays focused on mode handling.
func TestValidateMode(t *testing.T) {
	for _, mode := range []string{"default", "plan", "accept-edits"} {
		cfg := config{workspace: "/abs", mode: mode, mock: true}
		if err := cfg.validate(); err != nil {
			t.Errorf("mode %q rejected: %v", mode, err)
		}
	}
	bad := config{workspace: "/abs", mode: "nope", mock: true}
	if err := bad.validate(); err == nil {
		t.Error("invalid mode accepted")
	}
}

// TestValidateWorkspaceRequired asserts a non-absolute or empty workspace fails
// validation (the server requires absolute).
func TestValidateWorkspaceRequired(t *testing.T) {
	if err := (config{mode: "default"}).validate(); err == nil {
		t.Error("empty workspace accepted")
	}
	if err := (config{workspace: "rel", mode: "default"}).validate(); err == nil {
		t.Error("relative workspace accepted")
	}
}

// TestValidateEmbeddedProviderRequired asserts that the bare (embedded) path
// needs a resolvable provider, and that the error points to concise next steps
// rather than enumerating every supported provider and flag.
func TestValidateEmbeddedProviderRequired(t *testing.T) {
	// Bare mode, no credential and no mock cannot host an embedded server.
	if err := (config{workspace: "/abs", mode: "default", transportMode: modeLocal}).validate(); err == nil {
		t.Error("expected an error when embedding with no provider")
	} else {
		msg := err.Error()
		for _, want := range []string{
			"no LLM provider", "--auth-file", "ToolHive", "--mock", "connect", "docs/usage.md",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("validate() error %q does not mention %q", msg, want)
			}
		}
	}
	// --mock resolves the provider.
	if err := (config{workspace: "/abs", mode: "default", transportMode: modeLocal, mock: true}).validate(); err != nil {
		t.Errorf("--mock should satisfy the provider check: %v", err)
	}
	// An OpenAI key resolves the provider.
	if err := (config{workspace: "/abs", mode: "default", transportMode: modeLocal, openAIKey: "sk-x"}).validate(); err != nil {
		t.Errorf("OPENAI_API_KEY should satisfy the provider check: %v", err)
	}
	// connect mode means no embedded provider is needed.
	if err := (config{workspace: "/abs", mode: "default", transportMode: modeConnect, connectAddress: "127.0.0.1:8080"}).validate(); err != nil {
		t.Errorf("mecatui connect should not require a provider: %v", err)
	}
}

func TestConfigValidateAcceptsAuthFileCredential(t *testing.T) {
	for _, envName := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_API_KEY"} {
		t.Setenv(envName, "")
	}
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	tests := []struct {
		name  string
		body  string
		check func(*testing.T, app.Config)
	}{
		{
			name: "file-only API key",
			body: "providers:\n  openai:\n    api_key: sk-file-only\n",
			check: func(t *testing.T, got app.Config) {
				t.Helper()
				if got.OpenAIKey != "sk-file-only" || !got.UseOpenAI {
					t.Fatal("embedded config did not reuse the file-only API key snapshot")
				}
			},
		},
		{
			name: "file-only Codex token",
			body: fmt.Sprintf("providers:\n  openai-codex:\n    oauth:\n      access_token: %s\n      account_id: acct-tui\n      expires_at: %s\n", codextest.Token(expires, "acct-tui"), expires.Format(time.RFC3339)),
			check: func(t *testing.T, got app.Config) {
				t.Helper()
				if !got.OpenAICodexCredential.Configured() || got.OpenAICodexCredential.Validate(time.Now()) != nil {
					t.Fatal("embedded config did not reuse the Codex credential snapshot")
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.yaml")
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := parseFlags([]string{"--workspace", "/abs", "--auth-file", path, "--toolhive-llm=false"})
			if err != nil {
				t.Fatalf("parseFlags: %v", err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := cfg.validate(); err != nil {
				t.Fatalf("cached file-only credential rejected after file removal: %v", err)
			}
			for range 2 {
				got := embeddedConfig(cfg, port.NopDiagnostics{})
				test.check(t, got)
				if got.OpenAICodexCredential != cfg.providerKeys.OpenAICodex {
					t.Fatal("embedded config re-resolved the Codex credential snapshot")
				}
			}
		})
	}

	t.Run("warning emitted once outside pure projection", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing-auth.yaml")
		cfg, err := parseFlags([]string{"--workspace", "/abs", "--auth-file", missing, "--mock"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		var output bytes.Buffer
		emitAuthFileWarning(&output, cfg.providerKeys.AuthFileWarning)
		_ = embeddedConfig(cfg, port.NopDiagnostics{})
		_ = embeddedConfig(cfg, port.NopDiagnostics{})
		if got := strings.Count(output.String(), "mecatui: WARNING:"); got != 1 {
			t.Fatalf("warning count = %d, want 1: %q", got, output.String())
		}
	})
}

// TestOpenAICodexCommandRootSurfaces pins the embedded command-root half of
// AC8.4: local help exposes the file-auth entry point, a file-only credential
// survives pure projection, and an expired snapshot produces one actionable,
// secret-free pre-TUI warning.
func TestOpenAICodexCommandRootSurfaces(t *testing.T) {
	for _, envName := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_API_KEY"} {
		t.Setenv(envName, "")
	}
	help := helpRenderOut(t, modeLocal, []string{"--help"})
	if !hasFlagHeader(help, "auth-file") || !strings.Contains(help, "provider credentials YAML") {
		t.Fatalf("embedded help does not surface provider file auth:\n%s", help)
	}

	expiredAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	expiredToken := codextest.Token(expiredAt, "acct-expired")
	expiredPath := filepath.Join(t.TempDir(), "auth.yaml")
	expiredBody := fmt.Sprintf("providers:\n  openai-codex:\n    oauth:\n      access_token: %s\n      account_id: acct-expired\n      expires_at: %s\n", expiredToken, expiredAt.Format(time.RFC3339))
	if err := os.WriteFile(expiredPath, []byte(expiredBody), 0o600); err != nil {
		t.Fatal(err)
	}
	expiredCfg, err := parseFlags([]string{"--workspace", "/abs", "--auth-file", expiredPath, "--mock"})
	if err != nil {
		t.Fatalf("parse expired credential: %v", err)
	}
	var warning bytes.Buffer
	emitAuthFileWarning(&warning, expiredCfg.providerKeys.AuthFileWarning)
	for _, want := range []string{"expired", "auth.yaml", "restart"} {
		if !strings.Contains(warning.String(), want) {
			t.Errorf("expired warning %q missing %q", warning.String(), want)
		}
	}
	if strings.Count(warning.String(), "mecatui: WARNING:") != 1 || strings.Contains(warning.String(), expiredToken) {
		t.Fatalf("expired warning must be emitted once without the token: %q", warning.String())
	}
}

func TestConnectSkipsLocalAuthFile(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	authDir := filepath.Join(configHome, "mecatl")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(authDir, "auth.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  openai:\n    api_key: must-not-be-retained\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cfg, err := parseTransportFlags(modeConnect, &bytes.Buffer{}, []string{"--workspace", "/abs"})
	if err != nil {
		t.Fatalf("parseTransportFlags(connect): %v", err)
	}
	if cfg.providerKeys.Any() || cfg.providerKeys.AuthFileWarning != "" {
		t.Fatal("connect mode read or retained local auth-file state")
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("connect mode rejected: %v", err)
	}
}

// TestValidateToolhiveGatewaySatisfiesProvider asserts a ToolHive LLM gateway
// (here via an explicit --toolhive-llm-base-url) satisfies the embedded-provider
// check with no key/mock — the #263 auto-detect path that validate() must not
// reject before app.Build can register it.
func TestValidateToolhiveGatewaySatisfiesProvider(t *testing.T) {
	cfg, err := parseFlags([]string{"--workspace", "/abs", "--toolhive-llm-base-url", "http://127.0.0.1:14000/v1"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if err := cfg.validate(); err != nil {
		t.Errorf("a ToolHive gateway should satisfy the provider check: %v", err)
	}
}

func TestParseFlagsAllowAll(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if def.allowAllTools {
		t.Errorf("allowAllTools default = true, want false")
	}

	cfg, err := parseFlags([]string{"-yolo"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !cfg.allowAllTools {
		t.Errorf("allowAllTools = false, want true (flag set)")
	}
}

func TestEmbeddedConfigMapsAllowAll(t *testing.T) {
	on := embeddedConfig(config{workspace: "/ws", model: "m", mock: true, allowAllTools: true}, port.NopDiagnostics{})
	if !on.AllowAllTools {
		t.Errorf("embeddedConfig.AllowAllTools = false, want true")
	}
	off := embeddedConfig(config{workspace: "/ws", model: "m", mock: true}, port.NopDiagnostics{})
	if off.AllowAllTools {
		t.Errorf("embeddedConfig.AllowAllTools = true with flag off, want false")
	}
}

// TestParseFlagsPosture covers the embedded-server --posture surface: the value
// lands on cfg.posture and postureFlagSet flips ONLY when --posture is explicitly
// passed (so CLI out-ranks the operator-YAML key).
func TestParseFlagsPosture(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if def.posture != "" || def.postureFlagSet {
		t.Errorf("defaults: posture=%q postureFlagSet=%v, want empty/false", def.posture, def.postureFlagSet)
	}
	set, err := parseFlags([]string{"-posture", "auto"})
	if err != nil {
		t.Fatalf("parseFlags(-posture auto): %v", err)
	}
	if set.posture != "auto" || !set.postureFlagSet {
		t.Errorf("-posture auto: posture=%q postureFlagSet=%v, want \"auto\"/true", set.posture, set.postureFlagSet)
	}
}

// TestEmbeddedConfigMapsPosture pins the cmd→app.Config posture passthrough for the
// embedded server: the parsed token maps to app.Posture, PostureFlagSet rides through,
// and Privileged threads (false in the non-root test runner).
func TestEmbeddedConfigMapsPosture(t *testing.T) {
	ac := embeddedConfig(config{workspace: "/ws", model: "m", mock: true, posture: "yolo", postureFlagSet: true}, port.NopDiagnostics{})
	if ac.Posture != app.PostureYolo {
		t.Errorf("embeddedConfig.Posture = %v, want PostureYolo", ac.Posture)
	}
	if !ac.PostureFlagSet {
		t.Errorf("embeddedConfig.PostureFlagSet = false, want true")
	}
	if ac.Privileged != embeddedPrivileged() {
		t.Errorf("embeddedConfig.Privileged = %v, want %v (embeddedPrivileged())", ac.Privileged, embeddedPrivileged())
	}
}

// TestParseFlagsSubagentModelRouter covers the --subagent-model-router kill-switch
// (ADR 0042) end-to-end through mecatui's embeddedConfig: the router is enabled by the
// models.router: taxonomy, so the bool flag only sets RouterDisabled when given as
// =false. Unset → set==false → RouterDisabled==false (taxonomy governs); bare/=true →
// set==true/value==true, RouterDisabled stays false (a harmless no-op, does NOT disable);
// =false → the kill-switch, set==true/value==false, RouterDisabled==true. The
// RouterDisabled assertions would fail if the mapping inverted.
func TestParseFlagsSubagentModelRouter(t *testing.T) {
	// Default: flag never given → not set, router governed by the taxonomy (not disabled).
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if def.subagentModelRouterSet {
		t.Errorf("defaults: subagentModelRouterSet = true, want false (unset)")
	}
	if dc := embeddedConfig(config{workspace: "/ws", model: "m", mock: true}, port.NopDiagnostics{}); dc.RouterDisabled {
		t.Errorf("unset: embeddedConfig.RouterDisabled = true, want false (taxonomy governs)")
	}

	// Bare --subagent-model-router (==true): parses, set==true, value==true; this is a
	// harmless no-op — it must NOT disable the router (taxonomy governs).
	bare, err := parseFlags([]string{"-subagent-model-router"})
	if err != nil {
		t.Fatalf("parseFlags(-subagent-model-router): %v", err)
	}
	if !bare.subagentModelRouterSet || !bare.subagentModelRouter {
		t.Errorf("bare flag: set=%v value=%v, want true/true", bare.subagentModelRouterSet, bare.subagentModelRouter)
	}
	if ac := embeddedConfig(bare, port.NopDiagnostics{}); ac.RouterDisabled {
		t.Errorf("bare flag: embeddedConfig.RouterDisabled = true, want false (no-op does not disable)")
	}

	// --subagent-model-router=false: the kill-switch. set==true, value==false → disabled.
	off, err := parseFlags([]string{"-subagent-model-router=false"})
	if err != nil {
		t.Fatalf("parseFlags(-subagent-model-router=false): %v", err)
	}
	if !off.subagentModelRouterSet || off.subagentModelRouter {
		t.Errorf("=false: set=%v value=%v, want true/false", off.subagentModelRouterSet, off.subagentModelRouter)
	}
	if ac := embeddedConfig(off, port.NopDiagnostics{}); !ac.RouterDisabled {
		t.Errorf("=false: embeddedConfig.RouterDisabled = false, want true (kill-switch)")
	}
}

// TestPostureRefusalReason proves the generalised root-refusal (the exported
// app.PostureRefusalReason) gates auto AND yolo (both waive the mutate-ask floor) while
// strict/trusted are NEVER refused (they suppress no prompt), and only when PRIVILEGED.
// It would fail if the gate regressed to the historical yolo-only check.
func TestPostureRefusalReason(t *testing.T) {
	tests := []struct {
		name       string
		posture    app.Posture
		privileged bool
		wantErr    bool
	}{
		{"yolo privileged refused", app.PostureYolo, true, true},
		{"auto privileged refused", app.PostureAuto, true, true},
		{"yolo not privileged ok", app.PostureYolo, false, false},
		{"auto not privileged ok", app.PostureAuto, false, false},
		{"trusted privileged ok", app.PostureTrusted, true, false},
		{"strict privileged ok", app.PostureStrict, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := app.PostureRefusalReason(tt.posture, tt.privileged)
			if tt.wantErr && err == nil {
				t.Fatalf("expected a refusal error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

// TestValidateAllowAllConnectGuard asserts validate()'s mayEmbed guard: connect
// mode skips the allow-all root refusal entirely (on any euid), while the
// embedded path with a declared sandbox is permitted. The root-refused branch
// reads the real os.Geteuid(), so it is only assertable when actually running as
// root.
func TestValidateAllowAllConnectGuard(t *testing.T) {
	// connect: allow-all never trips the refusal regardless of euid.
	ext := config{transportMode: modeConnect, connectAddress: "127.0.0.1:8080", workspace: "/abs", mode: "default", allowAllTools: true}
	if err := ext.validate(); err != nil {
		t.Errorf("connect + allow-all should skip the refusal, got %v", err)
	}

	// Embedded + declared sandbox: permitted on any euid.
	t.Setenv("MECATL_SANDBOX", "1")
	emb := config{transportMode: modeLocal, workspace: "/abs", mode: "default", mock: true, allowAllTools: true}
	if err := emb.validate(); err != nil {
		t.Errorf("embedded + allow-all + MECATL_SANDBOX=1 should validate, got %v", err)
	}

	// Embedded + root + no sandbox: refused — only assertable when running as root.
	t.Setenv("MECATL_SANDBOX", "")
	t.Setenv("IS_SANDBOX", "")
	if os.Geteuid() == 0 {
		refused := config{transportMode: modeLocal, workspace: "/abs", mode: "default", mock: true, allowAllTools: true}
		if err := refused.validate(); err == nil {
			t.Error("embedded + allow-all as root without a sandbox should be refused")
		}
	}
}

// TestParseFlagsPromptDefaults asserts the seed-prompt flags default to empty
// (no seed) — the additive-only promise: nothing changes unless the operator
// asks for it.
func TestParseFlagsPromptDefaults(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if cfg.prompt != "" {
		t.Errorf("prompt = %q, want \"\" (no seed by default)", cfg.prompt)
	}
	if cfg.promptFile != "" {
		t.Errorf("promptFile = %q, want \"\" by default", cfg.promptFile)
	}
	if cfg.promptFileBody != "" {
		t.Errorf("promptFileBody = %q, want \"\" by default", cfg.promptFileBody)
	}
}

// TestParseFlagsPromptShortAndLong asserts both -p and --prompt bind to the SAME
// config field (the stdlib flag package tolerates multiple names targeting one
// variable), so the alias is byte-identical to the long form.
func TestParseFlagsPromptShortAndLong(t *testing.T) {
	for _, args := range [][]string{
		{"-p", "task one"},
		{"--prompt", "task one"},
	} {
		cfg, err := parseFlags(args)
		if err != nil {
			t.Fatalf("parseFlags(%v): %v", args, err)
		}
		if cfg.prompt != "task one" {
			t.Errorf("args=%v: prompt = %q, want \"task one\"", args, cfg.prompt)
		}
	}
}

// TestParseFlagsPromptFileRead asserts --prompt-file records the path AND reads
// the file body into promptFileBody at parse time (fail-fast on unreadable).
func TestParseFlagsPromptFileRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(path, []byte("read the greeting file\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := parseFlags([]string{"--prompt-file", path})
	if err != nil {
		t.Fatalf("parseFlags(--prompt-file): %v", err)
	}
	if cfg.promptFile != path {
		t.Errorf("promptFile = %q, want %q", cfg.promptFile, path)
	}
	if cfg.promptFileBody != "read the greeting file\n" {
		t.Errorf("promptFileBody = %q, want the file contents", cfg.promptFileBody)
	}
}

// TestParseFlagsPromptFileUnreadable asserts an unreadable --prompt-file fails
// fast with an error naming the path.
func TestParseFlagsPromptFileUnreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")
	_, err := parseFlags([]string{"--prompt-file", path})
	if err == nil {
		t.Fatalf("parseFlags with a missing --prompt-file should fail")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error = %q, want it to name the path %q", err.Error(), path)
	}
}
