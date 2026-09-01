package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClientMCPSpecRefusesRedirects closes the review finding that
// CheckRedirect was set ONLY on the OAuth branch, so a client-supplied endpoint
// inherited Go's default: follow up to 10 hops to ANY host.
//
// The consequence was SSRF amplification. ValidateClientURL is a scheme/host-shape
// allowlist with no IP-range screening, so it vets the URL the caller GAVE us —
// and a vetted https://evil.example/mcp could then 302 the daemon to
// http://169.254.169.254/latest/meta-data/ or a loopback port, an address the
// validator would have refused outright.
//
// Offline: the redirect target is a second httptest server on loopback, which is
// also the shape a real attack takes (aim the daemon at something only it can
// reach).
func TestClientMCPSpecRefusesRedirects(t *testing.T) {
	var reached bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/latest/meta-data/", http.StatusFound)
	}))
	defer redirector.Close()

	// A spec as PartitionClientServers would build it.
	specs, err := PartitionClientServers([]ClientServer{{
		Name: "notes",
		URL:  redirector.URL,
		Type: "http",
	}})
	if err != nil {
		t.Fatalf("PartitionClientServers: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("got %d specs, want 1", len(specs))
	}
	if !specs[0].NoRedirects {
		t.Fatal("a client-supplied spec must carry NoRedirects: it is what closes the post-redirect SSRF target")
	}

	client := newMCPHTTPClient(specs[0], nil)
	if client.CheckRedirect == nil {
		t.Fatal("client-MCP client follows redirects: a vetted URL can 302 the daemon to an unvetted host")
	}

	resp, err := client.Get(redirector.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if reached {
		t.Fatal("the redirect was followed to the internal target: ValidateClientURL vetted the first URL, nothing vetted the second")
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want the 302 surfaced to the caller unfollowed", resp.StatusCode)
	}
}

// TestOperatorSpecKeepsDefaultRedirects pins the SCOPE of the fix. An
// operator-configured server (no OAuth, no NoRedirects) keeps Go's default
// behaviour byte-for-byte: that URL is one the operator chose and may legitimately
// redirect to a canonical path, and its credentials are already protected
// cross-origin by the origin-scoped headerRoundTripper. Silently changing a
// shipped operator path was not this feature's to do.
func TestOperatorSpecKeepsDefaultRedirects(t *testing.T) {
	client := newMCPHTTPClient(ServerConfig{Name: "op", URL: "https://mcp.example/mcp"}, nil)
	if client.CheckRedirect != nil {
		t.Fatal("the operator path's redirect policy changed; the client-MCP fix must be scoped to client-supplied specs")
	}
}

// TestRedactURLDropsEveryCredentialChannel pins the redaction policy shared by
// ValidateClientURL's messages and composition's unreachable-server WARN.
//
// The query is dropped as a UNIT on purpose: "?access_token=" is syntactically
// identical to a benign parameter, so a parameter-name denylist would miss the
// next spelling.
func TestRedactURLDropsEveryCredentialChannel(t *testing.T) {
	const secret = "s3cr3t-value"
	for _, raw := range []string{
		"https://user:" + secret + "@mcp.example/mcp",
		"https://mcp.example/mcp?access_token=" + secret,
		"https://mcp.example/mcp?key=" + secret + "&x=1",
		"https://mcp.example/mcp#" + secret,
		"https://user:" + secret + "@mcp.example/mcp?sig=" + secret,
	} {
		got := RedactURL(raw)
		if strings.Contains(got, secret) {
			t.Fatalf("RedactURL(%q) = %q, still carries the credential", raw, got)
		}
		if !strings.Contains(got, "mcp.example") {
			t.Fatalf("RedactURL(%q) = %q, dropped the host it needs to stay diagnostic", raw, got)
		}
	}

	// The one case where echoing the input IS the leak.
	if got := RedactURL("ht tp://%%%"); strings.Contains(got, "%%%") {
		t.Fatalf("RedactURL echoed an unparseable input: %q", got)
	}
}
