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
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

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
	// Timeout bounds the connect handshake and tool listing. If zero,
	// defaultConnectTimeout is used. It does not bound later tool calls, which
	// are governed by the per-call context.
	Timeout time.Duration
}

// ValidateClientURL validates a CLIENT-PROVIDED Streamable HTTP MCP endpoint
// before it is mounted per-session (e.g. an editor's session/new mcpServers
// entry). It is a deliberate SSRF backstop applied ONLY to the untrusted client
// path — the operator-configured Connect/NewManager path is intentionally NOT
// gated this way, since an operator may legitimately point a server at an
// internal host.
//
// The contract: the URL must be absolute and carry a host, and the scheme must be
// "https" — OR "http" only when the host is an explicit loopback address
// ("127.0.0.1", "::1", "localhost"). Everything else (file/ftp/gopher/etc., a
// relative URL, a hostless URL, or plaintext http to a non-loopback host) is
// rejected. Note this is a SCHEME/host-shape allowlist, not metadata-IP
// filtering: the editor is a local-trusted process, so we filter the obviously
// dangerous shapes rather than resolving and blocking link-local/metadata IPs.
func ValidateClientURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("mcp: client MCP URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("mcp: invalid client MCP URL %q: %w", raw, err)
	}
	if !u.IsAbs() {
		return fmt.Errorf("mcp: client MCP URL %q must be absolute", raw)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("mcp: client MCP URL %q has no host", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(host) {
			return nil
		}
		return fmt.Errorf("mcp: client MCP URL %q uses plaintext http to a non-loopback host; use https", raw)
	default:
		return fmt.Errorf("mcp: client MCP URL %q scheme %q not allowed (https, or http to loopback only)", raw, u.Scheme)
	}
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

// requestOrigin returns the lowercased scheme://host origin of u (host includes the
// port). It is the comparison key headerRoundTripper uses to decide whether the
// per-server headers may ride on a request — a redirected cross-origin hop yields a
// different origin and so receives none of the injected headers.
func requestOrigin(u *url.URL) string {
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// Server is a live connection to one remote MCP server. It owns the SDK client
// session and the tool.Tool wrappers derived from the server's tool list, plus
// the snapshots of the server's resources and prompts captured once at connect.
//
// As of ADR 0057 the adapter holds the standalone SSE GET stream open per
// connected server and subscribes to server-initiated
// notifications/{tools,prompts,resources}/list_changed: a notification sets the
// matching *Dirty flag, and the next read of Tools()/Resources()/Prompts()
// lazily re-lists under a bounded context.Background() and swaps in the fresh
// snapshot. Catalog mutation (live tool.Catalog refresh) is deliberately Phase 2
// — it gets its own ADR; in Phase 1 a tool the server dropped surfaces a
// tool-call error on use (the registered remoteTool specs are untouched).
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
	mu         sync.Mutex
	dropped    bool // set by a dial failure (retry flag) OR Close (terminal); cleared on a successful dial ONLY when not closed
	closed     bool // set ONLY by Close; terminal — a post-close call never dials. Distinct from dropped (the retry flag).
	session    *mcpsdk.ClientSession
	tools      []tool.Tool
	resources  []Resource
	prompts    []Prompt
	// Phase 1 (ADR 0057): dirty flags set by the list-changed notification
	// handlers and cleared on the next lazy re-list. They are read-and-cleared
	// under s.mu by the accessors; the re-list itself runs WITHOUT s.mu held
	// (see refreshTools/refreshResources/refreshPrompts) to avoid deadlocking
	// against reconnect, which re-acquires s.mu.
	toolsDirty     bool // set by ToolListChangedHandler; cleared on next Tools() re-list
	resourcesDirty bool // set by ResourceListChangedHandler; cleared on next Resources() re-list
	promptsDirty   bool // set by PromptListChangedHandler; cleared on next Prompts() re-list
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
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	if cfg.Name == "" {
		return nil, errors.New("mcp: server config requires a Name")
	}
	if strings.Contains(cfg.Name, "__") {
		return nil, fmt.Errorf("mcp: server name %q must not contain %q", cfg.Name, "__")
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("mcp: server %q requires a URL", cfg.Name)
	}

	// connectCtx bounds the one-time tool/resource/prompt listing at connect.
	// dial applies its own establishment timeout (s.cfg.Timeout) on the passed
	// ctx, so the handshake and the listings share this one bound.
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpClient := &http.Client{}
	if len(cfg.Headers) > 0 {
		// Copy headers so later mutation of cfg can't affect the live client.
		headers := make(map[string]string, len(cfg.Headers))
		for k, v := range cfg.Headers {
			headers[k] = v
		}
		// Scope the headers to the configured endpoint's origin so a cross-origin
		// redirect never re-sends the auth header to an unvetted host. A malformed URL
		// yields an empty origin, so the headers simply never match (fail-closed).
		origin := ""
		if u, perr := url.Parse(cfg.URL); perr == nil {
			origin = requestOrigin(u)
		}
		httpClient.Transport = &headerRoundTripper{
			base:    http.DefaultTransport,
			headers: headers,
			origin:  origin,
		}
	}

	// srv is constructed early so dial can populate its session field; the config,
	// diag, and httpClient are retained here because reconnect (reconnect.go) needs
	// them to re-establish a dropped session later.
	srv := &Server{
		name:       cfg.Name,
		cfg:        cfg,
		diag:       diag,
		httpClient: httpClient,
	}

	// dial applies cfg.Timeout itself; pass the raw ctx so the handshake bound
	// is owned in one place (the connect-time listings below share connectCtx).
	sess, err := srv.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp: connect to server %q: %w", cfg.Name, err)
	}
	srv.session = sess

	tools, err := listTools(connectCtx, cfg.Name, srv, sess)
	if err != nil {
		_ = sess.Close()
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
// defaultConnectTimeout) on top of the passed ctx as an establishment bound.
//
// dial does NOT take s.mu: the serialization point is the CALLER (reconnect),
// so the lock is held across dial there. A standalone dial (the initial
// Connect path) runs uncontested.
func (s *Server) dial(ctx context.Context) (*mcpsdk.ClientSession, error) {
	timeout := s.cfg.Timeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
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

	// The three list-changed handlers are wired here — dial is the SINGLE
	// construction site reused by Connect (initial) and reconnect (after a
	// drop), so a reconnect re-attaches them automatically. Each handler only
	// sets a dirty flag + logs a WARN; it does NOT re-list eagerly (it runs on
	// the SDK's SSE goroutine, where a network call would stall notification
	// processing and could deadlock against s.mu). The lazy re-list runs on
	// the next read of the matching accessor.
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
// goroutine, so it MUST NOT re-list eagerly (a network call there would stall
// notification processing and could deadlock against s.mu, which reconnect
// holds across dial). It only sets the matching dirty flag and logs a WARN; the
// re-list runs lazily on the next read of the accessor.
func (s *Server) handleListChanged(ctx context.Context, list string) {
	s.diag.Log(ctx, port.LevelWarn, "mcp server list changed", "server", s.name, "list", list)
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
// re-acquire s.mu); only the final check-and-swap holds the lock. On any
// failure it leaves the prior snapshot and the dirty flag alone (the next read
// retries), after logging a WARN.
func (s *Server) refreshTools() {
	ctx, cancel := s.refreshCtx()
	defer cancel()
	sess, err := s.liveSession(ctx) // reconnects if dropped; does NOT run under s.mu held by caller
	if err != nil {
		s.logRefreshErr(ctx, "tools", err)
		return
	}
	tools, err := listTools(ctx, s.name, s, sess)
	if err != nil {
		s.logRefreshErr(ctx, "tools", err)
		return
	}
	s.mu.Lock()
	s.tools = tools
	s.toolsDirty = false
	s.mu.Unlock()
}

// refreshResources re-lists the server's resources and swaps in the fresh
// snapshot. See refreshTools for the lock discipline. It inlines the iterator
// over the LIVE sess fetched from liveSession (NOT s.listResources, which reads
// the possibly-stale s.session) so the refresh always runs against the session
// it just (re)established.
func (s *Server) refreshResources() {
	ctx, cancel := s.refreshCtx()
	defer cancel()
	sess, err := s.liveSession(ctx)
	if err != nil {
		s.logRefreshErr(ctx, "resources", err)
		return
	}
	var out []Resource
	for r, err := range sess.Resources(ctx, nil) {
		if err != nil {
			s.logRefreshErr(ctx, "resources", err)
			return
		}
		out = append(out, resourceFromSDK(s.name, r))
	}
	s.mu.Lock()
	s.resources = out
	s.resourcesDirty = false
	s.mu.Unlock()
}

// refreshPrompts re-lists the server's prompts and swaps in the fresh snapshot.
// See refreshTools for the lock discipline and refreshResources for the
// inline-over-live-sess rationale.
func (s *Server) refreshPrompts() {
	ctx, cancel := s.refreshCtx()
	defer cancel()
	sess, err := s.liveSession(ctx)
	if err != nil {
		s.logRefreshErr(ctx, "prompts", err)
		return
	}
	var out []Prompt
	for p, err := range sess.Prompts(ctx, nil) {
		if err != nil {
			s.logRefreshErr(ctx, "prompts", err)
			return
		}
		out = append(out, promptFromSDK(s.name, p))
	}
	s.mu.Lock()
	s.prompts = out
	s.promptsDirty = false
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
	s.mu.Unlock()
	if sess == nil {
		return nil
	}
	return sess.Close()
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
// NewManager returns an error only if no servers could be connected AND at
// least one was configured, so the caller can distinguish "nothing usable" from
// "all good".
func NewManager(ctx context.Context, configs []ServerConfig, onError func(cfg ServerConfig, err error), diag port.Diagnostics) (*Manager, error) {
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	m := &Manager{}
	var lastErr error
	var attempted int
	for _, cfg := range configs {
		attempted++
		srv, err := Connect(ctx, cfg, diag)
		if err != nil {
			lastErr = err
			if onError != nil {
				onError(cfg, err)
			}
			continue
		}
		m.servers = append(m.servers, srv)
	}
	if attempted > 0 && len(m.servers) == 0 {
		return m, fmt.Errorf("mcp: no servers could be connected: %w", lastErr)
	}
	return m, nil
}

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
	// ListResources returns the static resource snapshots. server=="" returns the
	// union across all servers; a specific name returns just that server's (or an
	// error if the name is unknown).
	ListResources(ctx context.Context, server string) ([]Resource, error)
	// ReadResource reads a single resource by URI from the named server.
	ReadResource(ctx context.Context, server, uri string) (ResourceContents, error)
	// ListPrompts returns the static prompt snapshots. server=="" returns the
	// union across all servers.
	ListPrompts(ctx context.Context, server string) ([]Prompt, error)
	// GetPrompt expands a named prompt with args on the named server.
	GetPrompt(ctx context.Context, server, name string, args map[string]string) (PromptResult, error)
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
