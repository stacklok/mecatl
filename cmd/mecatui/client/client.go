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
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/contracts/sessionaffinity"
)

// DialConfig is the connection-time configuration: address, optional bearer
// token, and TLS posture. It mirrors mecated's trust model — loopback is
// unauthenticated plaintext by default; non-loopback may need a token and/or
// TLS/mTLS.
type DialConfig struct {
	Server                 string      // host:port, e.g. 127.0.0.1:8080
	AuthToken              string      // optional static bearer; sent as "authorization: Bearer <tok>"
	TokenSource            TokenSource // optional dynamic bearer source, evaluated once per RPC
	ExplicitAnonymous      bool        // bypass saved OIDC state and send no bearer credential
	UseTLS                 bool        // enable transport TLS
	TLSCAFile              string      // optional custom CA bundle for server verification
	Insecure               bool        // skip TLS verification (testing only; with UseTLS)
	RemotePlaintextAllowed bool        // explicit authorization for non-loopback plaintext
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
	localContext mecatlv1.LocalSessionContextServiceClient
	scheduleSvc  mecatlv1.ScheduleServiceClient
	bearerBacked bool
	// displayServerEndpoint is a sanitized diagnostic projection of the configured
	// connection target, never a reconnect target, server response, or TLS/auth setting.
	displayServerEndpoint string
}

// withSessionAffinity adds the exact session identity to outgoing metadata while
// preserving credentials and any other metadata already carried by ctx.
func withSessionAffinity(ctx context.Context, id string) context.Context {
	if !sessionaffinity.ValidValue(id) {
		return ctx
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	if md == nil {
		md = metadata.MD{}
	}
	md.Set(sessionaffinity.HeaderName, id)
	return metadata.NewOutgoingContext(ctx, md)
}

// Dial connects to mecated per cfg. It uses grpc.NewClient (not the deprecated
// grpc.Dial), attaches a per-RPC bearer credential when a token is set, and
// configures transport credentials (plaintext for loopback by default, TLS/mTLS
// when requested). The connection is lazy; the first RPC (CreateSession)
// surfaces a connect error.
func Dial(cfg DialConfig) (*Client, error) {
	var opts []grpc.DialOption

	// A UNIX socket is as local as loopback and carries the same single-user
	// trust model, so IsLocalTarget — not IsLoopbackHost — is what gates every
	// plaintext decision below, INCLUDING bearerCreds.allowInsecure. Letting the
	// pre-dial guards accept a target the credential then rejects would hand the
	// operator gRPC's opaque "credentials require transport level security"
	// instead of our actionable message, which is the whole point of the guards.
	local := IsLocalTarget(cfg.Server)

	if !cfg.UseTLS && !local && !cfg.RemotePlaintextAllowed {
		return nil, fmt.Errorf("refusing plaintext to non-loopback %q without explicit authorization", cfg.Server)
	}

	if err := bearerTransportRefusal(cfg, local); err != nil {
		return nil, err
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
		creds := bearerCreds{token: cfg.AuthToken, allowInsecure: local}
		opts = append(opts, grpc.WithPerRPCCredentials(creds))
	} else if cfg.TokenSource != nil {
		opts = append(opts,
			grpc.WithPerRPCCredentials(bearerCreds{source: cfg.TokenSource, allowInsecure: local}),
			grpc.WithChainUnaryInterceptor(tokenSourceUnary(cfg.TokenSource)),
			grpc.WithChainStreamInterceptor(tokenSourceStream(cfg.TokenSource)),
		)
	} else {
		// Dialling with no credential at all is legitimate: the server is
		// authoritative about whether caller authentication is required. Annotate
		// only an actual server rejection; network and TLS failures stay transport
		// errors.
		opts = append(opts,
			grpc.WithChainUnaryInterceptor(credentialFreeUnaryHint(cfg.Server, cfg.ExplicitAnonymous)),
			grpc.WithChainStreamInterceptor(credentialFreeStreamHint(cfg.Server, cfg.ExplicitAnonymous)),
		)
	}

	conn, err := grpc.NewClient(cfg.Server, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", cfg.Server, err)
	}
	return &Client{
		conn:                  conn,
		svc:                   mecatlv1.NewHarnessServiceClient(conn),
		localContext:          mecatlv1.NewLocalSessionContextServiceClient(conn),
		scheduleSvc:           mecatlv1.NewScheduleServiceClient(conn),
		bearerBacked:          cfg.AuthToken != "" || cfg.TokenSource != nil,
		displayServerEndpoint: displayServerEndpoint(cfg),
	}, nil
}

// credentialFreeAuthError preserves the server's gRPC status while carrying a
// closed, server-authoritative recovery reason to the UI.
type credentialFreeAuthError struct {
	cause  error
	reason AuthReason
	msg    string
}

func (e *credentialFreeAuthError) Error() string              { return e.msg }
func (e *credentialFreeAuthError) Unwrap() error              { return e.cause }
func (e *credentialFreeAuthError) GRPCStatus() *status.Status { return status.Convert(e.cause) }
func (e *credentialFreeAuthError) AuthReason() AuthReason     { return e.reason }

func credentialFreeDialHint(server string, explicitAnonymous bool, err error) error {
	switch status.Code(err) {
	case codes.Unauthenticated:
		reason := AuthNotEnrolled
		prefix := "server requires caller authentication"
		if explicitAnonymous {
			reason = AuthAnonymousRejected
			prefix = "server rejected the explicit --anonymous connection because it requires caller authentication"
		}
		return &credentialFreeAuthError{
			cause:  err,
			reason: reason,
			msg:    fmt.Sprintf("%s; use --auth-token, or, if this server supports OIDC enrollment, run 'mecatui login %s': %v", prefix, server, err),
		}
	case codes.PermissionDenied:
		return fmt.Errorf("%w (server authorization denied this caller)", err)
	default:
		return err
	}
}

func credentialFreeUnaryHint(server string, explicitAnonymous bool) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return credentialFreeDialHint(server, explicitAnonymous, invoker(ctx, method, req, reply, cc, opts...))
	}
}

func credentialFreeStreamHint(server string, explicitAnonymous bool) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		stream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			return nil, credentialFreeDialHint(server, explicitAnonymous, err)
		}
		return hintingStream{ClientStream: stream, server: server, explicitAnonymous: explicitAnonymous}, nil
	}
}

type hintingStream struct {
	grpc.ClientStream
	server            string
	explicitAnonymous bool
}

func (s hintingStream) RecvMsg(m any) error {
	return credentialFreeDialHint(s.server, s.explicitAnonymous, s.ClientStream.RecvMsg(m))
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

// CreateSession allocates a server-owned session and returns its id together
// with the server's advertised capabilities and resolved model. The request
// deliberately carries no client filesystem path.
func (c *Client) CreateSession(ctx context.Context, mode mecatlv1.PermissionMode, sel ModelSelection) (string, Capabilities, ResolvedModel, error) {
	return c.createSession(ctx, &mecatlv1.CreateSessionRequest{
		Mode:            mode,
		ProviderId:      sel.ProviderID,
		ModelId:         sel.ModelID,
		ReasoningEffort: sel.ReasoningEffort,
	})
}

// SessionHandleWidth is the fixed maximum ASCII-column width of every ordinary
// session handle shown by mecatui.
const SessionHandleWidth = 12

// SessionHandle returns the fixed, terminal-safe escaped prefix used by every
// ordinary mecatui session presentation. Unreserved ASCII is copied verbatim,
// except that a leading hyphen is escaped; every other UTF-8 byte is one
// uppercase %HH atom. The longest complete-atom prefix fitting
// SessionHandleWidth is returned. Empty or invalid UTF-8 IDs have no handle.
func SessionHandle(id string) string {
	if id == "" || !utf8.ValidString(id) {
		return ""
	}
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	out.Grow(SessionHandleWidth)
	for i, b := range []byte(id) {
		safe := b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '.' || b == '_' || b == '-' && i > 0
		atomLen := 3
		if safe {
			atomLen = 1
		}
		if out.Len()+atomLen > SessionHandleWidth {
			break
		}
		if safe {
			out.WriteByte(b)
		} else {
			out.WriteByte('%')
			out.WriteByte(hex[b>>4])
			out.WriteByte(hex[b&0x0f])
		}
	}
	return out.String()
}

// CreateDebugSession creates a separate no-filesystem analysis session bound to
// targetID. A target with the canonical short-handle grammar is resolved against
// the caller-visible session inventory. Exact inventory equality wins; otherwise
// a unique projected handle resolves to its full ID. An inventory failure or no
// projected match leaves the target unchanged so the server's exact-ID authority
// decides the result. Capability absence is detected from the create response
// (the first common response carrying ServerCapabilities); an older server may
// ignore the new target field, so that accidentally-created ordinary session is
// closed before this method fails closed.
func (c *Client) CreateDebugSession(ctx context.Context, targetID string, mode mecatlv1.PermissionMode, sel ModelSelection, debugMCP ...string) (string, string, Capabilities, ResolvedModel, error) {
	resolvedTarget, err := c.resolveDebugTarget(ctx, targetID)
	if err != nil {
		return "", "", Capabilities{}, ResolvedModel{}, err
	}
	return c.createDebugSession(withSessionAffinity(ctx, resolvedTarget), resolvedTarget, mode, sel, debugMCP...)
}

func (c *Client) createDebugSession(ctx context.Context, resolvedTarget string, mode mecatlv1.PermissionMode, sel ModelSelection, debugMCP ...string) (string, string, Capabilities, ResolvedModel, error) {
	id, caps, resolved, err := c.createSession(ctx, &mecatlv1.CreateSessionRequest{
		Profile:              "no-fs",
		Mode:                 mode,
		ProviderId:           sel.ProviderID,
		ModelId:              sel.ModelID,
		ReasoningEffort:      sel.ReasoningEffort,
		DebugTargetSessionId: resolvedTarget,
		DebugMcpServers:      append([]string(nil), debugMCP...),
	})
	if err != nil {
		return "", resolvedTarget, Capabilities{}, ResolvedModel{}, err
	}
	if !caps.SessionDebug || len(debugMCP) > 0 && !caps.DebugMCP {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = c.CloseSession(cleanupCtx, id)
		if len(debugMCP) > 0 && !caps.DebugMCP {
			return "", resolvedTarget, Capabilities{}, ResolvedModel{}, errors.New("server does not support selected MCP tools in debug sessions")
		}
		return "", resolvedTarget, Capabilities{}, ResolvedModel{}, errors.New("server does not support dedicated session debugging")
	}
	return id, resolvedTarget, caps, resolved, nil
}

const debugTargetExactCopyGuidance = "open /session, copy the full exact session ID, and pass it as TARGET"

func validateDebugTarget(targetID string) error {
	if targetID == "" {
		return errors.New("debug target session ID must not be empty")
	}
	if !utf8.ValidString(targetID) {
		return errors.New("debug target session ID must be valid UTF-8")
	}
	return nil
}

func (c *Client) resolveDebugTarget(ctx context.Context, targetID string) (string, error) {
	if err := validateDebugTarget(targetID); err != nil {
		return "", err
	}
	if !isSessionHandleCandidate(targetID) {
		return targetID, nil
	}
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		return targetID, nil
	}

	ids := make(map[string]struct{}, len(sessions))
	for _, item := range sessions {
		ids[item.ID] = struct{}{}
	}
	if _, exact := ids[targetID]; exact {
		return targetID, nil
	}
	matches := make([]string, 0, 1)
	for id := range ids {
		if SessionHandle(id) == targetID {
			matches = append(matches, id)
		}
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("session handle %q is ambiguous; %s", targetID, debugTargetExactCopyGuidance)
	}
	if len(matches) == 0 {
		return targetID, nil
	}
	return matches[0], nil
}

func isSessionHandleCandidate(value string) bool {
	if value == "" || len(value) > SessionHandleWidth {
		return false
	}
	for i := 0; i < len(value); i++ {
		b := value[i]
		literal := b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '.' || b == '_' || b == '-' && i > 0
		if literal {
			continue
		}
		if b != '%' || i+2 >= len(value) || !isUpperHex(value[i+1]) || !isUpperHex(value[i+2]) {
			return false
		}
		i += 2
	}
	return true
}

func isUpperHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'A' && b <= 'F'
}

// CreateSessionWithCarryover forks sourceSessionID with model overrides. Server
// inheritance supplies placement, mode, limits, and any omitted model fields.
func (c *Client) CreateSessionWithCarryover(ctx context.Context, sel ModelSelection, sourceSessionID string) (string, Capabilities, ResolvedModel, error) {
	if sourceSessionID == "" {
		return "", Capabilities{}, ResolvedModel{}, fmt.Errorf("fork session: source session ID is required")
	}
	resp, err := c.svc.ForkSession(withSessionAffinity(ctx, sourceSessionID), &mecatlv1.ForkSessionRequest{
		SourceSessionId: sourceSessionID, ProviderId: sel.ProviderID, ModelId: sel.ModelID, ReasoningEffort: sel.ReasoningEffort,
	})
	if err != nil {
		return "", Capabilities{}, ResolvedModel{}, fmt.Errorf("fork session: %w", err)
	}
	snapshot, err := c.GetSession(ctx, resp.GetSessionId())
	if err != nil {
		return "", Capabilities{}, ResolvedModel{}, err
	}
	return resp.GetSessionId(), snapshot.Capabilities, snapshot.ResolvedModel, nil
}

func withoutSessionAffinity(ctx context.Context) context.Context {
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	md.Delete(sessionaffinity.HeaderName)
	return metadata.NewOutgoingContext(ctx, md)
}

// compatibilityCapabilities reads the sole server-wide capability source.
func (c *Client) compatibilityCapabilities(ctx context.Context) (Capabilities, error) {
	resp, err := c.svc.GetCompatibilityInfo(withoutSessionAffinity(ctx), &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		return Capabilities{}, fmt.Errorf("get compatibility info: %w", err)
	}
	return capabilitiesFrom(resp.GetCapabilities()), nil
}

// createSession is the shared CreateSession proto call and response unwrap body.
func (c *Client) createSession(ctx context.Context, req *mecatlv1.CreateSessionRequest) (string, Capabilities, ResolvedModel, error) {
	caps, err := c.compatibilityCapabilities(ctx)
	if err != nil {
		return "", Capabilities{}, ResolvedModel{}, err
	}
	resp, err := c.svc.CreateSession(ctx, req)
	if err != nil {
		return "", Capabilities{}, ResolvedModel{}, fmt.Errorf("create session: %w", err)
	}
	if media := resp.GetSessionCapabilities(); media != nil {
		caps.Image = media.GetImage()
		caps.Audio = media.GetAudio()
		caps.SessionMediaPresent = true
	}
	return resp.GetSessionId(), caps, resolvedModelFrom(resp.GetResolvedModel()), nil
}

// ClearSession creates an empty-history successor. A nil selector inherits the
// source placement; a non-nil selector must be one returned by ListWorktrees for
// this source session. The successor is fetched before return so callers bind
// only server-authored metadata.
func (c *Client) ClearSession(ctx context.Context, sourceID string, selector *WorktreeSelector) (string, SessionSnapshot, error) {
	var token *string
	if selector != nil {
		if selector.IsZero() {
			return "", SessionSnapshot{}, fmt.Errorf("clear session: worktree selector must not be empty")
		}
		token = &selector.token
	}
	resp, err := c.svc.ClearSession(withSessionAffinity(ctx, sourceID), &mecatlv1.ClearSessionRequest{SourceSessionId: sourceID, WorktreeSelector: token})
	if err != nil {
		return "", SessionSnapshot{}, fmt.Errorf("clear session: %w", err)
	}
	snapshot, err := c.GetSession(ctx, resp.GetSessionId())
	if err != nil {
		return "", SessionSnapshot{}, err
	}
	return resp.GetSessionId(), snapshot, nil
}

// ForkSession creates a peer session from the conversation-history snapshot of the
// session srcID (ADR 0065) and returns the bare new session id. reasoningEffort is
// the OPTIONAL effort override (ADR 0068): empty inherits the source's effort
// verbatim; provider and model ALWAYS inherit. This is the SINGLE proto-build point
// for the fork — the ui passes plain strings and never sees the proto request. The
// caller owns the follow-up GetSession refetch for the forked session's resolved
// model/capabilities echo (ForkSessionResponse carries only the id, no streaming).
func (c *Client) ForkSession(ctx context.Context, srcID, title, reasoningEffort string) (string, error) {
	resp, err := c.svc.ForkSession(withSessionAffinity(ctx, srcID), &mecatlv1.ForkSessionRequest{
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
	_, err := c.svc.CloseSession(withSessionAffinity(ctx, id), &mecatlv1.CloseSessionRequest{SessionId: id})
	if err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	return nil
}

// OpenConverse opens a fresh unbound bidi Converse stream for compatibility
// with raw callers. Mecatui uses OpenConverseForSession when it owns a target
// session.
func (c *Client) OpenConverse(ctx context.Context) (*Stream, error) {
	return c.openConverse(ctx)
}

// OpenConverseForSession opens a Converse stream carrying the exact session
// affinity metadata before the first prompt or retry frame is sent.
func (c *Client) OpenConverseForSession(ctx context.Context, sessionID string) (*Stream, error) {
	if !sessionaffinity.ValidValue(sessionID) {
		return nil, fmt.Errorf("open converse: session affinity requires 1-256 bytes of printable ASCII without boundary spaces")
	}
	return c.openConverse(withSessionAffinity(ctx, sessionID))
}

func (c *Client) openConverse(ctx context.Context) (*Stream, error) {
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

// RequestModeFromString maps the mode a client REQUESTS for a new session. It is
// ModeFromString except that an empty mode means "no preference" and goes out
// UNSPECIFIED, so the server applies its own configured default mode (ADR 0365)
// instead of receiving an explicit DEFAULT.
func RequestModeFromString(s string) mecatlv1.PermissionMode {
	if s == "" {
		return mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED
	}
	return ModeFromString(s)
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

// bearerTransportRefusal refuses to hand a bearer to a non-local server over a
// transport that cannot protect it. Both refusals live here, together, because
// they prevent the SAME credential leak and the per-RPC credential's
// RequireTransportSecurity() backstops NEITHER: it blocks plaintext only at send
// time (an opaque RPC failure instead of this actionable pre-dial error), and it
// cannot distinguish verified from unverified TLS at all.
//
//   - cleartext: anyone on the path reads the token off the wire.
//   - unverified TLS: encrypted but UNAUTHENTICATED, so an MITM presenting any
//     certificate terminates the session and reads the token just the same.
//     Note this arm is reachable precisely because Insecure turns UseTLS on,
//     which satisfies every other plaintext guard in Dial.
//
// It lives in Dial rather than in a caller's transport policy so EVERY caller of
// this package inherits it. See docs/adr/0287-target-aware-mecatui-tls.md.
func bearerTransportRefusal(cfg DialConfig, local bool) error {
	if local || (cfg.AuthToken == "" && cfg.TokenSource == nil) {
		return nil
	}
	switch {
	case !cfg.UseTLS:
		return fmt.Errorf(
			"refusing to send auth token in cleartext to non-loopback %q: use --tls", cfg.Server)
	case cfg.Insecure:
		return fmt.Errorf(
			"refusing to send auth token over unverified TLS to non-loopback %q: drop --insecure (use --tls-ca for a private CA)", cfg.Server)
	}
	return nil
}

// IsLocalTarget reports whether a gRPC dial target is local enough to carry a
// bearer over plaintext: a loopback host:port, or a "unix://" socket (which the
// filesystem, not the network, protects). Every plaintext/TLS decision in this
// package and in the mecatui connect TLS policy goes through THIS predicate, so
// the pre-dial guards and the per-RPC credential can never disagree about a
// target. See docs/adr/0287-target-aware-mecatui-tls.md.
func IsLocalTarget(server string) bool {
	return strings.HasPrefix(strings.TrimSpace(server), "unix://") || IsLoopbackHost(server)
}

// IsLoopbackHost reports whether the host part of a "host:port" (or bare host)
// target is loopback: an IP in 127.0.0.0/8, ::1, or the name "localhost".
// A target with no resolvable/parseable host is treated as NON-loopback (fail
// safe — we'd rather demand TLS than leak a token).
func IsLoopbackHost(server string) bool {
	host := strings.TrimSpace(server)
	if host == "" {
		return false
	}
	if strings.Contains(host, ":") {
		h, port, err := net.SplitHostPort(host)
		if err != nil {
			// A bare IPv6 literal is a valid host form; malformed host:port
			// spellings (including localhost: and localhost:not-a-port) fail closed.
			if ip := net.ParseIP(host); ip != nil {
				return ip.IsLoopback()
			}
			return false
		}
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return false
		}
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
