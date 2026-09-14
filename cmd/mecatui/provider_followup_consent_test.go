package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestProviderFollowupReplacementShadowWarning(t *testing.T) {
	for _, provider := range []string{"openai", "openrouter"} {
		t.Run(provider, func(t *testing.T) {
			_, ap := followupHome(t, "{}\n", "")
			t.Setenv("OPENAI_API_KEY", "environment-sentinel")
			c := newProviderCommands()
			c.terminal.readField = providerInput(t, "no", "yes", "no")
			var out, diagnostics bytes.Buffer
			c.terminal.readAPIKey = func(context.Context, string) (string, error) {
				if strings.Contains(diagnostics.String(), "will still win") != (provider == "openai") {
					t.Fatal("replacement warning did not follow credential precedence")
				}
				return "file-sentinel", nil
			}
			if err := c.runSetup(context.Background(), resolveInvocation([]string{"mecatui", "providers", "setup", provider}), &out, &diagnostics); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(readProviderCredentialTestFile(t, ap), "file-sentinel") || strings.Contains(readProviderCredentialTestFile(t, ap), "environment-sentinel") {
				t.Fatal("replacement did not save only the entered key")
			}
			inspection, err := inspectLocalProviders()
			if err != nil {
				t.Fatal(err)
			}
			want := "credential_store.api_key.file"
			if provider == "openai" {
				want = "OPENAI_API_KEY (environment)"
			}
			if inspection.sources[provider] != want {
				t.Fatal("post-save source disagrees with warning")
			}
		})
	}
}

func TestProviderFollowupConsentStages(t *testing.T) {
	for _, stage := range []string{"reuse-environment", "decline", "cancel-save", "cancel-default", "ambiguous-default", "oversized", "blank", "control"} {
		t.Run(stage, func(t *testing.T) {
			sp, ap := followupHome(t, "{}\n", "")
			c := newProviderCommands()
			key := "new-sensitive-value"
			defaultsCalled := false
			switch stage {
			case "reuse-environment":
				t.Setenv("OPENAI_API_KEY", "environment-sensitive-value")
				c.terminal.readField = providerInput(t, "yes", "no")
				c.terminal.readAPIKey = func(context.Context, string) (string, error) { t.Fatal("reuse read key"); return "", nil }
			case "decline":
				c.terminal.readField = providerInput(t, "no")
			case "cancel-save":
				c.terminal.readField = func(context.Context, string) (string, error) { return "", context.Canceled }
			case "cancel-default":
				i := 0
				c.terminal.readField = func(context.Context, string) (string, error) {
					i++
					if i == 1 {
						return "yes", nil
					}
					return "", context.Canceled
				}
			case "ambiguous-default":
				c.terminal.readField = providerInput(t, "yes", "yes", "gpt-5")
				c.backend.updateDefaults = func(context.Context, string, permconfig.DefaultUpdate) (authfile.CommitState, error) {
					defaultsCalled = true
					return authfile.CommitReplacementAppliedDurabilityUnknown, context.Canceled
				}
			case "oversized":
				key = strings.Repeat("s", 8193)
			case "blank":
				key = " \t"
			case "control":
				key = "new-sensitive-value\x1b[31m"
			}
			if stage != "reuse-environment" {
				c.terminal.readAPIKey = func(context.Context, string) (string, error) { return key, nil }
			}
			var out, diagnostics bytes.Buffer
			err := c.runSetup(context.Background(), resolveInvocation([]string{"mecatui", "providers", "setup", "openai"}), &out, &diagnostics)
			saved := stage == "cancel-default" || stage == "ambiguous-default"
			if saved {
				if !strings.Contains(readProviderCredentialTestFile(t, ap), "new-sensitive-value") {
					t.Fatal("committed key lost")
				}
				if strings.Contains(diagnostics.String(), "no changes made") {
					t.Fatal("false whole-command rollback")
				}
			} else if _, statErr := os.Stat(ap); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("unconsented key written")
			}
			if readProviderCredentialTestFile(t, sp) != "{}\n" {
				t.Fatal("settings changed without default consent")
			}
			if stage == "cancel-save" || stage == "cancel-default" {
				if !errors.Is(err, errProviderCredentialCancelled) {
					t.Fatalf("wrong cancellation: %v", err)
				}
			}
			if stage == "ambiguous-default" {
				if !defaultsCalled || err == nil || errors.Is(err, errProviderCredentialCancelled) || strings.Contains(diagnostics.String(), "default was not changed") {
					t.Fatal("ambiguous write misreported as rollback")
				}
			}
			if (stage == "oversized" || stage == "blank" || stage == "control") && err == nil {
				t.Fatal("invalid key accepted")
			}
			text := out.String() + diagnostics.String()
			if err != nil {
				text += err.Error()
			}
			if strings.Contains(text, "sensitive-value") {
				t.Fatal("secret disclosed")
			}
		})
	}
}

func TestProviderFollowupStatusInputFailures(t *testing.T) {
	for _, kind := range []string{"missing", "malformed", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
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
