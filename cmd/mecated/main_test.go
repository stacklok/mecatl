package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/trace"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpperf"
	"github.com/stacklok/mecatl/internal/adapter/skills"
	"github.com/stacklok/mecatl/internal/adapter/telemetry"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/internal/testutil/codextest"
)

func TestLogListenerPostureClassifiesCallerAuthentication(t *testing.T) {
	ordinaryTLS := &tls.Config{MinVersion: tls.VersionTLS12}
	for _, tc := range []struct {
		name     string
		cfg      config
		tls      *tls.Config
		wantWarn bool
	}{
		{name: "TLS only warns", tls: ordinaryTLS, wantWarn: true},
		{name: "static bearer authenticates", cfg: config{authToken: "token"}, tls: ordinaryTLS},
		{name: "OIDC authenticates", cfg: config{oidc: cliconfig.OIDCConfig{Issuer: "https://issuer.example"}}, tls: ordinaryTLS},
		{name: "verified mTLS authenticates", tls: &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			tc.cfg.grpcAddr = "100.64.0.10:9080"
			logListenerPosture(tc.cfg, callerAuthenticationConfigured(tc.cfg, tc.tls))
			got := logs()
			warned := strings.Contains(got, "NO caller authentication")
			if warned != tc.wantWarn {
				t.Fatalf("warning=%v, want %v; logs: %s", warned, tc.wantWarn, got)
			}
			if tc.wantWarn && !strings.Contains(got, "TLS alone is not caller authentication") {
				t.Fatalf("TLS-only posture omitted warning rationale: %s", got)
			}
			if !tc.wantWarn && !strings.Contains(got, "WITH caller authentication") {
				t.Fatalf("authenticated posture not reported: %s", got)
			}
		})
	}
}

func TestOpenAICodexCommandRootReusesResolvedSnapshot(t *testing.T) {
	for _, envName := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_API_KEY"} {
		t.Setenv(envName, "")
	}
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	token := codextest.Token(expires, "acct-mecated")
	path := filepath.Join(t.TempDir(), "auth.yaml")
	body := fmt.Sprintf("providers:\n  openai-codex:\n    oauth:\n      access_token: %s\n      account_id: acct-mecated\n      expires_at: %s\n", token, expires.Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseFlags([]string{"--auth-file", path})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	want := cfg.providerCredentials.OpenAICodex
	for range 2 {
		got := appConfig(cfg, nil, nil, nil, nil, nil)
		if got.ServerImplementation != mecatedServerImplementation {
			t.Fatalf("ServerImplementation = %q, want %q", got.ServerImplementation, mecatedServerImplementation)
		}
		profile, _, err := got.ProviderCredentialLoader.Load(nil)
		if err != nil || profile.OpenAICodexCredential != want || profile.OpenAICodexCredential.Validate(time.Now()) != nil {
			t.Fatal("mecated provider credential loader omitted or re-resolved the parsed credential")
		}
	}
}

// seedQuarantine writes a model-drafted-looking SKILL.md (with origin: model
// provenance) into <quarantine>/<name>/SKILL.md so the promote CLI has something
// to review and move.
func seedQuarantine(t *testing.T, quarantine, name, body string) {
	t.Helper()
	dir := filepath.Join(quarantine, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: \"a seeded candidate\"\norigin: model\ndrafted_at: 2026-01-01T00:00:00Z\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, skills.SkillFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunSkillsPromote(t *testing.T) {
	t.Run("missing flags is a usage error", func(t *testing.T) {
		err := runSkillsPromote([]string{}, strings.NewReader(""), io.Discard)
		if err == nil {
			t.Fatal("expected a usage error with no name/dirs")
		}
	})

	t.Run("--yes promotes a valid candidate", func(t *testing.T) {
		base := t.TempDir()
		quarantine := filepath.Join(base, "quarantine")
		active := filepath.Join(base, "active")
		seedQuarantine(t, quarantine, "deploy-thing", "1. do it\nDone when: done.")

		err := runSkillsPromote(
			[]string{"--skills-draft-dir", quarantine, "--skills-dir", active, "--yes", "deploy-thing"},
			strings.NewReader(""), io.Discard)
		if err != nil {
			t.Fatalf("promote: %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(active, "deploy-thing", skills.SkillFileName)); statErr != nil {
			t.Fatalf("promoted skill not present under active: %v", statErr)
		}
		if _, statErr := os.Stat(filepath.Join(quarantine, "deploy-thing")); !os.IsNotExist(statErr) {
			t.Fatal("candidate should have been moved out of quarantine")
		}
	})

	t.Run("interactive 'n' aborts the promotion", func(t *testing.T) {
		base := t.TempDir()
		quarantine := filepath.Join(base, "quarantine")
		active := filepath.Join(base, "active")
		seedQuarantine(t, quarantine, "risky", "1. step\nDone when: ok.")

		err := runSkillsPromote(
			[]string{"--skills-draft-dir", quarantine, "--skills-dir", active, "risky"},
			strings.NewReader("n\n"), io.Discard)
		if err == nil {
			t.Fatal("expected an abort error when the operator declines")
		}
		if _, statErr := os.Stat(filepath.Join(active, "risky")); !os.IsNotExist(statErr) {
			t.Fatal("a declined candidate must not be promoted")
		}
	})
}

// TestParseFlagsSchedulerMinIntervalDefault pins the cadence-floor security
// default (ADR 0073, the panel-review repair): --scheduler-min-interval
// defaults to 1m (NOT 0/off), so an on-by-default scheduler + the floor-Allow
// Schedule tool cannot mint an unbounded tight-cadence recurring fire out of
// the box. An operator can still set it explicitly (tighter, or 0 to disable).
func TestLegacyUserModelReviewFlagsMapToAppConfig(t *testing.T) {
	parsed, err := parseFlags([]string{"--user-model-review", "--user-model-review-interval=7"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	got := appConfig(parsed, nil, nil, nil, nil, nil)
	if !got.UserModelReview || got.UserModelReviewInterval != 7 {
		t.Fatalf("legacy learning wiring = enabled:%t interval:%d, want true/7", got.UserModelReview, got.UserModelReviewInterval)
	}
}

func TestParseFlagsSchedulerMinIntervalDefault(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if def.schedulerMinInterval != time.Minute {
		t.Errorf("--scheduler-min-interval default = %v, want 1m (the bounded-by-default cadence floor); 0/off would let a model mint an unbounded tight-cadence recurring fire", def.schedulerMinInterval)
	}
	// An explicit override wins (tighter).
	tight, err := parseFlags([]string{"--scheduler-min-interval=5s"})
	if err != nil {
		t.Fatalf("parseFlags --scheduler-min-interval=5s: %v", err)
	}
	if tight.schedulerMinInterval != 5*time.Second {
		t.Errorf("--scheduler-min-interval=5s = %v, want 5s (an explicit operator override wins)", tight.schedulerMinInterval)
	}
	// An explicit 0 disables the floor.
	off, err := parseFlags([]string{"--scheduler-min-interval=0"})
	if err != nil {
		t.Fatalf("parseFlags --scheduler-min-interval=0: %v", err)
	}
	if off.schedulerMinInterval != 0 {
		t.Errorf("--scheduler-min-interval=0 = %v, want 0 (the floor explicitly disabled)", off.schedulerMinInterval)
	}
}

func TestParseFlagsOIDCMaxJWKSStaleness(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if def.oidc.MaxJWKSStaleness != time.Hour {
		t.Fatalf("default --oidc-max-jwks-staleness = %v, want 1h", def.oidc.MaxJWKSStaleness)
	}
	override, err := parseFlags([]string{"--oidc-max-jwks-staleness=0"})
	if err != nil {
		t.Fatalf("parseFlags override: %v", err)
	}
	if override.oidc.MaxJWKSStaleness != 0 {
		t.Fatalf("--oidc-max-jwks-staleness=0 = %v, want disabled", override.oidc.MaxJWKSStaleness)
	}
}

// TestParseFlagsSubagentModelRouter asserts the ADR 0042 kill-switch parses:
// unset → not set (router governed by taxonomy); a bare flag / =true still PARSES and is
// a harmless no-op (router stays governed by taxonomy); =false maps to RouterDisabled via
// appConfig.
func TestParseFlagsSubagentModelRouter(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if def.subagentModelRouterSet {
		t.Error("subagentModelRouterSet default = true, want false (flag not given → router governed by taxonomy)")
	}

	// A bare invocation must still PARSE without error.
	bare, err := parseFlags([]string{"--subagent-model-router"})
	if err != nil {
		t.Fatalf("bare --subagent-model-router must parse: %v", err)
	}
	if !bare.subagentModelRouterSet || !bare.subagentModelRouter {
		t.Error("--subagent-model-router (bare) must record set=true value=true")
	}
	// A bare flag / =true is a no-op: it must NOT disable the router (taxonomy governs).
	if cfg := appConfig(bare, nil, nil, nil, nil, nil); cfg.RouterDisabled {
		t.Error("a bare --subagent-model-router must leave RouterDisabled false")
	}

	// =false is the kill-switch: it must map to RouterDisabled in app.Config.
	off, err := parseFlags([]string{"--subagent-model-router=false"})
	if err != nil {
		t.Fatalf("parseFlags --subagent-model-router=false: %v", err)
	}
	if !off.subagentModelRouterSet || off.subagentModelRouter {
		t.Error("--subagent-model-router=false must record set=true value=false")
	}
	if cfg := appConfig(off, nil, nil, nil, nil, nil); !cfg.RouterDisabled {
		t.Error("--subagent-model-router=false must set RouterDisabled (the kill-switch)")
	}
	// Unset → RouterDisabled false (router governed by taxonomy presence).
	if cfg := appConfig(def, nil, nil, nil, nil, nil); cfg.RouterDisabled {
		t.Error("an unset --subagent-model-router must leave RouterDisabled false")
	}
}

func TestParseFlagsSkillsDraft(t *testing.T) {
	cfg, err := parseFlags([]string{"--skills-draft-dir", "/tmp/q"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.skillsDraftDir != "/tmp/q" {
		t.Errorf("skillsDraftDir = %q", cfg.skillsDraftDir)
	}
	if cfg.skillsDraftThreshold != skills.DefaultSimilarityThreshold {
		t.Errorf("default threshold = %v, want %v", cfg.skillsDraftThreshold, skills.DefaultSimilarityThreshold)
	}
}

// TestParseFlagsAgentDefs asserts the Tier 1b agent-definition flags parse into the
// config: repeatable --agents-dir, the conventional toggle (default ON), the global
// --subagent-model, and repeatable key=value --model-alias.
func TestParseFlagsAgentDefs(t *testing.T) {
	// Defaults: conventional discovery ON (inert when absent), no explicit dirs.
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if !def.agentsConventional {
		t.Errorf("agentsConventional default = false, want true (on-but-inert)")
	}
	if len(def.agentsDirs) != 0 {
		t.Errorf("agentsDirs default = %v, want empty", def.agentsDirs)
	}

	cfg, err := parseFlags([]string{
		"--agents-dir", "/a/one",
		"--agents-dir", "/a/two",
		"--agents-conventional=false",
		"--subagent-model", "cheap-id",
		"--model-alias", "fast=gpt-4o-mini",
		"--model-alias", "smart=gpt-5",
		"--model-slot", "compaction=cheap",
		"--model-slot", "guardrail=fast",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got := []string(cfg.agentsDirs); len(got) != 2 || got[0] != "/a/one" || got[1] != "/a/two" {
		t.Errorf("agentsDirs = %v, want [/a/one /a/two]", got)
	}
	if cfg.agentsConventional {
		t.Errorf("agentsConventional = true, want false (explicitly disabled)")
	}
	if cfg.subagentModel != "cheap-id" {
		t.Errorf("subagentModel = %q, want cheap-id", cfg.subagentModel)
	}
	if got := cfg.modelAliases.AsMap(); got["fast"] != "gpt-4o-mini" || got["smart"] != "gpt-5" {
		t.Errorf("modelAliases = %v, want fast=gpt-4o-mini smart=gpt-5", got)
	}
	if got := cfg.modelSlots.AsMap(); got["compaction"] != "cheap" || got["guardrail"] != "fast" {
		t.Errorf("modelSlots = %v, want compaction=cheap guardrail=fast", got)
	}

	// A malformed alias (no '=') is a parse error.
	if _, err := parseFlags([]string{"--model-alias", "bogus"}); err == nil {
		t.Error("parseFlags(--model-alias bogus) should error on a missing '='")
	}
	// A malformed slot (no '=') is a parse error too (same cliconfig.KeyValueList grammar).
	if _, err := parseFlags([]string{"--model-slot", "bogus"}); err == nil {
		t.Error("parseFlags(--model-slot bogus) should error on a missing '='")
	}
}

// TestParseFlagsPermissionConfig asserts the issue #13 permission-config flags
// parse into the config: --permissions-conventional defaults ON, --trust-project /
// --import-claude-permissions default OFF, and --permission-config is repeatable.
func TestParseFlagsPermissionConfig(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if !def.permissionsConventional {
		t.Errorf("permissionsConventional default = false, want true (auto-discover ON)")
	}
	if def.trustProject {
		t.Errorf("trustProject default = true, want false (safe stance)")
	}
	if def.importClaudePermissions {
		t.Errorf("importClaudePermissions default = true, want false")
	}

	cfg, err := parseFlags([]string{
		"--permission-config", "/etc/a.yaml",
		"--permission-config", "/etc/b.yaml",
		"--permissions-conventional=false",
		"--import-claude-permissions",
		"--trust-project",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got := []string(cfg.permissionConfigs); len(got) != 2 || got[0] != "/etc/a.yaml" || got[1] != "/etc/b.yaml" {
		t.Errorf("permissionConfigs = %v, want [/etc/a.yaml /etc/b.yaml]", got)
	}
	if cfg.permissionsConventional {
		t.Errorf("permissionsConventional = true, want false (explicitly disabled)")
	}
	if !cfg.importClaudePermissions {
		t.Errorf("importClaudePermissions = false, want true")
	}
	if !cfg.trustProject {
		t.Errorf("trustProject = false, want true")
	}
}

// TestAppConfigMapsPermissionConfig asserts appConfig threads the 4 permission-
// config fields onto the shared app.Config.
func TestAppConfigMapsPermissionConfig(t *testing.T) {
	cfg, err := parseFlags([]string{
		"--permission-config", "/etc/a.yaml",
		"--import-claude-permissions",
		"--trust-project",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	ac := appConfig(cfg, nil, nil, nil, nil, nil)
	if !ac.PermissionsConventional {
		t.Errorf("PermissionsConventional = false, want true (default)")
	}
	if !ac.ImportClaudePermissions {
		t.Errorf("ImportClaudePermissions = false, want true")
	}
	if !ac.TrustProject {
		t.Errorf("TrustProject = false, want true")
	}
	if len(ac.PermissionConfigs) != 1 || ac.PermissionConfigs[0] != "/etc/a.yaml" {
		t.Errorf("PermissionConfigs = %v, want [/etc/a.yaml]", ac.PermissionConfigs)
	}
}

// TestParseFlagsServerDefaultModel asserts the issue-#21 deployment-default
// flags parse into the config (empty by default — the zero-config posture) and
// appConfig threads them onto the shared app.Config Default* fields.
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
	if cfg.defaultProvider != "openrouter" {
		t.Errorf("defaultProvider = %q, want openrouter", cfg.defaultProvider)
	}
	if cfg.defaultModel != "openai/gpt-5-mini" {
		t.Errorf("defaultModel = %q, want openai/gpt-5-mini", cfg.defaultModel)
	}
	ac := appConfig(cfg, nil, nil, nil, nil, nil)
	if ac.DefaultProvider != "openrouter" || ac.DefaultModel != "openai/gpt-5-mini" {
		t.Errorf("app.Config Default* = (%q, %q), want the parsed flags threaded through", ac.DefaultProvider, ac.DefaultModel)
	}
}

// TestAppConfigMapsAgentDefs asserts appConfig threads the agent-def fields onto the
// shared app.Config.
func TestAppConfigMapsAgentDefs(t *testing.T) {
	cfg, err := parseFlags([]string{
		"--agents-dir", "/x",
		"--subagent-model", "sub",
		"--model-alias", "fast=cheap",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	ac := appConfig(cfg, nil, nil, nil, nil, nil)
	if len(ac.AgentsDirs) != 1 || ac.AgentsDirs[0] != "/x" {
		t.Errorf("AgentsDirs = %v", ac.AgentsDirs)
	}
	if !ac.AgentsConventional {
		t.Errorf("AgentsConventional = false, want true (default)")
	}
	if ac.SubagentModel != "sub" {
		t.Errorf("SubagentModel = %q", ac.SubagentModel)
	}
	if ac.ModelAliases["fast"] != "cheap" {
		t.Errorf("ModelAliases = %v", ac.ModelAliases)
	}
}

// TestParseFlagsAndAppConfigMapsAskReviewer asserts the issue-#31 headless ask
// reviewer flags parse (off by default, breaker default 3), the policy FILE is
// read into a string by parseFlags (the composition layer never touches os),
// and appConfig threads all three onto the shared app.Config.
func TestParseFlagsAndAppConfigMapsAskReviewer(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if def.subagentAskReviewer != "" {
		t.Errorf("subagentAskReviewer default = %q, want empty (reviewer off)", def.subagentAskReviewer)
	}
	if def.subagentAskReviewerMaxDenies != 3 {
		t.Errorf("subagentAskReviewerMaxDenies default = %d, want 3", def.subagentAskReviewerMaxDenies)
	}

	policyFile := filepath.Join(t.TempDir(), "rubric.txt")
	if werr := os.WriteFile(policyFile, []byte("ALLOW read-only only."), 0o600); werr != nil {
		t.Fatalf("write rubric: %v", werr)
	}
	cfg, err := parseFlags([]string{
		"--subagent-ask-reviewer", "gpt-5-mini",
		"--subagent-ask-reviewer-max-denies", "5",
		"--subagent-ask-reviewer-policy", policyFile,
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.subagentAskReviewerPolicy != "ALLOW read-only only." {
		t.Errorf("policy content = %q, want the file's content", cfg.subagentAskReviewerPolicy)
	}
	ac := appConfig(cfg, nil, nil, nil, nil, nil)
	if ac.SubagentAskReviewerModel != "gpt-5-mini" {
		t.Errorf("SubagentAskReviewerModel = %q", ac.SubagentAskReviewerModel)
	}
	if ac.SubagentAskReviewerMaxDenies != 5 {
		t.Errorf("SubagentAskReviewerMaxDenies = %d", ac.SubagentAskReviewerMaxDenies)
	}
	if ac.SubagentAskReviewerPolicy != "ALLOW read-only only." {
		t.Errorf("SubagentAskReviewerPolicy = %q", ac.SubagentAskReviewerPolicy)
	}

	// An unreadable policy file FAILS startup (loud-misconfig).
	if _, err := parseFlags([]string{"--subagent-ask-reviewer-policy", filepath.Join(t.TempDir(), "absent.txt")}); err == nil {
		t.Errorf("an unreadable --subagent-ask-reviewer-policy must fail parseFlags")
	}
}

// TestHeadlessFlagDrivesInteractive is the reachability fix (finding 0): mecated
// is interactive by default (asks surface to the client), and --headless makes
// it non-interactive (app.Config.Interactive=false) so a child's unresolved ask
// engages the auto-deny / --subagent-ask-reviewer path instead of surfacing to a
// client that would never answer it.
func TestHeadlessFlagDrivesInteractive(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if def.headless {
		t.Errorf("headless default = true, want false")
	}
	if ac := appConfig(def, nil, nil, nil, nil, nil); !ac.Interactive || ac.Headless {
		t.Errorf("default mecated must map Interactive=true, Headless=false")
	}
	on, err := parseFlags([]string{"--headless"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if ac := appConfig(on, nil, nil, nil, nil, nil); ac.Interactive || !ac.Headless {
		t.Errorf("--headless must map Interactive=false, Headless=true")
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

	cfg, err := parseFlags([]string{"--yolo"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !cfg.allowAllTools {
		t.Errorf("allowAllTools = false, want true (flag set)")
	}
}

func TestAppConfigMapsAllowAll(t *testing.T) {
	cfg, err := parseFlags([]string{"--yolo"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if ac := appConfig(cfg, nil, nil, nil, nil, nil); !ac.AllowAllTools {
		t.Errorf("appConfig.AllowAllTools = false, want true")
	}

	off, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if ac := appConfig(off, nil, nil, nil, nil, nil); ac.AllowAllTools {
		t.Errorf("appConfig.AllowAllTools = true with flag off, want false")
	}
}

// TestAppConfigMapsMaxTeamTokens pins the --max-team-tokens flag → appConfig.MaxTeamTokens
// mapping (the team-aggregate token budget), mirroring TestAppConfigMapsAllowAll.
func TestAppConfigMapsMaxTeamTokens(t *testing.T) {
	cfg, err := parseFlags([]string{"--max-team-tokens", "12345"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if ac := appConfig(cfg, nil, nil, nil, nil, nil); ac.MaxTeamTokens != 12345 {
		t.Errorf("appConfig.MaxTeamTokens = %d, want 12345", ac.MaxTeamTokens)
	}

	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if ac := appConfig(def, nil, nil, nil, nil, nil); ac.MaxTeamTokens != 0 {
		t.Errorf("appConfig.MaxTeamTokens = %d with flag absent, want 0 (disabled by default)", ac.MaxTeamTokens)
	}
}

// TestNewAdminMuxServesIntrospectionEndpoints drives the REAL telemetry.NewAdminMux
// helper (the one serve mounts) and asserts each runtime-introspection endpoint
// serves a non-trivial body. Building the mux through the production helper —
// rather than a parallel hand-rolled mux — means deleting a mux.Handle in
// telemetry.NewAdminMux would fail this test.
func TestNewAdminMuxServesIntrospectionEndpoints(t *testing.T) {
	reg := prometheus.NewRegistry()
	// Seed one series so /metrics renders a non-empty exposition body (an empty
	// registry would otherwise produce an empty body and mask whether the handler
	// is even mounted).
	reg.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "mecatl_admin_mux_test_seed",
		Help: "Test seed series so /metrics is non-empty.",
	}))
	recorder := telemetry.NewFlightRecorder(trace.FlightRecorderConfig{})
	if err := recorder.Start(); err != nil {
		t.Fatalf("flight recorder Start: %v", err)
	}
	defer recorder.Stop()

	srv := httptest.NewServer(telemetry.NewAdminMux(reg, recorder))
	defer srv.Close()

	cases := []struct {
		path       string
		wantSubstr string // a marker that must appear in the body (empty = any non-empty body)
	}{
		{"/metrics", "mecatl_admin_mux_test_seed"}, // the seeded series proves the exporter is wired
		{"/debug/pprof/", "Types of profiles"},     // the pprof index page
		{"/debug/vars", "mecatl_runtime"},          // the curated expvar key
		{"/debug/flightrecorder", ""},              // a binary trace snapshot
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tc.path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200", tc.path, resp.StatusCode)
			}
			body := readBody(t, resp)
			if len(body) == 0 {
				t.Fatalf("GET %s returned an empty body", tc.path)
			}
			if tc.wantSubstr != "" && !strings.Contains(string(body), tc.wantSubstr) {
				t.Errorf("GET %s body missing %q; got %.160q", tc.path, tc.wantSubstr, body)
			}
		})
	}
}

// TestNewAdminMuxOmitsFlightRecorderWhenDisabled asserts the recorder==nil
// (FlightRecorder disabled) branch: /debug/flightrecorder is ABSENT (404) while
// the always-on endpoints still serve.
func TestNewAdminMuxOmitsFlightRecorderWhenDisabled(t *testing.T) {
	srv := httptest.NewServer(telemetry.NewAdminMux(prometheus.NewRegistry(), nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/debug/flightrecorder")
	if err != nil {
		t.Fatalf("GET /debug/flightrecorder: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /debug/flightrecorder status = %d, want 404 when the recorder is disabled", resp.StatusCode)
	}

	// An always-on endpoint must still be mounted.
	varsResp, err := http.Get(srv.URL + "/debug/vars")
	if err != nil {
		t.Fatalf("GET /debug/vars: %v", err)
	}
	defer func() { _ = varsResp.Body.Close() }()
	if varsResp.StatusCode != http.StatusOK {
		t.Errorf("GET /debug/vars status = %d, want 200 even with the recorder disabled", varsResp.StatusCode)
	}
}

// TestParseFlagsRuntimeIntrospection asserts the runtime-introspection flags
// parse: --flight-recorder defaults ON and --flight-recorder=false flips it, and
// --mutex-profile-fraction / --block-profile-rate parse into the config.
func TestParseFlagsRuntimeIntrospection(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if !def.flightRecorder {
		t.Errorf("flightRecorder default = false, want true (ON by default)")
	}
	if def.mutexProfileFraction != 0 {
		t.Errorf("mutexProfileFraction default = %d, want 0 (off)", def.mutexProfileFraction)
	}
	if def.blockProfileRate != 0 {
		t.Errorf("blockProfileRate default = %d, want 0 (off)", def.blockProfileRate)
	}

	cfg, err := parseFlags([]string{
		"--flight-recorder=false",
		"--mutex-profile-fraction=5",
		"--block-profile-rate=1000",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.flightRecorder {
		t.Errorf("flightRecorder = true, want false (--flight-recorder=false)")
	}
	if cfg.mutexProfileFraction != 5 {
		t.Errorf("mutexProfileFraction = %d, want 5", cfg.mutexProfileFraction)
	}
	if cfg.blockProfileRate != 1000 {
		t.Errorf("blockProfileRate = %d, want 1000", cfg.blockProfileRate)
	}
}

// readBody reads an entire response body, failing the test on error.
func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// TestPostureRefusalReason proves the generalised root-refusal (the exported
// app.PostureRefusalReason) gates auto AND yolo (both waive the mutate-ask floor) while
// strict/trusted are NEVER refused, and only when the process is PRIVILEGED. It would
// fail if the gate regressed to the historical yolo-only check.
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

// TestParseFlagsPosture covers the --posture flag surface: the value lands on
// cfg.posture and postureFlagSet flips ONLY when --posture is explicitly passed (so
// CLI can out-rank the operator-YAML key).
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

// TestAppConfigPostureMapping pins the cmd→app.Config posture passthrough: the parsed
// --posture token maps to app.Posture, postureFlagSet rides through, and Privileged is
// set (here false, since the test process is not root). It would fail if a field were
// dropped from appConfig's posture mapping.
func TestAppConfigPostureMapping(t *testing.T) {
	cfg, err := parseFlags([]string{"-posture", "yolo"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	ac := appConfig(cfg, nil, nil, nil, nil, nil)
	if ac.Posture != app.PostureYolo {
		t.Errorf("appConfig.Posture = %v, want PostureYolo", ac.Posture)
	}
	if !ac.PostureFlagSet {
		t.Errorf("appConfig.PostureFlagSet = false, want true (CLI must out-rank YAML)")
	}
	// privilegedProcess() is false in the test runner (non-root) — assert it threads.
	if ac.Privileged != privilegedProcess() {
		t.Errorf("appConfig.Privileged = %v, want %v (privilegedProcess())", ac.Privileged, privilegedProcess())
	}
}

// TestParseFlagsPerfMCP asserts --perf-mcp defaults OFF and parses ON. The
// effective-value cross-validation (empty / non-loopback --metrics-addr) moved
// to validateEffectiveConfig (review fix #1) so a file-supplied metrics_addr
// cannot bypass it; see TestValidateEffectiveConfigPerfMCP.
func TestParseFlagsPerfMCP(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if def.perfMCP {
		t.Errorf("perfMCP default = true, want false (OFF by default)")
	}

	on, err := parseFlags([]string{"--perf-mcp"})
	if err != nil {
		t.Fatalf("parseFlags(--perf-mcp): %v", err)
	}
	if !on.perfMCP {
		t.Errorf("perfMCP = false, want true (--perf-mcp)")
	}
}

// TestValidateEffectiveConfigPerfMCP is the mecated-side fail-closed proof
// (mirroring embed's TestStartPerfMCPRefusesNonLoopback): --perf-mcp on a
// non-loopback or empty --metrics-addr is rejected as a fatal CONFIG error in
// validateEffectiveConfig — the PURE helper run AFTER the daemon config merge and
// BEFORE serve() binds any listener. Because the refusal lives in the post-merge
// validation, proving the address never reaches serve() is exactly proving no
// listener is bound. A loopback address with --perf-mcp must still validate.
func TestValidateEffectiveConfigPerfMCP(t *testing.T) {
	// Empty --metrics-addr with --perf-mcp is a fatal error.
	empty, err := parseFlags([]string{"--perf-mcp", "--metrics-addr", ""})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if verr := validateEffectiveConfig(empty); verr == nil {
		t.Error("validateEffectiveConfig(--perf-mcp with empty --metrics-addr) should be a fatal config error")
	}

	nonLoopback := []string{"0.0.0.0:9090", "192.168.1.10:9090", "example.com:9090"}
	for _, addr := range nonLoopback {
		cfg, err := parseFlags([]string{"--perf-mcp", "--metrics-addr", addr})
		if err != nil {
			t.Fatalf("parseFlags(--metrics-addr %s): %v", addr, err)
		}
		if verr := validateEffectiveConfig(cfg); verr == nil {
			t.Errorf("validateEffectiveConfig(--perf-mcp --metrics-addr %s) = nil error, want a non-loopback refusal (fail-closed, no listener bound)", addr)
			continue
		}
		if !strings.Contains(validateEffectiveConfig(cfg).Error(), "non-loopback") {
			t.Errorf("validateEffectiveConfig(--perf-mcp --metrics-addr %s) error should mention non-loopback", addr)
		}
	}

	// A loopback address with --perf-mcp still validates cleanly (the gate is targeted).
	for _, addr := range []string{"127.0.0.1:9090", "[::1]:9090", "localhost:9090"} {
		cfg, err := parseFlags([]string{"--perf-mcp", "--metrics-addr", addr})
		if err != nil {
			t.Fatalf("parseFlags(--metrics-addr %s): %v", addr, err)
		}
		if verr := validateEffectiveConfig(cfg); verr != nil {
			t.Errorf("validateEffectiveConfig(--perf-mcp --metrics-addr %s) = %v, want loopback to pass", addr, verr)
		}
	}
}

// TestIsLoopbackHostPort asserts the fail-closed loopback gate: loopback hosts
// (127.x, ::1, localhost) pass; non-loopback / unparseable hosts do NOT (so a
// misconfigured bind refuses the unauthenticated MCP surface).
func TestIsLoopbackHostPort(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:9090", true},
		{"127.0.0.2:9090", true},
		{"[::1]:9090", true},
		{"localhost:9090", true},
		{"0.0.0.0:9090", false},
		{"192.168.1.10:9090", false},
		{"example.com:9090", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isLoopbackHostPort(tt.addr); got != tt.want {
			t.Errorf("isLoopbackHostPort(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}

// TestSlowTurnSourceBridge asserts the cmd-boundary bridge maps the telemetry
// buffer to mcpperf.SlowTurnSource (and that a nil buffer yields a nil source so
// list_slow_turns reports "history not enabled").
func TestSlowTurnSourceBridge(t *testing.T) {
	if src := slowTurnSource(nil); src != nil {
		t.Errorf("slowTurnSource(nil) = %v, want nil source", src)
	}

	buf := telemetry.NewSlowTurnBuffer(8, func() time.Time { return time.Unix(0, 0) })
	buf.Emit(context.Background(), session.Event{
		Type:    session.EvTurnEnd,
		Turn:    7,
		TurnEnd: &session.TurnEndPayload{DurationMs: 1234, TTFTMs: 12, InterTokenMaxMs: 34},
	})
	src := slowTurnSource(buf)
	got := src.Recent(0)
	if len(got) != 1 {
		t.Fatalf("bridged Recent returned %d turns, want 1", len(got))
	}
	w := got[0]
	if w.TurnIndex != 7 || w.DurationMs != 1234 || w.TTFTMs != 12 || w.InterTokenMaxMs != 34 {
		t.Errorf("bridge did not copy scalars: %+v", w)
	}
}

// TestPerfMCPPrintConfig asserts the print-config subcommand emits a paste-ready
// .mcp.json with the right url and NO Authorization/headers (decision 6: loopback,
// no auth).
func TestPerfMCPPrintConfig(t *testing.T) {
	var out bytes.Buffer
	if err := runPerfMCPPrintConfig([]string{"--metrics-addr", "127.0.0.1:7777"}, &out); err != nil {
		t.Fatalf("runPerfMCPPrintConfig: %v", err)
	}
	body := out.String()
	if strings.Contains(strings.ToLower(body), "authorization") || strings.Contains(strings.ToLower(body), "headers") {
		t.Errorf("print-config emitted an auth header; decision 6 is loopback/no-auth:\n%s", body)
	}

	var parsed struct {
		McpServers map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("print-config output is not valid JSON: %v\n%s", err, body)
	}
	srv, ok := parsed.McpServers["mecatl-perf"]
	if !ok {
		t.Fatalf("print-config missing the mecatl-perf server entry:\n%s", body)
	}
	if srv.Type != "http" {
		t.Errorf("server type = %q, want http", srv.Type)
	}
	if srv.URL != "http://127.0.0.1:7777/mcp" {
		t.Errorf("server url = %q, want http://127.0.0.1:7777/mcp", srv.URL)
	}

	// Strict key-set: decode into a map and assert the server object's keys are
	// EXACTLY {type,url}. A stray token/env/headers/Authorization field would
	// otherwise slip past the typed struct (which silently drops unknown keys) —
	// this is stronger than the substring grep above (decision 6: loopback, no auth).
	var raw struct {
		McpServers map[string]map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("print-config output is not valid JSON: %v\n%s", err, body)
	}
	rawSrv, ok := raw.McpServers["mecatl-perf"]
	if !ok {
		t.Fatalf("strict decode missing the mecatl-perf server entry:\n%s", body)
	}
	wantKeys := map[string]bool{"type": true, "url": true}
	for k := range rawSrv {
		if !wantKeys[k] {
			t.Errorf("server object carries an unexpected key %q (want exactly {type,url} — decision 6 forbids token/env/headers):\n%s", k, body)
		}
	}
	for k := range wantKeys {
		if _, present := rawSrv[k]; !present {
			t.Errorf("server object missing required key %q:\n%s", k, body)
		}
	}

	// Default --metrics-addr (no flag) uses the loopback :9090 default.
	out.Reset()
	if err := runPerfMCPPrintConfig(nil, &out); err != nil {
		t.Fatalf("runPerfMCPPrintConfig(nil): %v", err)
	}
	if !strings.Contains(out.String(), "http://127.0.0.1:9090/mcp") {
		t.Errorf("default url missing; got:\n%s", out.String())
	}
}

// TestAdminMuxMountsPerfMCP is the e2e mount proof: an admin mux built the way
// serve() builds it (NewAdminMux + a /mcp Handle of mcpperf.Handler) answers an
// MCP initialize at /mcp while /metrics and /debug/pprof still work; and a mux
// WITHOUT the /mcp mount returns 404 there. This mirrors serve()'s wiring without
// standing up the full daemon.
func TestAdminMuxMountsPerfMCP(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "mecatl_perfmcp_mount_seed",
		Help: "seed series so /metrics is non-empty",
	}))

	// --- WITH /mcp mounted ---
	withMux := telemetry.NewAdminMux(reg, nil)
	withMux.Handle("/mcp", mcpperf.Handler(mcpperf.Deps{
		Snapshot:  telemetry.Snapshot,
		Gatherer:  reg,
		Profiler:  mcpperf.NewProfiler(),
		SlowTurns: slowTurnSource(telemetry.NewSlowTurnBuffer(8, nil)),
		Clock:     time.Now,
	}))
	on := httptest.NewServer(withMux)
	defer on.Close()

	// /mcp answers an MCP initialize over the SDK client (proves the handler is
	// mounted and speaks the protocol).
	mc := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "mecated-test", Version: "v1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := mc.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:             on.URL + "/mcp",
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect to mounted /mcp: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// The sibling admin endpoints still serve alongside /mcp.
	for _, p := range []string{"/metrics", "/debug/pprof/"} {
		resp, gerr := http.Get(on.URL + p)
		if gerr != nil {
			t.Fatalf("GET %s: %v", p, gerr)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200 (must coexist with /mcp)", p, resp.StatusCode)
		}
	}

	// --- WITHOUT /mcp mounted: /mcp must be 404 (absent when --perf-mcp is off) ---
	off := httptest.NewServer(telemetry.NewAdminMux(reg, nil))
	defer off.Close()
	resp, gerr := http.Post(off.URL+"/mcp", "application/json", bytes.NewReader([]byte(`{}`)))
	if gerr != nil {
		t.Fatalf("POST /mcp (off): %v", gerr)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /mcp status = %d, want 404 when --perf-mcp is off", resp.StatusCode)
	}
}
