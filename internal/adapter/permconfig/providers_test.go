package permconfig

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
)

func TestValidateProviderID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      string
		wantErr string
	}{
		{"valid", "myprovider", ""},
		{"valid with hyphen and digits", "my-provider2", ""},
		{"uppercase rejected", "MYPROVIDER", "lowercase letter"},
		{"mixed case rejected", "MyProvider", "lowercase letter"},
		{"reserved rejected", "openai", "reserved"},
		{"empty rejected", "", "lowercase letter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProviderID(tc.id)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateProviderID(%q) = %v, want nil", tc.id, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateProviderID(%q) = %v, want error containing %q", tc.id, err, tc.wantErr)
			}
		})
	}
}

func TestOperatorDefinedLLMProviders_Scenario1_ValidDefinition(t *testing.T) {
	cfg, err := parseYAML([]byte(`
providers:
  gateway:
    base_url: https://gateway.example/v1
    default_model: gateway-chat
    api_flavor: openai-responses
    auth:
      method: api_key
provider_overrides:
  openai:
    base_url: https://openai-proxy.example/v1
`))
	if err != nil {
		t.Fatalf("parseYAML: %v", err)
	}
	provider := cfg.Providers["gateway"]
	if provider.ID != "gateway" || provider.BaseURL != "https://gateway.example/v1" || provider.DefaultModel != "gateway-chat" || provider.APIFlavor != "openai-responses" || provider.Auth.Method != "api_key" {
		t.Fatalf("provider = %+v, want validated gateway definition", provider)
	}
	if got := cfg.ProviderOverrides["openai"].BaseURL; got != "https://openai-proxy.example/v1" {
		t.Fatalf("openai override = %q", got)
	}
}

func TestOperatorDefinedLLMProviders_Scenario1_InvalidDefinitionsFailClosed(t *testing.T) {
	cases := []string{
		"providers:\n  OpenAI:\n    base_url: https://x.example\n    default_model: m\n    api_flavor: openai-responses",
		"providers:\n  custom:\n    base_url: http://x.example\n    default_model: m\n    api_flavor: openai-responses",
		"providers:\n  custom:\n    base_url: https://key@x.example/?token=secret\n    default_model: m\n    api_flavor: openai-responses",
		"providers:\n  custom:\n    base_url: https://x.example\n    default_model: m\n    api_flavor: invalid",
		"providers:\n  custom:\n    base_url: https://x.example\n    api_flavor: openai-responses",
		"providers:\n  mock:\n    base_url: https://x.example\n    default_model: m\n    api_flavor: openai-responses",
		"providers:\n  toolhive-anthropic:\n    base_url: https://x.example\n    default_model: m\n    api_flavor: anthropic-messages",
		"provider_overrides:\n  toolhive:\n    base_url: https://x.example",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := parseYAML([]byte(input))
			if err == nil {
				t.Fatal("parseYAML succeeded for invalid provider configuration")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error leaked a configuration value: %v", err)
			}
		})
	}
}

func TestOperatorDefinedLLMProviders_CredentialStoreParseFailureFailsClosed(t *testing.T) {
	r, _ := newCapturedResolver(t, "/etc/mecatl/operator.yaml", "credential_store:\n  oidc:\n    home: /var/lib/mecatl\n    key: {source: unsupported}\n", true)
	_, _, err := r.OperatorProviders()
	if err == nil || err.Error() != "operator provider configuration is invalid" {
		t.Fatalf("OperatorProviders error = %v, want fail-closed provider configuration error", err)
	}
}

func TestInvariant_custom_providers_operator_tier_only(t *testing.T) {
	r, logs := newCapturedResolver(t, "/etc/mecatl/operator.yaml", `
providers:
  operator-gateway:
    base_url: https://operator.example
    default_model: operator-model
    api_flavor: openai-responses
`, true)
	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, `
providers:
  project-gateway:
    base_url: https://project.example
    default_model: project-model
    api_flavor: openai-responses
provider_overrides:
  openai:
    base_url: https://project.example
`)
	r.Resolve(context.Background(), ws)
	providers, overrides, err := r.OperatorProviders()
	if err != nil {
		t.Fatalf("OperatorProviders: %v", err)
	}
	if _, ok := providers["operator-gateway"]; !ok {
		t.Fatalf("operator provider missing: %+v", providers)
	}
	if _, ok := providers["project-gateway"]; ok || overrides != nil {
		t.Fatalf("project provider configuration became authoritative: providers=%+v overrides=%+v", providers, overrides)
	}
	if log := logs.String(); !strings.Contains(log, "IGNORING project-tier providers") || !strings.Contains(log, "IGNORING project-tier provider_overrides") || strings.Contains(log, "project.example") {
		t.Fatalf("project-tier warning must be present and value-free: %s", log)
	}
}

func TestADR_0238_BuiltinOverrideAllowlist(t *testing.T) {
	for _, id := range []string{"openai", "openrouter", "anthropic", "opencode"} {
		if _, err := parseYAML([]byte("provider_overrides:\n  " + id + ":\n    base_url: https://proxy.example")); err != nil {
			t.Fatalf("%s override rejected: %v", id, err)
		}
	}
	for _, id := range []string{"openai-codex", "toolhive", "toolhive-anthropic"} {
		if _, err := parseYAML([]byte("provider_overrides:\n  " + id + ":\n    base_url: https://proxy.example")); err == nil {
			t.Fatalf("%s override accepted", id)
		}
	}
}
