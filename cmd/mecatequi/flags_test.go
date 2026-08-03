package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/app"
)

func TestLegacyOutputEconomyFlagIsNoOpAndWarns(t *testing.T) {
	f, err := parseFlags([]string{"--prompt", "hi", "--output-economy", "terse"})
	if err != nil {
		t.Fatalf("legacy --output-economy must remain parseable for one release: %v", err)
	}
	if !f.outputEconomyFlagSet || f.outputEconomy != "terse" {
		t.Fatalf("legacy flag capture = (%q, %v), want (terse, true)", f.outputEconomy, f.outputEconomyFlagSet)
	}
	if _, ok := reflect.TypeOf(appConfig(f, nil)).FieldByName("OutputEconomy"); ok {
		t.Fatal("app.Config unexpectedly exposes OutputEconomy; legacy CLI value could affect behavior")
	}
	var warning bytes.Buffer
	warnDeprecatedOutputEconomy(&warning, true)
	if got := warning.String(); !strings.Contains(got, "--output-economy is deprecated and has no effect") || !strings.Contains(got, "remove it") {
		t.Fatalf("deprecation warning missing no-effect/removal guidance: %q", got)
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
		cfg := appConfig(f, newDiagnostics())
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
		cfg := appConfig(f, newDiagnostics())
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
		cfg := appConfig(f, newDiagnostics())
		if cfg.Interactive {
			t.Error("default headless must map to Interactive=false")
		}
	})

	t.Run("--headless=false maps to Interactive=true", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x", "--headless=false"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		cfg := appConfig(f, newDiagnostics())
		if !cfg.Interactive {
			t.Error("--headless=false must map to Interactive=true")
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
		cfg := appConfig(f, newDiagnostics())
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
		cfg := appConfig(f, newDiagnostics())
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
		cfg := appConfig(f, newDiagnostics())
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
		if appConfig(f, newDiagnostics()).RouterDisabled {
			t.Error("an unset --subagent-model-router must leave RouterDisabled false (router governed by taxonomy)")
		}
		// A bare invocation must still PARSE; it is a harmless no-op and must NOT disable
		// the router (taxonomy governs).
		f, err = parseFlags([]string{"--prompt", "x", "--subagent-model-router"})
		if err != nil {
			t.Fatalf("bare --subagent-model-router must parse: %v", err)
		}
		if appConfig(f, newDiagnostics()).RouterDisabled {
			t.Error("a bare --subagent-model-router must NOT set RouterDisabled")
		}
		// =false is the kill-switch → RouterDisabled.
		f, err = parseFlags([]string{"--prompt", "x", "--subagent-model-router=false"})
		if err != nil {
			t.Fatalf("parseFlags --subagent-model-router=false: %v", err)
		}
		if !appConfig(f, newDiagnostics()).RouterDisabled {
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
		cfg := appConfig(f, newDiagnostics())
		if cfg.Workspace != "/repo" || cfg.Model != "gpt-x" || !cfg.UseMock {
			t.Errorf("core knobs not mapped: %+v", cfg)
		}
		if cfg.MaxRunTokens != 1234 || cfg.MaxTeamTokens != 5678 {
			t.Errorf("budgets not mapped: run=%d team=%d", cfg.MaxRunTokens, cfg.MaxTeamTokens)
		}
		if !cfg.NoBash {
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
		cfg := appConfig(f, newDiagnostics())
		if cfg.OpenAIKey != "sk-oai" || cfg.OpenRouterKey != "sk-or" || cfg.AnthropicKey != "sk-ant" {
			t.Errorf("all three keys must be read: %q / %q / %q", cfg.OpenAIKey, cfg.OpenRouterKey, cfg.AnthropicKey)
		}
		if cfg.OpenAIBaseURL != "https://oai.example" || cfg.OpenRouterBaseURL != "https://or.example" || cfg.AnthropicBaseURL != "https://ant.example" {
			t.Errorf("all three base-urls must map: %q / %q / %q", cfg.OpenAIBaseURL, cfg.OpenRouterBaseURL, cfg.AnthropicBaseURL)
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

// TestHelpOutputExcludesDeprecatedOutputEconomyFlag exercises the ACTUAL help
// output: it calls the usageEpilogue helper parseFlags wires (cmd/mecatequi/flags.go
// line 189) and asserts the deprecated --output-economy flag is absent while a
// real flag (--workspace) is present. Without this, a call-site regression (e.g.
// switching PrintDefaultsHide to PrintDefaults) would re-expose the flag in
// --help without a failing test.
func TestHelpOutputExcludesDeprecatedOutputEconomyFlag(t *testing.T) {
	fs := flag.NewFlagSet("mecatequi", flag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)

	// Register the deprecated flag + a control flag so we can assert real flags
	// ARE printed.
	fs.String("output-economy", "", "DEPRECATED")
	fs.String("workspace", "", "workspace root")

	// The EXACT Usage closure parseFlags wires (cmd/mecatequi/flags.go:189).
	usageEpilogue(fs)()

	out := buf.String()
	if strings.Contains(out, "--output-economy") || strings.Contains(out, "-output-economy") {
		t.Fatalf("help output must not advertise the deprecated --output-economy flag:\n%s", out)
	}
	if !strings.Contains(out, "-workspace") {
		t.Fatalf("help output missing the -workspace control flag (flags are not being printed at all):\n%s", out)
	}
}
