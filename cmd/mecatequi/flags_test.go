package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/buildinfo"
	"github.com/stacklok/mecatl/internal/testutil/codextest"
)

func TestVersionInvocationIsExact(t *testing.T) {
	if !buildinfo.IsVersion([]string{"mecatequi", "--version"}) {
		t.Fatal("exact --version was not recognized")
	}
	for _, args := range [][]string{{"--version", "--mock"}, {"-version"}} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("parseFlags(%v) accepted a non-exact version invocation", args)
		}
	}
}

func TestOpenAICodexCommandRootReusesResolvedSnapshot(t *testing.T) {
	for _, envName := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_API_KEY"} {
		t.Setenv(envName, "")
	}
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	token := codextest.Token(expires, "acct-mecatequi")
	path := filepath.Join(t.TempDir(), "auth.yaml")
	body := fmt.Sprintf("providers:\n  openai-codex:\n    oauth:\n      access_token: %s\n      account_id: acct-mecatequi\n      expires_at: %s\n", token, expires.Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	flags, err := parseFlags([]string{"--prompt", "test", "--auth-file", path})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	want := flags.providerCredentials.OpenAICodex
	for range 2 {
		got := appConfig(flags, nil, observability{})
		if got.OpenAICodexCredential != want || got.OpenAICodexCredential.Validate(time.Now()) != nil {
			t.Fatal("mecatequi appConfig omitted or re-resolved the parsed credential")
		}
	}
}

// TestOpenAICodexCommandRootSurfaces pins mecatequi's production noninteractive
// AC8.4 surface. realMain emits the resolved warning before workspace validation,
// so a non-git workspace makes the proof hermetic while still failing if the
// production emitAuthFileWarning call is deleted.
func TestOpenAICodexCommandRootSurfaces(t *testing.T) {
	for _, envName := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_API_KEY"} {
		t.Setenv(envName, "")
	}
	expires := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	token := codextest.Token(expires, "acct-mecatequi-expired")
	path := filepath.Join(t.TempDir(), "auth.yaml")
	body := fmt.Sprintf("providers:\n  openai-codex:\n    oauth:\n      access_token: %s\n      account_id: acct-mecatequi-expired\n      expires_at: %s\n", token, expires.Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := realMain([]string{
		"--prompt", "test",
		"--auth-file", path,
		"--mock",
		"--workspace", t.TempDir(), // deliberately not a git repository
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("realMain(non-git workspace) = %d, want setup failure 2; stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"expired", "auth.yaml", "restart"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("mecatequi warning %q missing %q", stderr.String(), want)
		}
	}
	if strings.Count(stderr.String(), "mecatequi: WARNING:") != 1 || strings.Contains(stderr.String(), token) {
		t.Fatalf("mecatequi warning must be emitted once without the token: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "not a git repository") {
		t.Fatalf("test did not reach the post-warning workspace-validation boundary: %q", stderr.String())
	}
}

func TestOutputEconomyFlagIsUnknownFlag(t *testing.T) {
	// The --output-economy compatibility flag is DELETED (ADR 0041, superseded;
	// clean break): it now fails at flag-parse time with the standard unknown-flag
	// error instead of parsing as a no-op.
	_, err := parseFlags([]string{"--prompt", "hi", "--output-economy", "terse"})
	if err == nil {
		t.Fatal("parseFlags(--output-economy terse) = nil error; want 'flag provided but not defined'")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Errorf("parseFlags(--output-economy terse) err = %q, want 'flag provided but not defined'", err)
	}
}

// TestParseFlagsPromptInputs covers the prompt-input validation matrix: a literal
// prompt, a prompt file, both, neither (error), and an unreadable file (error).
func TestParseFlagsPromptInputs(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(good, []byte("file body\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("literal only", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "hi"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if f.prompt != "hi" || f.promptFileBody != "" {
			t.Errorf("got prompt=%q fileBody=%q", f.prompt, f.promptFileBody)
		}
	})

	t.Run("prompt-file only", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt-file", good})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if f.promptFileBody != "file body\n" {
			t.Errorf("fileBody = %q, want file content", f.promptFileBody)
		}
	})

	t.Run("both", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "lead", "--prompt-file", good})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if f.prompt != "lead" || f.promptFileBody == "" {
			t.Errorf("both sources must be captured; got prompt=%q fileBody=%q", f.prompt, f.promptFileBody)
		}
	})

	t.Run("neither is an error", func(t *testing.T) {
		if _, err := parseFlags(nil); err == nil {
			t.Fatal("parseFlags with no prompt: want error, got nil")
		}
	})

	t.Run("unreadable file is an error", func(t *testing.T) {
		missing := filepath.Join(dir, "does-not-exist.txt")
		if _, err := parseFlags([]string{"--prompt-file", missing}); err == nil {
			t.Fatal("parseFlags with an unreadable --prompt-file: want error, got nil")
		}
	})
}

// TestParseFlagsInstructions covers the cmd-local --instructions knob: it is captured on
// the flags struct, defaults to empty, and is DELIBERATELY not mapped onto app.Config (it
// is a prompt-assembly knob consumed only by buildPrompt).
func TestParseFlagsInstructions(t *testing.T) {
	t.Run("captured on the flags struct", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x", "--instructions", "frame me"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if f.instructions != "frame me" {
			t.Errorf("f.instructions = %q, want %q", f.instructions, "frame me")
		}
	})

	t.Run("defaults to empty", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if f.instructions != "" {
			t.Errorf("f.instructions default = %q, want empty", f.instructions)
		}
	})

	t.Run("does not leak into app.Config", func(t *testing.T) {
		// app.Config has no field carrying the framing text; --instructions is a
		// cmd-local prompt-assembly knob. Assert the parsed value is NOT present in the
		// mapped config by stringifying every field with %+v (app.Config carries func/
		// interface fields, so it is not JSON-marshalable — %+v still renders them all).
		f, err := parseFlags([]string{"--prompt", "x", "--instructions", "DISTINCT_FRAMING_SENTINEL"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		cfg := appConfig(f, newDiagnostics(), observability{})
		blob := fmt.Sprintf("%+v", cfg)
		if strings.Contains(blob, "DISTINCT_FRAMING_SENTINEL") {
			t.Errorf("--instructions leaked into app.Config: %s", blob)
		}
	})
}

// TestParseFlagsMaxTurns covers the --max-turns knob: it is captured on the flags
// struct, defaults to 0 (inherit the deployment default), and is DELIBERATELY not
// mapped onto app.Config — it is a per-SESSION limit threaded to CreateSession via
// run(), not an engine-build knob.
func TestParseFlagsMaxTurns(t *testing.T) {
	t.Run("captured on the flags struct", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x", "--max-turns", "250"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if f.maxTurns != 250 {
			t.Errorf("f.maxTurns = %d, want 250", f.maxTurns)
		}
	})

	t.Run("defaults to 0 (inherit the deployment default)", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if f.maxTurns != 0 {
			t.Errorf("f.maxTurns default = %d, want 0", f.maxTurns)
		}
	})

	t.Run("does not leak into app.Config", func(t *testing.T) {
		// --max-turns is a per-session limit (threaded to CreateSession via run), not
		// an engine-build knob, so it must NOT appear on app.Config. A distinctive
		// value would otherwise show up in the %+v render of every mapped field.
		f, err := parseFlags([]string{"--prompt", "x", "--max-turns", "987654"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		cfg := appConfig(f, newDiagnostics(), observability{})
		if strings.Contains(fmt.Sprintf("%+v", cfg), "987654") {
			t.Errorf("--max-turns leaked into app.Config: %+v", cfg)
		}
	})
}

// TestAppConfigMapping is a table over the flag->app.Config mapping, including the
// deliberate headless->Interactive inversion, the --guardrails=off kill switch, and
// the ask-reviewer trio.
func TestAppConfigMapping(t *testing.T) {
	t.Run("headless default inverts to Interactive=false", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if !f.headless {
			t.Fatal("headless must default to true (the inversion from mecated)")
		}
		cfg := appConfig(f, newDiagnostics(), observability{})
		if cfg.Interactive {
			t.Error("default headless must map to Interactive=false")
		}
	})

	t.Run("--headless=false maps to Interactive=true", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x", "--headless=false"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		cfg := appConfig(f, newDiagnostics(), observability{})
		if !cfg.Interactive {
			t.Error("--headless=false must map to Interactive=true")
		}
		if cfg.Headless {
			t.Error("--headless=false must map to Headless=false (the explicit deployment identity)")
		}
	})

	t.Run("headless default maps Headless=true", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		cfg := appConfig(f, newDiagnostics(), observability{})
		if !cfg.Headless {
			t.Error("default headless must map to Headless=true (the explicit deployment identity)")
		}
	})

	t.Run("--trust-project maps to TrustProject", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x", "--trust-project"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if !f.trustProject {
			t.Error("--trust-project must set f.trustProject")
		}
		cfg := appConfig(f, newDiagnostics(), observability{})
		if !cfg.TrustProject {
			t.Error("--trust-project must map to app.Config.TrustProject=true (the ingestion opt-in on a headless root)")
		}
		// Default OFF (the fail-safe): a CI run over an untrusted repo ingests nothing.
		fDef, err := parseFlags([]string{"--prompt", "x"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if appConfig(fDef, newDiagnostics(), observability{}).TrustProject {
			t.Error("--trust-project must default OFF (the fail-safe default)")
		}
	})

	t.Run("--guardrails=off sets GuardrailsDisabled", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x", "--guardrails", "off", "--guardrails-model", "m"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if !f.guardrailsOff {
			t.Fatal("--guardrails=off must set guardrailsOff")
		}
		cfg := appConfig(f, newDiagnostics(), observability{})
		if !cfg.GuardrailsDisabled {
			t.Error("guardrailsOff must map to GuardrailsDisabled=true")
		}
		if cfg.GuardrailsModel != "m" {
			t.Errorf("GuardrailsModel = %q, want m", cfg.GuardrailsModel)
		}
	})

	t.Run("guardrails left on by default", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x", "--guardrails-model", "m"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		cfg := appConfig(f, newDiagnostics(), observability{})
		if cfg.GuardrailsDisabled {
			t.Error("guardrails must NOT be disabled when --guardrails is unset")
		}
	})

	t.Run("ask-reviewer trio", func(t *testing.T) {
		policy := filepath.Join(t.TempDir(), "rubric.md")
		if err := os.WriteFile(policy, []byte("RUBRIC BODY"), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := parseFlags([]string{
			"--prompt", "x",
			"--subagent-ask-reviewer", "reviewer-model",
			"--subagent-ask-reviewer-max-denies", "7",
			"--subagent-ask-reviewer-policy", policy,
		})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		cfg := appConfig(f, newDiagnostics(), observability{})
		if cfg.SubagentAskReviewerModel != "reviewer-model" {
			t.Errorf("SubagentAskReviewerModel = %q", cfg.SubagentAskReviewerModel)
		}
		if cfg.SubagentAskReviewerMaxDenies != 7 {
			t.Errorf("SubagentAskReviewerMaxDenies = %d, want 7", cfg.SubagentAskReviewerMaxDenies)
		}
		if cfg.SubagentAskReviewerPolicy != "RUBRIC BODY" {
			t.Errorf("SubagentAskReviewerPolicy = %q, want the file CONTENT", cfg.SubagentAskReviewerPolicy)
		}
	})

	t.Run("subagent-model-router kill-switch (ADR 0042)", func(t *testing.T) {
		// Unset → router governed by the taxonomy (RouterDisabled false).
		f, err := parseFlags([]string{"--prompt", "x"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if appConfig(f, newDiagnostics(), observability{}).RouterDisabled {
			t.Error("an unset --subagent-model-router must leave RouterDisabled false (router governed by taxonomy)")
		}
		// A bare invocation must still PARSE; it is a harmless no-op and must NOT disable
		// the router (taxonomy governs).
		f, err = parseFlags([]string{"--prompt", "x", "--subagent-model-router"})
		if err != nil {
			t.Fatalf("bare --subagent-model-router must parse: %v", err)
		}
		if appConfig(f, newDiagnostics(), observability{}).RouterDisabled {
			t.Error("a bare --subagent-model-router must NOT set RouterDisabled")
		}
		// =false is the kill-switch → RouterDisabled.
		f, err = parseFlags([]string{"--prompt", "x", "--subagent-model-router=false"})
		if err != nil {
			t.Fatalf("parseFlags --subagent-model-router=false: %v", err)
		}
		if !appConfig(f, newDiagnostics(), observability{}).RouterDisabled {
			t.Error("--subagent-model-router=false must set RouterDisabled (the kill-switch)")
		}
	})

	t.Run("core knobs pass through", func(t *testing.T) {
		f, err := parseFlags([]string{
			"--prompt", "x",
			"--workspace", "/repo",
			"--model", "gpt-x",
			"--mock",
			"--max-run-tokens", "1234",
			"--max-team-tokens", "5678",
			"--no-bash",
			"--posture", "auto",
		})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if !f.postureFlagSet {
			t.Error("explicit --posture must set postureFlagSet")
		}
		cfg := appConfig(f, newDiagnostics(), observability{})
		if cfg.Workspace != "/repo" || cfg.Model != "gpt-x" || !cfg.UseMock {
			t.Errorf("core knobs not mapped: %+v", cfg)
		}
		if cfg.MaxRunTokens != 1234 || cfg.MaxTeamTokens != 5678 {
			t.Errorf("budgets not mapped: run=%d team=%d", cfg.MaxRunTokens, cfg.MaxTeamTokens)
		}
		if !cfg.NoShell {
			t.Error("--no-bash not mapped")
		}
		if cfg.Posture != app.PostureAuto {
			t.Errorf("Posture = %v, want auto", cfg.Posture)
		}
		if !cfg.PostureFlagSet {
			t.Error("PostureFlagSet not mapped")
		}
	})

	t.Run("all three provider creds + base-urls (the gained behavior)", func(t *testing.T) {
		// FIX B: mecatequi previously read ONLY OPENAI_API_KEY and only
		// --openai-base-url. Via the shared cliconfig helper it now reads all three
		// keys and registers all three base-URL flags.
		t.Setenv("OPENAI_API_KEY", "sk-oai")
		t.Setenv("OPENROUTER_API_KEY", "sk-or")
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant")
		f, err := parseFlags([]string{
			"--prompt", "x",
			"--default-provider", "anthropic",
			"--openai-base-url", "https://oai.example",
			"--openrouter-base-url", "https://or.example",
			"--anthropic-base-url", "https://ant.example",
		})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		cfg := appConfig(f, newDiagnostics(), observability{})
		if cfg.OpenAIKey != "sk-oai" || cfg.OpenRouterKey != "sk-or" || cfg.AnthropicKey != "sk-ant" {
			t.Errorf("all three keys must be read: %q / %q / %q", cfg.OpenAIKey, cfg.OpenRouterKey, cfg.AnthropicKey)
		}
		if cfg.ProviderOverrides["openai"].BaseURL != "https://oai.example" || cfg.ProviderOverrides["openrouter"].BaseURL != "https://or.example" || cfg.ProviderOverrides["anthropic"].BaseURL != "https://ant.example" {
			t.Errorf("all three endpoint overrides must map: %#v", cfg.ProviderOverrides)
		}
		if cfg.DefaultProvider != "anthropic" {
			t.Errorf("DefaultProvider = %q, want anthropic (mecatequi can now run Anthropic explicitly)", cfg.DefaultProvider)
		}
		// An OPENAI_API_KEY present flips UseOpenAI on (matching mecated).
		if !cfg.UseOpenAI {
			t.Error("a present OPENAI_API_KEY should flip UseOpenAI on (mecated parity)")
		}
	})
}

func TestCanonicalShellTool_Scenario2_LegacyNoBashFlag(t *testing.T) {
	for _, name := range []string{"--no-shell", "--no-bash"} {
		f, err := parseFlags([]string{"--prompt", "x", name})
		if err != nil {
			t.Fatalf("parseFlags(%s): %v", name, err)
		}
		if !f.noShell {
			t.Fatalf("%s did not disable Shell", name)
		}
	}
}
