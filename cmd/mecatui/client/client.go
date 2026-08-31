package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	pathpkg "path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// DialConfig is the connection-time configuration: address, optional bearer
// token, and TLS posture. It mirrors mecated's trust model — loopback is
// unauthenticated plaintext by default; non-loopback may need a token and/or
// TLS/mTLS.
type DialConfig struct {
	Server      string      // host:port, e.g. 127.0.0.1:8080
	AuthToken   string      // optional static bearer; sent as "authorization: Bearer <tok>"
	TokenSource TokenSource // optional dynamic bearer source, evaluated once per RPC
	UseTLS      bool        // enable transport TLS
	TLSCAFile   string      // optional custom CA bundle for server verification
	Insecure    bool        // skip TLS verification (testing only; with UseTLS)
}

// TokenSource supplies a current bearer token for an RPC. Dial must not invoke it.
type TokenSource interface {
	Token(context.Context) (string, error)
}

// Client is a connected mecated gRPC client: the dialled conn plus the generated
// service stub. Close it on shutdown.
type Client struct {
	conn         *grpc.ClientConn
	svc          mecatlv1.HarnessServiceClient
	scheduleSvc  mecatlv1.ScheduleServiceClient
	bearerBacked bool
	// displayServerEndpoint is a sanitized diagnostic projection of the configured
	// connection target, never a reconnect target, server response, or TLS/auth setting.
	displayServerEndpoint string
}

// Dial connects to mecated per cfg. It uses grpc.NewClient (not the deprecated
// grpc.Dial), attaches a per-RPC bearer credential when a token is set, and
// configures transport credentials (plaintext for loopback by default, TLS/mTLS
// when requested). The connection is lazy; the first RPC (CreateSession)
// surfaces a connect error.
func Dial(cfg DialConfig) (*Client, error) {
	var opts []grpc.DialOption

	loopback := IsLoopbackHost(cfg.Server)

	// Refuse to leak a bearer token in cleartext to a non-loopback server. The
	// per-RPC credential's RequireTransportSecurity() also blocks this at send
	// time, but a hard pre-dial guard gives the operator a clear, actionable
	// error instead of an opaque RPC failure later.
	if (cfg.AuthToken != "" || cfg.TokenSource != nil) && !cfg.UseTLS && !loopback {
		return nil, fmt.Errorf(
			"refusing to send auth token in cleartext to non-loopback %q: use --tls", cfg.Server)
	}

	if cfg.UseTLS {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec // Insecure is opt-in and documented testing-only
		if cfg.Insecure {
			tlsCfg.InsecureSkipVerify = true
		} else if cfg.TLSCAFile != "" {
			pool, err := loadCAPool(cfg.TLSCAFile)
			if err != nil {
				return nil, err
			}
			tlsCfg.RootCAs = pool
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	if cfg.AuthToken != "" {
		creds := bearerCreds{token: cfg.AuthToken, allowInsecure: loopback}
		opts = append(opts, grpc.WithPerRPCCredentials(creds))
	} else if cfg.TokenSource != nil {
		opts = append(opts,
			grpc.WithPerRPCCredentials(bearerCreds{source: cfg.TokenSource, allowInsecure: loopback}),
			grpc.WithChainUnaryInterceptor(tokenSourceUnary(cfg.TokenSource)),
			grpc.WithChainStreamInterceptor(tokenSourceStream(cfg.TokenSource)),
		)
	} else {
		// Dialling with no credential at all is legitimate: a mecated with no OIDC
		// configured needs none, so a missing saved credential cannot be an error
		// here. It becomes actionable only when the server actually demands one,
		// which is where these interceptors add the enrolment hint.
		opts = append(opts,
			grpc.WithChainUnaryInterceptor(anonymousUnaryHint(cfg.Server)),
			grpc.WithChainStreamInterceptor(anonymousStreamHint(cfg.Server)),
		)
	}

	conn, err := grpc.NewClient(cfg.Server, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", cfg.Server, err)
	}
	return &Client{
		conn:                  conn,
		svc:                   mecatlv1.NewHarnessServiceClient(conn),
		scheduleSvc:           mecatlv1.NewScheduleServiceClient(conn),
		bearerBacked:          cfg.AuthToken != "" || cfg.TokenSource != nil,
		displayServerEndpoint: displayServerEndpoint(cfg),
	}, nil
}

// anonymousDialHint annotates a server Unauthenticated rejection received by a
// client that carried no bearer credential. Without it the operator sees only the
// far side's "missing or invalid bearer token" and no indication that the local
// cause is an unenrolled target. Wrapping preserves the gRPC status, so
// status.Code classification elsewhere is unaffected.
func anonymousDialHint(server string, err error) error {
	if status.Code(err) != codes.Unauthenticated {
		return err
	}
	return fmt.Errorf("%w (no saved credential for %s; run 'mecatui login %s')", err, server, server)
}

func anonymousUnaryHint(server string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return anonymousDialHint(server, invoker(ctx, method, req, reply, cc, opts...))
	}
}

func anonymousStreamHint(server string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		stream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			return nil, anonymousDialHint(server, err)
		}
		// Server-side auth rejects before the handler runs, and gRPC surfaces that
		// on the first Recv rather than at stream creation, so the hint has to
		// cover RecvMsg too.
		return hintingStream{ClientStream: stream, server: server}, nil
	}
}

type hintingStream struct {
	grpc.ClientStream
	server string
}

func (s hintingStream) RecvMsg(m any) error {
	return anonymousDialHint(s.server, s.ClientStream.RecvMsg(m))
}

type tokenContextKey struct{}
type tokenContextValue struct {
	token string
}

func tokenSourceUnary(source TokenSource) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		token, err := source.Token(ctx)
		if err != nil {
			return err
		}
		ctx = context.WithValue(ctx, tokenContextKey{}, tokenContextValue{token: token})
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func tokenSourceStream(source TokenSource) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		token, err := source.Token(ctx)
		if err != nil {
			return nil, err
		}
		ctx = context.WithValue(ctx, tokenContextKey{}, tokenContextValue{token: token})
		return streamer(ctx, desc, cc, method, opts...)
	}
}

const maxDiagnosticEndpointBytes = 2048

// displayServerEndpoint projects the configured connection target as sanitized
// diagnostic display data, never as a reconnect target or connection instruction.
func displayServerEndpoint(cfg DialConfig) string {
	scheme := "http"
	if cfg.UseTLS {
		scheme = "https"
	}
	return sanitizeDiagnosticEndpoint(scheme + "://" + cfg.Server)
}

// sanitizeDiagnosticEndpoint accepts only an absolute URL and retains its
// scheme, host/port, and escaped clean path. Invalid input becomes unavailable.
func sanitizeDiagnosticEndpoint(raw string) string {
	if raw == "" || len(raw) > maxDiagnosticEndpointBytes || !utf8.ValidString(raw) {
		return ""
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return ""
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Hostname() == "" || strings.ContainsAny(u.Host, "\\/?#@") {
		return ""
	}
	escapedPath := u.EscapedPath()
	if escapedPath == "" {
		escapedPath = "/"
	} else {
		escapedPath = pathpkg.Clean(escapedPath)
		if !strings.HasPrefix(escapedPath, "/") {
			escapedPath = "/" + escapedPath
		}
	}
	return strings.ToLower(u.Scheme) + "://" + u.Host + escapedPath
}

// Close releases the underlying connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// CreateSession allocates a server-side session against an absolute workspace and
// returns its id together with the server's advertised Capabilities. mode is the
// proto PermissionMode (see ModeFromString). sel is the optional, proto-free model
// selection (its zero value ⇒ no provider_id/model_id set ⇒ the server's default).
// This is the SINGLE proto-build point for the model selection: the ui passes a
// plain ModelSelection and never sees the proto request. The Capabilities are the
// proto-free mirror of the create response's ServerCapabilities; an older server
// that omits the field yields the all-false zero value (see capabilitiesFrom).
// The ResolvedModel is the EFFECTIVE provider+model the server resolved the session
// to (echoed verbatim); an older server that omits the field yields the zero value
// (see resolvedModelFrom), which the ui renders as no model segment.
func (c *Client) CreateSession(ctx context.Context, workspace string, mode mecatlv1.PermissionMode, sel ModelSelection) (string, Capabilities, ResolvedModel, error) {
	return c.createSession(ctx, &mecatlv1.CreateSessionRequest{
		Workspace:       workspace,
		Mode:            mode,
		ProviderId:      sel.ProviderID,
		ModelId:         sel.ModelID,
		ReasoningEffort: sel.ReasoningEffort,
	})
}

// SessionIDDisplayWidth is the one session-ID width used by the TUI header and
// by debug-target prefix resolution.
const SessionIDDisplayWidth = 12

// DisplaySessionID returns the session ID exactly as shown in the TUI header.
func DisplaySessionID(id string) string {
	if len(id) <= SessionIDDisplayWidth {
		return id
	}
	return id[:SessionIDDisplayWidth]
}

// CreateDebugSession creates a separate no-filesystem analysis session bound to
// targetID. A target written exactly as the TUI's 12-character header ID is
// resolved against the caller-visible session inventory; the server still receives
// and authorizes only an exact ID. Capability absence is detected from the create
// response (the first common response carrying ServerCapabilities); an older server
// may ignore the new target field, so that accidentally-created ordinary session is
// closed before this method fails closed.
func (c *Client) CreateDebugSession(ctx context.Context, targetID string, mode mecatlv1.PermissionMode, sel ModelSelection) (string, string, Capabilities, ResolvedModel, error) {
	resolvedTarget, err := c.resolveDebugTarget(ctx, targetID)
	if err != nil {
		return "", "", Capabilities{}, ResolvedModel{}, err
	}
	id, caps, resolved, err := c.createSession(ctx, &mecatlv1.CreateSessionRequest{
		Profile:              "no-fs",
		Mode:                 mode,
		ProviderId:           sel.ProviderID,
		ModelId:              sel.ModelID,
		ReasoningEffort:      sel.ReasoningEffort,
		DebugTargetSessionId: resolvedTarget,
	})
	if err != nil {
		return "", resolvedTarget, Capabilities{}, ResolvedModel{}, err
	}
	if !caps.SessionDebug {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = c.CloseSession(cleanupCtx, id)
		return "", resolvedTarget, Capabilities{}, ResolvedModel{}, errors.New("server does not support dedicated session debugging")
	}
	return id, resolvedTarget, caps, resolved, nil
}

func (c *Client) resolveDebugTarget(ctx context.Context, targetID string) (string, error) {
	if len(targetID) != SessionIDDisplayWidth {
		return targetID, nil
	}
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve debug target: list sessions: %w", err)
	}
	for _, item := range sessions {
		if item.ID == targetID {
			return targetID, nil
		}
	}
	match := ""
	for _, item := range sessions {
		if !strings.HasPrefix(item.ID, targetID) {
			continue
		}
		if match != "" {
			return "", fmt.Errorf("session ID prefix %q is ambiguous; use the full session ID", targetID)
		}
		match = item.ID
	}
	if match != "" {
		return match, nil
	}
	// Preserve the server's ordinary not-found posture. The caller-filtered
	// inventory is only a convenience resolver; the server remains authoritative.
	return targetID, nil
}

// CreateSessionWithCarryover is CreateSession seeded with the source session's
// conversation history (issue #20). sourceSessionID, when non-empty, sets
// source_session_id on the request; the server snapshots the source (it must be
// at a turn boundary) and seeds the new session's history. The server is the
// authority on same-vs-cross: a same-provider carryover replays verbatim, a
// cross-provider carryover strips the prior provider's private replay blobs.
// An empty sourceSessionID is byte-identical to CreateSession (no carryover).
// The caller owns closing the source session AFTER the new one is ready (the
// server snapshotted it at create time). This is the SINGLE proto-build point
// for the carryover selector — the ui passes plain strings and never sees the
// proto.
func (c *Client) CreateSessionWithCarryover(ctx context.Context, workspace string, mode mecatlv1.PermissionMode, sel ModelSelection, sourceSessionID string) (string, Capabilities, ResolvedModel, error) {
	return c.createSession(ctx, &mecatlv1.CreateSessionRequest{
		Workspace:       workspace,
		Mode:            mode,
		ProviderId:      sel.ProviderID,
		ModelId:         sel.ModelID,
		ReasoningEffort: sel.ReasoningEffort,
		SourceSessionId: sourceSessionID,
	})
}

// createSession is the shared proto-build→call→unwrap body for both CreateSession
// and CreateSessionWithCarryover, so the carryover variant stays byte-identical to
// the plain create apart from the source_session_id field.
func (c *Client) createSession(ctx context.Context, req *mecatlv1.CreateSessionRequest) (string, Capabilities, ResolvedModel, error) {
	resp, err := c.svc.CreateSession(ctx, req)
	if err != nil {
		return "", Capabilities{}, ResolvedModel{}, fmt.Errorf("create session: %w", err)
	}
	return resp.GetSessionId(), capabilitiesFrom(resp.GetCapabilities()), resolvedModelFrom(resp.GetResolvedModel()), nil
}

// ForkSession creates a peer session from the conversation-history snapshot of the
// session srcID (ADR 0065) and returns the bare new session id. reasoningEffort is
// the OPTIONAL effort override (ADR 0068): empty inherits the source's effort
// verbatim; provider and model ALWAYS inherit. This is the SINGLE proto-build point
// for the fork — the ui passes plain strings and never sees the proto request. The
// caller owns the follow-up GetSession refetch for the forked session's resolved
// model/capabilities echo (ForkSessionResponse carries only the id, no streaming).
func (c *Client) ForkSession(ctx context.Context, srcID, title, reasoningEffort string) (string, error) {
	resp, err := c.svc.ForkSession(ctx, &mecatlv1.ForkSessionRequest{
		SourceSessionId: srcID,
		Title:           title,
		ReasoningEffort: reasoningEffort,
	})
	if err != nil {
		return "", fmt.Errorf("fork session: %w", err)
	}
	return resp.GetSessionId(), nil
}

// IsInvalidArgument reports whether err carries gRPC codes.InvalidArgument — the
// code the server maps a REJECTED CreateSession selector to (an unknown
// provider_id surfaces as server.ErrInvalidArgument → codes.InvalidArgument via
// toStatus; both the in-process UNIX-socket server and a remote mecated speak the
// same gRPC path). status.Code traverses wrapped errors, so CreateSession's
// "create session: %w" wrap above survives classification. The ui's connect-time
// fallback (issue #41) gates its zero-selection retry on this: only a genuine
// REJECTION of the carried selector falls back to the server default — a
// transient failure (unavailable, deadline) keeps the fatal path with the
// original error, never a dishonest "rejected" warning. Nil → false (codes.OK);
// a non-status error → false (codes.Unknown).
func IsInvalidArgument(err error) bool { return status.Code(err) == codes.InvalidArgument }

// IsNotFound reports the server's privacy-preserving absent-session result.
func IsNotFound(err error) bool { return status.Code(err) == codes.NotFound }

// transientVocab is the shared, case-insensitive legacy presentation vocabulary.
// It is deliberately kept in the client package (the ONLY mecatui layer with
// gRPC/proto access): the ui reads only the derived Transient bool on ResultMsg/
// StreamErrMsg, never classifies and never uses it to authorize replay. The bare
// "server_error" token is excluded because OpenRouter also uses it for permanent
// upstream faults.
//
// The word entries are matched as substrings (they are alphabetic, so they land
// word-bounded in real error text). The numeric HTTP status codes in transientCodes
// are matched with digit-boundary checks so a code like 503 matches "HTTP 503" but
// NOT "port 50378" or "model 1230503" — a bare substring match would misclassify a
// port/model id carrying those digits as transient.
var transientVocab = []string{
	"idle timeout", "stream stalled", "stream idle", "unavailable",
	"overloaded", "deadline exceeded", "too many requests",
	"temporarily", "engine_overloaded", "service_unavailable",
}

var transientCodes = []string{"429", "502", "503", "504"}

// matchesTransientVocab reports whether s contains any transientVocab substring
// (case-insensitive) or any transientCodes entry as a digit-bounded token. A blank
// string never matches.
func matchesTransientVocab(s string) bool {
	low := strings.ToLower(s)
	for _, v := range transientVocab {
		if strings.Contains(low, v) {
			return true
		}
	}
	for _, code := range transientCodes {
		if containsBoundedCode(low, code) {
			return true
		}
	}
	return false
}

// containsBoundedCode reports whether code appears in s bounded by non-digit
// characters (or the start/end of s), so "503" matches "HTTP 503" and "got a 503."
// but not "port 50378" or "model 1230503". s and code are assumed lower-cased.
func containsBoundedCode(s, code string) bool {
	for i := 0; i+len(code) <= len(s); i++ {
		if s[i:i+len(code)] != code {
			continue
		}
		if i > 0 && isDigitByte(s[i-1]) {
			continue
		}
		if end := i + len(code); end < len(s) && isDigitByte(s[end]) {
			continue
		}
		return true
	}
	return false
}

func isDigitByte(b byte) bool { return b >= '0' && b <= '9' }

// TransientStreamErr classifies a Converse stream Recv error for presentation and
// compatibility only. A stream error has no semantic commit fact, so the TUI never
// uses this signal to authorize automatic replay or queue draining. The gRPC status
// code is the primary signal, with vocabulary fallback for older servers.
func TransientStreamErr(err error) bool {
	if err == nil {
		return false
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return matchesTransientVocab(status.Convert(err).Message()) || matchesTransientVocab(err.Error())
}

// TransientResultError classifies a terminal ResultMsg's error TEXT as transient. A
// result-carried error has no gRPC status (the run completed with stop=error and an
// error string), so the classification is vocabulary-only. Empty text is never
// transient (a clean end_turn carries no error).
func TransientResultError(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	return matchesTransientVocab(text)
}

// CloseSession asks the server to end (and forget) the session under id, tearing
// down its per-session engine + any per-session MCP manager server-side. The
// /models restart-now handoff calls it on the OLD session before creating the new
// one, so a model switch leaves no orphaned server-side session. A nil/unknown id
// surfaces the server's error; the caller treats a close failure best-effort (the
// new session is created regardless).
func (c *Client) CloseSession(ctx context.Context, id string) error {
	_, err := c.svc.CloseSession(ctx, &mecatlv1.CloseSessionRequest{SessionId: id})
	if err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	return nil
}

// OpenConverse opens a fresh bidi Converse stream and wraps it in a Stream
// (serialised Sends + a Recver for the reader goroutine). Each user prompt opens
// one stream — matching the "one run per Converse" model. The stream's lifetime
// is bound to ctx; cancelling ctx aborts the run.
func (c *Client) OpenConverse(ctx context.Context) (*Stream, error) {
	bidi, err := c.svc.Converse(ctx)
	if err != nil {
		return nil, fmt.Errorf("open converse: %w", err)
	}
	return newAuthenticatedStream(bidi, bidi, c.bearerBacked), nil
}

// ModeDefaultString is the canonical CLI/UI spelling for default permission mode.
const ModeDefaultString = "default"

// ModeFromString maps a CLI mode string to the proto enum. Unknown/empty maps to
// UNSPECIFIED (the server defaults that to DEFAULT).
func ModeFromString(s string) mecatlv1.PermissionMode {
	switch s {
	case "plan":
		return mecatlv1.PermissionMode_PERMISSION_MODE_PLAN
	case "accept-edits", "acceptEdits", "accept_edits", "accept edits":
		return mecatlv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS
	case ModeDefaultString, "":
		return mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT
	default:
		return mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED
	}
}

// ModeString maps the proto enum to the CLI/UI spelling. Unknown/unspecified values
// degrade to "default", matching the server boundary.
func ModeString(m mecatlv1.PermissionMode) string {
	switch m {
	case mecatlv1.PermissionMode_PERMISSION_MODE_PLAN:
		return "plan"
	case mecatlv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS:
		return "accept-edits"
	case mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED:
		return ModeDefaultString
	default:
		return ModeDefaultString
	}
}

// NextMode returns the next mode in the TUI's cycle order.
func NextMode(mode string) string {
	switch ModeString(ModeFromString(mode)) {
	case ModeDefaultString:
		return "plan"
	case "plan":
		return "accept-edits"
	default:
		return ModeDefaultString
	}
}

// loadCAPool reads a PEM CA bundle into a cert pool for server verification.
func loadCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path) //nolint:gosec // operator-supplied CA path
	if err != nil {
		return nil, fmt.Errorf("read TLS CA %q: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates parsed from CA %q", path)
	}
	return pool, nil
}

// bearerCreds implements grpc.PerRPCCredentials, attaching the mecated bearer
// token as the lowercase "authorization" metadata the server reads.
// RequireTransportSecurity() returns false ONLY for a loopback target (the
// documented plaintext single-user default); for any non-loopback target it
// returns true, so grpc-go refuses to send the token over a cleartext wire.
type bearerCreds struct {
	token         string
	source        TokenSource
	allowInsecure bool // true only for loopback targets
}

func (b bearerCreds) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	token := b.token
	if b.source != nil {
		if cached, ok := ctx.Value(tokenContextKey{}).(tokenContextValue); ok {
			token = cached.token
		} else {
			var err error
			token, err = b.source.Token(ctx)
			if err != nil {
				return nil, err
			}
		}
	}
	if token == "" {
		return nil, errors.New("empty bearer token")
	}
	return map[string]string{"authorization": "Bearer " + token}, nil
}

func (b bearerCreds) RequireTransportSecurity() bool { return !b.allowInsecure }

// IsLoopbackHost reports whether the host part of a "host:port" (or bare host)
// target is loopback: an IP in 127.0.0.0/8, ::1, or the name "localhost".
// A target with no resolvable/parseable host is treated as NON-loopback (fail
// safe — we'd rather demand TLS than leak a token).
func IsLoopbackHost(server string) bool {
	host := server
	if h, _, err := net.SplitHostPort(server); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
