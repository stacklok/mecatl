package configgen

import (
	"os"
	"strings"
	"testing"
)

func TestProviderConfiguration_SharedGatewayIdentityDocumentation(t *testing.T) {
	doc := readProviderDoc(t, "../../docs/architecture/providers.md")
	for _, want := range []string{
		"Gateway authority is deployment-scoped", "gateway identity", "quota",
		"gateway-side audit/retention posture", "model availability",
		"dedicated deployment/service identity",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("provider documentation must state %q", want)
		}
	}
}

func TestProviderConfiguration_UpstreamAuthorizationBoundary(t *testing.T) {
	doc := readProviderDoc(t, "../../docs/architecture/providers.md")
	for _, want := range []string{
		"raw inbound caller bearers are dropped after authentication",
		"never forwarded or retained", "Separate deployments",
		"RFC 8693 token-exchange contracts",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("provider documentation must state %q", want)
		}
	}
}

func TestProviderConfiguration_UserFacingVocabulary(t *testing.T) {
	doc := readProviderDoc(t, "../../user-docs/mecatui/index.md")
	if !strings.Contains(doc, "custom provider") {
		t.Error("user documentation must call configured entries providers")
	}
	if strings.Contains(doc, "LLM endpoint") {
		t.Error("user documentation must not call configured entries LLM endpoints")
	}
	if strings.Contains(doc, "native provider") {
		t.Error("user documentation must not call configured entries native providers")
	}
}

func readProviderDoc(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(body)), " ")
}
