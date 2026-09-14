package permconfig

import (
	"strings"
	"testing"
)

func TestNativeEndpointSchemaLegacyConfigurationRejected(t *testing.T) {
	for _, legacy := range []string{
		"llm:\n  credential_home: /var/lib/mecatl/provider-oidc\n",
		"llm:\n  credential_key: {source: keyring}\n",
		"llm:\n  endpoints: {}\n",
	} {
		r, _ := newCapturedResolver(t, "/etc/mecatl/operator.yaml", legacy, true)
		_, _, err := r.OperatorProviders()
		if err == nil || !strings.Contains(err.Error(), "mecatui providers") {
			t.Fatalf("legacy LLM configuration error = %v, want migration guidance", err)
		}
	}
}
