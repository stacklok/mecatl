package permconfig

import (
	"strings"
	"testing"
)

func TestProviderUnification_Scenario1_CustomProviderAuthSchema(t *testing.T) {
	cfg, err := parseYAML([]byte(`
providers:
  keyed:
    base_url: https://keyed.example/v1
    default_model: keyed-model
    api_flavor: openai-responses
    auth:
      method: api_key
  anonymous:
    base_url: https://anonymous.example/v1
    default_model: anonymous-model
    api_flavor: openai-chat-completions
    auth:
      method: none
  oidc:
    base_url: https://oidc.example/v1
    default_model: oidc-model
    api_flavor: openai-responses
    auth:
      method: oidc
      oidc:
        issuer: https://issuer.example
        client_id: mecatl
        scopes: [openid, offline_access]
        issuer_trust: {policy: public}
        gateway_trust: {policy: public}
credential_store:
  oidc:
    home: /var/lib/mecatl/provider-oidc
    key: {source: keyring}
`))
	if err != nil {
		t.Fatalf("parseYAML: %v", err)
	}
	if got := cfg.Providers["keyed"].Auth.Method; got != "api_key" {
		t.Fatalf("keyed auth method = %q, want api_key", got)
	}
	if got := cfg.Providers["anonymous"].Auth.Method; got != "none" {
		t.Fatalf("anonymous auth method = %q, want none", got)
	}
	oidc := cfg.Providers["oidc"]
	if oidc.Auth.Method != "oidc" || oidc.Auth.OIDC == nil || oidc.Auth.OIDC.ClientID != "mecatl" || oidc.Auth.OIDC.CredentialStore == nil {
		t.Fatalf("OIDC provider was not retained: %+v", oidc)
	}
}

func TestProviderUnification_Scenario1_OIDCProviderSchemaFailsClosed(t *testing.T) {
	valid := `
providers:
  oidc:
    base_url: https://oidc.example/v1
    default_model: oidc-model
    api_flavor: openai-responses
    auth:
      method: oidc
      oidc:
        issuer: https://issuer.example
        client_id: mecatl
        scopes: [openid]
        issuer_trust: {policy: public}
        gateway_trust: {policy: public}
credential_store:
  oidc:
    home: /var/lib/mecatl/provider-oidc
    key: {source: keyring}
`
	if _, err := parseYAML([]byte(valid)); err != nil {
		t.Fatalf("valid unified OIDC config rejected: %v", err)
	}
	for _, input := range []string{
		strings.Replace(valid, "method: oidc", "method: api_key", 1),
		strings.Replace(valid, "api_flavor: openai-responses", "api_flavor: anthropic-messages", 1),
		strings.Replace(valid, "issuer_trust: {policy: public}\n", "", 1),
		strings.Replace(valid, "key: {source: keyring}", "key: {source: environment}", 1),
	} {
		if _, err := parseYAML([]byte(input)); err == nil {
			t.Fatal("invalid unified OIDC config was accepted")
		}
	}
}

func TestProviderUnification_Scenario1_ProviderIdentityCollisionAndSelectorFailure(t *testing.T) {
	for _, input := range []string{
		"providers:\n  openai:\n    base_url: https://x.example\n    default_model: m\n    api_flavor: openai-responses\n    auth: {method: none}",
		"providers:\n  duplicate:\n    base_url: https://x.example\n    default_model: m\n    api_flavor: openai-responses\n    auth: {method: none}\n  duplicate:\n    base_url: https://y.example\n    default_model: m\n    api_flavor: openai-responses\n    auth: {method: none}",
	} {
		if _, err := parseYAML([]byte(input)); err == nil {
			t.Fatal("provider identity collision was accepted")
		}
	}
}
