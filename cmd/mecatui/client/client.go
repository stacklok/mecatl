package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

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
	Server    string // host:port, e.g. 127.0.0.1:8080
	AuthToken string // optional bearer; sent as "authorization: Bearer <tok>"
	UseTLS    bool   // enable transport TLS
	TLSCAFile string // optional custom CA bundle for server verification
	Insecure  bool   // skip TLS verification (testing only; with UseTLS)
}

// Client is a connected mecated gRPC client: the dialled conn plus the generated
// service stub. Close it on shutdown.
type Client struct {
	conn        *grpc.ClientConn
	svc         mecatlv1.HarnessServiceClient
	scheduleSvc mecatlv1.ScheduleServiceClient
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
	if cfg.AuthToken != "" && !cfg.UseTLS && !loopback {
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
		// The credential requires transport security UNLESS the target is
		// loopback (the documented plaintext single-user default). For any
		// non-loopback target it demands TLS even when --tls is unset, so the
		// token can never ride a cleartext wire to a remote host.
		creds := bearerCreds{token: cfg.AuthToken, allowInsecure: loopback}
		opts = append(opts, grpc.WithPerRPCCredentials(creds))
	}

	conn, err := grpc.NewClient(cfg.Server, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", cfg.Server, err)
	}
	return &Client{conn: conn, svc: mecatlv1.NewHarnessServiceClient(conn), scheduleSvc: mecatlv1.NewScheduleServiceClient(conn)}, nil
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
func IsInvalidArgument(err error) bool {
	return status.Code(err) == codes.InvalidArgument
}

// transientVocab is the shared, case-insensitive vocabulary that marks a terminal
// error as TRANSIENT — a failure the run is likely to survive on a plain retry (an
// idle/stalled stream, an overloaded/unavailable backend, a rate limit, a transient
// upstream 5xx). It is deliberately kept in the client package (the ONLY mecatui
// layer with gRPC/proto access): the ui reads only the derived Transient bool on
// ResultMsg/StreamErrMsg, never classifies. Deliberately EXCLUDES the bare
// "server_error" token — OpenRouter reuses that string for non-transient upstream
// faults too, so auto-resuming on it would fight a genuinely broken run.
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

// TransientStreamErr classifies a Converse stream Recv error as transient (safe to
// auto-resume a paused queue against) vs a hard failure. The gRPC status code is the
// primary signal — Unavailable / DeadlineExceeded / ResourceExhausted are the classic
// retryable trio; a context deadline is transient too — with the status message and the
// raw error text as a vocabulary fallback for servers that fold a transient upstream
// condition into a generic code. A nil error is never transient.
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
	return NewStream(bidi, bidi), nil
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
	allowInsecure bool // true only for loopback targets
}

func (b bearerCreds) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + b.token}, nil
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
