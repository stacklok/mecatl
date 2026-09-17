package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/testutil/codextest"
)

func TestProviderReviewConfiguredUnsafeEnrollment(t *testing.T) {
	for _, kind := range []string{"malformed", "unsafe", "unreadable"} {
		for _, action := range []string{"login", "setup"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				sp, ap := followupHome(t, "{}\n", "")
				if err := os.WriteFile(sp, []byte("credential_store:\n  api_key:\n    file: "+ap+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "malformed":
					if err := os.WriteFile(ap, []byte("not-yaml: [private-sentinel"), 0600); err != nil {
						t.Fatal(err)
					}
				case "unsafe":
					if err := os.WriteFile(ap, []byte("providers: {}\n"), 0644); err != nil {
						t.Fatal(err)
					}
				case "unreadable":
					if err := os.Mkdir(ap, 0700); err != nil {
						t.Fatal(err)
					}
				}
				c := newProviderCommands()
				c.terminal.readAPIKey = func(context.Context, string) (string, error) {
					t.Fatal("unsafe input prompted for key")
					return "", nil
				}
				c.terminal.readField = func(context.Context, string) (string, error) { t.Fatal("unsafe input prompted"); return "", nil }
				var err error
				res := resolveInvocation([]string{"mecatui", "providers", action, "openai"})
				if action == "setup" {
					err = c.runSetup(context.Background(), res, io.Discard, io.Discard)
				} else {
					err = c.runCredential(context.Background(), res, io.Discard, io.Discard)
				}
				if err == nil || strings.Contains(err.Error(), "private-sentinel") || strings.Contains(err.Error(), "missing") {
					t.Fatalf("invalid custody outcome: %v", err)
				}
			})
		}
	}
}

func TestProviderReviewCustomPartialConsent(t *testing.T) {
	for _, stage := range []string{"decline-key", "decline-default", "cancel-key", "eof-default", "failed-key", "ambiguous-key", "missing-definition"} {
		t.Run(stage, func(t *testing.T) {
			sp, ap := followupHome(t, "{}\n", "")
			c := newProviderCommands()
			answers := []string{"https://gateway.example", "1", "model-1", "1"}
			switch stage {
			case "decline-key":
				answers = append(answers, "no")
			case "decline-default":
				answers = append(answers, "yes", "no")
			case "eof-default", "failed-key", "ambiguous-key", "missing-definition":
				answers = append(answers, "yes")
			}
			fields := providerInput(t, answers...)
			c.terminal.readField = func(ctx context.Context, prompt string) (string, error) {
				if stage == "eof-default" && strings.Contains(prompt, "deployment default") {
					return "", io.EOF
				}
				return fields(ctx, prompt)
			}
			c.terminal.readAPIKey = func(context.Context, string) (string, error) {
				if stage == "cancel-key" {
					return "", io.EOF
				}
				return "review-secret", nil
			}
			if stage == "failed-key" || stage == "ambiguous-key" {
				c.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
					if stage == "ambiguous-key" {
						return authfile.CommitReplacementAppliedDurabilityUnknown, errors.New("sync fault")
					}
					return authfile.CommitNotApplied, errors.New("write fault")
				}
			}
			if stage == "missing-definition" {
				c.backend.updateAPIKey = func(ctx context.Context, path string, update authfile.APIKeyUpdate) (authfile.CommitState, error) {
					state, err := authfile.UpdateAPIKey(ctx, path, update)
					// Simulate a concurrent settings editor after credential save.
					if err == nil {
						err = os.WriteFile(sp, []byte("{}\n"), 0600)
					}
					return state, err
				}
			}
			var out bytes.Buffer
			err := c.runNamedSetup(context.Background(), "custom", &out, &out)
			if stage == "decline-key" || stage == "decline-default" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("partial/cancelled flow claimed success")
			}
			if stage == "cancel-key" || stage == "eof-default" {
				if !errors.Is(err, errProviderCredentialCancelled) {
					t.Fatalf("cancellation: %v", err)
				}
			}
			if stage == "cancel-key" {
				inspection, inspectErr := inspectLocalProviders()
				if inspectErr != nil || len(inspection.definitions) != 0 {
					t.Fatal("definition not restored")
				}
			} else if strings.Contains(out.String(), "no changes made") {
				t.Fatal("committed definition misreported")
			}
			if stage == "decline-default" || stage == "eof-default" {
				if !strings.Contains(readProviderCredentialTestFile(t, ap), "review-secret") {
					t.Fatal("key not retained")
				}
			}
			if stage == "decline-key" && !strings.Contains(out.String(), "Provider definition saved") {
				t.Fatal("lost definition commit notice")
			}
			if strings.Contains(out.String(), "review-secret") {
				t.Fatal("secret in output")
			}
		})
	}
}

func TestProviderReviewAPIKeyWriteOutcome(t *testing.T) {
	for _, action := range []string{"login", "setup", "custom-setup", "add"} {
		for _, outcome := range []struct {
			name  string
			state authfile.CommitState
			err   error
		}{
			{"unknown", authfile.CommitReplacementAppliedDurabilityUnknown, errors.New("sync fault: review-secret")},
			{"unknown-cancelled", authfile.CommitReplacementAppliedDurabilityUnknown, context.Canceled},
			{"unknown-no-error", authfile.CommitReplacementAppliedDurabilityUnknown, nil},
			{"not-applied", authfile.CommitNotApplied, errors.New("write not applied")},
		} {
			t.Run(action+"/"+outcome.name, func(t *testing.T) {
				sp, ap := followupHome(t, "{}\n", "")
				c := newProviderCommands()
				provider := "openai"
				answers := []string{"yes"}
				custom := action == "custom-setup" || action == "add"
				if custom {
					provider = "custom"
					answers = []string{"https://gateway.example", "1", "model-1", "1", "yes"}
				}
				c.terminal.readField = providerInput(t, answers...)
				c.terminal.readAPIKey = func(context.Context, string) (string, error) { return "review-secret", nil }
				c.backend.updateDefaults = func(context.Context, string, permconfig.DefaultUpdate) (authfile.CommitState, error) {
					t.Fatal("key write failure proceeded to default mutation")
					return authfile.CommitNotApplied, nil
				}
				writes := 0
				c.backend.updateAPIKey = func(ctx context.Context, path string, update authfile.APIKeyUpdate) (authfile.CommitState, error) {
					writes++
					if writes != 1 || update.APIKey == nil {
						t.Fatal("automatic key retry or deletion")
					}
					if outcome.state == authfile.CommitReplacementAppliedDurabilityUnknown {
						if _, err := authfile.UpdateAPIKey(ctx, path, update); err != nil {
							t.Fatal(err)
						}
					}
					return outcome.state, outcome.err
				}
				var out bytes.Buffer
				res := resolveInvocation([]string{"mecatui", "providers", action, provider})
				var err error
				switch action {
				case "login":
					err = c.runCredential(context.Background(), res, &out, &out)
				case "setup":
					err = c.runSetup(context.Background(), res, &out, &out)
				case "custom-setup":
					err = c.runSetup(context.Background(), resolveInvocation([]string{"mecatui", "providers", "setup", provider}), &out, &out)
				case "add":
					err = c.runAdd(context.Background(), res, &out, &out)
				}
				if err == nil || errors.Is(err, errProviderCredentialCancelled) || writes != 1 {
					t.Fatalf("write outcome: err=%v writes=%d", err, writes)
				}
				text := out.String() + err.Error()
				for _, forbidden := range []string{"review-secret", ap, "no changes made", "definition restored", "rollback", "default set"} {
					if strings.Contains(text, forbidden) {
						t.Fatalf("unsafe or untrue outcome contains %q", forbidden)
					}
				}
				if outcome.state == authfile.CommitReplacementAppliedDurabilityUnknown {
					for _, want := range []string{"replacement_applied_durability_unknown", "may already be active", "crash durability", "mecatui providers status " + provider, "configured credential file", "before a manual retry"} {
						if !strings.Contains(text, want) {
							t.Errorf("missing %q in outcome: %s", want, text)
						}
					}
					if !strings.Contains(readProviderCredentialTestFile(t, ap), "review-secret") {
						t.Fatal("possibly applied key was removed")
					}
				} else if strings.Contains(text, "may already be active") || strings.Contains(text, "replacement_applied_durability_unknown") {
					t.Fatal("unapplied write reported as possibly active")
				}
				inspection, inspectErr := inspectLocalProviders()
				if inspectErr != nil {
					t.Fatal(inspectErr)
				}
				_, definitionRemains := inspection.definitions[provider]
				if inspection.selectedProvider != "" || definitionRemains != custom || (!custom && readProviderCredentialTestFile(t, sp) != "{}\n") {
					t.Fatal("definition removed or deployment default changed")
				}
			})
		}
	}
}

func TestProviderReviewOIDCAddCancellation(t *testing.T) {
	followupHome(t, "{}\n", "")
	c := newProviderCommands()
	c.terminal.readField = providerInput(t, "https://gateway.example", "1", "model-1", "2", "https://issuer.example", "client-id", "openid profile", "audience", "1", "1")
	var root string
	c.backend.openOIDCRuntime = func(_ context.Context, def permconfig.ProviderDefinition, _ bool, _ io.Writer) (nativeEndpointRuntime, error) {
		root = def.Auth.OIDC.CredentialStore.Home
		return &fakeProviderOIDCRuntime{loginErr: context.Canceled}, nil
	}
	var out bytes.Buffer
	err := c.runNamedSetup(context.Background(), "custom", &out, &out)
	if !errors.Is(err, errProviderCredentialCancelled) || strings.Contains(out.String(), "no changes made") || !strings.Contains(out.String(), "definition restored") || !strings.Contains(out.String(), "directory may have been created") {
		t.Fatalf("OIDC add cancel: %v, %s", err, &out)
	}
	inspection, inspectErr := inspectLocalProviders()
	if inspectErr != nil || len(inspection.definitions) != 0 || inspection.oidcStoreConfigured {
		t.Fatal("definition or inserted OIDC settings not restored")
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatal("credential root removed")
	}
}

func TestProviderReviewEnvironmentOnlyProvenance(t *testing.T) {
	followupHome(t, "{}\n", "")
	t.Setenv("OPENAI_API_KEY", "environment-sentinel")
	c := newProviderCommands()
	for provider, want := range map[string]string{"openai": "OPENAI_API_KEY (environment)", "openrouter": "OPENAI_API_KEY (environment fallback)"} {
		var out bytes.Buffer
		if err := c.runStatus(context.Background(), resolveInvocation([]string{"mecatui", "providers", "status", provider}), &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), want) || strings.Contains(out.String(), "shadowed") || strings.Contains(out.String(), "environment-sentinel") {
			t.Fatalf("wrong environment-only provenance: %s", &out)
		}
	}
}

func TestProviderReviewConfiguredCodexReuse(t *testing.T) {
	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	token := codextest.Token(expiry, "account-sentinel")
	body := fmt.Sprintf("providers:\n  openai-codex:\n    oauth:\n      access_token: %s\n      account_id: account-sentinel\n      expires_at: %s\n", token, expiry.Format(time.RFC3339))
	sp, conventional := followupHome(t, "{}\n", "not-yaml: [invalid-conventional\n")
	configured := filepath.Join(filepath.Dir(conventional), "configured-credentials.yaml")
	settings := "credential_store:\n  api_key:\n    file: " + configured + "\nmodels:\n  default_provider: openai-codex\n  default: gpt-5\n"
	if err := os.WriteFile(sp, []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configured, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	c := newProviderCommands()
	c.terminal.readField = providerInput(t, "yes", "")
	c.terminal.readAPIKey = func(context.Context, string) (string, error) { t.Fatal("Codex secret entry"); return "", nil }
	var out bytes.Buffer
	if err := c.runStatus(context.Background(), resolveInvocation([]string{"mecatui", "providers", "status", "openai-codex"}), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "locally usable") {
		t.Fatal("configured token not used")
	}
	if err := c.runNamedSetup(context.Background(), "openai-codex", &out, &out); err != nil {
		t.Fatal(err)
	}
	if readProviderCredentialTestFile(t, configured) != body || readProviderCredentialTestFile(t, sp) != settings || readProviderCredentialTestFile(t, conventional) != "not-yaml: [invalid-conventional\n" {
		t.Fatal("Codex reuse changed bytes")
	}
	if strings.Contains(out.String(), token) || strings.Contains(out.String(), "account-sentinel") {
		t.Fatal("Codex metadata leaked")
	}
}
