package mcpbroker

import "testing"

func TestSingletonBrokerRemediation_Scenario3_ProtectedURLsAndOIDCDiscoveryFailClosed(t *testing.T) {
	for _, raw := range []string{
		"http://idp.internal.example/issuer",
		"https://user:pass@idp.internal.example/issuer",
		"https://idp.internal.example/issuer?x=1",
		"https://idp.internal.example/issuer#fragment",
		"https://idp.internal.example/issuer%2Fpart",
	} {
		if err := ValidateProtectedURL(raw, "endpoint"); err == nil {
			t.Errorf("ValidateProtectedURL(%q) accepted unsafe URL", raw)
		}
	}
	if err := ValidateProtectedURL("https://idp.internal.example/issuer", "endpoint"); err != nil {
		t.Fatalf("private HTTPS endpoint rejected: %v", err)
	}
}
