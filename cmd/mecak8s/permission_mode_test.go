package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/app"
)

// k8sPair returns the (posture, default session mode) pair and the app.Config
// that a mecak8s argv hands to app.Build.
func k8sPair(t *testing.T, argv ...string) (app.Posture, session.PermissionMode, app.Config) {
	t.Helper()
	cfg, err := parseFlags(argv)
	if err != nil {
		t.Fatalf("parseFlags(%q): %v", argv, err)
	}
	ac := appConfig(cfg, port.NopDiagnostics{}, observability{})
	if ac.PermissionModeFlagSet {
		tok, err := app.ParsePermissionMode(ac.PermissionMode)
		if err != nil {
			t.Fatalf("appConfig carried an invalid token %q: %v", ac.PermissionMode, err)
		}
		return app.ResolveAliasPosture(tok.Posture, ac.AllowAllTools, ac.TrustProject), tok.SessionMode, ac
	}
	mode := ac.DefaultSessionMode
	if mode == "" {
		mode = session.ModeDefault
	}
	return app.ResolveAuthoritativePosture(ac), mode, ac
}

func k8sDeprecationWarnings(t *testing.T, argv ...string) []string {
	t.Helper()
	cfg, err := parseFlags(argv)
	if err != nil {
		t.Fatalf("parseFlags(%q): %v", argv, err)
	}
	var buf bytes.Buffer
	warnDeprecatedPermissionFlags(slogdiag.NewFromLogger(slog.New(slog.NewTextHandler(&buf, nil))), cfg)
	var out []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "DEPRECATED") {
			out = append(out, line)
		}
	}
	return out
}

func TestDeprecatedAliasesStillResolve(t *testing.T) {
	// Each deprecated --posture alias must resolve to the correct posture and emit
	// exactly one deprecation WARN naming the replacement --permission-mode token.
	// Session mode for deprecated paths is set inside Build (not at flag-parse time).
	for _, tc := range []struct {
		posture     string
		wantPosture app.Posture
		token       string
	}{
		{"strict", app.PostureStrict, "default"},
		{"auto", app.PostureAuto, "auto"},
		{"yolo", app.PostureYolo, "yolo"},
	} {
		t.Run(tc.posture, func(t *testing.T) {
			aliasPosture, _, ac := k8sPair(t, "--posture", tc.posture)
			if aliasPosture != tc.wantPosture {
				t.Fatalf("--posture %s posture = %s, want %s", tc.posture, aliasPosture, tc.wantPosture)
			}
			if ac.DefaultSessionMode != "" {
				t.Fatalf("--posture %s: cmd layer must not set DefaultSessionMode (got %q); Build sets it", tc.posture, ac.DefaultSessionMode)
			}
			warns := k8sDeprecationWarnings(t, "--posture", tc.posture)
			if len(warns) != 1 || !strings.Contains(warns[0], "--permission-mode "+tc.token) {
				t.Fatalf("--posture %s deprecation WARNs = %q, want exactly one naming --permission-mode %s", tc.posture, warns, tc.token)
			}
		})
	}
	// The --posture DEFAULT is not a use of the alias.
	if warns := k8sDeprecationWarnings(t); len(warns) != 0 {
		t.Fatalf("default mecak8s emitted deprecation WARNs: %q", warns)
	}
	if warns := k8sDeprecationWarnings(t, "--permission-mode", "auto"); len(warns) != 0 {
		t.Fatalf("--permission-mode emitted deprecation WARNs: %q", warns)
	}
}

func TestAliasCombinationsStillResolve(t *testing.T) {
	// Headless root: no token grants project trust, so strict plus explicit trust
	// (today: trusted posture, TrustProject) must keep resolving as it does today.
	posture, mode, ac := k8sPair(t, "--posture", "strict", "--trust-project")
	if posture != app.PostureTrusted || mode != session.ModeDefault || !ac.TrustProject {
		t.Fatalf("--posture strict --trust-project = (%s, %s, trust=%v), want (trusted, default, trust=true)", posture, mode, ac.TrustProject)
	}
	posture, _, ac = k8sPair(t, "--posture", "yolo", "--trust-project")
	if posture != app.PostureYolo || !ac.TrustProject {
		t.Fatalf("--posture yolo --trust-project = (%s, trust=%v), want (yolo, trust=true)", posture, ac.TrustProject)
	}
	posture, _, ac = k8sPair(t, "--permission-mode", "auto", "--trust-project")
	if posture != app.PostureAuto || !ac.TrustProject {
		t.Fatalf("--permission-mode auto --trust-project = (%s, trust=%v), want (auto, trust=true)", posture, ac.TrustProject)
	}
}

func TestDefaultsReproduceCurrentBehaviour(t *testing.T) {
	posture, mode, ac := k8sPair(t)
	if posture != app.PostureAuto || mode != session.ModeDefault {
		t.Fatalf("default mecak8s = (%s, %s), want (auto, default)", posture, mode)
	}
	// The auto default stays a non-explicit --posture default, so operator YAML
	// can still override it exactly as today.
	if ac.Posture != app.PostureAuto || ac.PostureFlagSet || ac.PermissionModeFlagSet || ac.DefaultSessionMode != "" {
		t.Fatalf("default app.Config posture=%s postureSet=%v modeSet=%v default=%q, want auto/false/false/empty",
			ac.Posture, ac.PostureFlagSet, ac.PermissionModeFlagSet, ac.DefaultSessionMode)
	}
	if !ac.Headless || ac.Interactive || ac.TrustProject {
		t.Fatalf("default mecak8s headless=%v interactive=%v trust=%v, want true/false/false", ac.Headless, ac.Interactive, ac.TrustProject)
	}
}

func TestBareMecak8sRefusesWithoutCheckerChoice(t *testing.T) {
	// The bare default is auto, which requires an enforcing checker: no checker
	// and the explicit kill-switch both refuse; unchecked autonomy is yolo.
	build := func(t *testing.T, args ...string) error {
		t.Helper()
		cfg, err := parseFlags(args)
		if err != nil {
			t.Fatalf("parseFlags %v: %v", args, err)
		}
		ac := appConfig(cfg, port.NopDiagnostics{}, observability{})
		ac.RedisURL = ""
		ac.SchedulerEnabled = false
		ac.SessionLeaseK8sNamespace = ""
		ac.Workspace = t.TempDir()
		ac.MockProvider = mockllm.New()
		built, err := buildIsolated(t, context.Background(), ac)
		if err == nil {
			built.Close()
		}
		return err
	}
	for _, args := range [][]string{nil, {"--guardrails=off"}} {
		if err := build(t, args...); err == nil || !strings.Contains(err.Error(), "requires an enforcing guardrails checker") {
			t.Fatalf("mecak8s %v: err = %v, want the enforcing-checker refusal", args, err)
		}
	}
	if err := build(t, "--permission-mode=yolo", "--guardrails=off"); err != nil {
		t.Fatalf("mecak8s --permission-mode=yolo --guardrails=off must start: %v", err)
	}
}

// TestChartAutoInheritsMountedSettingsChecker covers the chart's default argv
// shape (--permission-mode=auto with no guardrails flag, because guardrails.model
// and guardrails.disabled are unset) composed with mounted operator settings:
// a checker configured there lets auto start, none refuses, and the explicit
// kill-switch (guardrails.disabled) wins over the settings checker.
func TestChartAutoInheritsMountedSettingsChecker(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte("guardrails:\n  model: mock-checker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	slot := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(slot, []byte("models:\n  slots:\n    guardrail: mock-checker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	build := func(argv ...string) error {
		t.Helper()
		cfg, err := parseFlags(append([]string{"--mock", "--headless=true", "--permission-mode=auto"}, argv...))
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		ac := appConfig(cfg, port.NopDiagnostics{}, observability{})
		ac.RedisURL = ""
		ac.SchedulerEnabled = false
		ac.SessionLeaseK8sNamespace = ""
		ac.Workspace = t.TempDir()
		built, err := buildIsolated(t, context.Background(), ac)
		if err == nil {
			built.Close()
		}
		return err
	}
	for name, path := range map[string]string{"guardrails model": settings, "guardrail slot": slot} {
		if err := build("--permission-config=" + path); err != nil {
			t.Fatalf("auto with a mounted %s checker must start: %v", name, err)
		}
	}
	for name, argv := range map[string][]string{
		"no inherited checker": nil,
		"explicit kill-switch": {"--guardrails=off", "--permission-config=" + settings},
	} {
		if err := build(argv...); err == nil || !strings.Contains(err.Error(), "requires an enforcing guardrails checker") {
			t.Fatalf("auto with %s err = %v, want the enforcing-checker refusal", name, err)
		}
	}
}

func TestPermissionModeFlagConflictsAndUnknownTokens(t *testing.T) {
	_, err := parseFlags([]string{"--permission-mode", "auto", "--posture", "auto"})
	if err == nil || !strings.Contains(err.Error(), "pass only --permission-mode") {
		t.Fatalf("--permission-mode with --posture err = %v, want a pass-one startup error", err)
	}
	_, err = parseFlags([]string{"--permission-mode", "bogus"})
	if err == nil {
		t.Fatal("unknown --permission-mode token parsed without error")
	}
	for _, name := range app.PermissionModeNames() {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("unknown-token error %q does not name %q", err, name)
		}
	}
	for _, want := range append(app.PermissionModeNames(), "process-wide", "new sessions") {
		if !strings.Contains(permissionModeHelp, want) {
			t.Fatalf("--permission-mode help lacks %q", want)
		}
	}
}
