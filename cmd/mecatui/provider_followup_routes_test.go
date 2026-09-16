package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/testutil/codextest"
)

type followupOIDCStatus struct {
	t             *testing.T
	state         llmendpoint.Status
	reads, closes int
}

func (s *followupOIDCStatus) Status(context.Context) llmendpoint.Status { s.reads++; return s.state }
func (s *followupOIDCStatus) Close() error                              { s.closes++; return nil }
func (s *followupOIDCStatus) Login(context.Context) error {
	s.t.Fatal("passive status enrolled")
	return nil
}
func (s *followupOIDCStatus) Logout(context.Context) error {
	s.t.Fatal("passive status removed credential")
	return nil
}

func TestProviderSetupFollowup_Scenario1_PassiveBoundaries(t *testing.T) {
	for _, state := range []llmendpoint.Status{llmendpoint.StatusUsable, llmendpoint.StatusExpired, llmendpoint.StatusNotEnrolled} {
		t.Run(string(state), func(t *testing.T) {
			followupHome(t, "{}\n", "")
			c := newProviderCommands()
			root := filepath.Join(t.TempDir(), "absent")
			definition := providerOIDCTestDefinition(root)
			definition.DefaultModel = "unsafe\x1b[31m\u202e" + strings.Repeat("x", 1000)
			c.backend.inspect = providerInspectionLoader(providerInspection{definitions: permconfig.ProviderDefinitions{"custom": definition}})
			probe := &followupOIDCStatus{t: t, state: state}
			c.backend.openOIDCRuntime = func(_ context.Context, got permconfig.ProviderDefinition, _ bool, _ io.Writer) (nativeEndpointRuntime, error) {
				if got.Auth.OIDC.CredentialStore.Home != root {
					t.Fatal("wrong store inspected")
				}
				return probe, nil
			}
			c.backend.prepareOIDCRoot = func(string) error { t.Fatal("status created store"); return nil }
			var out bytes.Buffer
			if err := c.runStatus(context.Background(), resolveInvocation([]string{"mecatui", "providers"}), &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			if probe.reads != 1 || probe.closes != 1 {
				t.Fatal("local status not inspected and closed")
			}
			for _, bad := range []string{"\x1b", "\u202e", strings.Repeat("x", 241), "toolhive"} {
				if strings.Contains(out.String(), bad) {
					t.Fatal("unsafe or invented status output")
				}
			}
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("status initialized store")
			}
		})
	}
	t.Run("production-unavailable-store", func(t *testing.T) {
		followupHome(t, "{}\n", "")
		root := filepath.Join(t.TempDir(), "absent")
		c := newProviderCommands()
		c.backend.inspect = providerInspectionLoader(providerInspection{definitions: permconfig.ProviderDefinitions{"custom": providerOIDCTestDefinition(root)}})
		var out bytes.Buffer
		if err := c.runStatus(context.Background(), resolveInvocation([]string{"mecatui", "providers", "status", "custom"}), &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "OIDC storage unavailable") {
			t.Fatalf("status = %s", &out)
		}
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("status created store")
		}
	})
}

func TestProviderSetupFollowup_Scenario3_CodexLoaderAndReuse(t *testing.T) {
	for _, state := range []string{"missing", "valid", "expired", "invalid"} {
		t.Run(state, func(t *testing.T) {
			expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			if state == "expired" {
				expires = expires.Add(-2 * time.Hour)
			}
			token := codextest.Token(expires, "account-sentinel")
			if state == "invalid" {
				token = "token-sentinel"
			}
			body := fmt.Sprintf("providers:\n  openai-codex:\n    oauth:\n      access_token: %s\n      account_id: account-sentinel\n      expires_at: %s\n", token, expires.Format(time.RFC3339))
			if state == "missing" {
				body = "providers: {}\n"
			}
			sp, ap := followupHome(t, "models:\n  default_provider: openai-codex\n  default: gpt-5\n", body)
			c := newProviderCommands()
			c.terminal.readAPIKey = func(context.Context, string) (string, error) { t.Fatal("Codex prompted for token"); return "", nil }
			c.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
				t.Fatal("Codex wrote credentials")
				return authfile.CommitNotApplied, nil
			}
			c.backend.openOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
				t.Fatal("Codex used OIDC")
				return nil, nil
			}
			before := readProviderCredentialTestFile(t, sp)
			if state == "valid" {
				c.terminal.readField = providerInput(t, "yes", "")
			} else {
				c.terminal.readField = func(context.Context, string) (string, error) {
					t.Fatal("unusable Codex offered default")
					return "", nil
				}
			}
			var out bytes.Buffer
			if err := c.runStatus(context.Background(), resolveInvocation([]string{"mecatui", "providers", "status", "openai-codex"}), &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			want := "invalid or expired"
			switch state {
			case "valid":
				want = "locally usable"
			case "missing":
				want = "missing"
			}
			if !strings.Contains(out.String(), want) {
				t.Fatalf("state = %s", &out)
			}
			if err := c.runSetup(context.Background(), resolveInvocation([]string{"mecatui", "providers", "setup", "openai-codex"}), &out, &out); err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{token, "account-sentinel", expires.Format(time.RFC3339)} {
				if strings.Contains(out.String(), secret) {
					t.Fatal("Codex leaked private metadata")
				}
			}
			if readProviderCredentialTestFile(t, ap) != body || readProviderCredentialTestFile(t, sp) != before {
				t.Fatal("Codex reuse changed files")
			}
		})
	}
}

const followupCustomSettings = "providers:\n  custom:\n    base_url: https://gateway.example\n    api_flavor: openai-responses\n    default_model: declared-model\n    auth:\n      method: none\n"

func TestProviderSetupFollowup_Scenario3_SharedDefaultResolution(t *testing.T) {
	for _, selector := range []string{"", "declared-model", "chosen", "unknown", "inherit", "openai/gpt-5"} {
		t.Run(selector, func(t *testing.T) {
			settings := followupCustomSettings + "models:\n  aliases:\n    chosen: declared-model\n"
			sp, _ := followupHome(t, settings, "")
			c := newProviderCommands()
			args := []string{"mecatui", "providers", "set-default", "custom"}
			if selector != "" {
				args = append(args, selector)
			}
			err := c.runSetDefault(context.Background(), resolveInvocation(args), io.Discard, io.Discard)
			valid := selector == "" || selector == "declared-model" || selector == "chosen"
			if (err == nil) != valid {
				t.Fatalf("selector %q: %v", selector, err)
			}
			if !valid && readProviderCredentialTestFile(t, sp) != settings {
				t.Fatal("rejected default mutated settings")
			}
			if valid {
				inspection, err := inspectLocalProviders()
				if err != nil || inspection.selectedProvider != "custom" {
					t.Fatalf("default not persisted: %v", err)
				}
				if _, _, err := resolveProviderDefaultWithContext(context.Background(), "custom", ""); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	t.Run("setup-shared-default", func(t *testing.T) {
		followupHome(t, followupCustomSettings, "")
		c := newProviderCommands()
		c.terminal.readField = providerInput(t, "yes", "")
		if err := c.runSetup(context.Background(), resolveInvocation([]string{"mecatui", "providers", "setup", "custom"}), io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		inspection, err := inspectLocalProviders()
		if err != nil || inspection.selectedProvider != "custom" || inspection.selectedModel != "declared-model" {
			t.Fatal("setup bypassed shared default")
		}
	})
}

func TestProviderSetupFollowup_Scenario4_UnifiedLifecyclePreserved(t *testing.T) {
	for _, noLogin := range []bool{false, true} {
		t.Run(fmt.Sprint(noLogin), func(t *testing.T) {
			sp, ap := followupHome(t, "{}\n", "")
			c := newProviderCommands()
			c.terminal.readField = providerInput(t, "https://gateway.example", "1", "declared-model", "1", "yes")
			c.terminal.readAPIKey = func(context.Context, string) (string, error) {
				if noLogin {
					t.Fatal("--no-login prompted")
				}
				return "managed-sentinel", nil
			}
			args := []string{"mecatui", "providers", "add", "custom"}
			if noLogin {
				args = append(args, "--no-login")
			}
			if err := c.runAdd(context.Background(), resolveInvocation(args), io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if noLogin {
				if _, err := os.Stat(ap); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("--no-login wrote key")
				}
				return
			}
			if err := c.runSetDefault(context.Background(), resolveInvocation([]string{"mecatui", "providers", "set-default", "custom"}), io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			before := readProviderCredentialTestFile(t, sp)
			if err := c.runCredential(context.Background(), resolveInvocation([]string{"mecatui", "providers", "logout", "custom"}), io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if readProviderCredentialTestFile(t, sp) != before || strings.Contains(readProviderCredentialTestFile(t, ap), "managed-sentinel") {
				t.Fatal("logout changed definition or retained key")
			}
			c.terminal.readRemoval = func(context.Context, string) (bool, error) { return true, nil }
			if err := c.runRemove(context.Background(), resolveInvocation([]string{"mecatui", "providers", "remove", "custom"}), io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			inspection, err := inspectLocalProviders()
			if err != nil || len(inspection.definitions) != 0 || inspection.selectedProvider != "custom" {
				t.Fatalf("remove result: definitions=%d selected=%q err=%v", len(inspection.definitions), inspection.selectedProvider, err)
			}
		})
	}
	// The registered top-level catalog is the command surface: llm must not
	// silently return as an accepted compatibility route.
	for _, command := range topLevelCommands {
		if command.name == "llm" {
			t.Fatal("retired llm command is registered")
		}
	}
	if res := resolveInvocation([]string{"mecatui", "llm", llmActionStatus}); res.err == nil {
		t.Fatal("legacy llm command accepted")
	}

	// Provider lifecycle commands own their grammar. Every transport flag
	// registered for an embedded invocation must be rejected after such a command.
	flags, _, err := parseTransportFlagsTest(t, modeLocal, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	flags.VisitAll(func(f *flag.Flag) {
		if !isHelpMetaFlag("--" + f.Name) {
			names = append(names, f.Name)
		}
	})
	for _, command := range [][]string{{"providers", providerActionLogin, "openai"}, {"providers", providerActionSetup}} {
		for _, name := range names {
			t.Run(strings.Join(command, "/")+"/--"+name, func(t *testing.T) {
				args := append([]string{"mecatui"}, command...)
				args = append(args, "--"+name)
				if res := resolveInvocation(args); res.err == nil {
					t.Fatalf("transport flag accepted by provider lifecycle command: %v", args)
				}
			})
		}
	}
}
