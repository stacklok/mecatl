// Package mcp adapts tools served by external Model Context Protocol (MCP)
// servers into the harness's tool.Tool interface, so the agent loop can call
// remote MCP tools exactly as it calls built-in ones.
//
// # Transport
//
// This adapter speaks ONLY the MCP Streamable HTTP client transport (HTTP POST
// plus SSE), per the project's hard constraint. The stdio transport is
// deliberately never imported, constructed, or exposed; no MCP server process
// is ever spawned. If the underlying SDK ships a stdio/command transport, this
// package intentionally does not use it.
//
// # SDK
//
// It wraps github.com/modelcontextprotocol/go-sdk/mcp (the official Go SDK),
// using mcp.StreamableClientTransport for the transport and mcp.Client /
// mcp.ClientSession for the handshake, tool listing, and tool invocation.
//
// # Trust
//
// Remote MCP servers are an untrusted supply-chain surface (see
// docs/harnesses/08). Tool names are namespaced as mcp__<server>__<tool> so a
// remote server can never shadow a built-in tool, and the conservative ReadOnly
// default keeps remote tools serialized unless they explicitly advertise a
// read-only hint.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
)

// ErrUnknownServer is returned (wrapped) by the Provider routing methods when a
// caller names a server that is not connected. It is a CLIENT error (the name is
// wrong), distinct from a transport/protocol fault on a known server — callers
// at the network surface map it to InvalidArgument, while other faults map to
// Internal/Unavailable.
var ErrUnknownServer = errors.New("mcp: unknown server")

// defaultConnectTimeout bounds the initialize handshake and initial tool
// listing so an unresponsive server cannot stall startup indefinitely.
const defaultConnectTimeout = 30 * time.Second

// oauthLoginDeadline starts the normal machine deadline for initial network
// exchange, pauses it while an explicit browser authorization is pending, then
// starts a fresh deadline when the SDK handler successfully authorizes and
// retries its request.
type oauthLoginDeadline struct {
	ctx     context.Context
	cancel  context.CancelFunc
	timeout time.Duration

	mu         sync.Mutex
	timer      *time.Timer
	generation uint64
	closed     bool
}

func newOAuthLoginDeadline(ctx context.Context, timeout time.Duration) *oauthLoginDeadline {
	deadlineCtx, cancel := context.WithCancel(ctx)
	return &oauthLoginDeadline{ctx: deadlineCtx, cancel: cancel, timeout: timeout}
}

func (d *oauthLoginDeadline) start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.timer != nil {
		return
	}
	d.generation++
	generation := d.generation
	d.timer = time.AfterFunc(d.timeout, func() { d.expire(generation) })
}

func (d *oauthLoginDeadline) pause() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.timer == nil {
		return
	}
	d.generation++
	d.timer.Stop()
	d.timer = nil
}

func (d *oauthLoginDeadline) expire(generation uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || generation != d.generation {
		return
	}
	d.timer = nil
	d.cancel()
}

func (d *oauthLoginDeadline) close() {
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		d.generation++
		if d.timer != nil {
			d.timer.Stop()
		}
		d.cancel()
	}
	d.mu.Unlock()
}

// clientName / clientVersion identify this harness to MCP servers in the
// initialize handshake.
const (
	clientName    = "mecatl"
	clientVersion = "v0"
)

// ServerConfig describes a single remote MCP server to connect to over the
// Streamable HTTP transport.
type ServerConfig struct {
	// Name is a short, stable identifier for the server. It becomes the
	// <server> segment of every wrapped tool's namespaced name, so it should be
	// unique across the configured servers and contain no "__" sequence.
	Name string
	// URL is the server's Streamable HTTP endpoint (e.g. https://host/mcp).
	URL string
	// Headers are extra HTTP headers sent on every request to the server, such
	// as "Authorization: Bearer ...". Optional.
	Headers map[string]string
	// TokenSource supplies a session-scoped OAuth bearer at request time. It is
	// mutually exclusive with credential Headers and preserves token refresh/expiry.
	TokenSource oauth2.TokenSource
	// OAuth enables the adapter-local authorization-code controller. It is
	// mutually exclusive with a static Authorization header.
	OAuth *OAuthOptions
	// HTTPClient optionally supplies the transport for the Streamable HTTP client.
	// Nil preserves the default transport behavior.
	HTTPClient *http.Client
	// Timeout bounds the connect handshake and tool listing. If zero,
	// defaultConnectTimeout is used. It does not bound later tool calls, which
	// are governed by the per-call context.
	Timeout time.Duration
	// NoRedirects refuses HTTP redirects on this server's client. It is set for
	// CLIENT-SUPPLIED specs (PartitionClientServers) and left false for
	// operator-configured servers, so the operator path keeps Go's default
	// behaviour byte-for-byte. See newMCPHTTPClient for why the two differ.
	NoRedirects bool
}

// ValidateClientURL validates a CLIENT-PROVIDED Streamable HTTP MCP endpoint
// before it is mounted per-session (e.g. an editor's session/new mcpServers
// entry). It is a deliberate SSRF backstop applied ONLY to the untrusted client
// path — the operator-configured Connect/NewManager path is intentionally NOT
// gated this way, since an operator may legitimately point a server at an
// internal host.
//
// The contract: the URL must be absolute, carry a host, carry NO userinfo, and
// use scheme "https" — OR "http" only when the host is an explicit loopback
// address ("127.0.0.1", "::1", "localhost"). Everything else (file/ftp/gopher/
// etc., a relative URL, a hostless URL, credentials in userinfo, or plaintext
// http to a non-loopback host) is rejected.
//
// KNOW WHAT THIS IS NOT. It is a scheme/host-SHAPE allowlist, and it is the
// weaker of this repo's two outbound-URL standards. It does NOT screen IP ranges,
// so it permits https:// to 169.254.169.254, metadata.google.internal, 10.0.0.1,
// or the inet_aton form 2130706433; and it validates by NAME while the dial
// resolves by name again, so it does not close DNS rebinding. The stronger
// standard is session.ValidateMediaURL + session.ValidateResolvedIP (used by
// FetchMcpResource and webfetch), which screens ranges, normalises numeric and
// trailing-dot hosts, and re-validates every redirect hop against a pinned
// dialer.
//
// Two things bound the residual here. Every spec this validator accepts is
// mounted with ServerConfig.NoRedirects, so newMCPHTTPClient refuses redirects and
// a vetted URL cannot 302 the daemon onward to an address this check would have
// refused — which was the sharper half of the gap.
// And headerRoundTripper is origin-scoped, so credentials never travel to a host
// other than the one they were configured for. What remains is blind SSRF from
// the daemon's own network position (and loopback port probing) by a caller that
// already reaches a UNIX-socket-only API — a local process the operator trusts.
// Adopting the pinned-dialer standard here is the right fix and is deliberately
// NOT bundled into the client-MCP wire feature; it changes the operator MCP path
// too.
func ValidateClientURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("mcp: client MCP URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		// Neither the raw string nor the *url.Error is echoed: url.Error embeds the
		// URL it failed on, so %w would reintroduce exactly what RedactURL exists to
		// strip. The inner reason is unwrapped and carries no URL.
		return fmt.Errorf("mcp: client MCP URL is not parseable: %v", innerURLError(err))
	}
	if !u.IsAbs() {
		return fmt.Errorf("mcp: client MCP URL %q must be absolute", RedactURL(raw))
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("mcp: client MCP URL %q has no host", RedactURL(raw))
	}
	// USERINFO is an unguarded credential channel and must not be a back door
	// around the Headers discipline. net/http promotes URL.User to a Basic
	// Authorization header automatically, so "https://user:pass@host/mcp" is a
	// fully functional credential path that never passes through ServerConfig.
	// Headers and inherits none of its secret-shaped protections — not the
	// no-logging rule, not the no-event rule, not the no-error rule. Reject it and
	// say where credentials belong. (Errors here use RedactURL for the same reason:
	// a rejection message must not be the thing that leaks the secret.)
	if u.User != nil {
		return errors.New("mcp: client MCP URL must not carry userinfo credentials; use headers")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(host) {
			return nil
		}
		return fmt.Errorf("mcp: client MCP URL %q uses plaintext http to a non-loopback host; use https", RedactURL(raw))
	default:
		return fmt.Errorf("mcp: client MCP URL %q scheme %q not allowed (https, or http to loopback only)", RedactURL(raw), u.Scheme)
	}
}

// RedactURL renders a client-supplied URL for a MESSAGE OR AN OPERATOR LOG with
// any embedded credential removed: scheme://host/path only, dropping userinfo, the
// whole query string, and the fragment.
//
// The query goes as a UNIT rather than being filtered key-by-key. A credential in
// the query ("?access_token=...", "?key=...", "?sig=...") is syntactically
// indistinguishable from a benign parameter, so a denylist of parameter names
// would miss the next spelling; dropping the query costs a little diagnostic
// detail and closes the class. userinfo is separately REJECTED outright by
// ValidateClientURL — this is the backstop for the channel that cannot be.
//
// It is exported because composition logs these URLs too (the
// client-MCP-unreachable WARN in internal/app), and one redaction policy shared is
// the point: a second local copy is how the two drift.
//
// An unparseable or hostless input degrades to a placeholder rather than the raw
// string, since that is precisely the case where echoing the input is the leak.
func RedactURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "(redacted url)"
	}
	safe := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
	return safe.String()
}

// urlInText matches a scheme-bearing URL embedded in free text. The terminator set
// is deliberately small — whitespace, quotes, angle brackets, backslash, backtick —
// because transport errors render URLs inside quotes (`Post "https://..."`) and
// over-capturing a trailing comma or paren is harmless: RedactURL drops the query
// regardless, so any junk lands in the discarded tail rather than in the output.
var urlInText = regexp.MustCompile(`(?i)\bhttps?://[^\s"'` + "`" + `<>\\]+`)

// RedactText scrubs every URL embedded in free text, replacing each with its
// RedactURL form (scheme://host/path — no userinfo, no query, no fragment).
//
// It exists because redacting a ServerConfig.URL at a log site is NOT sufficient:
// the ERROR logged beside it embeds the full request URL independently.
// net/http's *url.Error carries it, and the MCP SDK formats it into its own
// message text (`rejected by transport: Post "http://host/mcp?access_token=..."`),
// so the URL arrives as a STRING inside a wrapped message rather than as an
// unwrappable field — innerURLError cannot reach it and neither can any
// error-chain approach. A text scrub is the only thing that does.
//
// This is why the redaction is a TEXT operation rather than a URL one: the
// sensitive value can appear anywhere in a message composed by a layer we do not
// control, including a future SDK version that words it differently.
func RedactText(s string) string {
	if s == "" {
		return s
	}
	return urlInText.ReplaceAllStringFunc(s, RedactURL)
}

// RedactError renders err for a log with every URL it embeds redacted. A nil error
// renders as the empty string.
//
// Prefer this over logging err directly ANYWHERE an MCP server's URL could reach
// the error — which in practice means every MCP transport error, since the URL is
// what the transport was asked to reach.
func RedactError(err error) string {
	if err == nil {
		return ""
	}
	return RedactText(err.Error())
}

// redactedError renders a redacted message while preserving the ORIGINAL error
// chain, so errors.Is / errors.As still see through it. That combination is the
// point: callers legitimately branch on sentinels (ErrOAuthLoginRequired,
// ErrOAuthUnavailable) and must keep doing so, while anything that PRINTS the
// error gets the scrubbed text.
//
// Unwrap does expose the unredacted original, so a caller can still leak by
// unwrapping and printing deliberately. That is an explicit act rather than the
// default, which is the distinction this type exists to create.
type redactedError struct {
	inner error
	msg   string
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.inner }

// RedactErrorValue wraps err so that printing it cannot leak an embedded URL.
// An error with nothing to redact is returned AS-IS, so the common case adds no
// wrapper and no allocation.
func RedactErrorValue(err error) error {
	if err == nil {
		return nil
	}
	raw := err.Error()
	msg := RedactText(raw)
	if msg == raw {
		return err
	}
	return &redactedError{inner: err, msg: msg}
}

// OAuthDiagnostics is the small diagnostic seam used by the OAuth implementation.
// Its zero value is safe; the port adapter stays here so the OAuth files do not
// depend on engine/port.
type OAuthDiagnostics struct {
	warn func(context.Context, string, ...any)
}

// NewOAuthDiagnostics adapts the application diagnostics port for OAuth.
func NewOAuthDiagnostics(diag port.Diagnostics) OAuthDiagnostics {
	if diag == nil {
		return OAuthDiagnostics{}
	}
	return OAuthDiagnostics{warn: func(ctx context.Context, msg string, attrs ...any) {
		diag.Log(ctx, port.LevelWarn, msg, attrs...)
	}}
}

// Warn records a warning when a diagnostic sink is configured.
func (d OAuthDiagnostics) Warn(ctx context.Context, msg string, attrs ...any) {
	if d.warn != nil {
		d.warn(ctx, msg, attrs...)
	}
}

// Redacted returns a diagnostic sink that applies the MCP adapter's redaction policy.
func (d OAuthDiagnostics) Redacted() OAuthDiagnostics {
	if d.warn == nil {
		return d
	}
	return OAuthDiagnostics{warn: func(ctx context.Context, msg string, attrs ...any) {
		d.Warn(ctx, RedactText(msg), redactAttrs(attrs)...)
	}}
}

// redactingDiagnostics wraps a port.Diagnostics so every message and every
// string/error attribute logged THROUGH IT has its embedded URLs scrubbed.
//
// It is applied once, at this package's diagnostics ENTRY POINTS (Connect and
// NewManager), rather than at each of the package's log sites. That is the point:
// there were four in-package sites logging a transport error (resource listing,
// prompt listing, list refresh, reconnect), every one of them able to carry a
// credential-bearing URL, and dressing each call individually leaves the next one
// added to leak by default. Wrapping the sink makes the safe behaviour the
// automatic one.
//
// Attribute KEYS are scrubbed too. They are harness-authored constants that never
// contain a URL, so this is a no-op on them — but treating every string uniformly
// removes the need for the wrapper to reason about slog's key/value positions,
// which is exactly the kind of assumption that breaks quietly.
type redactingDiagnostics struct{ inner port.Diagnostics }

// redactDiagnostics wraps diag unless it is already wrapped, so the
// NewManager -> Connect path does not double-decorate. Double scrubbing would be
// harmless (RedactText is idempotent — a redacted URL has no query left to drop)
// but the guard keeps the log path cheap.
func redactDiagnostics(diag port.Diagnostics) port.Diagnostics {
	if diag == nil {
		return port.NopDiagnostics{}
	}
	if _, already := diag.(redactingDiagnostics); already {
		return diag
	}
	return redactingDiagnostics{inner: diag}
}

func (d redactingDiagnostics) Log(ctx context.Context, level port.Level, msg string, attrs ...any) {
	d.inner.Log(ctx, level, RedactText(msg), redactAttrs(attrs)...)
}

func (d redactingDiagnostics) With(attrs ...any) port.Diagnostics {
	return redactingDiagnostics{inner: d.inner.With(redactAttrs(attrs)...)}
}

// redactAttrs scrubs the string and error values in a slog-style attribute list,
// leaving every other type untouched. It copies rather than mutating in place: the
// caller may reuse the slice, and a logger must never edit its caller's data.
func redactAttrs(attrs []any) []any {
	if len(attrs) == 0 {
		return attrs
	}
	out := make([]any, len(attrs))
	for i, a := range attrs {
		switch v := a.(type) {
		case string:
			out[i] = RedactText(v)
		case error:
			out[i] = RedactError(v)
		default:
			out[i] = a
		}
	}
	return out
}

// innerURLError unwraps a *url.Error to its underlying reason, which — unlike the
// wrapper — does not embed the offending URL.
func innerURLError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Err != nil {
		return uerr.Err
	}
	return err
}

// isLoopbackHost reports whether host is an explicit loopback address that
// plaintext http is permitted to reach. It accepts the literal "localhost" name
// and the loopback IPs (127.0.0.0/8, ::1).
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// headerRoundTripper injects static headers onto outbound requests whose origin
// (scheme+host) MATCHES the configured server's origin. It is how per-server auth
// headers reach the Streamable HTTP transport, which only exposes an *http.Client
// seam.
//
// The origin check is a deliberate SSRF/credential-leak backstop (CWE-918/601):
// the standard library follows redirects on the same client, and a server that
// 302s to a DIFFERENT origin would otherwise have this RoundTripper re-apply the
// caller's Authorization (and any other) header on the redirected hop — leaking the
// credential cross-origin, to a host ValidateClientURL never vetted. By gating on
// origin we send the headers ONLY to the host they were configured for; a
// cross-origin hop carries none of them. (Go's own redirect handling already
// strips sensitive headers set on the original *Request across origins, but headers
// a RoundTripper injects bypass that, so we enforce it here.)
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
	// origin is the lowercased scheme://host the headers are scoped to.
	origin string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone so we never mutate a request the caller may reuse.
	req = req.Clone(req.Context())
	if requestOrigin(req.URL) == h.origin {
		for k, v := range h.headers {
			req.Header.Set(k, v)
		}
	}
	return h.base.RoundTrip(req)
}

type bearerRoundTripper struct {
	base   http.RoundTripper
	origin string
	source oauth2.TokenSource
}

func (t *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if requestOrigin(req.URL) != t.origin {
		return base.RoundTrip(req)
	}
	return (&oauth2.Transport{Source: bearerTokenSource{source: t.source}, Base: base}).RoundTrip(req)
}

type bearerTokenSource struct {
	source oauth2.TokenSource
}

func (s bearerTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	if token == nil || token.AccessToken == "" || (token.TokenType != "" && !strings.EqualFold(token.TokenType, "Bearer")) {
		return nil, errors.New("mcp: token source returned a non-bearer token")
	}
	if token.TokenType == "" {
		tokenClone := *token
		tokenClone.TokenType = "Bearer"
		token = &tokenClone
	}
	return token, nil
}

// requestOrigin returns the lowercased scheme://host origin of u (host includes the
// port). It is the comparison key headerRoundTripper uses to decide whether the
// per-server headers may ride on a request — a redirected cross-origin hop yields a
// different origin and so receives none of the injected headers.
func requestOrigin(u *url.URL) string {
	return urlOrigin(u)
}

// Server is a live connection to one remote MCP server. It owns the SDK client
// session and the tool.Tool wrappers derived from the server's tool list, plus
// the snapshots of the server's resources and prompts captured at connect.
//
// As of ADR 0057 the adapter holds the standalone SSE GET stream open per
// connected server and subscribes to server-initiated
// notifications/{tools,prompts,resources}/list_changed: a notification sets the
// matching *Dirty flag, and the next read of Tools()/Resources()/Prompts()
// lazily re-lists under a bounded context.Background() and swaps in the fresh
// snapshot. The dirty flag is cleared BEFORE the fetch (not after) so a
// notification arriving during the re-list re-arms it — the safe direction
// (at worst one redundant refresh, never a lost update).
//
// Catalog mutation (live tool.Catalog refresh) is deliberately Phase 2 — it
// gets its own ADR. In Phase 1, Tools() DOES re-list on dirty (so a per-session
// catalog assembly that calls mgr.Tools() after a list_changed picks up the
// fresh set), but the already-registered remoteTool specs in an existing session
// are NOT updated — a tool the server dropped surfaces a tool-call error on
// use. This means two sessions created around the same notification may see
// different tool surfaces (a timing-dependent split); this is the accepted
// Phase 1 trade-off, documented in ADR 0057.
//
// A dropped session (the SDK's ErrConnectionClosed / errSessionMissing, surfacing
// as "session not found" / "connection closed") is re-established transparently
// by withSession: a single bounded reconnect attempt per call, serialized under
// mu so N concurrent failing calls produce ONE dial. See reconnect.go and ADR 0056.
type Server struct {
	name       string
	cfg        ServerConfig
	diag       port.Diagnostics
	httpClient *http.Client
	oauth      *OAuthController
	mu         sync.Mutex
	dropped    bool // set by a dial failure (retry flag) OR Close (terminal); cleared on a successful dial ONLY when not closed
	closed     bool // set ONLY by Close; terminal — a post-close call never dials. Distinct from dropped (the retry flag).
	session    *mcpsdk.ClientSession
	tools      []tool.Tool
	resources  []Resource
	prompts    []Prompt
	// Phase 1 (ADR 0057): dirty flags set by the list-changed notification
	// handlers and cleared before the next lazy re-list. They are read-and-cleared
	// under s.mu by the accessors; the re-list itself runs WITHOUT s.mu held
	// (see refreshTools/refreshResources/refreshPrompts) because liveSession may
	// re-enter reconnect, which re-acquires s.mu — holding it across the network
	// would serialize all accessors behind a reconnect dial.
	//
	// The generation counters prevent a concurrent-refresh lost-update: two
	// goroutines that both entered refresh* before either cleared the flag can
	// interleave their fetches, and a slower goroutine carrying older data would
	// overwrite a newer snapshot in the final swap. The generation counter is
	// snapshotted at clear time and checked at swap time — a stale result (whose
	// generation no longer matches) is discarded. See PR #197 review.
	toolsDirty     bool   // set by ToolListChangedHandler; cleared on next Tools() re-list
	resourcesDirty bool   // set by ResourceListChangedHandler; cleared on next Resources() re-list
	promptsDirty   bool   // set by PromptListChangedHandler; cleared on next Prompts() re-list
	toolsGen       uint64 // generation counter for concurrent-refresh guard
	resourcesGen   uint64
	promptsGen     uint64
}

// HasCredentialHeaders reports whether headers contain a credential-bearing
// header that cannot be combined with OAuth. Matching is case-insensitive.
func HasCredentialHeaders(headers map[string]string) bool {
	for name := range headers {
		switch {
		case strings.EqualFold(name, "Authorization"),
			strings.EqualFold(name, "Proxy-Authorization"),
			strings.EqualFold(name, "Cookie"):
			return true
		}
	}
	return false
}

func prepareOAuthServerConfig(ctx context.Context, cfg ServerConfig) (ServerConfig, *OAuthController, error) {
	if cfg.TokenSource != nil && HasCredentialHeaders(cfg.Headers) {
		return cfg, nil, errors.New("mcp: static credential headers and token source are mutually exclusive")
	}
	if cfg.TokenSource != nil && cfg.OAuth != nil {
		return cfg, nil, errors.New("mcp: token source and OAuth are mutually exclusive")
	}
	if cfg.TokenSource != nil {
		if err := ValidateClientURL(cfg.URL); err != nil {
			return cfg, nil, err
		}
	}
	if cfg.HTTPClient != nil && cfg.OAuth != nil {
		return cfg, nil, errors.New("mcp: custom HTTP client and OAuth are mutually exclusive")
	}
	if cfg.OAuth == nil {
		return cfg, nil, nil
	}
	if HasCredentialHeaders(cfg.Headers) {
		return cfg, nil, errors.New("mcp: static credential headers and OAuth are mutually exclusive")
	}
	canonical, err := canonicalOAuthResource(cfg.URL)
	if err != nil {
		return cfg, nil, err
	}
	cfg.URL = canonical
	oauth := *cfg.OAuth
	if oauth.RedirectURL == "" {
		// Runtime restoration needs no presenter/listener, but the SDK handler
		// requires a syntactically valid redirect URI. The explicit login path
		// replaces this inert value with its bound callback before connecting.
		oauth.RedirectURL = "http://127.0.0.1/callback"
	}
	cfg.OAuth = &oauth
	controller, err := NewOAuthController(ctx, cfg.URL, oauth)
	return cfg, controller, err
}

func newMCPHTTPClient(cfg ServerConfig, oauth *OAuthController) *http.Client {
	client := &http.Client{}
	if cfg.HTTPClient != nil {
		clientCopy := *cfg.HTTPClient
		client = &clientCopy
	}
	if oauth != nil {
		resourceURL, _ := url.Parse(cfg.URL)
		client.CheckRedirect = mcpOAuthRedirectPolicy(requestOrigin(resourceURL), cfg.OAuth.Network.MaxRedirects)
		// Resource requests carrying restored or refreshed bearer tokens must use
		// the same no-proxy, DNS-pinned destination policy as OAuth endpoints.
		client.Transport = oauthResourceRoundTripper{base: oauth.transport}
	} else if cfg.NoRedirects {
		// NO REDIRECTS for a CLIENT-SUPPLIED endpoint. Go's default follows up to 10
		// hops to ANY host, so a URL that passed ValidateClientURL's scheme/host
		// shape check could 302 the daemon onward to an address that never would
		// have — a cloud metadata endpoint, a private range, a loopback port.
		// ValidateClientURL vets the URL the caller GAVE us; only this closes the one
		// it can be sent to next. A Streamable-HTTP MCP endpoint has no legitimate
		// need to redirect, so refusing costs nothing functional.
		//
		// Scoped to the client path ON PURPOSE. The OPERATOR path keeps Go's default
		// (this branch is not taken) because an operator's endpoint is a URL they
		// chose themselves and may legitimately redirect to a canonical path, and
		// their credentials are already protected cross-origin by the origin-scoped
		// headerRoundTripper. Disabling it there would be an unrequested behaviour
		// change to a shipped path; here it closes an SSRF amplifier on a surface
		// this feature is the first to expose to a wire caller.
		//
		// The OAuth branch above has its own stricter origin-pinned policy
		// (mcpOAuthRedirectPolicy) and keeps it; this is the branch that had none.
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	if cfg.TokenSource != nil {
		origin := ""
		if parsed, err := url.Parse(cfg.URL); err == nil {
			origin = requestOrigin(parsed)
		}
		client.Transport = &bearerRoundTripper{base: client.Transport, origin: origin, source: cfg.TokenSource}
	}
	if len(cfg.Headers) == 0 {
		return client
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	headers := make(map[string]string, len(cfg.Headers))
	for key, value := range cfg.Headers {
		headers[key] = value
	}
	origin := ""
	if parsed, err := url.Parse(cfg.URL); err == nil {
		origin = requestOrigin(parsed)
	}
	client.Transport = &headerRoundTripper{base: base, headers: headers, origin: origin}
	return client
}

// Connect establishes a Streamable HTTP session to the configured MCP server,
// performs the initialize handshake, lists the server's tools, and returns a
// Server whose Tools() are ready to register into a catalog.
//
// The connect handshake and tool listing are bounded by cfg.Timeout (or
// defaultConnectTimeout). A non-nil error means the server should be treated as
// unavailable; callers (the composition root) are expected to log-and-skip such
// a server rather than aborting the whole harness.
func Connect(ctx context.Context, cfg ServerConfig, diag port.Diagnostics) (*Server, error) {
	// REDACT AT THE SOURCE. Every error this package hands out is produced here or
	// below, and net/http embeds the full request URL — query string included — in
	// any connection error. Redacting at the one exit rather than at each consumer
	// is what makes every downstream safe by default: NewManager's onError
	// callback, NewManager's returned error, and the direct callers that return
	// this error onward (internal/app's MCP login flow) all inherit it without
	// needing to remember.
	//
	// The alternative — asking each consumer to call RedactError — was tried and
	// demonstrably does not hold: of three NewManager callers, one logged the raw
	// error and the raw URL, and the audit that was supposed to find it missed it.
	srv, err := connect(ctx, cfg, diag)
	return srv, RedactErrorValue(err)
}

func connect(ctx context.Context, cfg ServerConfig, diag port.Diagnostics) (*Server, error) {
	// Wrap the sink so no log line from this package — nor from the *Server it
	// builds, which inherits this diag — can carry a credential-bearing URL. See
	// redactingDiagnostics for why this is a sink decoration rather than a fix at
	// each call site.
	diag = redactDiagnostics(diag)
	if cfg.Name == "" {
		return nil, errors.New("mcp: server config requires a Name")
	}
	if strings.Contains(cfg.Name, "__") {
		return nil, fmt.Errorf("mcp: server name %q must not contain %q", cfg.Name, "__")
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("mcp: server %q requires a URL", cfg.Name)
	}
	// The explicit local login presenter is invoked by the SDK from Client.Connect.
	// Bound initial network exchange, pause that deadline for the browser/callback,
	// then arm a fresh machine deadline when authorization succeeds and the SDK
	// retries its request.
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	var connectCtx context.Context
	var cancel func()
	var loginDeadline *oauthLoginDeadline
	if cfg.OAuth != nil && cfg.OAuth.Presenter != nil {
		loginDeadline = newOAuthLoginDeadline(ctx, timeout)
		loginDeadline.start()
		connectCtx, cancel = loginDeadline.ctx, loginDeadline.close
	} else {
		connectCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	var oauthController *OAuthController
	var err error
	cfg, oauthController, err = prepareOAuthServerConfig(connectCtx, cfg)
	if err != nil {
		return nil, err
	}
	if loginDeadline != nil {
		oauthController.authorizationContext = ctx
		oauthController.onAuthorizationStart = loginDeadline.pause
		oauthController.onAuthorizationSuccess = loginDeadline.start
	}

	httpClient := newMCPHTTPClient(cfg, oauthController)

	// srv is constructed early so dial can populate its session field; the config,
	// diag, and httpClient are retained here because reconnect (reconnect.go) needs
	// them to re-establish a dropped session later.
	srv := &Server{
		name:       cfg.Name,
		cfg:        cfg,
		diag:       diag,
		httpClient: httpClient,
		oauth:      oauthController,
	}

	sess, err := srv.dial(connectCtx, loginDeadline != nil)
	if err != nil {
		_ = oauthController.Close()
		return nil, fmt.Errorf("mcp: connect to server %q: %w", cfg.Name, err)
	}
	srv.session = sess

	tools, err := listTools(connectCtx, cfg.Name, srv, sess)
	if err != nil {
		_ = sess.Close()
		_ = oauthController.Close()
		return nil, fmt.Errorf("mcp: list tools on server %q: %w", cfg.Name, err)
	}
	srv.tools = tools

	// Resources and prompts are STATIC SNAPSHOTS taken once here, and only when
	// the server advertised the matching capability in the initialize handshake.
	// A server that exposes tools but not resources/prompts is fine: we skip the
	// absent capability so one limited server never breaks the harness. Listing
	// is also non-fatal — a server that advertises the capability but errors the
	// list is logged-and-skipped rather than failing the whole connect, since the
	// tools are already usable.
	caps := serverCapabilities(sess)
	if caps != nil && caps.Resources != nil {
		if res, rerr := srv.listResources(connectCtx); rerr != nil {
			diag.Log(connectCtx, port.LevelWarn, "mcp: listing resources failed; continuing without them",
				"server", cfg.Name, "err", rerr)
		} else {
			srv.resources = res
		}
	}
	if caps != nil && caps.Prompts != nil {
		if pr, perr := srv.listPrompts(connectCtx); perr != nil {
			diag.Log(connectCtx, port.LevelWarn, "mcp: listing prompts failed; continuing without them",
				"server", cfg.Name, "err", perr)
		} else {
			srv.prompts = pr
		}
	}

	return srv, nil
}

// dial establishes a fresh SDK ClientSession against the configured server. It
// is the single construction site for transport + client + connect, reused by
// Connect (initial) and reconnect (after a drop). It applies cfg.Timeout (or
// defaultConnectTimeout) on top of the passed ctx as an establishment bound,
// except for the initial interactive login dial: its caller owns the pausable
// deadline so browser authorization can temporarily suspend it.
//
// dial does NOT take s.mu: the serialization point is the CALLER (reconnect),
// so the lock is held across dial there. A standalone dial (the initial
// Connect path) runs uncontested.
func (s *Server) dial(ctx context.Context, initialInteractiveLogin bool) (*mcpsdk.ClientSession, error) {
	timeout := s.cfg.Timeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	dialCtx := ctx
	cancel := func() {}
	if !initialInteractiveLogin {
		dialCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   s.cfg.URL,
		HTTPClient: s.httpClient,
		// The standalone SSE GET stream is ENABLED (ADR 0057) so the server can
		// push notifications/* (tools|prompts|resources/list_changed). The SDK
		// opens it after initialize and drains it on session.Close(), so a
		// persistent goroutine per connected server is owned by the session and
		// unwinds on Close (inventoried in ADR 0027 List 1).
	}
	if s.oauth != nil {
		transport.OAuthHandler = s.oauth
	}

	// The three list-changed handlers are wired here — dial is the SINGLE
	// construction site reused by Connect (initial) and reconnect (after a
	// drop), so a reconnect re-attaches them automatically. Each handler only
	// sets a dirty flag + logs a WARN; it does NOT re-list eagerly (it runs on
	// the SDK's SSE goroutine — the SDK dispatches notifications sequentially
	// over the SSE stream, so an eager 30s re-list in the handler would stall
	// all later notifications on that session, i.e. head-of-line blocking).
	// The lazy re-list runs on the next read of the matching accessor.
	opts := &mcpsdk.ClientOptions{
		ToolListChangedHandler:     func(ctx context.Context, _ *mcpsdk.ToolListChangedRequest) { s.handleListChanged(ctx, "tools") },
		PromptListChangedHandler:   func(ctx context.Context, _ *mcpsdk.PromptListChangedRequest) { s.handleListChanged(ctx, "prompts") },
		ResourceListChangedHandler: func(ctx context.Context, _ *mcpsdk.ResourceListChangedRequest) { s.handleListChanged(ctx, "resources") },
	}

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: clientName, Version: clientVersion},
		opts,
	)

	sess, err := client.Connect(dialCtx, transport, nil)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// serverCapabilities returns the capabilities the server advertised in the
// initialize handshake, or nil if unavailable. It is the single place this
// adapter inspects negotiated capabilities, so the "skip absent capability"
// policy lives in one spot.
func serverCapabilities(sess *mcpsdk.ClientSession) *mcpsdk.ServerCapabilities {
	init := sess.InitializeResult()
	if init == nil {
		return nil
	}
	return init.Capabilities
}

// listTools pages through the server's tools and wraps each as a tool.Tool.
// The srv is the Server being built (its session is the freshly-connected one);
// the wrapper holds the *Server so Execute can re-establish a dropped session.
func listTools(ctx context.Context, serverName string, srv *Server, sess *mcpsdk.ClientSession) ([]tool.Tool, error) {
	var tools []tool.Tool
	for remote, err := range sess.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		wrapped, werr := newRemoteTool(serverName, srv, remote)
		if werr != nil {
			return nil, werr
		}
		tools = append(tools, wrapped)
	}
	return tools, nil
}

// HasOAuthCredential reports whether this connected OAuth server restored or
// durably stored a credential. It exposes readiness only and never reads or
// returns token data.
func (s *Server) HasOAuthCredential() bool {
	return s != nil && s.oauth != nil && s.oauth.state.hasCredential()
}

// Name returns the server's configured name.
func (s *Server) Name() string { return s.name }

// Tools returns the wrapped remote tools exposed by this server. If a
// notifications/tools/list_changed has fired since the last read (ADR 0057), the
// snapshot is lazily re-listed under a bounded context.Background() before
// returning, so a post-notification caller sees the server's current tool set.
// The re-list runs WITHOUT s.mu held (it may reconnect, which re-acquires
// s.mu); only the dirty-check and the final swap hold the lock.
func (s *Server) Tools() []tool.Tool {
	s.mu.Lock()
	dirty := s.toolsDirty
	tools := s.tools
	s.mu.Unlock()
	if dirty {
		s.refreshTools()
		s.mu.Lock()
		tools = s.tools
		s.mu.Unlock()
	}
	return tools
}

// Resources returns the server's resource snapshot. Lazily re-listed on a
// notifications/resources/list_changed (ADR 0057). See Tools() for the lock
// discipline.
func (s *Server) Resources() []Resource {
	s.mu.Lock()
	dirty := s.resourcesDirty
	res := s.resources
	s.mu.Unlock()
	if dirty {
		s.refreshResources()
		s.mu.Lock()
		res = s.resources
		s.mu.Unlock()
	}
	return res
}

// Prompts returns the server's prompt snapshot. Lazily re-listed on a
// notifications/prompts/list_changed (ADR 0057). See Tools() for the lock
// discipline.
func (s *Server) Prompts() []Prompt {
	s.mu.Lock()
	dirty := s.promptsDirty
	pr := s.prompts
	s.mu.Unlock()
	if dirty {
		s.refreshPrompts()
		s.mu.Lock()
		pr = s.prompts
		s.mu.Unlock()
	}
	return pr
}

// handleListChanged is the single entry point for the three SDK
// list-changed notification handlers wired in dial. It runs on the SDK's SSE
// goroutine, so it MUST NOT re-list eagerly: the SDK dispatches notifications
// sequentially over the SSE stream, so an eager 30s re-list in the handler
// would stall all later notifications on that session (head-of-line blocking).
// It only sets the matching dirty flag and logs a WARN; the re-list runs lazily
// on the next read of the accessor.
//
// The WARN is logged only on the false→true transition (the first notification
// since the last re-list cleared the flag), NOT on every notification. This
// bounds the log rate to the read rate (caller-bounded) rather than the
// notification rate (server-bounded), so a malicious server firing a
// notification storm cannot flood the diagnostics sink.
func (s *Server) handleListChanged(ctx context.Context, list string) {
	s.mu.Lock()
	already := true // already dirty? (suppress the WARN if so)
	switch list {
	case "tools":
		already = s.toolsDirty
		s.toolsDirty = true
		s.toolsGen++
	case "resources":
		already = s.resourcesDirty
		s.resourcesDirty = true
		s.resourcesGen++
	case "prompts":
		already = s.promptsDirty
		s.promptsDirty = true
		s.promptsGen++
	}
	s.mu.Unlock()
	if !already {
		s.diag.Log(ctx, port.LevelWarn, "mcp server list changed", "server", s.name, "list", list)
	}
}

// refreshCtx returns a bounded context for a lazy re-list. It is detached from
// any caller request context (a notification can fire with no live request)
// and bounded by the server's configured connect timeout so a stuck server
// cannot wedge an accessor.
func (s *Server) refreshCtx() (context.Context, context.CancelFunc) {
	timeout := s.cfg.Timeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}

// logRefreshErr is the single WARN path for a lazy re-list failure. It never
// aborts the accessor — the caller falls back to the prior snapshot, so a
// transient refresh failure surfaces as a stale (not missing) list.
func (s *Server) logRefreshErr(ctx context.Context, list string, err error) {
	s.diag.Log(ctx, port.LevelWarn, "mcp server list refresh failed",
		"server", s.name, "list", list, "err", clampErr(err))
}

// refreshTools re-lists the server's tools and swaps in the fresh snapshot.
// It runs WITHOUT s.mu held across the network (liveSession/reconnect
// re-acquires s.mu); only the dirty-clear+gen-snapshot (before the fetch) and
// the final conditional swap hold the lock. The dirty flag is cleared BEFORE
// the fetch so a notification arriving during the re-list re-arms it; the
// generation counter prevents a concurrent refresh from overwriting a newer
// snapshot with a stale one -- the swap only publishes if gen is unchanged.
// On a fetch failure the flag is re-set so the next read retries.
func (s *Server) refreshTools() {
	ctx, cancel := s.refreshCtx()
	defer cancel()
	s.mu.Lock()
	s.toolsDirty = false
	gen := s.toolsGen
	s.mu.Unlock()
	sess, err := s.liveSession(ctx)
	if err != nil {
		s.markDirty("tools")
		s.logRefreshErr(ctx, "tools", err)
		return
	}
	tools, err := listTools(ctx, s.name, s, sess)
	if err != nil {
		s.markDirty("tools")
		s.logRefreshErr(ctx, "tools", err)
		return
	}
	s.mu.Lock()
	if s.toolsGen == gen {
		s.tools = tools
	}
	s.mu.Unlock()
}

// markDirty re-sets the dirty flag for a list after a failed refresh, so the
// next read retries. It acquires s.mu internally (NOT a ...Locked suffix --
// the caller does NOT hold the lock).
func (s *Server) markDirty(list string) {
	s.mu.Lock()
	switch list {
	case "tools":
		s.toolsDirty = true
	case "resources":
		s.resourcesDirty = true
	case "prompts":
		s.promptsDirty = true
	}
	s.mu.Unlock()
}

// maxListEntries caps the number of entries ingested from a server's list
// iterator, so a malicious server cannot exhaust memory by streaming an
// unbounded list (CWE-770). It mirrors toolkit.MaxOutputBytes's defense of
// read-resource bodies — the missing cap on list cardinality was an
// inconsistency flagged in PR #197's review.
const maxListEntries = 10000

// refreshResources re-lists the server's resources and swaps in the fresh
// snapshot. See refreshTools for the lock discipline (clear-before-fetch) and
// the inline-over-live-sess rationale (NOT s.listResources, which reads the
// possibly-stale s.session). The ingestion is capped by maxListEntries.
func (s *Server) refreshResources() {
	ctx, cancel := s.refreshCtx()
	defer cancel()
	s.mu.Lock()
	s.resourcesDirty = false
	gen := s.resourcesGen
	s.mu.Unlock()
	sess, err := s.liveSession(ctx)
	if err != nil {
		s.markDirty("resources")
		s.logRefreshErr(ctx, "resources", err)
		return
	}
	var out []Resource
	count := 0
	for r, err := range sess.Resources(ctx, nil) {
		if err != nil {
			s.markDirty("resources")
			s.logRefreshErr(ctx, "resources", err)
			return
		}
		if count >= maxListEntries {
			s.diag.Log(ctx, port.LevelWarn, "mcp server list truncated",
				"server", s.name, "list", "resources", "cap", maxListEntries)
			break
		}
		out = append(out, resourceFromSDK(s.name, r))
		count++
	}
	s.mu.Lock()
	if s.resourcesGen == gen {
		s.resources = out
	}
	s.mu.Unlock()
}

// refreshPrompts re-lists the server's prompts and swaps in the fresh snapshot.
// See refreshTools for the lock discipline and refreshResources for the
// inline-over-live-sess rationale + ingestion cap.
func (s *Server) refreshPrompts() {
	ctx, cancel := s.refreshCtx()
	defer cancel()
	s.mu.Lock()
	s.promptsDirty = false
	gen := s.promptsGen
	s.mu.Unlock()
	sess, err := s.liveSession(ctx)
	if err != nil {
		s.markDirty("prompts")
		s.logRefreshErr(ctx, "prompts", err)
		return
	}
	var out []Prompt
	count := 0
	for p, err := range sess.Prompts(ctx, nil) {
		if err != nil {
			s.markDirty("prompts")
			s.logRefreshErr(ctx, "prompts", err)
			return
		}
		if count >= maxListEntries {
			s.diag.Log(ctx, port.LevelWarn, "mcp server list truncated",
				"server", s.name, "list", "prompts", "cap", maxListEntries)
			break
		}
		out = append(out, promptFromSDK(s.name, p))
		count++
	}
	s.mu.Lock()
	if s.promptsGen == gen {
		s.prompts = out
	}
	s.mu.Unlock()
}

// Close terminates the MCP session. It is safe to call once; subsequent calls
// return the SDK's session-close result. It takes s.mu and sets closed (the
// terminal flag) and dropped so a post-close call path never attempts a
// reconnect dial: reconnect's top-of-function `if s.closed` check returns
// errServerClosed before any dial.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	s.dropped = true
	sess := s.session
	oauthController := s.oauth
	s.mu.Unlock()
	var sessionErr error
	if sess != nil {
		sessionErr = sess.Close()
	}
	return errors.Join(sessionErr, oauthController.Close())
}

// Manager holds a set of connected MCP servers and presents their tools as a
// single aggregate. It is the convenient entry point when wiring several
// servers at once.
type Manager struct {
	servers []*Server
}

// NewManager connects to each ServerConfig over Streamable HTTP. By default a
// server that fails to connect is logged-and-skipped (via onError) so one bad
// server does not take down the harness; the surviving servers are returned in
// the Manager. If onError is nil, connection errors are silently skipped.
//
// Connections run CONCURRENTLY with a bounded fan-out (maxConnectConcurrency),
// so N independent servers connect in ~max(handshake) instead of N×(handshake).
// This is the dominant cost of embedded-server startup when ToolHive discovers
// multiple workloads (issue #218): the connects were serial, each bounded by
// defaultConnectTimeout. Order of m.servers is NOT guaranteed — callers must not
// assume insertion order (none do; Tools/Servers are name-routed, not positional).
//
// NewManager returns an error only if no servers could be connected AND at
// least one was configured, so the caller can distinguish "nothing usable" from
// "all good".
func NewManager(ctx context.Context, configs []ServerConfig, onError func(cfg ServerConfig, err error), diag port.Diagnostics) (*Manager, error) {
	// Same sink decoration as Connect.
	//
	// Unlike an earlier version of this comment, callers do NOT have to redact what
	// they are handed: the error reaches onError already redacted (Connect redacts
	// at the source) and the ServerConfig is replaced with a safe view
	// (safeCallbackConfig). The returned error is redacted too. That inversion is
	// deliberate — "the consumer owns its own log site" was the documented contract,
	// and one of three consumers still leaked, which is evidence the contract was
	// the wrong shape rather than that the consumer was careless.
	diag = redactDiagnostics(diag)
	m := &Manager{}
	if len(configs) == 0 {
		return m, nil
	}

	// Connect every server concurrently under a bounded semaphore. A per-server
	// result is collected regardless of outcome so the attempted/lastErr accounting
	// is identical to the prior serial loop. The cap bounds goroutine blast for an
	// operator with dozens of static servers; it is well above the typical ToolHive
	// default-group size so it is not the limiting factor in practice.
	type result struct {
		srv *Server
		cfg ServerConfig
		err error
	}
	results := make([]result, len(configs))
	sem := make(chan struct{}, maxConnectConcurrency)
	var wg sync.WaitGroup
	for i, cfg := range configs {
		wg.Add(1)
		go func(i int, cfg ServerConfig) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			srv, err := Connect(ctx, cfg, diag)
			results[i] = result{srv: srv, cfg: cfg, err: err}
		}(i, cfg)
	}
	wg.Wait()

	// Fold results in config order so the error reporting (onError + lastErr) is
	// deterministic regardless of which connect finished first.
	var lastErr error
	for _, r := range results {
		if r.err != nil {
			lastErr = r.err
			if onError != nil {
				// The CONFIG is a second credential channel, independent of the error:
				// a callback that logs sc.URL leaks a query token even when the error
				// beside it is clean, and one that logs sc.Headers leaks a bearer
				// outright. Connect's redaction cannot reach either, because the config
				// is the caller's own value travelling back to it. So the callback gets
				// a SAFE VIEW: URL redacted to scheme://host/path, Headers dropped.
				//
				// Names and everything else are preserved, which is what a callback
				// actually needs — the three in-tree callbacks log Name, and one logs
				// the URL for context. A future callback that genuinely needs the raw
				// URL or the headers has to reach for the original config it passed in,
				// which makes that a visible decision instead of an accident.
				onError(safeCallbackConfig(r.cfg), r.err)
			}
			continue
		}
		m.servers = append(m.servers, r.srv)
	}
	if len(results) > 0 && len(m.servers) == 0 {
		// lastErr already arrives redacted from Connect; RedactErrorValue here is the
		// belt on the braces, so a future change to this message cannot reintroduce a
		// URL without the wrapper catching it.
		return m, RedactErrorValue(fmt.Errorf("mcp: no servers could be connected: %w", lastErr))
	}
	return m, nil
}

// safeCallbackConfig returns cfg with its two credential-bearing fields made safe
// for an error callback to log: the URL redacted, the headers dropped. Every other
// field is preserved verbatim.
func safeCallbackConfig(cfg ServerConfig) ServerConfig {
	cfg.URL = RedactURL(cfg.URL)
	cfg.Headers = nil
	return cfg
}

// maxConnectConcurrency caps the number of MCP servers connecting in parallel.
// It is well above the typical ToolHive default-group size (7) while bounding
// goroutine/HTTP-connection fan-out for an operator with dozens of static
// servers. Each Connect is still independently bounded by its own (or
// defaultConnectTimeout) timeout.
const maxConnectConcurrency = 16

// Servers returns the successfully connected *Server values, for callers that
// need the live objects (tools/close), such as the composition root and tests.
func (m *Manager) Servers() []*Server { return m.servers }

// Tools returns the union of every connected server's wrapped tools.
func (m *Manager) Tools() []tool.Tool {
	var all []tool.Tool
	for _, s := range m.servers {
		all = append(all, s.Tools()...)
	}
	return all
}

// SelectedTools returns only direct tools from the named connected servers.
// ceiling, when non-empty, is an exact persisted tool-name ceiling: every name
// must still be advertised and no newly advertised tool is returned. The view
// borrows this Manager and never owns or closes a connection.
func (m *Manager) SelectedTools(names, ceiling []string) ([]tool.Tool, error) {
	if len(names) == 0 {
		if len(ceiling) != 0 {
			return nil, errors.New("mcp: tool ceiling requires selected servers")
		}
		return nil, nil
	}
	if m == nil {
		return nil, errors.New("mcp: no global manager is configured")
	}
	var selected []tool.Tool
	for _, name := range names {
		s, err := m.byName(name)
		if err != nil {
			return nil, err
		}
		tools := s.Tools()
		if len(tools) == 0 {
			return nil, fmt.Errorf("mcp: selected server %q advertises no tools", name)
		}
		selected = append(selected, tools...)
	}
	if len(ceiling) == 0 {
		return selected, nil
	}
	current := make(map[string]tool.Tool, len(selected))
	for _, candidate := range selected {
		name := candidate.Spec().Name
		if _, exists := current[name]; exists {
			return nil, fmt.Errorf("mcp: duplicate selected tool %q", name)
		}
		current[name] = candidate
	}
	if len(current) != len(ceiling) {
		return nil, fmt.Errorf("mcp: selected debug tool set changed (current=%d persisted=%d)", len(current), len(ceiling))
	}
	bounded := make([]tool.Tool, 0, len(ceiling))
	for _, name := range ceiling {
		candidate, ok := current[name]
		if !ok {
			return nil, fmt.Errorf("mcp: persisted debug tool %q is no longer available", name)
		}
		bounded = append(bounded, candidate)
	}
	return bounded, nil
}

// Provider is the read-side seam over the connected MCP servers' resources and
// prompts. It is what the resource/prompt meta-tools and the prompt expander are
// built against, and is the surface a later (gRPC) stage consumes to expose
// resources/prompts to clients. *Manager is the production implementation;
// routing is by server name, with the empty server name meaning "all servers".
//
// LAYERING: this seam lives in the adapter package (not the domain) for the same
// reason as skills.Source — resources/prompts are packaged at composition time;
// no domain port consumes them. ReadResource/GetPrompt return Go errors only for
// genuine faults (unknown server, transport failure); the tools/expander built
// over a Provider translate those into model-facing tool errors, never aborting
// a turn.
type Provider interface {
	// ListResources returns the resource snapshots. As of ADR 0057 these are
	// lazily refreshed on a notifications/resources/list_changed (the first call
	// after a notification pays a bounded synchronous re-list). server==""
	// returns the union across all servers; a specific name returns just that
	// server's (or an error if the name is unknown).
	ListResources(ctx context.Context, server string) ([]Resource, error)
	// ReadResource reads a single resource by URI from the named server.
	ReadResource(ctx context.Context, server, uri string) (ResourceContents, error)
	// ListPrompts returns the prompt snapshots. As of ADR 0057 these are lazily
	// refreshed on a notifications/prompts/list_changed. server=="" returns the
	// union across all servers.
	ListPrompts(ctx context.Context, server string) ([]Prompt, error)
	// GetPrompt expands a named prompt with args on the named server.
	GetPrompt(ctx context.Context, server, name string, args map[string]string) (PromptResult, error)
	// CallTool invokes a remote tool by name and returns the raw typed result
	// (content blocks + structured content), BEFORE any model-facing truncation.
	// Used by CallMcpWithQuery to filter the full result through jq before it
	// enters model context (remoteTool.Execute truncates/fail-closes, so it
	// cannot serve that path). Returns ErrUnknownServer (wrapped) for an
	// unknown server name; transport faults surface verbatim. args (a
	// json.RawMessage of the remote tool's input) are passed verbatim to the
	// remote tool.
	CallTool(ctx context.Context, server, toolName string, args json.RawMessage) (CallResult, error)
}

// Compile-time assertion that *Manager satisfies Provider.
var _ Provider = (*Manager)(nil)

// byName looks up a connected server by its configured name.
func (m *Manager) byName(server string) (*Server, error) {
	for _, s := range m.servers {
		if s.name == server {
			return s, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownServer, server)
}

// ListResources implements Provider. With an empty server name it returns the
// union of every connected server's resource snapshot; otherwise it returns the
// named server's snapshot (error if the name is unknown).
func (m *Manager) ListResources(_ context.Context, server string) ([]Resource, error) {
	if server == "" {
		var all []Resource
		for _, s := range m.servers {
			all = append(all, s.Resources()...)
		}
		return all, nil
	}
	s, err := m.byName(server)
	if err != nil {
		return nil, err
	}
	return s.Resources(), nil
}

// ReadResource implements Provider by routing the read to the named server.
func (m *Manager) ReadResource(ctx context.Context, server, uri string) (ResourceContents, error) {
	s, err := m.byName(server)
	if err != nil {
		return ResourceContents{}, err
	}
	chunks, err := s.readResource(ctx, uri)
	if err != nil {
		return ResourceContents{}, err
	}
	// Collapse the chunks into a single contents value: the flattened text/blob
	// summary is what model-facing callers want. URI/MIMEType are taken from the
	// first chunk when present.
	out := ResourceContents{URI: uri, Text: flattenResourceContents(chunks)}
	if len(chunks) > 0 {
		out.URI = chunks[0].URI
		out.MIMEType = chunks[0].MIMEType
	}
	return out, nil
}

// ListPrompts implements Provider. With an empty server name it returns the
// union of every connected server's prompt snapshot; otherwise the named one.
func (m *Manager) ListPrompts(_ context.Context, server string) ([]Prompt, error) {
	if server == "" {
		var all []Prompt
		for _, s := range m.servers {
			all = append(all, s.Prompts()...)
		}
		return all, nil
	}
	s, err := m.byName(server)
	if err != nil {
		return nil, err
	}
	return s.Prompts(), nil
}

// GetPrompt implements Provider by routing the expansion to the named server.
func (m *Manager) GetPrompt(ctx context.Context, server, name string, args map[string]string) (PromptResult, error) {
	s, err := m.byName(server)
	if err != nil {
		return PromptResult{}, err
	}
	return s.getPrompt(ctx, name, args)
}

// CallTool implements Provider by routing the raw tool call to the named server.
// Unlike remoteTool.Execute it returns the UNTRUNCATED raw result so a caller
// (CallMcpWithQuery) can filter it through jq before it enters model context.
func (m *Manager) CallTool(ctx context.Context, server, toolName string, args json.RawMessage) (CallResult, error) {
	s, err := m.byName(server)
	if err != nil {
		return CallResult{}, err
	}
	return s.callTool(ctx, toolName, args)
}

// Close closes every connected server, returning the first error encountered
// (after attempting to close them all).
func (m *Manager) Close() error {
	var firstErr error
	for _, s := range m.servers {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Register adds each wrapped tool to the catalog. It uses Catalog.Register (not
// MustRegister) so a name collision surfaces as an error rather than a panic; a
// collision should be impossible given the mcp__ namespacing, but two servers
// configured with the same Name (or a server advertising duplicate tools) would
// trip it.
//
// SKIP-AND-CONTINUE on collision: a duplicate-tool error is NON-fatal — the
// already-registered tool wins (FIRST-wins, so a caller registering the
// authoritative set first — e.g. the server-global MCP tools before client tools —
// keeps it), the colliding tool is skipped, and Register KEEPS registering the rest.
// This is what makes "global wins, the other non-colliding client tools are
// preserved" actually true (a return-on-first-dup would silently drop every tool
// ordered after the collider). Non-duplicate registration errors are also
// accumulated rather than aborting.
//
// It returns the NAMES of the skipped/failed tools (in encounter order) alongside the
// joined error, so a caller can emit ONE provenance-bearing WARN listing exactly which
// tools were dropped (e.g. "client MCP: shadowed by a server-global tool: <names>")
// rather than a generic line. The skipped slice is non-nil iff the error is non-nil;
// the error still satisfies errors.Is(_, tool.ErrDuplicateTool) when any collision
// occurred. Both are nil/empty when every tool registered cleanly.
func Register(cat *tool.Catalog, tools []tool.Tool) (skipped []string, err error) {
	var errs []error
	for _, t := range tools {
		if rerr := cat.Register(t); rerr != nil {
			name := t.Spec().Name
			skipped = append(skipped, name)
			errs = append(errs, fmt.Errorf("mcp: register %q: %w", name, rerr))
		}
	}
	return skipped, errors.Join(errs...)
}
