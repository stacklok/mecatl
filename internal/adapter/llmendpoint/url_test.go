package llmendpoint

import (
	"net/url"
	"testing"
)

func TestNativeLLMGatewayLogin_Scenario1_CanonicalGatewayURLAndJoin(t *testing.T) {
	t.Parallel()

	canonical, err := CanonicalGatewayURL("HTTPS://Gateway.EXAMPLE:443//tenant/%7euser/v1/")
	if err != nil {
		t.Fatalf("CanonicalGatewayURL: %v", err)
	}
	if canonical != "https://gateway.example/tenant/~user/v1" {
		t.Fatalf("canonical URL = %q", canonical)
	}

	joined, err := JoinGatewayURL(canonical, "responses/%6dodels")
	if err != nil {
		t.Fatalf("JoinGatewayURL: %v", err)
	}
	if joined.String() != "https://gateway.example/tenant/~user/v1/responses/models" {
		t.Fatalf("joined URL = %q", joined)
	}
	joined.RawQuery = url.Values{"limit": {"1"}}.Encode()
	if joined.String() != "https://gateway.example/tenant/~user/v1/responses/models?limit=1" {
		t.Fatalf("joined URL with post-validation query = %q", joined)
	}

	badGatewayURLs := []string{
		"http://gateway.example/v1", "https://user@gateway.example/v1",
		"https://gateway.example/v1?q=x", "https://gateway.example/v1#x",
		"https://gateway.example/a/../v1", "https://gateway.example/a/%2e%2e/v1",
		"https://gateway.example/a%2fb", "https://gateway.example/a%5cb",
		"https://gateway.example/%00", "https://gateway.example/%1f",
	}
	for _, raw := range badGatewayURLs {
		t.Run("gateway_"+raw, func(t *testing.T) {
			if _, err := CanonicalGatewayURL(raw); err == nil {
				t.Fatalf("CanonicalGatewayURL(%q) succeeded", raw)
			}
		})
	}

	badRelative := []string{
		"/responses", "//evil.example/responses", "https://evil.example/responses",
		"../responses", "%2e%2e/responses", "a/../responses", "a/%2e%2e/responses",
		"a%2fb", "a%5cb", "a\\b", "responses?next=https://evil.example",
		"responses#fragment", "responses/%00",
	}
	for _, relative := range badRelative {
		t.Run("join_"+relative, func(t *testing.T) {
			if _, err := JoinGatewayURL(canonical, relative); err == nil {
				t.Fatalf("JoinGatewayURL(%q) succeeded", relative)
			}
		})
	}
}
