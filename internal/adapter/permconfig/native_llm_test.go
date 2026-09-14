package permconfig

import (
	"strings"
	"testing"
)

func TestLegacyLLMConfigurationIsIgnored(t *testing.T) {
	current := `
providers:
  current:
    base_url: https://gateway.example/v1
    default_model: current-model
    api_flavor: openai-responses
    auth: {method: none}
credential_store:
  oidc:
    home: /var/lib/mecatl/provider-oidc
    key: {source: keyring}
`
	for name, legacy := range map[string]string{
		"mapping":  "llm:\n  credential_home: /private\n  arbitrary: {nested: true}\n",
		"scalar":   "llm: obsolete\n",
		"sequence": "llm: [obsolete, {shape: accepted}]\n",
	} {
		t.Run(name, func(t *testing.T) {
			alone, aloneDiagnostics := newCapturedResolver(t, "/etc/mecatl/operator.yaml", legacy, true)
			providers, overrides, err := alone.OperatorProviders()
			if err != nil || providers != nil || overrides != nil {
				t.Fatalf("standalone llm key was not ignored: providers=%v overrides=%v err=%v", providers, overrides, err)
			}
			if strings.Contains(strings.ToLower(aloneDiagnostics.String()), "llm") {
				t.Fatalf("standalone legacy llm key emitted a warning: %s", aloneDiagnostics.String())
			}

			withCurrent, currentDiagnostics := newCapturedResolver(t, "/etc/mecatl/operator.yaml", legacy+current, true)
			providers, _, err = withCurrent.OperatorProviders()
			if err != nil {
				t.Fatalf("OperatorProviders: %v", err)
			}
			if _, ok := providers["current"]; !ok {
				t.Fatalf("current provider missing: %+v", providers)
			}
			if strings.Contains(strings.ToLower(currentDiagnostics.String()), "llm") {
				t.Fatalf("legacy llm key alongside current configuration emitted a warning: %s", currentDiagnostics.String())
			}
		})
	}
}

func TestLegacyLLMDoesNotRelaxCurrentProviderValidation(t *testing.T) {
	for name, input := range map[string]string{
		"invalid YAML":                  "llm: [\n",
		"invalid auth":                  "llm: obsolete\nproviders:\n  current:\n    base_url: https://gateway.example\n    default_model: model\n    api_flavor: openai-responses\n    auth: {method: invalid}\n",
		"invalid credential store":      "llm: obsolete\ncredential_store:\n  oidc:\n    home: /var/lib/mecatl/provider-oidc\n    key: {source: unsupported}\n",
		"missing OIDC credential store": "llm: obsolete\nproviders:\n  current:\n    base_url: https://gateway.example/v1\n    default_model: current-model\n    api_flavor: openai-responses\n    auth:\n      method: oidc\n      oidc:\n        issuer: https://issuer.example\n        client_id: mecatl\n        scopes: [openid]\n        issuer_trust: {policy: public}\n        gateway_trust: {policy: public}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseYAML([]byte(input)); err == nil {
				t.Fatal("parseYAML accepted invalid current configuration")
			}
		})
	}
}
