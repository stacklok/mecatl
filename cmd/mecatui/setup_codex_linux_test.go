//go:build linux

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/testutil/codextest"
)

func TestMecatuiLocalProviderSetup_CodexReuseAndBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, input, wantModel, credential string
		wantError                          bool
	}{
		{"select and start", "2\nopenai-codex\nfast\ny\ny\n", "gpt-5", "valid", false},
		{"reject unknown model", "2\nopenai-codex\nunlisted-model-sentinel\n", "", "valid", true},
		{"reject missing credential", "2\nopenai-codex\n", "", "missing", true},
		{"reject expired credential", "2\nopenai-codex\n", "", "expired", true},
		{"reject invalid credential", "2\nopenai-codex\n", "", "invalid", true},
		{"add guidance only", "1\nopenai-codex\n4\n", "", "valid", false},
		{"remove guidance only", "3\nopenai-codex\n4\n", "", "valid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expires := time.Now().Add(24 * time.Hour)
			const account = "account-sentinel"
			token := codextest.Token(expires, account)
			switch tc.credential {
			case "missing":
				token = ""
			case "expired":
				expires = time.Now().Add(-time.Hour)
			case "invalid":
				token = "invalid-token-sentinel"
			}
			path, before := codexSetupFixture(t, token, account, expires.Format(time.RFC3339))
			settings := filepath.Join(filepath.Dir(path), "settings.yaml")
			const originalSettings = "models:\n  aliases:\n    fast: gpt-5\n"
			if err := os.WriteFile(settings, []byte(originalSettings), 0o600); err != nil {
				t.Fatal(err)
			}
			input := codexSetupOutput(t)
			if _, err := input.WriteString(tc.input); err != nil {
				t.Fatal(err)
			}
			if _, err := input.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			out := codexSetupOutput(t)
			starts := 0
			deps := setupDepsForRun(func(argv []string) error {
				starts++
				if strings.Join(argv, "\x00") != strings.Join([]string{"mecatui", "--auth-file", path}, "\x00") {
					t.Fatal("startup lost auth-file override")
				}
				cfg, err := parseRunConfig(resolveInvocation(argv))
				if err != nil {
					return err
				}
				if !cfg.providerKeys.HasOpenAICodex() || cfg.providerKeys.OpenAICodex.Validate(time.Now()) != nil || cfg.providerKeys.OpenAI != "api-key-sentinel" {
					t.Fatal("startup lost the real Codex credential or public OpenAI identity")
				}
				snapshot, err := loadSetupSnapshot(path, true)
				if err != nil {
					return err
				}
				if snapshot.Default != (defaultSelection{Provider: "openai-codex", Model: tc.wantModel}) || snapshot.Aliases["fast"] != "gpt-5" {
					t.Fatal("startup did not re-read saved defaults/aliases")
				}
				for _, row := range snapshot.Rows {
					if row.ID == "openai-codex" && (!row.Default || row.Model != tc.wantModel || row.LocalCredential != credentialLocallyUsable) {
						t.Fatal("Codex status lost the selected default/model or credential state")
					}
				}
				return app.ValidateDeploymentDefaultModel(snapshot.Default.Provider, snapshot.Default.Model, snapshot.ProviderDefaults["openai-codex"])
			})
			deps.isTerminal = func(int) bool { return true }
			deps.updateKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
				t.Fatal("Codex reached API-key writer")
				return "", nil
			}
			services := llmCommandDeps{setup: deps, openNative: func(context.Context, bool, io.Writer) (nativeLLMHost, error) {
				t.Fatal("Codex reached native login/status")
				return nil, nil
			}}
			res := resolveLLMCommand([]string{"setup", "--auth-file", path})
			if res.err != nil {
				t.Fatal(res.err)
			}
			err := runLLMCommandWith(res, input, out, io.Discard, services)
			if (err != nil) != tc.wantError {
				t.Fatalf("setup error = %v", err)
			}
			if (starts == 1) != (tc.wantModel != "") {
				t.Fatalf("unexpected startup count %d", starts)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != before {
				t.Fatal("Codex reuse changed auth bytes")
			}
			if tc.wantModel == "" {
				afterSettings, readErr := os.ReadFile(settings)
				if readErr != nil || string(afterSettings) != originalSettings {
					t.Fatal("guidance or rejected selection changed defaults")
				}
			}
			output, readErr := os.ReadFile(out.Name())
			if readErr != nil {
				t.Fatal(readErr)
			}
			got := string(output)
			for _, forbidden := range []string{"Enter an API/developer key", "Save this credential?", "native in-process login?"} {
				if strings.Contains(got, forbidden) {
					t.Errorf("Codex reached forbidden prompt %q", forbidden)
				}
			}
			for _, secret := range []string{token, account, expires.Format(time.RFC3339), "api-key-sentinel"} {
				if secret != "" && (strings.Contains(got, secret) || (err != nil && strings.Contains(err.Error(), secret))) {
					t.Error("setup exposed credential material")
				}
			}
			for _, want := range []string{"OpenAI (API key)", "OpenAI Codex (existing subscription token)", "interactive sign-in/refresh not supported"} {
				if !strings.Contains(got, want) {
					t.Errorf("setup missing %q", want)
				}
			}
			if tc.wantModel != "" {
				for _, want := range []string{"manual", "entitlement", "no static default"} {
					if !strings.Contains(got, want) {
						t.Errorf("model guidance missing %q", want)
					}
				}
			}
		})
	}
}
