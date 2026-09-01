package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestResolveDebugGrammarLocalAndRemote(t *testing.T) {
	const target = "123456789012"
	tests := []struct {
		argv    []string
		mode    transportMode
		address string
	}{
		{[]string{"mecatui", "debug", target, "--prompt", "why"}, modeLocal, ""},
		{[]string{"mecatui", "connect", "host:9443", "debug", target, "--tls"}, modeConnect, "host:9443"},
	}
	for _, tc := range tests {
		res := resolveInvocation(tc.argv)
		if res.err != nil || res.mode != tc.mode || res.address != tc.address || res.debugTarget != target {
			t.Fatalf("resolve(%v) = %+v", tc.argv, res)
		}
	}
}

func TestResolveDebugRejectsMissingFlagFirstAndExtraOperands(t *testing.T) {
	for _, argv := range [][]string{
		{"mecatui", "debug"},
		{"mecatui", "debug", "--prompt", "why"},
		{"mecatui", "connect", "host:1", "debug"},
	} {
		if res := resolveInvocation(argv); res.err == nil {
			t.Fatalf("resolve(%v) unexpectedly succeeded: %+v", argv, res)
		}
	}
	res := resolveInvocation([]string{"mecatui", "debug", "target", "extra"})
	if res.err != nil {
		t.Fatal(res.err)
	}
	if _, err := parseRunConfig(res); err == nil || !strings.Contains(err.Error(), "unexpected operand") {
		t.Fatalf("extra operand error = %v", err)
	}
}

func TestPredictableSessionHandles_Scenario3_CommandHelp(t *testing.T) {
	var out bytes.Buffer
	writeTopLevelHelp(&out)
	text := out.String()
	for _, want := range []string{
		"mecatui debug SESSION_ID [flags]",
		"mecatui connect ADDRESS debug SESSION_ID [flags]",
		"exact full ID or its displayed 12-column short\n    handle",
		"[A-Za-z0-9._-] or uppercase %HH atoms, no leading #",
		"ambiguous or unmatched handles, use /session to copy the exact ID",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing %q:\n%s", want, text)
		}
	}
}

func TestDebugMCPFlagsRepeatAndRejectNonDebug(t *testing.T) {
	res := resolveInvocation([]string{"mecatui", "connect", "example.test:9443", "debug", "target", "--debug-mcp", "github", "--debug-mcp=slack"})
	cfg, err := parseRunConfig(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.debugMCP, ",") != "github,slack" {
		t.Fatalf("debug MCP selection = %v", cfg.debugMCP)
	}
	if prompt := initialPromptForConfig(cfg); !strings.Contains(prompt, "github, slack") || !strings.Contains(prompt, "does not authorize publication") {
		t.Fatalf("debug prompt does not disclose selected servers without authority: %q", prompt)
	}
	plain := config{debugMCP: []string{"github"}}
	if err := plain.validate(); err == nil || !strings.Contains(err.Error(), "only with the debug command") {
		t.Fatalf("non-debug --debug-mcp error = %v", err)
	}
}

func TestDebugPrivacyWarningUsesRuntimeEmissionPathAndNamesSensitiveEvidence(t *testing.T) {
	var out bytes.Buffer
	emitDebugPrivacyWarning(&out, "target")
	warning := out.String()
	for _, want := range []string{"mecatui: PRIVACY:", "prompts", "assistant output", "tool arguments and results", "file paths", "secrets", "sent to the configured model"} {
		if !strings.Contains(warning, want) {
			t.Fatalf("runtime warning missing %q: %q", want, warning)
		}
	}
	before := out.Len()
	emitDebugPrivacyWarning(&out, "")
	if out.Len() != before {
		t.Fatalf("non-debug launch emitted privacy warning: %q", out.String()[before:])
	}
}

func TestDebugConfigHasNoWorkspaceAndUsesDefaultPrompt(t *testing.T) {
	res := resolveInvocation([]string{"mecatui", "connect", "example.test:9443", "debug", "target"})
	cfg, err := parseRunConfig(res)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.workspace != "" {
		t.Fatalf("remote debug workspace leaked cwd: %q", cfg.workspace)
	}
	if got := initialPromptForConfig(cfg); got != defaultDebugPrompt {
		t.Fatalf("default prompt = %q", got)
	}
	cfg.prompt = "specific question"
	if got := initialPromptForConfig(cfg); got != "specific question" {
		t.Fatalf("explicit prompt = %q", got)
	}
}
