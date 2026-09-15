package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/port"
)

// errServerClosed is the terminal sentinel returned by reconnect once Close has
// set the `closed` flag. Close makes the server terminal: a post-close call
// never dials, and a racing in-flight call cannot resurrect a closed server by
// clearing `dropped` on a successful dial. (It checks `closed`, the Close-only
// flag — NOT `dropped`, which is ALSO set by a dial failure as a clearable
// retry flag; a bare `if s.dropped` would make one transient dial failure
// permanently kill the server.)
var errServerClosed = errors.New("mcp: server closed")

// errReconnectFailed wraps a reconnect DIAL failure so call sites can distinguish
// "the server could not be re-established" from a genuine call-level fault and map
// it to a clear model-facing message instead of leaking the raw transport string
// (e.g. "connection refused" / "context deadline exceeded"). It is returned ONLY
// from reconnect's dial-failure path; the retry-drop path (a second drop after a
// successful reconnect) stays a plain drop error, which the call sites already map
// to the clear message via isConnectionDrop.
var errReconnectFailed = errors.New("mcp: server unavailable after reconnect")

// connectionDropSignatures are concrete fallback strings for transport/session
// loss emitted by older SDKs that did not expose a sentinel. They are consulted
// only after typed network causes and after ruling out a structured JSON-RPC peer
// response: peer-controlled response text can contain any of these signatures
// and must never trigger replay. "rejected by transport" is deliberately
// excluded because it also wraps per-call failures. A concrete legacy
// EOF/refused signature nested beneath that wrapper still matches. nil is never a drop.
var connectionDropSignatures = []string{
	"session not found",
	"client is closing",
	"connection closed",
	"connection refused",
	"EOF",
}

// isConnectionDrop reports whether err is in the class of "the MCP session is
// no longer usable and should be re-established". It is the single source of
// truth for that class: withSession, the tool/resource/prompt call sites, and
// the tests all consult it. nil → false.
func isConnectionDrop(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, mcpsdk.ErrConnectionClosed) {
		return true
	}
	if errors.Is(err, mcpsdk.ErrSessionMissing) {
		return true
	}
	// ErrRejected is itself a public jsonrpc.Error, and the SDK joins it with
	// the concrete client.Do cause. Preserve genuine network loss using only
	// typed causes before treating all remaining JSON-RPC errors as peer faults.
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	// The SDK wraps structured peer responses with %w. This check must precede
	// every text fallback because Error.Message is controlled by the peer and
	// may deliberately resemble a transport or session-loss error.
	var rpcErr *jsonrpc.Error
	if errors.As(err, &rpcErr) {
		return false
	}
	msg := err.Error()
	for _, sig := range connectionDropSignatures {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

// isAmbiguousTransportFailure reports a request whose delivery cannot be known.
// net/http emits this unexported error after an idle server connection closes while
// a POST may already have been written. It must be normalized for the model but
// never retried: replaying a mutating MCP call could apply it twice.
func isAmbiguousTransportFailure(err error) bool {
	var urlErr *url.Error
	return errors.As(err, &urlErr) && urlErr.Err != nil &&
		urlErr.Err.Error() == "http: server closed idle connection"
}

// unavailableMessage returns a safe model-facing error for a transport failure.
// An ambiguous delivery is never retried, whereas an unambiguous session drop
// retains the existing reconnect-and-retry behavior.
func unavailableMessage(err error, operation, server string) string {
	if isAmbiguousTransportFailure(err) {
		return fmt.Sprintf("%s: MCP server %q unavailable; request outcome is unknown", operation, server)
	}
	if isConnectionDrop(err) || errors.Is(err, errReconnectFailed) {
		return fmt.Sprintf("%s: MCP server %q unavailable after reconnect", operation, server)
	}
	return ""
}

// liveSession returns the live SDK session, re-establishing it if the current
// one has been marked dropped (a prior dial failure / Close / never-connected).
// The hot path — a call whose session is live — does not touch this reconnect
// machinery at all; a call that OBSERVES a drop drives reconnect directly via
// withSession's stale-session CAS. So `dropped` here is only the cold-path flag
// (a dial failure sets it so the next call retries; Close sets it AND closed so
// a post-close call short-circuits in reconnect; a never-connected server has
// session==nil).
func (s *Server) liveSession(ctx context.Context) (*mcpsdk.ClientSession, error) {
	s.mu.Lock()
	if !s.dropped && s.session != nil {
		sess := s.session
		s.mu.Unlock()
		return sess, nil
	}
	s.mu.Unlock()
	// Cold path: dropped (prior dial failure) or never-connected. Reconnect,
	// which short-circuits with errServerClosed if Close set `closed`.
	return s.reconnect(ctx, nil)
}

// reconnect re-establishes the session. It is the serialized reconnect path:
// the dial happens UNDER s.mu (the serialization point — N concurrent failing
// calls produce ONE dial, the rest wait and receive the fresh session via the
// stale-session double-check). After acquiring the lock it checks whether the
// live session is STILL the one the caller knew (stale): if another goroutine
// already reconnected (s.session != stale), hand back the fresh session without
// a second dial. A nil stale means "reconnect unconditionally" (the cold path
// from liveSession). The dial is bounded by cfg.Timeout, so the lock is held
// for bounded time.
//
// On failure it sets s.dropped=true so the next call retries again (closed is
// NOT set — a dial failure is transient, not terminal) and returns an
// errReconnectFailed-wrapped error (never a raw transport error). On success it
// swaps in the new session and clears dropped. The tool/resource/prompt lists are NOT re-fetched:
// the catalog registration (taken at connect) is preserved, so a server
// re-advertising a different tool set after restart keeps the old specs
// (accepted; same as the v1 snapshot contract).
func (s *Server) reconnect(ctx context.Context, stale *mcpsdk.ClientSession) (*mcpsdk.ClientSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Close is terminal: once closed is set by Close, a call must NEVER dial.
	// This runs BEFORE the CAS-skip and BEFORE any dial, so a racing in-flight
	// call that observed a drop cannot resurrect a closed server by clearing
	// dropped on a successful dial. Note this checks `closed` (the Close-only
	// terminal flag), NOT `dropped`: `dropped` is ALSO set by a dial FAILURE
	// (the retry flag), and a bare `if s.dropped` here would make one transient
	// dial failure permanently kill the server — blocking the next call's
	// retry, contrary to ADR 0056's "each call pays at most one reconnect
	// timeout" per-call retry semantics. `closed` is set ONLY by Close, so the
	// `s.dropped = false` on a successful dial (further down) can only clear a
	// self-set retry flag — never the Close flag.
	if s.closed {
		return nil, errServerClosed
	}

	// Stale-session double-check: another goroutine may have reconnected while
	// we waited on the mutex. If the live session is no longer the one the
	// caller failed against (or is non-nil for the unconditional cold path),
	// hand back the fresh one without a second dial. (The closed check above
	// is authoritative, so no `!s.dropped` here.)
	if stale != nil && s.session != stale && s.session != nil {
		return s.session, nil
	}

	s.diag.Log(ctx, port.LevelInfo, "mcp server reconnecting", "server", s.name)

	sess, err := s.dial(ctx, false)
	if err != nil {
		s.dropped = true // leave dropped so the next call retries again
		s.diag.Log(ctx, port.LevelWarn, "mcp server reconnect failed",
			"server", s.name, "err", clampErr(err))
		// Wrap with errReconnectFailed so call sites can map a dial failure
		// (connection refused / timeout / etc.) to the clear "unavailable after
		// reconnect" message instead of leaking the raw transport string. The
		// server name stays in the message for operator legibility.
		return nil, fmt.Errorf("%w: mcp: reconnect to server %q failed: %v", errReconnectFailed, s.name, err)
	}

	// Best-effort close of the dead session. Close is idempotent in the SDK; a
	// nil (never-connected) session is guarded. A close error is irrelevant —
	// the session was already dead.
	if s.session != nil {
		_ = s.session.Close()
	}
	s.session = sess
	s.dropped = false
	s.diag.Log(ctx, port.LevelInfo, "mcp server reconnected", "server", s.name)
	return sess, nil
}

// withSession is the single retry shape for every Server session-call site
// (tool Execute, readResource, getPrompt). It runs fn against the live session;
// on a connection-drop fault (and only that, not ctx cancellation or a genuine
// call-level fault) it reconnects ONCE — passing the stale session so reconnect
// can CAS-skip a redundant dial when a concurrent caller already reconnected —
// then re-runs fn. N concurrent calls that all observe the drop thus produce
// exactly ONE dial. A second drop on the retry is surfaced as the drop error
// (withSession does not reconnect twice), which the caller maps to a clear
// terminal message.
func (s *Server) withSession(ctx context.Context, fn func(*mcpsdk.ClientSession) error) error {
	sess, err := s.liveSession(ctx)
	if err != nil {
		return err
	}
	if err = fn(sess); err == nil {
		return nil
	}
	if ctx.Err() != nil { // cancellation, not a drop
		return err
	}
	if !isConnectionDrop(err) { // genuine call-level fault
		return err
	}
	sess, err = s.reconnect(ctx, sess) // stale=sess → CAS-skip if a peer already reconnected
	if err != nil {
		return err
	}
	return fn(sess) // one retry, no further reconnect
}

// clampErr renders err to a length-bounded string for a diagnostics arg, so a
// verbose transport error cannot blow a log line. It mirrors the local helper
// in internal/adapter/llmresilience (kept local rather than cross-imported);
// see ADR 0056. nil → "".
func clampErr(err error) string {
	if err == nil {
		return ""
	}
	const maxLen = 200
	// REDACT BEFORE CLAMPING. Order is load-bearing: clamping first can cut the
	// middle of a query string and leave a partial credential in the retained
	// prefix, which the diagnostics sink wrapper then cannot recognise as a URL to
	// scrub. Redacting first removes the query outright, so the clamp only ever
	// shortens already-safe text.
	s := RedactError(err)
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
