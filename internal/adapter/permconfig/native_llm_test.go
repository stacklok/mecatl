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

func TestNativeCredentialKeySchema(t *testing.T) {
	for _, tc := range []struct {
		name, value, source, keyEnv string
		valid                       bool
	}{
		{name: "omitted", source: "keyring", valid: true},
		{name: "keyring", value: "{source: keyring}", source: "keyring", valid: true},
		{name: "environment", value: "{source: environment, key_env: MECATL_NATIVE_LLM_CREDENTIAL_KEY}", source: "environment", keyEnv: "MECATL_NATIVE_LLM_CREDENTIAL_KEY", valid: true},
		{name: "other reference", value: "{source: environment, key_env: MECATL_OTHER_KEY}", source: "environment", keyEnv: "MECATL_OTHER_KEY", valid: true},
		{name: "missing source", value: "{}"},
		{name: "null", value: "null"},
		{name: "unknown source", value: "{source: plaintext}"},
		{name: "unknown field", value: "{source: keyring, fallback: environment}"},
		{name: "duplicate", value: "{source: keyring, source: environment}"},
		{name: "missing reference", value: "{source: environment}"},
		{name: "empty reference", value: "{source: environment, key_env: ''}"},
		{name: "null reference", value: "{source: environment, key_env: null}"},
		{name: "wrong prefix", value: "{source: environment, key_env: SECRET_KEY}"},
		{name: "invalid reference", value: "{source: environment, key_env: 'MECATL_KEY$(x)'}"},
		{name: "keyring reference", value: "{source: keyring, key_env: MECATL_KEY}"},
		{name: "keyring empty reference", value: "{source: keyring, key_env: ''}"},
		{name: "keyring null reference", value: "{source: keyring, key_env: null}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validNativeLLMYAML
			if tc.value != "" {
				input = strings.Replace(input, "llm:\n", "llm:\n  credential_key: "+tc.value+"\n", 1)
			}
			if err := ValidateYAML([]byte(input)); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if !tc.valid {
				return
			}
			r, _ := newCapturedResolver(t, "/etc/mecatl/operator.yaml", input, true)
			providers, _, err := r.OperatorProviders()
			if err != nil {
				t.Fatal(err)
			}
			if providers["corp-gateway"].Native.CredentialKey != (NativeCredentialKey{Source: tc.source, KeyEnv: tc.keyEnv}) {
				t.Fatal("shared credential key selection lost during normalization")
			}
		})
	}
	input := strings.Replace(validNativeLLMYAML, "      protocol:", "      credential_key: {source: keyring}\n      protocol:", 1)
	if err := ValidateYAML([]byte(input)); err == nil {
		t.Fatal("per-endpoint key selection accepted")
	}
}

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
	if got.Native.CredentialHome != "/var/lib/mecatl/provider-oidc" || got.Native.OIDC.ResourceAudience != "https://gateway.example" || strings.Join(got.Native.OIDC.Scopes, ",") != "models.read,offline_access" {
		t.Fatalf("native identity was not canonicalized once: %+v", got.Native)
	}

	withoutAudience := strings.Replace(validNativeLLMYAML, "        resource_audience: https://gateway.example\n", "", 1)
	r, _ = newCapturedResolver(t, "/etc/mecatl/operator.yaml", withoutAudience, true)
	providers, _, err = r.OperatorProviders()
	if err != nil || providers["corp-gateway"].Native == nil || providers["corp-gateway"].Native.OIDC.ResourceAudience != "" {
		t.Fatalf("optional resource audience was rejected or populated: providers=%+v err=%v", providers, err)
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
  credential_key: {source: environment, key_env: MECATL_PROJECT_KEY_CANARY}
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
	if providers["corp-gateway"].Native.CredentialKey.Source != "keyring" {
		t.Fatal("project-tier key source became authoritative")
	}
	log := logs.String()
	if !strings.Contains(log, "IGNORING project-tier llm") || strings.Contains(log, projectSecret) || strings.Contains(log, projectURL) || strings.Contains(log, "MECATL_PROJECT_KEY_CANARY") {
		t.Fatalf("project warning was missing or disclosed values: %s", log)
	}

	invalid := []string{
		strings.Replace(validNativeLLMYAML, "  credential_home: /var/lib/mecatl/provider-oidc\n", "", 1),
		strings.Replace(validNativeLLMYAML, "https://Gateway.EXAMPLE:443//tenant/%7ev1/", "http://gateway.example/v1", 1),
		strings.Replace(validNativeLLMYAML, "policy: public", "policy: private-ca", 1),
		strings.Replace(validNativeLLMYAML, "policy: private-ca\n        ca_bundle: /etc/mecatl/gateway-ca.pem", "policy: public\n        ca_bundle: /etc/mecatl/gateway-ca.pem", 1),
		strings.Replace(validNativeLLMYAML, "models.read, offline_access, models.read", "models.read, bad scope", 1),
		strings.Replace(validNativeLLMYAML, "https://gateway.example\n        scopes:", "'   '\n        scopes:", 1),
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
