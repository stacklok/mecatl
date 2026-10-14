package main

import (
	"bytes"
	"context"
	"log/slog"
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
	// The checker gate is now a startup WARN, not a refusal.
	// Build succeeds; entering auto mode without a checker is refused at SetMode time.
	var buf bytes.Buffer
	diag := slogdiag.NewFromLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	ac := appConfig(cfg, diag, observability{})
	ac.RedisURL = ""
	ac.SchedulerEnabled = false
	ac.SessionLeaseK8sNamespace = ""
	ac.Workspace = t.TempDir()
	ac.MockProvider = mockllm.New()
	built, err := buildIsolated(t, context.Background(), ac)
	if err != nil {
		t.Fatalf("bare mecak8s must start (checker gate is now a WARN): %v", err)
	}
	built.Close()
	if !strings.Contains(buf.String(), "no guardrails checker") || !strings.Contains(buf.String(), "--guardrails off") {
		t.Errorf("bare mecak8s must WARN naming --guardrails off; log:\n%s", buf.String())
	}
	// With --guardrails off, Build must still succeed (no checker needed).
	cfgOff, err := parseFlags([]string{"--guardrails=off"})
	if err != nil {
		t.Fatalf("parseFlags --guardrails=off: %v", err)
	}
	acOff := appConfig(cfgOff, port.NopDiagnostics{}, observability{})
	acOff.RedisURL = ""
	acOff.SchedulerEnabled = false
	acOff.SessionLeaseK8sNamespace = ""
	acOff.Workspace = t.TempDir()
	acOff.MockProvider = mockllm.New()
	built2, err := buildIsolated(t, context.Background(), acOff)
	if err != nil {
		t.Fatalf("mecak8s with an explicit --guardrails=off: %v", err)
	}
	built2.Close()
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
