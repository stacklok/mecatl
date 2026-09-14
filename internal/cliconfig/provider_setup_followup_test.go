package cliconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/testutil/codextest"
)

func TestProviderSetupFollowup_Scenario3_CodexLoaderAndReuse(t *testing.T) {
	for _, state := range []string{"valid", "expired", "invalid", "missing"} {
		t.Run(state, func(t *testing.T) {
			clearProviderEnv(t)
			expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			if state == "expired" {
				expires = expires.Add(-2 * time.Hour)
			}
			const account = "private-account-sentinel"
			token := codextest.Token(expires, account)
			if state == "invalid" {
				token = "private-invalid-token-sentinel"
			}
			t.Setenv("OPENAI_CODEX_ACCESS_TOKEN", token)
			t.Setenv("CODEX_ACCESS_TOKEN", token)
			t.Setenv("OPENAI_ACCESS_TOKEN", token)
			writeAuthFile(t, "providers: {}\n")
			flags := &ProviderFlags{}
			initial := flags.Resolve()
			loader := NewProviderCredentialResolver(flags, initial)
			path := filepath.Join(t.TempDir(), "configured.yaml")
			body := codexAuthYAML(token, account, expires)
			if state == "missing" {
				body = "providers: {}\n"
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			loader.SetAPIKeyFile(path)
			runtime, _, err := loader.Load(nil)
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := ResolveProviderCredentials(flags, nil, xdgconfig.OSEnv)
			if err != nil {
				t.Fatal(err)
			}
			want := state == "valid"
			if inspection.HasOpenAICodex() != want || runtime.OpenAICodexCredential.Validate(time.Now()) == nil != want {
				t.Fatal("configured runtime reload and inspection must agree on local credential usability")
			}
			legacy := flags.Resolve()
			if legacy.OpenAICodex != inspection.OpenAICodex || legacy.AuthFileWarning != inspection.AuthFileWarning {
				t.Fatal("legacy and strict loaders disagree")
			}
			if (inspection.AuthFileWarning != "") != (state == "expired" || state == "invalid") {
				t.Fatal("invalid credential must be distinguishable from absent credential")
			}
			for _, secret := range []string{token, account, expires.Format(time.RFC3339)} {
				if strings.Contains(inspection.AuthFileWarning, secret) {
					t.Fatal("credential warning leaked private material")
				}
			}
			if err := os.WriteFile(path, []byte("providers: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cached, err := ResolveProviderCredentials(flags, nil, xdgconfig.OSEnv)
			if err != nil || cached.OpenAICodex != legacy.OpenAICodex {
				t.Fatal("resolved file snapshot was reread")
			}
		})
	}
}

func TestProviderSetupFollowup_Scenario1_RuntimeCredentialParity(t *testing.T) {
	for _, tc := range []struct {
		name, routerEnv, openAIEnv, routerFile string
		wantAvailable                          bool
	}{
		{"OpenAI file is not a fallback", "", "", "", false},
		{"OpenAI environment is a fallback", "", "env-openai", "", true},
		{"own file before OpenAI environment", "", "env-openai", "file-router", true},
		{"own environment before own file", "env-router", "env-openai", "file-router", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderEnv(t)
			t.Setenv(envOpenAIKey, tc.openAIEnv)
			t.Setenv(envOpenRouterKey, tc.routerEnv)
			t.Setenv("GATEWAY_API_KEY", "not-a-custom-fallback")
			body := "providers:\n  openai:\n    api_key: file-openai\n"
			if tc.routerFile != "" {
				body += "  openrouter:\n    api_key: " + tc.routerFile + "\n"
			}
			path := writeAuthFile(t, body)
			flags := &ProviderFlags{}
			flags.SetAPIKeyFile(path)
			defs := permconfig.ProviderDefinitions{"gateway": {ID: "gateway", Auth: permconfig.ProviderAuth{Method: "api_key"}}}
			keys, err := ResolveProviderCredentials(flags, defs, xdgconfig.OSEnv)
			if err != nil {
				t.Fatal(err)
			}
			wantKey := tc.routerEnv
			if wantKey == "" {
				wantKey = tc.routerFile
			}
			if keys.OpenRouter != wantKey || keys.CustomAvailable("gateway") {
				t.Fatal("loader violated own-key precedence or custom file-only custody")
			}
			cfg := app.Config{DefaultProvider: "openrouter", DefaultModel: "openai/gpt-5-mini"}
			flags.ApplyResolved(&cfg, keys)
			provider, _, err := app.ResolveDeploymentDefault(t.Context(), cfg)
			if (err == nil) != tc.wantAvailable || (err == nil && provider != "openrouter") {
				t.Fatalf("composition-owned environment fallback availability = %v, want %v", err == nil, tc.wantAvailable)
			}
		})
	}
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "configured", true: "CLI override"}[explicit], func(t *testing.T) {
			clearProviderEnv(t)
			writeAuthFile(t, "providers: {}\n")
			path := filepath.Join(t.TempDir(), "custody.yaml")
			body := "providers:\n  openai:\n    api_key: file-openai\n  openrouter:\n    api_key: file-router\n  anthropic:\n    api_key: file-anthropic\n  opencode:\n    api_key: file-opencode\n  gateway:\n    api_key: file-custom\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			flags := &ProviderFlags{}
			if explicit {
				flags.authFile = &path
			}
			initial := flags.Resolve()
			loader := NewProviderCredentialResolver(flags, initial)
			if explicit {
				loader.SetAPIKeyFile(filepath.Join(t.TempDir(), "must-not-read"))
			} else {
				loader.SetAPIKeyFile(path)
			}
			t.Setenv(envOpenAIKey, "env-openai")
			t.Setenv("GATEWAY_API_KEY", "not-a-custom-fallback")
			// Capture the CLI override snapshot after setting the environment, just as startup does.
			if explicit {
				loader = NewProviderCredentialResolver(flags, flags.Resolve())
			}
			defs := permconfig.ProviderDefinitions{"gateway": {ID: "gateway", Auth: permconfig.ProviderAuth{Method: "api_key"}}}
			runtime, _, err := loader.Load(defs)
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := ResolveProviderCredentials(flags, defs, xdgconfig.OSEnv)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.OpenAIKey != "env-openai" || runtime.OpenRouterKey != "file-router" || runtime.AnthropicKey != "file-anthropic" || runtime.OpenCodeKey != "file-opencode" || runtime.CustomProviderAPIKeys["gateway"] != "file-custom" {
				t.Fatal("runtime violated configured custody or credential precedence")
			}
			if runtime.OpenAIKey != inspection.OpenAI || runtime.OpenRouterKey != inspection.OpenRouter || runtime.AnthropicKey != inspection.Anthropic || runtime.OpenCodeKey != inspection.OpenCode || runtime.CustomProviderAPIKeys["gateway"] != inspection.CustomAPIKey("gateway") {
				t.Fatal("runtime and inspection disagree")
			}
		})
	}
}
