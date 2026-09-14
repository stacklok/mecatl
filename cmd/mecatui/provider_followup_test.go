package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func followupHome(t *testing.T, settings, credentials string) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, name := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_API_KEY"} {
		t.Setenv(name, "")
	}
	dir := filepath.Join(home, "mecatl")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	sp, ap := filepath.Join(dir, "settings.yaml"), filepath.Join(dir, "auth.yaml")
	if err := os.WriteFile(sp, []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	if credentials != "" {
		if err := os.WriteFile(ap, []byte(credentials), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return sp, ap
}

func TestProviderSetupFollowup_Scenario1_StatusProvenance(t *testing.T) {
	followupHome(t, "models:\n  default_provider: openai\n  default: openai/gpt-5\n", "providers:\n  openai:\n    api_key: file-sentinel\n")
	t.Setenv("OPENAI_API_KEY", "environment-sentinel")
	c := newProviderCommands()
	var out bytes.Buffer
	if err := c.runStatus(context.Background(), resolveInvocation([]string{"mecatui", "providers"}), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"selected default", "OPENAI_API_KEY", "openrouter", "verification: not checked", "file present (shadowed)", "openai/gpt-5"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, &out)
		}
	}
	if strings.Contains(out.String(), "sentinel") {
		t.Fatal("secret in status")
	}
	t.Setenv("OPENAI_API_KEY", "")
	if err := os.Remove(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "mecatl", "auth.yaml")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := c.runStatus(context.Background(), resolveInvocation([]string{"mecatui", "providers"}), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "selected default") || !strings.Contains(out.String(), "not configured") {
		t.Fatalf("lost recovery row: %s", &out)
	}
}

func TestProviderSetupFollowup_Scenario2_ConsoleAndCustodyGuidance(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai", "openrouter", "opencode", "custom"} {
		t.Run(provider, func(t *testing.T) {
			settings := "{}\n"
			if provider == "custom" {
				settings = strings.ReplaceAll(followupCustomSettings, "method: none", "method: api_key")
			}
			followupHome(t, settings, "")
			c := newProviderCommands()
			var out, diagnostics bytes.Buffer
			c.terminal.readAPIKey = func(context.Context, string) (string, error) {
				for _, want := range []string{"API", "plaintext", "same-UID", "Shell"} {
					if !strings.Contains(diagnostics.String(), want) {
						t.Errorf("guidance before input missing %q: %s", want, &diagnostics)
					}
				}
				if provider == "custom" {
					if strings.Contains(diagnostics.String(), "https://") || !strings.Contains(diagnostics.String(), "operator or service documentation") {
						t.Fatal("custom console invented")
					}
				} else if !strings.Contains(diagnostics.String(), "https://") {
					t.Fatal("stock console missing")
				}
				if provider == "opencode" && (!strings.Contains(diagnostics.String(), "Go subscription") || !strings.Contains(diagnostics.String(), "Zen")) {
					t.Error("Go/Zen distinction missing")
				}
				return "", context.Canceled
			}
			err := c.runCredential(context.Background(), providerCredentialResolution(providerActionLogin, provider), &out, &diagnostics)
			if !errors.Is(err, errProviderCredentialCancelled) {
				t.Fatal(err)
			}
		})
	}
}

func TestProviderSetupFollowup_Scenario2_ReuseAndIndependentConsent(t *testing.T) {
	for _, choice := range []string{"reuse", "replace"} {
		t.Run(choice, func(t *testing.T) {
			sp, ap := followupHome(t, "{}\n", "providers:\n  openai:\n    api_key: original-sentinel\n")
			before := readProviderCredentialTestFile(t, ap)
			c := newProviderCommands()
			if choice == "reuse" {
				c.terminal.readField = providerInput(t, "yes", "no")
				c.terminal.readAPIKey = func(context.Context, string) (string, error) { t.Fatal("reuse prompted for key"); return "", nil }
			} else {
				c.terminal.readField = providerInput(t, "no", "no")
				c.terminal.readAPIKey = func(context.Context, string) (string, error) { return "replacement-sentinel", nil }
			}
			var out, diag bytes.Buffer
			if err := c.runSetup(context.Background(), invocationResolution{providerName: "openai"}, &out, &diag); err != nil {
				t.Fatal(err)
			}
			if readProviderCredentialTestFile(t, ap) != before || readProviderCredentialTestFile(t, sp) != "{}\n" {
				t.Fatal("decline/reuse mutated files")
			}
			if strings.Contains(out.String()+diag.String(), "sentinel") {
				t.Fatal("secret output")
			}
		})
	}
}
