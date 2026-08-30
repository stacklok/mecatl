package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestResolveDebugGrammarLocalAndRemote(t *testing.T) {
	tests := []struct {
		argv    []string
		mode    transportMode
		address string
	}{
		{[]string{"mecatui", "debug", "target-1", "--prompt", "why"}, modeLocal, ""},
		{[]string{"mecatui", "connect", "host:9443", "debug", "target-2", "--tls"}, modeConnect, "host:9443"},
	}
	for _, tc := range tests {
		res := resolveInvocation(tc.argv)
		if res.err != nil || res.mode != tc.mode || res.address != tc.address || !strings.HasPrefix(res.debugTarget, "target-") {
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

func TestDebugHelpDocumentsBothCanonicalForms(t *testing.T) {
	var out bytes.Buffer
	writeTopLevelHelp(&out)
	text := out.String()
	for _, want := range []string{"mecatui debug SESSION_ID [flags]", "mecatui connect ADDRESS debug SESSION_ID [flags]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing %q:\n%s", want, text)
		}
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
