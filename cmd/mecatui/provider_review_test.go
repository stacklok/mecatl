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

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestProviderReviewConfiguredMissingEnrollment(t *testing.T) {
	for _, action := range []string{"login", "setup"} {
		for _, consent := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "/decline", true: "/save"}[consent], func(t *testing.T) {
				sp, conventional := followupHome(t, "{}\n", "")
				ap := filepath.Join(filepath.Dir(conventional), "custom-keys.yaml")
				if err := os.WriteFile(sp, []byte("credential_store:\n  api_key:\n    file: "+ap+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				c := newProviderCommands()
				err := c.runStatus(context.Background(), resolveInvocation([]string{"mecatui", "providers", "status", "openai"}), io.Discard, io.Discard)
				if err == nil || !strings.Contains(err.Error(), "missing") {
					t.Fatalf("missing status: %v", err)
				}
				c.terminal.readAPIKey = func(context.Context, string) (string, error) {
					if _, err := os.Stat(ap); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("created before consent")
					}
					return "review-secret", nil
				}
				answers := []string{"no"}
				if consent {
					answers = []string{"yes", "no"}
				}
				c.terminal.readField = providerInput(t, answers...)
				res := resolveInvocation([]string{"mecatui", "providers", action, "openai"})
				if action == "setup" {
					err = c.runSetup(context.Background(), res, io.Discard, io.Discard)
				} else {
					err = c.runCredential(context.Background(), res, io.Discard, io.Discard)
				}
				if err != nil {
					t.Fatal(err)
				}
				if consent {
					if !strings.Contains(readProviderCredentialTestFile(t, ap), "review-secret") {
						t.Fatal("not saved")
					}
				} else if _, err := os.Stat(ap); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unconsented write")
				}
				if _, err := os.Stat(conventional); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("wrong custody path")
				}
			})
		}
	}
}

func TestProviderReviewCustomSetupDefault(t *testing.T) {
	for _, method := range []string{"api_key", "none", "oidc"} {
		for _, action := range []string{"setup", "add"} {
			t.Run(method+"/"+action, func(t *testing.T) {
				followupHome(t, "{}\n", "")
				c := newProviderCommands()
				authChoice := "1"
				if method == "none" {
					authChoice = "3"
				}
				if method == "oidc" {
					authChoice = "2"
				}
				answers := []string{"https://gateway.example", "1", "model-1", authChoice}
				if method == "oidc" {
					answers = append(answers, "https://issuer.example", "client-id", "openid profile", "audience", "1", "1")
					c.backend.openOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
						return &providerAddOIDCRuntime{}, nil
					}
					if action == "setup" {
						c.backend.resolveDefault = func(context.Context, string, string) (string, string, error) { return "custom", "model-1", nil }
					}
				}
				if method == "api_key" {
					answers = append(answers, "yes")
				}
				if action == "setup" {
					answers = append(answers, "yes", "")
				}
				c.terminal.readField = providerInput(t, answers...)
				c.terminal.readAPIKey = func(context.Context, string) (string, error) { return "review-secret", nil }
				calls := 0
				c.backend.updateDefaults = func(context.Context, string, permconfig.DefaultUpdate) (authfile.CommitState, error) {
					calls++
					return authfile.CommitDurable, nil
				}
				res := resolveInvocation([]string{"mecatui", "providers", action, "custom"})
				var err error
				if action == "setup" {
					err = c.runSetup(context.Background(), res, io.Discard, io.Discard)
				} else {
					err = c.runAdd(context.Background(), res, io.Discard, io.Discard)
				}
				if err != nil {
					t.Fatal(err)
				}
				if calls != map[string]int{"setup": 1, "add": 0}[action] {
					t.Fatalf("default calls %d", calls)
				}
			})
		}
	}
}

func TestProviderReviewOIDCCancelDirectory(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "oidc")
			if existing {
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			c := newProviderCommands()
			c.backend.openOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
				return &fakeProviderOIDCRuntime{loginErr: context.Canceled}, nil
			}
			var out bytes.Buffer
			err := c.runOIDC(context.Background(), providerCredentialResolution(providerActionLogin, "custom"), providerOIDCTestDefinition(root), io.Discard, &out)
			if !errors.Is(err, errProviderCredentialCancelled) {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "no changes made") || !strings.Contains(out.String(), "OIDC enrollment did not complete") {
				t.Fatalf("false cancellation: %s", &out)
			}
			if strings.Contains(out.String(), "directory may have been created") == existing {
				t.Fatalf("wrong root outcome: %s", &out)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("root changed unexpectedly: %v", err)
			}
		})
	}
}

func TestProviderReviewHelp(t *testing.T) {
	for action, wants := range map[string][]string{
		providerActionSetup:      {"reuse", "replace", "separate", "optional"},
		providerActionSetDefault: {"current selector", "same provider", "declared default", "explicit"},
	} {
		var out bytes.Buffer
		if err := writeProviderHelp(&out, action); err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%s missing %q", action, want)
			}
		}
	}
	status := codexProviderStatus(providerInspection{}.credentials)
	if !strings.Contains(status.Next, "https://mecatl.dev/docs/features/choose-models#reuse-a-manual-openai-codex-token") {
		t.Fatal("Codex guidance is not a navigable public link")
	}
}

func TestProviderReviewKeyEOF(t *testing.T) {
	c := testProviderCommands()
	c.terminal.readAPIKey = func(context.Context, string) (string, error) { return "", io.EOF }
	var out bytes.Buffer
	err := c.runAPIKey(context.Background(), providerCredentialResolution(providerActionLogin, "openai"), "unused", io.Discard, &out)
	if !errors.Is(err, errProviderCredentialCancelled) || !strings.Contains(out.String(), "Cancelled; no changes made") {
		t.Fatalf("EOF: %v, %s", err, &out)
	}
}

func TestProviderReviewAmbiguousDefault(t *testing.T) {
	for _, setup := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "setup"}[setup], func(t *testing.T) {
			followupHome(t, "{}\n", "")
			c := newProviderCommands()
			c.terminal.readAPIKey = func(context.Context, string) (string, error) { return "review-secret", nil }
			c.terminal.readField = providerInput(t, "yes", "yes", "gpt-5")
			c.backend.resolveDefault = func(context.Context, string, string) (string, string, error) { return "openai", "gpt-5", nil }
			c.backend.updateDefaults = func(context.Context, string, permconfig.DefaultUpdate) (authfile.CommitState, error) {
				return authfile.CommitReplacementAppliedDurabilityUnknown, errors.New("post-rename sync fault")
			}
			var out bytes.Buffer
			var err error
			if setup {
				err = c.runNamedSetup(context.Background(), "openai", &out, &out)
			} else {
				err = c.runSetDefault(context.Background(), invocationResolution{providerName: "openai"}, &out, &out)
			}
			if err == nil {
				t.Fatal("ambiguous write succeeded")
			}
			text := out.String() + err.Error()
			for _, want := range []string{"replacement_applied_durability_unknown", "may already be active", "providers status", "retry"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q: %s", want, text)
				}
			}
			if setup && !strings.Contains(text, "API key remains saved") {
				t.Fatal("lost prior commit notice")
			}
		})
	}
}
