package tools

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// fetchMcpResourceDescription is the model-facing documentation for the
// FetchMcpResource tool (issue #223, Phase 2): it is the resource-fetching
// affordance that lets the model ACT on an MCP tool's resource_link (Phase 1
// surfaced the link as a typed block the model can see; this tool fetches it).
const fetchMcpResourceDescription = `Fetch the contents of an https:// resource URI returned by an MCP tool's resource_link.

When to use:
- When an MCP tool result carried a resource_link whose URI is https:// and you
  need the actual contents (a file's text, a document body) the link points to.
- This is the client-side dereference for an https:// resource_link: the URI is
  validated for SSRF (no internal/private/metadata IPs) and fetched directly.

When NOT to use:
- For NON-https URIs (perf://, file://, custom schemes): those are
  SERVER-readonly. Use ReadMcpResource with the OWNING MCP server name and the
  URI instead — this tool does not fetch them. The model knows the owning
  server from the resource_link's metadata or a ListMcpResources listing.
- Never auto-dereference untrusted URIs: only https:// is client-fetched here.

Arguments:
- uri (required): the resource URI to fetch. Must be https://.

Limits:
- Only https:// URIs are fetched; the URI is re-validated for SSRF on every
  redirect target. Output is capped; binary content is summarized as
  "[binary resource: <mime>, <n> bytes]".`

// fetchMcpResourceTimeout bounds a single FetchMcpResource call (connect +
// fetch + body read up to the cap). It is short because the body is capped at
// toolkit.MaxOutputBytes anyway: a responsive fetch should finish well inside
// this, and a hung host must not stall the agent loop.
const fetchMcpResourceTimeout = 30 * time.Second

// fetchMcpResourceMaxBody is the most bytes read from a fetched body. It is
// the cap+1 read that lets the tool detect-and-truncate: it reads up to
// MaxOutputBytes+1 and truncates at MaxOutputBytes with the shared marker.
const fetchMcpResourceMaxBody = toolkit.MaxOutputBytes + 1

// FetchMcpResourceTool fetches the contents of an https:// resource URI
// returned by an MCP tool's resource_link (issue #223 Phase 2). It is the
// client-side affordance that lets the model ACT on a resource_link Phase 1
// surfaced as a typed block.
//
// SECURITY (Decision #5 of issue #223): server-returned resource_link URIs are
// NEVER auto-dereferenced blindly. Only https:// URIs are client-fetched, and
// every one is validated through session.ValidateMediaURL — the STRICTER SSRF
// IP-deny (no plaintext-http-to-loopback, IP-deny for the metadata IP /
// RFC1918 / link-local / CGNAT, inet_aton-style rejection) — both on the
// request URL AND on each redirect target's origin. Non-https schemes
// (perf://, file://, custom) stay SERVER-readonly: the tool returns a
// model-facing error pointing the model at ReadMcpResource with the owning
// server name, rather than guessing which server owns the URI.
//
// SSRF is defended at TWO layers, each closing a gap the other cannot:
//
//  1. URL-STRING layer (redirects): the per-call client's CheckRedirect
//     re-runs session.ValidateMediaURL on every redirect target's origin URL
//     string. A public origin that 302s to https://169.254.169.254/ is
//     rejected before the redirect dial.
//  2. DIAL-IP layer (initial + redirects): the transport's DialContext resolves
//     the hostname via net.DefaultResolver.LookupIPAddr and calls
//     session.ValidateResolvedIP on each resolved IP, rejecting any that is
//     not a routable public address. This closes the DNS-rebinding window the
//     URL-string layer cannot: an attacker-controlled resolver can answer
//     ValidateMediaURL's hostname check with a public IP, then return
//     169.254.169.254 (or RFC1918) when the dialer actually connects. The
//     dial-time check fires on BOTH the initial hop and every redirect dial
//     (a redirect that passed the URL-string screen still dials through this
//     transport), so neither layer is bypassable by the other.
//
// It is read-only (an outward read, no mutation), so it slots into the loop's
// read-parallel dispatch path. Its http.Client is constructed PER CALL (a
// bounded one-shot fetch), so it owns no outlives-a-call resource and needs
// no CLOUD-NATIVE.md List 1 inventory row (ADR 0059 caveat).
//
// The zero value is the production tool (per-call client). Tests inject a
// custom http.Client via withHTTPClient to drive the fetch path offline.
type FetchMcpResourceTool struct {
	// httpClient is OPTIONAL: when nil, Execute builds a fresh per-call client
	// (timeout-bounded, redirect-re-validating, dial-IP-screening). A non-nil
	// value (tests only) replaces it wholesale so a stub RoundTripper can drive
	// the fetch path without a real network egress.
	httpClient *http.Client
	// dialContext is OPTIONAL: when nil, the per-call transport uses
	// ssrfGuardedDialContext (the production DNS-rebinding defense). Tests that
	// need Execute to build its own transport (e.g. to trust a test TLS cert)
	// inject a dialContext here so the SSRF guard still runs while the transport
	// is constructed fresh per call. A non-nil httpClient bypasses this entirely.
	dialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// tlsConfig is OPTIONAL (tests only): when the per-call client is built
	// (httpClient == nil), this TLS config is installed on the transport so a
	// test TLS server's self-signed cert is trusted. The production path leaves
	// this nil (the stdlib roots apply).
	tlsConfig *tls.Config
}

// Compile-time assertion that FetchMcpResourceTool implements tool.Tool.
var _ tool.Tool = FetchMcpResourceTool{}

// fetchMcpResourceArgs is the JSON argument shape for the FetchMcpResource
// tool: a single required uri.
type fetchMcpResourceArgs struct {
	URI string `json:"uri"`
}

// Spec returns the model-facing specification of the FetchMcpResource tool.
func (FetchMcpResourceTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        "FetchMcpResource",
		Description: fetchMcpResourceDescription,
		Schema: schema(`{
  "type": "object",
  "properties": {
    "uri": {"type": "string", "description": "The https:// resource URI to fetch (as returned by an MCP tool's resource_link). Non-https URIs are server-readonly; use ReadMcpResource with the owning MCP server name instead."}
  },
  "required": ["uri"]
}`),
	}
}

// ReadOnly reports that FetchMcpResource is a read-only operation (an outward
// fetch, no state mutation), so it runs in the loop's read-parallel batch.
func (FetchMcpResourceTool) ReadOnly() bool { return true }

// Execute parses+validates the call, then either fetches an https:// URI or
// guides the model to ReadMcpResource for non-https URIs. Recoverable failures
// (a missing uri, a rejected SSRF target, a non-https scheme, a fetch error)
// are returned as model-facing tool errors (NewToolError), never a Go error —
// the Go error return is reserved for harness-level faults (ctx cancellation).
func (t FetchMcpResourceTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
	var args fetchMcpResourceArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if strings.TrimSpace(args.URI) == "" {
		return session.NewToolError(in.ID, "the \"uri\" argument is required"), nil
	}

	// Split the scheme once, cheaply, to route https:// vs server-readonly. A
	// malformed URI falls through to ValidateMediaURL, which parses strictly.
	scheme, _ := splitScheme(args.URI)
	if strings.ToLower(scheme) != "https" {
		return session.NewToolError(in.ID,
			"FetchMcpResource only fetches https:// URIs. The URI "+args.URI+
				" uses the scheme "+reprScheme(scheme)+
				", which is server-readonly: use ReadMcpResource with the owning MCP server name and this URI instead."), nil
	}

	if err := session.ValidateMediaURL(args.URI); err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("FetchMcpResource rejected URI %q: %v", args.URI, err)), nil
	}

	client := t.httpClient
	if client == nil {
		client = &http.Client{
			Timeout: fetchMcpResourceTimeout,
			// Re-validate the SSRF IP-deny on EVERY redirect target's origin. A
			// public origin can redirect to an internal/metadata host; the initial
			// ValidateMediaURL only screened the request URL. No credentials are
			// attached (this tool has no MCP server creds to leak), but the
			// redirect re-validation is the URL-STRING layer of the two-layer
			// SSRF defense (see the type doc); the DIAL-IP layer lives in the
			// transport's DialContext below.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("stopped after 10 redirects")
				}
				if err := session.ValidateMediaURL(req.URL.String()); err != nil {
					return fmt.Errorf("redirect target %q rejected: %w", req.URL.String(), err)
				}
				return nil
			},
			Transport: &http.Transport{
				// DialContext is the DIAL-IP layer of the two-layer SSRF defense:
				// it resolves the hostname and rejects any resolved IP that is not
				// a routable public address, closing the DNS-rebinding window the
				// URL-string layer (CheckRedirect) cannot. It fires on BOTH the
				// initial hop and every redirect dial. t.dialContext (test seam)
				// overrides the production guard.
				DialContext: ssrfGuardedDialContext,
				// A custom *http.Transport (unlike http.DefaultTransport) does not
				// negotiate HTTP/2 unless explicitly opted in; ForceAttemptHTTP2
				// re-enables it so this transport isn't silently downgraded to
				// HTTP/1.1 just because it has a custom DialContext.
				ForceAttemptHTTP2: true,
			},
		}
		if t.dialContext != nil {
			client.Transport.(*http.Transport).DialContext = t.dialContext
		}
		if t.tlsConfig != nil {
			client.Transport.(*http.Transport).TLSClientConfig = t.tlsConfig
		}
	}

	return fetchResource(ctx, in.ID, args.URI, client)
}

// ssrfGuardedDialContext is the DIAL-IP layer of the two-layer SSRF defense
// (see the FetchMcpResourceTool type doc). It is installed as the per-call
// http.Transport.DialContext so it fires on the initial hop AND every redirect
// dial. It resolves the hostname via net.DefaultResolver.LookupIPAddr, then
// calls session.ValidateResolvedIP on each resolved IP — rejecting the dial
// if ALL resolved IPs are internal (loopback/private/link-local/CGNAT/metadata/
// unspecified/multicast). The first IP that passes is dialed; TLS SNI/cert
// verification is unaffected because Go's TLS layer uses the request URL's
// host for ServerName, not the dial address.
//
// This closes the DNS-rebinding window the URL-STRING layer
// (CheckRedirect→ValidateMediaURL) cannot: an attacker-controlled resolver can
// answer ValidateMediaURL's hostname check with a public IP, then return
// 169.254.169.254 (or RFC1918) at dial time. The dial-time check sees the
// real resolved IP and rejects it.
func ssrfGuardedDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ssrf guard: bad dial address %q: %w", addr, err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ssrf guard: resolving %q: %w", host, err)
	}
	var firstValid net.IP
	for _, ip := range ips {
		if err := session.ValidateResolvedIP(ip.IP); err != nil {
			continue
		}
		firstValid = ip.IP
		break
	}
	if firstValid == nil {
		// All resolved IPs are internal/non-routable. Reject the dial — this is
		// the SSRF rejection (a DNS-rebinding attack or an internal target).
		return nil, fmt.Errorf("ssrf guard: %q resolved only to internal/non-routable addresses: %v", host, ips)
	}
	// Dial the validated IP directly. The TLS layer uses the original URL host
	// for SNI/cert verification, so passing the IP as the dial address is safe.
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(firstValid.String(), port))
}

// fetchResource performs the validated GET, reads a bounded body, and renders
// the model-facing result. It is a free helper over an injected client so the
// production per-call client and a test-stubbed client share the one fetch +
// render path. A redirect-re-validating client is the SSRF defense; this helper
// assumes the request URL was already screened by ValidateMediaURL.
func fetchResource(ctx context.Context, callID session.ToolCallID, uri string, client *http.Client) (session.ToolResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return session.NewToolError(callID, fmt.Sprintf("FetchMcpResource: building request for %q failed: %v", uri, err)), nil
	}
	// No cross-origin credentials: this tool is an unauthenticated client fetch
	// of a public resource URI. The per-call http.Client carries no CookieJar,
	// so no ambient credentials ride the request or its redirects.
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return session.ToolResult{}, ctxErr
		}
		return session.NewToolError(callID, fmt.Sprintf("FetchMcpResource: fetching %q failed: %v", uri, err)), nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return session.NewToolError(callID, fmt.Sprintf("FetchMcpResource: %q returned HTTP %s", uri, resp.Status)), nil
	}

	// Read up to MaxOutputBytes+1 so a too-large body is detected and
	// truncated with the shared marker (a single bounded read — no streaming
	// into the model context).
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMcpResourceMaxBody))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return session.ToolResult{}, ctxErr
		}
		return session.NewToolError(callID, fmt.Sprintf("FetchMcpResource: reading body of %q failed: %v", uri, err)), nil
	}

	return session.NewToolResult(callID, renderFetchedBody(resp.Header.Get("Content-Type"), body)), nil
}

// renderFetchedBody renders a fetched body into the model-facing string:
// text content verbatim (truncated to the shared cap with the marker), binary
// content summarized as "[binary resource: <mime>, <n> bytes]" — mirroring
// mcp.flattenResourceContents. The byte count is the body actually read (capped
// at MaxOutputBytes+1), honest about the cap when the resource exceeded it.
func renderFetchedBody(mime string, body []byte) string {
	if isTextContentType(mime) {
		return truncateBytes(string(body))
	}
	m := mime
	if m == "" {
		m = "application/octet-stream"
	}
	return toolkit.Truncate(fmt.Sprintf("[binary resource: %s, %d bytes]", m, len(body)), toolkit.MaxOutputBytes)
}

// splitScheme returns the lowercase scheme of raw and the remainder, without
// parsing. It is a cheap pre-screen used only to route https:// vs
// server-readonly before the strict ValidateMediaURL parse handles the real
// validation. A malformed URI returns an empty scheme and the raw remainder,
// letting ValidateMediaURL produce the precise error.
func splitScheme(raw string) (scheme, rest string) {
	i := strings.Index(raw, "://")
	if i < 0 {
		return "", raw
	}
	return strings.ToLower(raw[:i]), raw[i+3:]
}

// reprScheme renders a possibly-empty scheme for a model-facing message.
func reprScheme(scheme string) string {
	if scheme == "" {
		return "(none / malformed)"
	}
	return scheme
}

// isTextContentType reports whether a Content-Type header names a textual
// media type whose body should be rendered verbatim (truncated) rather than
// summarized. It treats the absence of a Content-Type as text (many servers
// omit it for plain-text resources), and matches the usual text/*,
// application/json, application/xml, +xml/+json suffixes. Anything else
// (image/*, audio/*, video/*, application/octet-stream, ...) is binary.
func isTextContentType(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if mime == "" {
		return true
	}
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	if strings.HasSuffix(mime, "+json") || strings.HasSuffix(mime, "+xml") {
		return true
	}
	switch mime {
	case "application/json", "application/xml",
		"application/javascript", "application/x-www-form-urlencoded",
		"application/x-yaml", "application/yaml":
		return true
	}
	return false
}
