package main

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/app"
)

// resolvedPair is the (posture, default session mode) pair a mecated argv
// produces, read through the same fast-path resolver applyPostureCLI uses and
// the app.Config the root hands to app.Build.
func resolvedPair(t *testing.T, argv ...string) (app.Posture, session.PermissionMode, app.Config) {
	t.Helper()
	cfg, err := parseFlags(argv)
	if err != nil {
		t.Fatalf("parseFlags(%q): %v", argv, err)
	}
	posture := app.ResolveAuthoritativePosture(posturePreCheckConfig(cfg, port.NopDiagnostics{}))
	mode := session.ModeDefault
	ac := appConfig(cfg, nil, nil, nil, nil, nil)
	if ac.PermissionModeFlagSet {
		tok, err := app.ParsePermissionMode(ac.PermissionMode)
		if err != nil {
			t.Fatalf("appConfig carried an invalid token %q: %v", ac.PermissionMode, err)
		}
		mode = tok.SessionMode
	} else if ac.DefaultSessionMode != "" {
		mode = ac.DefaultSessionMode
	}
	return posture, mode, ac
}

// deprecationWarnings runs the real startup posture surface (applyPostureCLI)
// and returns the deprecation WARN lines it logged.
func deprecationWarnings(t *testing.T, argv ...string) []string {
	t.Helper()
	cfg, err := parseFlags(argv)
	if err != nil {
		t.Fatalf("parseFlags(%q): %v", argv, err)
	}
	logs := captureLogs(t)
	if err := applyPostureCLI(cfg, slogdiag.NewFromLogger(slog.Default())); err != nil {
		t.Fatalf("applyPostureCLI(%q): %v", argv, err)
	}
	var out []string
	for _, line := range strings.Split(logs(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "DEPRECATED") {
			out = append(out, line)
		}
	}
	return out
}

func TestADR_0365_DeprecatedAliasesStillResolve(t *testing.T) {
	cases := []struct {
		name  string
		alias []string
		token string
	}{
		{"posture strict", []string{"--posture", "strict"}, "default"},
		{"posture trusted", []string{"--posture", "trusted"}, "trusted"},
		{"posture auto", []string{"--posture", "auto"}, "auto"},
		{"posture yolo", []string{"--posture", "yolo"}, "yolo"},
		{"yolo", []string{"--yolo"}, "yolo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aliasPosture, aliasMode, _ := resolvedPair(t, tc.alias...)
			tokPosture, tokMode, _ := resolvedPair(t, "--permission-mode", tc.token)
			if aliasPosture != tokPosture || aliasMode != tokMode {
				t.Fatalf("%q resolved to (%s, %s), want the %q token's (%s, %s)",
					tc.alias, aliasPosture, aliasMode, tc.token, tokPosture, tokMode)
			}
			warns := deprecationWarnings(t, tc.alias...)
			if len(warns) != 1 {
				t.Fatalf("%q emitted %d deprecation WARNs, want exactly 1: %q", tc.alias, len(warns), warns)
			}
			if !strings.Contains(warns[0], "--permission-mode") {
				t.Fatalf("deprecation WARN does not name --permission-mode: %s", warns[0])
			}
			if !strings.Contains(warns[0], "--permission-mode "+tc.token) {
				t.Fatalf("deprecation WARN does not name the replacement token %q: %s", tc.token, warns[0])
			}
		})
	}
	// The replacement itself is not deprecated.
	if warns := deprecationWarnings(t, "--permission-mode", "auto"); len(warns) != 0 {
		t.Fatalf("--permission-mode emitted deprecation WARNs: %q", warns)
	}
}

func TestADR_0365_AliasCombinationsStillResolve(t *testing.T) {
	cases := []struct {
		name        string
		argv        []string
		wantPosture app.Posture
		wantTrust   bool
		wantAllow   bool
	}{
		// Interactive root: --trust-project raises an explicit strict to trusted, today's MAX fold.
		{"posture strict trust-project interactive", []string{"--posture", "strict", "--trust-project"}, app.PostureTrusted, true, false},
		// Headless root: no token grants trust on headless, so yolo plus explicit trust has no single-token spelling.
		{"yolo trust-project headless", []string{"--headless", "--yolo", "--trust-project"}, app.PostureYolo, true, true},
		{"posture auto yolo", []string{"--posture", "auto", "--yolo"}, app.PostureYolo, false, true},
		// --trust-project is not deprecated and still combines with the new flag.
		{"permission-mode auto trust-project headless", []string{"--headless", "--permission-mode", "auto", "--trust-project"}, app.PostureAuto, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			posture, mode, ac := resolvedPair(t, tc.argv...)
			if posture != tc.wantPosture {
				t.Fatalf("%q posture = %s, want %s", tc.argv, posture, tc.wantPosture)
			}
			if mode != session.ModeDefault {
				t.Fatalf("%q session mode = %s, want default", tc.argv, mode)
			}
			if ac.TrustProject != tc.wantTrust {
				t.Fatalf("%q TrustProject = %v, want %v", tc.argv, ac.TrustProject, tc.wantTrust)
			}
			if ac.AllowAllTools != tc.wantAllow {
				t.Fatalf("%q AllowAllTools = %v, want %v", tc.argv, ac.AllowAllTools, tc.wantAllow)
			}
		})
	}
}

func TestADR_0365_DefaultsReproduceCurrentBehaviour(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"interactive", nil},
		{"headless", []string{"--headless"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posture, mode, ac := resolvedPair(t, tc.argv...)
			if posture != app.PostureStrict || mode != session.ModeDefault {
				t.Fatalf("unconfigured %s mecated = (%s, %s), want (strict, default)", tc.name, posture, mode)
			}
			if ac.Posture != app.PostureStrict || ac.PostureFlagSet || ac.PermissionModeFlagSet || ac.PermissionMode != "" || ac.DefaultSessionMode != "" {
				t.Fatalf("unconfigured %s app.Config carries a permission choice: posture=%s postureSet=%v modeSet=%v mode=%q default=%q",
					tc.name, ac.Posture, ac.PostureFlagSet, ac.PermissionModeFlagSet, ac.PermissionMode, ac.DefaultSessionMode)
			}
			if ac.AllowAllTools || ac.TrustProject {
				t.Fatalf("unconfigured %s mecated raised allow-all=%v trust=%v", tc.name, ac.AllowAllTools, ac.TrustProject)
			}
			if warns := deprecationWarnings(t, tc.argv...); len(warns) != 0 {
				t.Fatalf("unconfigured %s mecated emitted deprecation WARNs: %q", tc.name, warns)
			}
		})
	}
}

func TestADR_0365_PermissionModeFlagConflictsAndUnknownTokens(t *testing.T) {
	for _, argv := range [][]string{
		{"--permission-mode", "auto", "--posture", "auto"},
		{"--posture", "strict", "--permission-mode", "default"},
		{"--permission-mode", "yolo", "--yolo"},
	} {
		_, err := parseFlags(argv)
		if err == nil || !strings.Contains(err.Error(), "pass only --permission-mode") {
			t.Fatalf("parseFlags(%q) err = %v, want a pass-one startup error", argv, err)
		}
	}
	_, err := parseFlags([]string{"--permission-mode", "bogus"})
	if err == nil {
		t.Fatal("unknown --permission-mode token parsed without error")
	}
	for _, name := range app.PermissionModeNames() {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("unknown-token error %q does not name valid token %q", err, name)
		}
	}
	// --trust-project is not an alias and combines freely.
	if _, err := parseFlags([]string{"--permission-mode", "default", "--trust-project"}); err != nil {
		t.Fatalf("--permission-mode with --trust-project: %v", err)
	}
}

func TestADR_0365_PermissionModeHitsRootRefusalFastPath(t *testing.T) {
	for _, tok := range []string{"auto", "yolo"} {
		cfg, err := parseFlags([]string{"--permission-mode", tok})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		eff := app.ResolveAuthoritativePosture(posturePreCheckConfig(cfg, port.NopDiagnostics{}))
		if err := app.PostureRefusalReason(eff, true); err == nil {
			t.Fatalf("--permission-mode %s as unsandboxed root was not refused on the fast path (posture %s)", tok, eff)
		}
	}
	yolo, err := parseFlags([]string{"--yolo"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if err := app.PostureRefusalReason(app.ResolveAuthoritativePosture(posturePreCheckConfig(yolo, port.NopDiagnostics{})), true); err == nil {
		t.Fatal("--yolo as unsandboxed root was not refused on the fast path")
	}
}

func TestADR_0365_PermissionModeHelpNamesBothHalvesAndEveryToken(t *testing.T) {
	for _, want := range append(app.PermissionModeNames(), "process-wide", "new sessions") {
		if !strings.Contains(permissionModeHelp, want) {
			t.Fatalf("--permission-mode help lacks %q: %s", want, permissionModeHelp)
		}
	}
}
