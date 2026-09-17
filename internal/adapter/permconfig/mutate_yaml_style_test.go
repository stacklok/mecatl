package permconfig

import (
	"strings"
	"testing"
)

func TestMutateProviderMapRemovesFinalProvidersSection(t *testing.T) {
	definition := ProviderDefinition{
		BaseURL: "https://custom.example/v1", DefaultModel: "custom-model", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"},
	}
	out, unchanged, err := mutateProviderMap([]byte("providers:\n  custom:\n    base_url: https://custom.example/v1\n    default_model: custom-model\n    api_flavor: openai-responses\n    auth: {method: none}\n"), ProviderMapUpdate{Provider: "custom", ExpectedDefinition: &definition})
	if err != nil || unchanged {
		t.Fatalf("mutateProviderMap = (%q, %v, %v), want changed success", out, unchanged, err)
	}
	if got := string(out); got != "" {
		t.Fatalf("final provider should leave an empty document, got %q", got)
	}
	if err := ValidateYAML(out); err != nil {
		t.Fatalf("updated settings are invalid: %v\n%s", err, out)
	}
}

func TestRemoveOIDCCredentialStoreRemovesFinalSection(t *testing.T) {
	store := OIDCCredentialStore{Home: "/var/lib/mecatl/provider-oidc", Key: NativeCredentialKey{Source: "keyring"}}
	definition := ProviderDefinition{
		BaseURL: "https://custom.example/v1", DefaultModel: "custom-model", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"},
	}
	out, unchanged, err := mutateProviderMap([]byte("credential_store:\n  oidc:\n    home: /var/lib/mecatl/provider-oidc\n    key: {source: keyring}\nproviders:\n  custom:\n    base_url: https://custom.example/v1\n    default_model: custom-model\n    api_flavor: openai-responses\n    auth: {method: none}\n"), ProviderMapUpdate{Provider: "custom", ExpectedDefinition: &definition, RemoveOIDCCredentialStore: &store})
	if err != nil || unchanged {
		t.Fatalf("mutateProviderMap = (%q, %v, %v), want changed success", out, unchanged, err)
	}
	if got := string(out); got != "" {
		t.Fatalf("final credential store should leave an empty document, got %q", got)
	}
	if err := ValidateYAML(out); err != nil {
		t.Fatalf("updated settings are invalid: %v\n%s", err, out)
	}
}

func TestMutateDefaultsUsesBlockStyleForEmptyDocument(t *testing.T) {
	out, unchanged, err := mutateDefaults(nil, DefaultUpdate{Provider: "openai", Model: "gpt-5"})
	if err != nil || unchanged {
		t.Fatalf("mutateDefaults = (%q, %v, %v), want changed success", out, unchanged, err)
	}
	if got, want := string(out), "models:\n  default_provider: 'openai'\n  default: 'gpt-5'\n"; got != want {
		t.Fatalf("mutateDefaults output = %q, want %q", got, want)
	}
}

func TestMutateProviderMapKeepsExistingFlowStyleWhenRemovingSibling(t *testing.T) {
	definition := ProviderDefinition{
		BaseURL: "https://custom.example/v1", DefaultModel: "custom-model", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"},
	}
	out, unchanged, err := mutateProviderMap([]byte("providers: {custom: {base_url: https://custom.example/v1, default_model: custom-model, api_flavor: openai-responses, auth: {method: none}}, retained: {base_url: https://retained.example/v1, default_model: retained-model, api_flavor: openai-responses, auth: {method: none}}}\n"), ProviderMapUpdate{Provider: "custom", ExpectedDefinition: &definition})
	if err != nil || unchanged {
		t.Fatalf("mutateProviderMap = (%q, %v, %v), want changed success", out, unchanged, err)
	}
	if !strings.Contains(string(out), "providers: {retained:") {
		t.Fatalf("existing flow-style providers section was reformatted:\n%s", out)
	}
}
