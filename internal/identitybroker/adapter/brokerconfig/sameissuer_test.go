package brokerconfig

import "testing"

// TestSameIssuer compares issuers including their path: the same host under
// another path is another issuer.
func TestSameIssuer(t *testing.T) {
	t.Parallel()
	const issuer = "https://broker.example/v1/mcp/broker"
	for other, want := range map[string]bool{
		issuer:                                 true,
		"HTTPS://Broker.Example/v1/mcp/broker": true,
		"https://broker.example:443/v1/mcp/broker":  true,
		"https://broker.example/v1/mcp/broker/":     true,
		"http://broker.example/v1/mcp/broker":       false,
		"https://broker.example:8443/v1/mcp/broker": false,
		"https://other.example/v1/mcp/broker":       false,
		"https://broker.example/v1/mcp/BROKER":      false,
		"https://broker.example/as1":                false,
		"https://broker.example":                    false,
		"https://broker.example/v1/mcp":             false,
	} {
		if got := sameIssuer(issuer, other); got != want {
			t.Errorf("sameIssuer(%q, %q) = %v, want %v", issuer, other, got, want)
		}
		if got := sameIssuer(other, issuer); got != want {
			t.Errorf("sameIssuer(%q, %q) = %v, want %v", other, issuer, got, want)
		}
	}
	// A host-only issuer equals itself with a trailing slash.
	if !sameIssuer("https://as1.example", "https://AS1.example:443/") {
		t.Error("a host-only issuer differs from itself with a slash")
	}
}
