package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/testutil/codextest"
)

func codexSetupFixture(t *testing.T, token, account, expiry string) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	for _, name := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_API_KEY"} {
		t.Setenv(name, "")
	}
	managed := filepath.Join(home, "mecatl")
	if err := os.Mkdir(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "# preserve public API identity\nproviders:\n  openai:\n    api_key: api-key-sentinel\n"
	if token != "" {
		body += fmt.Sprintf("  openai-codex:\n    oauth:\n      access_token: %s\n      account_id: %s\n      expires_at: '%s'\n", token, account, expiry)
	}
	path := filepath.Join(managed, "selected-auth.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, body
}

func codexSetupOutput(t *testing.T) *os.File {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	return out
}

func TestMecatuiLocalProviderSetup_CodexPassiveStatus(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-time.Hour)
	const account = "account-sentinel"
	for _, tc := range []struct {
		name, token, expiry, state string
		usable                     bool
	}{
		{"valid", codextest.Token(future, account), future.Format(time.RFC3339), "locally usable", true},
		{"missing", "", "", "missing", false},
		{"expired JWT", codextest.Token(past, account), future.Format(time.RFC3339), "invalid or expired", false},
		{"expired explicit", codextest.Token(future, account), past.Format(time.RFC3339), "invalid or expired", false},
		{"invalid token", "invalid-token-sentinel", future.Format(time.RFC3339), "invalid or expired", false},
		{"invalid expiry", codextest.Token(future, account), "invalid-expiry-sentinel", "invalid or expired", false},
		{"account mismatch", codextest.Token(future, "other-account-sentinel"), future.Format(time.RFC3339), "invalid or expired", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, before := codexSetupFixture(t, tc.token, account, tc.expiry)
			t.Setenv("OPENAI_ACCESS_TOKEN", codextest.Token(future, account))
			t.Setenv("OPENAI_CODEX_ACCESS_TOKEN", codextest.Token(future, account))
			t.Setenv("OPENAI_API_KEY", "environment-api-sentinel")
			deps := defaultSetupDeps()
			deps.updateKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
				t.Fatal("credential mutation during status")
				return "", nil
			}
			deps.updateDefaults = func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
				t.Fatal("defaults mutation during status")
				return "", nil
			}
			deps.start = func(string) error { t.Fatal("startup during status"); return nil }
			services := llmCommandDeps{setup: deps, openNative: func(context.Context, bool, io.Writer) (nativeLLMHost, error) {
				t.Fatal("native login/status for Codex")
				return nil, nil
			}}
			out := codexSetupOutput(t)
			res := resolveLLMCommand([]string{"status", "--auth-file", path})
			if res.err != nil {
				t.Fatal(res.err)
			}
			if err := runLLMCommandWith(res, os.Stdin, out, io.Discard, services); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(out.Name())
			if err != nil {
				t.Fatal(err)
			}
			got := string(body)
			for _, want := range []string{"OpenAI (API key)", "OpenAI Codex (existing subscription token)", "local credential: " + tc.state, "verification: not checked", "interactive sign-in/refresh not supported", "https://mecatl.dev/docs/building/deployment/mecatui#experimental-chatgpt-codex-subscription"} {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in status", want)
				}
			}
			for _, secret := range []string{tc.token, account, tc.expiry, "api-key-sentinel", "environment-api-sentinel"} {
				if secret != "" && strings.Contains(got, secret) {
					t.Error("status exposed credential material")
				}
			}
			snapshot, err := loadSetupSnapshot(path, true)
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(defaultProviderIDs(snapshot), "openai-codex") != tc.usable {
				t.Errorf("Codex default eligibility does not match runtime local validation")
			}
			if slices.Contains(defaultProviderIDsAfterRemoval(snapshot, "openai"), "openai-codex") != tc.usable {
				t.Error("replacement default eligibility lost Codex state")
			}
			if slices.Contains(mutableProviderIDs(snapshot), "openai-codex") {
				t.Error("Codex offered for API-key mutation")
			}
			if len(snapshot.Models["openai-codex"]) != 0 || snapshot.ProviderDefaults["openai-codex"] != "" {
				t.Error("invented offline Codex inventory/default")
			}
			if !slices.Contains(defaultProviderIDs(snapshot), "openai") {
				t.Error("public OpenAI identity lost")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != before {
				t.Fatal("status changed auth file")
			}
			// The override must not fall back to a usable token from the conventional path.
			if err := os.Rename(path, filepath.Join(filepath.Dir(path), "auth.yaml")); err != nil {
				t.Fatal(err)
			}
			missing, err := loadSetupSnapshot(path, true)
			if err != nil || !missing.ExplicitMissing || slices.Contains(defaultProviderIDs(missing), "openai-codex") {
				t.Fatal("missing override used conventional credentials")
			}
			conventionalOut := codexSetupOutput(t)
			if err := runLLMCommandWith(resolveLLMCommand([]string{"status"}), os.Stdin, conventionalOut, io.Discard, services); err != nil {
				t.Fatal(err)
			}
			conventional, err := os.ReadFile(conventionalOut.Name())
			if err != nil || !strings.Contains(string(conventional), "local credential: "+tc.state) {
				t.Fatal("conventional auth path not respected")
			}
		})
	}
}
