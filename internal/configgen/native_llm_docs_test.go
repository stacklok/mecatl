package configgen

import (
	"os"
	"strings"
	"testing"
)

func TestNativeLLMGatewayLogin_Scenario2_SharedGatewayIdentityDocumentation(t *testing.T) {
	doc := readNativeLLMDoc(t, "../../user-docs/building/deployment/mecated.md")
	for _, want := range []string{
		"deployment-scoped gateway identity",
		"quota", "gateway-side audit/retention posture", "model availability",
		"dedicated deployment/service gateway identity",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("usage documentation must state %q", want)
		}
	}
}

func TestNativeLLMGatewayLogin_Scenario8_UpstreamAuthorizationBoundary(t *testing.T) {
	doc := readNativeLLMDoc(t, "../../user-docs/building/deployment/mecated.md")
	for _, want := range []string{
		"drops the raw inbound caller bearer", "never forwards or retains caller credentials",
		"separate deployments", "RFC 8693-style token exchange",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("usage documentation must state %q", want)
		}
	}
}

func TestNativeLLMGatewayLogin_Scenario8_UserFacingEndpointVocabulary(t *testing.T) {
	doc := readNativeLLMDoc(t, "../../user-docs/mecatui/index.md")
	if !strings.Contains(doc, "LLM endpoint") {
		t.Error("user documentation must call configured entries LLM endpoints")
	}
	if strings.Contains(doc, "native provider") {
		t.Error("user documentation must not call native entries providers")
	}
}

func readNativeLLMDoc(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(body)), " ")
}
