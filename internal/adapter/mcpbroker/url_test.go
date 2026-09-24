package mcpbroker

import "testing"

func TestSingletonBrokerRemediation_Scenario3_ProtectedURLsAndOIDCDiscoveryFailClosed(t *testing.T) {
	for _, raw := range []string{
		"http://idp.internal.example/issuer",
		"https://user:pass@idp.internal.example/issuer",
		"https://idp.internal.example/issuer?x=1",
		"https://idp.internal.example/issuer#fragment",
		"https://idp.internal.example/issuer%2Fpart",
		"https://idp_internal.example/issuer",
		"https://idp.internal.example:0/issuer",
		"https://idp.internal.example:65536/issuer",
		"https://[2001:0db8:0:0:0:0:0:1]/issuer",
		"https://[2001:db8::01]/issuer",
	} {
		if err := ValidateProtectedURL(raw, "endpoint"); err == nil {
			t.Errorf("ValidateProtectedURL(%q) accepted unsafe URL", raw)
		}
	}
	if err := ValidateProtectedURL("https://idp.internal.example/issuer", "endpoint"); err != nil {
		t.Fatalf("private HTTPS endpoint rejected: %v", err)
	}
}
