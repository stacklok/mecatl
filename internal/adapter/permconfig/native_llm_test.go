package permconfig

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
)

const validNativeLLMYAML = `
llm:
  credential_home: /var/lib/mecatl/provider-oidc
  endpoints:
    corp-gateway:
      protocol: openai-responses
      url: https://Gateway.EXAMPLE:443//tenant/%7ev1/
      default_model: corp-model
      oidc:
        issuer: https://issuer.example/realms/corp
        client_id: mecatl
        resource_audience: https://gateway.example
        scopes: [models.read, offline_access, models.read]
      issuer_trust:
        policy: public
      gateway_trust:
        policy: private-ca
        ca_bundle: /etc/mecatl/gateway-ca.pem
`

func TestInvariant_native_llm_endpoint_facade_normalizes_once(t *testing.T) {
	r, _ := newCapturedResolver(t, "/etc/mecatl/operator.yaml", validNativeLLMYAML, true)
	providers, _, err := r.OperatorProviders()
	if err != nil {
		t.Fatalf("OperatorProviders: %v", err)
	}
	got, ok := providers["corp-gateway"]
	if !ok {
		t.Fatalf("native endpoint was not normalized into provider definitions: %+v", providers)
	}
	if got.ID != "corp-gateway" || got.APIFlavor != "openai-responses" || got.BaseURL != "https://gateway.example/tenant/~v1" || got.DefaultModel != "corp-model" || got.Native == nil {
		t.Fatalf("normalized provider = %+v", got)
	}
	if got.Native.CredentialHome != "/var/lib/mecatl/provider-oidc" || strings.Join(got.Native.OIDC.Scopes, ",") != "models.read,offline_access" {
		t.Fatalf("native identity was not canonicalized once: %+v", got.Native)
	}

	for _, input := range []string{
		strings.Replace(validNativeLLMYAML, "corp-gateway:", "toolhive:", 1),
		strings.Replace(validNativeLLMYAML, "openai-responses", "openai-chat-completions", 1),
		validNativeLLMYAML + "providers:\n  corp-gateway:\n    base_url: https://legacy.example\n    default_model: m\n    api_flavor: openai-responses\n",
	} {
		t.Run("rejected", func(t *testing.T) {
			r, _ := newCapturedResolver(t, "/etc/mecatl/operator.yaml", input, true)
			if _, _, err := r.OperatorProviders(); err == nil {
				t.Fatal("invalid/reserved/colliding endpoint configuration was accepted")
			}
		})
	}
}

func TestNativeLLMGatewayLogin_Scenario1_StrictOperatorTierOnly(t *testing.T) {
	r, logs := newCapturedResolver(t, "/etc/mecatl/operator.yaml", validNativeLLMYAML, true)
	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	projectSecret := "project-credential-home-canary"
	projectURL := "project-endpoint-canary.example"
	ws.seed(t, projectFileMecatl, `
llm:
  credential_home: /`+projectSecret+`
  endpoints:
    project:
      protocol: openai-responses
      url: https://`+projectURL+`
      default_model: m
      oidc:
        issuer: https://issuer.example
        client_id: c
        resource_audience: r
        scopes: [s]
      issuer_trust: {policy: public}
      gateway_trust: {policy: public}
`)
	r.Resolve(context.Background(), ws)
	providers, _, err := r.OperatorProviders()
	if err != nil {
		t.Fatalf("OperatorProviders: %v", err)
	}
	if _, exists := providers["project"]; exists {
		t.Fatal("project-tier native endpoint became authoritative")
	}
	log := logs.String()
	if !strings.Contains(log, "IGNORING project-tier llm") || strings.Contains(log, projectSecret) || strings.Contains(log, projectURL) {
		t.Fatalf("project warning was missing or disclosed values: %s", log)
	}

	invalid := []string{
		strings.Replace(validNativeLLMYAML, "  credential_home: /var/lib/mecatl/provider-oidc\n", "", 1),
		strings.Replace(validNativeLLMYAML, "https://Gateway.EXAMPLE:443//tenant/%7ev1/", "http://gateway.example/v1", 1),
		strings.Replace(validNativeLLMYAML, "policy: public", "policy: private-ca", 1),
		strings.Replace(validNativeLLMYAML, "policy: private-ca\n        ca_bundle: /etc/mecatl/gateway-ca.pem", "policy: public\n        ca_bundle: /etc/mecatl/gateway-ca.pem", 1),
		strings.Replace(validNativeLLMYAML, "models.read, offline_access, models.read", "models.read, bad scope", 1),
		strings.Replace(validNativeLLMYAML, "      default_model: corp-model", "      default_model: corp-model\n      unknown: value", 1),
		strings.Replace(validNativeLLMYAML, "      default_model: corp-model", "      default_model: corp-model\n      allowed_principals: [alice]", 1),
	}
	for _, input := range invalid {
		t.Run("fail_closed", func(t *testing.T) {
			r, _ := newCapturedResolver(t, "/etc/mecatl/operator.yaml", input, true)
			if _, _, err := r.OperatorProviders(); err == nil {
				t.Fatal("invalid native endpoint configuration was accepted")
			}
		})
	}
}
