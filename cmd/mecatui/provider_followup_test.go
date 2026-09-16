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
	t.Run("precedence", func(t *testing.T) {
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
	})

	for _, kind := range []string{"missing", "malformed", "unreadable"} {
		t.Run("configured credential input/"+kind, func(t *testing.T) {
			sp, ap := followupHome(t, "{}\n", "")
			if err := os.WriteFile(sp, []byte("credential_store:\n  api_key:\n    file: "+ap+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "malformed":
				if err := os.WriteFile(ap, []byte("not-yaml: [private-sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if err := os.Mkdir(ap, 0700); err != nil {
					t.Fatal(err)
				}
			}
			c := newProviderCommands()
			err := c.runStatus(context.Background(), resolveInvocation([]string{"mecatui", "providers"}), io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), kind) || strings.Contains(err.Error(), "private-sentinel") {
				t.Fatalf("%s state: %v", kind, err)
			}
		})
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
	t.Run("reuse declines default independently", func(t *testing.T) {
		sp, ap := followupHome(t, "{}\n", "providers:\n  openai:\n    api_key: original-sentinel\n")
		before := readProviderCredentialTestFile(t, ap)
		c := newProviderCommands()
		c.terminal.readField = providerInput(t, "yes", "no")
		c.terminal.readAPIKey = func(context.Context, string) (string, error) { t.Fatal("reuse prompted for key"); return "", nil }
		var out, diag bytes.Buffer
		if err := c.runSetup(context.Background(), invocationResolution{providerName: "openai"}, &out, &diag); err != nil {
			t.Fatal(err)
		}
		if readProviderCredentialTestFile(t, ap) != before || readProviderCredentialTestFile(t, sp) != "{}\n" {
			t.Fatal("reuse or declined default mutated files")
		}
	})

	t.Run("consented replacement warns about environment shadow", func(t *testing.T) {
		sp, ap := followupHome(t, "{}\n", "providers:\n  openai:\n    api_key: original-sentinel\n")
		t.Setenv("OPENAI_API_KEY", "environment-sentinel")
		c := newProviderCommands()
		c.terminal.readField = providerInput(t, "no", "yes", "no")
		c.terminal.readAPIKey = func(context.Context, string) (string, error) { return "replacement-sentinel", nil }
		var out, diag bytes.Buffer
		if err := c.runSetup(context.Background(), invocationResolution{providerName: "openai"}, &out, &diag); err != nil {
			t.Fatal(err)
		}
		credentials := readProviderCredentialTestFile(t, ap)
		if !strings.Contains(credentials, "replacement-sentinel") || strings.Contains(credentials, "original-sentinel") {
			t.Fatalf("consented replacement was not saved: %q", credentials)
		}
		if readProviderCredentialTestFile(t, sp) != "{}\n" {
			t.Fatal("declined default mutated settings")
		}
		if !strings.Contains(diag.String(), "effective environment credential will still win") {
			t.Fatalf("missing environment shadow warning: %s", &diag)
		}
		if strings.Contains(out.String()+diag.String(), "sentinel") {
			t.Fatal("secret output")
		}
	})

	t.Run("blank key is rejected before consent or write", func(t *testing.T) {
		_, ap := followupHome(t, "{}\n", "")
		c := newProviderCommands()
		c.terminal.readAPIKey = func(context.Context, string) (string, error) { return " \t", nil }
		err := c.runSetup(context.Background(), invocationResolution{providerName: "openai"}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "API key cannot be empty") {
			t.Fatalf("blank API key = %v", err)
		}
		if _, statErr := os.Stat(ap); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("blank API key wrote credentials: %v", statErr)
		}
	})
}

func TestInvariant_ProviderSetupFollowup_SecretSafety(t *testing.T) {
	const secret = "terminal-error-secret"
	c := testProviderCommands()
	c.backend.loadCredentials = providerCredentialConfigLoader(providerCredentialConfig{authPath: "unused"})
	c.terminal.readAPIKey = func(context.Context, string) (string, error) { return "", errors.New(secret) }
	var stderr bytes.Buffer
	err := c.runCredential(context.Background(), providerCredentialResolution(providerActionLogin, "openai"), io.Discard, &stderr)
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("terminal input error leaked secret: err=%v stderr=%q", err, stderr.String())
	}
}
