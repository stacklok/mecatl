// Package llmresilience provides a harness-level resilience decorator around any
// port.LLMProvider. It adds bounded retries with exponential backoff and jitter,
// a consecutive-failure circuit breaker, per-attempt timeouts, and pluggable
// error classification.
//
// The load-bearing correctness rule is no replay after semantic visibility.
// Leading whitespace-only text, reasoning/replay metadata, phase, provider route,
// and tool calls are tentative and remain buffered in wire order. Usage is also
// buffered but is accounting, not semantic visibility: discarded attempts add it
// to the eventual success or terminal error without exposing their content. The
// first text delta that makes cumulative text non-whitespace flushes the semantic
// buffer and commits the attempt; after it escapes, a failure is terminal. A clean
// ChunkDone instead flushes the whole tentative turn, including pure-tool-call
// and whitespace-only turns. A retryable failure before either boundary discards
// the tentative attempt and may be replayed within the configured attempt limit.
//
// Establishment ends on the first RAW chunk, independently of semantic progress.
// Subsequent raw chunk activity resets StreamIdleTimeout even while all chunks
// remain tentative, so active reasoning or tool assembly cannot trip either
// watchdog merely because it is not yet model-visible.
//
// The breaker counts consecutive TRANSIENT establishment failures across calls
// (HTTP 429/408/5xx, network errors, per-attempt timeouts — see
// isTransientForBreaker). Permanent client errors (4xx other than 408/429, e.g.
// a policy-blocked or unavailable model returning 400/403/404) and caller
// cancellations are breaker-neutral: they neither open the breaker nor reset it.
// After BreakerThreshold transient failures it opens for BreakerCooldown, then
// admits one half-open trial. Denied callers wait within RecoveryBudget; a zero
// budget fails fast with *BreakerError. Only clean completion resets the breaker.
// All breaker state is concurrency-safe.
//
// The package depends only on the standard library and engine/port; the
// classifier reaches *openai.Error via errors.As to read its StatusCode, which
// is acceptable for an adapter.
package llmresilience

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	oai "github.com/openai/openai-go/v3"
	"golang.org/x/net/http2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Config tunes the resilience decorator. The zero value is usable but inert
// (a single attempt, no breaker); supply sensible values via Wrap.
type Config struct {
	// MaxAttempts is the total number of attempts for establishing the stream
	// (the initial call plus retries). Values < 1 are treated as 1.
	MaxAttempts int
	// RecoveryBudget bounds precommit recovery from the first retryable failure
	// or breaker rejection. Zero preserves ordinary attempts/backoff only.
	RecoveryBudget time.Duration
	// BaseBackoff is the backoff before the first retry; it grows exponentially.
	BaseBackoff time.Duration
	// MaxBackoff caps the per-attempt backoff. 0 means no cap.
	MaxBackoff time.Duration
	// PerAttemptTimeout bounds each attempt's establishment (connect + first raw
	// chunk). 0 disables it. It never overrides a shorter caller deadline.
	PerAttemptTimeout time.Duration
	// StreamIdleTimeout bounds the gap between consecutive raw chunks AFTER the
	// first chunk has been observed. 0 disables it. A longer stall is retryable
	// while semantic progress remains precommit and terminal after visible output.
	StreamIdleTimeout time.Duration
	// BreakerThreshold is the number of consecutive failed attempts that opens
	// the breaker. Values < 1 disable the breaker.
	BreakerThreshold int
	// BreakerCooldown is how long the breaker stays open before half-opening.
	BreakerCooldown time.Duration
	// Classifier is a legacy tighten-only retry-policy veto. Typed causal
	// classification runs first; Classifier is consulted only for errors already
	// classified retryable and can refuse another attempt. It cannot upgrade an
	// unknown or permanent error. nil selects DefaultClassifier.
	Classifier func(error) bool
	// Clock returns the current time; injectable for tests. nil selects
	// time.Now.
	Clock func() time.Time
	// Diagnostics is the optional operational-logging sink for stream-lifecycle
	// events (retries, per-attempt timeouts, idle stalls, breaker transitions,
	// exhaustion). nil selects port.NopDiagnostics. This is an ADAPTER seam — it is
	// NOT the loop's run-scoped sink and so is NOT subject to the loop's three-line
	// budget (see docs/adr/0020-diagnostics.md): the wrapper is per-provider and
	// logs provider-level lifecycle. It sees only port.LLMRequest + errors, never
	// prompt text, so every emitted record is metadata-only.
	Diagnostics port.Diagnostics
}

// Wrap decorates inner with the resilience behaviour described by cfg and
// returns a port.LLMProvider. The returned provider is safe for concurrent use.
func Wrap(inner port.LLMProvider, cfg Config) port.LLMProvider {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	if cfg.Classifier == nil {
		cfg.Classifier = DefaultClassifier
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Diagnostics == nil {
		cfg.Diagnostics = port.NopDiagnostics{}
	}
	return &resilientProvider{inner: inner, cfg: cfg}
}

// ErrCredentials marks a failure to MINT OR LOAD the credential a request
// needs, as distinct from a failure of the provider that would have served it. A
// transport that resolves a token per request (the direct-mode bearer
// RoundTripper) wraps its token-source failures with it.
//
// It exists because the two are otherwise indistinguishable here. net/http wraps
// ANY error a RoundTripper returns in *url.Error, and *url.Error carries
// Timeout()/Temporary() so it satisfies net.Error — meaning "I could not get a
// token" arrives looking exactly like "the network flaked". Both classifiers
// below would then call it retryable AND provider-unhealthy, so the one error
// the user can act on gets retried MaxAttempts times, counted toward the shared
// breaker, and finally replaced by the breaker's own error. Wrapping with this
// sentinel routes the failure to the permanent, breaker-neutral path instead, so
// the real cause reaches the caller on the first attempt.
var ErrCredentials = errors.New("credential unavailable")

// BreakerError is returned by Stream while the circuit breaker is open. It
// carries the time at which the breaker is next eligible to half-open so callers
// can surface a meaningful terminal error.
type BreakerError struct {
	// RetryAfter is how long until the breaker half-opens.
	RetryAfter time.Duration
}

// RetryDisposition keeps admission exhaustion eligible for a later manual retry.
func (*BreakerError) RetryDisposition() session.RetryDisposition {
	return session.RetryDispositionRetryable
}

func (e *BreakerError) Error() string {
	return fmt.Sprintf("llmresilience: circuit breaker open, retry after %s", e.RetryAfter)
}

// ExhaustedError is returned when every attempt to establish the stream failed.
// It wraps the last underlying error.
type ExhaustedError struct {
	// Attempts is how many attempts were made.
	Attempts int
	// Err is the final underlying error.
	Err error
	// PerAttempt is the per-attempt establishment budget that was in force, used to
	// name the timeout duration in the operator-facing message. 0 when unset.
	PerAttempt time.Duration
}

// Error renders an OPERATOR-FACING message (issue #82): it surfaces verbatim to the
// TUI footer / transcript, so it avoids the internal package prefix and the raw inner
// sentinel, says in plain language what went wrong, and names the next action. The
// first-chunk timeout (the model connected but never started responding within the
// per-attempt budget — errFirstChunkTimeout) gets a tailored message; any other
// exhausted cause falls back to a generic but still prefix-free, action-bearing line.
func (e *ExhaustedError) Error() string {
	if errors.Is(e.Err, errFirstChunkTimeout) {
		within := "the per-attempt timeout"
		if e.PerAttempt > 0 {
			within = fmt.Sprintf("%s (%s)", within, e.PerAttempt)
		}
		return fmt.Sprintf(
			"the model did not start responding within %s, across %d attempt(s). "+
				"Try resending, switching to a faster model, or raising --llm-per-attempt-timeout.",
			within, e.Attempts)
	}
	return fmt.Sprintf(
		"the model stream could not be established after %d attempt(s): %v. "+
			"Try resending or raising --llm-per-attempt-timeout.",
		e.Attempts, e.Err)
}

// Unwrap exposes the final underlying error to errors.Is/As.
func (e *ExhaustedError) Unwrap() error { return e.Err }

// StreamIdleError is synthesized when no raw chunk arrives within
// StreamIdleTimeout after activity began. The wrapper must synthesize it because
// providers may swallow the context error used to unblock their stream. It is a
// retryable transport failure while the semantic attempt is precommit, and
// terminal after visible output has escaped.
type StreamIdleError struct {
	// Idle is the configured idle budget that elapsed without a chunk.
	Idle time.Duration
}

func (e *StreamIdleError) Error() string {
	return fmt.Sprintf("llmresilience: llm stream stalled: no chunk for %s", e.Idle)
}

// Unwrap returns context.DeadlineExceeded so errors.Is(err, context.DeadlineExceeded)
// holds, classifying the stall as a deadline (not a caller cancel).
func (*StreamIdleError) Unwrap() error { return context.DeadlineExceeded }

// errFirstChunkTimeout is the synthesized inner error for an establishment
// attempt that connected but produced no first chunk before the per-attempt
// deadline fired (the adapter swallowed the ctx error and yielded nothing).
// attemptError wraps it as `DeadlineExceeded: errFirstChunkTimeout`, so it is
// retryable per DefaultClassifier and transient per isTransientForBreaker (both
// key off context.DeadlineExceeded), and it surfaces a diagnosable message
// instead of a phantom empty-success completion. See establish's empty branch.
var errFirstChunkTimeout = errors.New("llmresilience: per-attempt timeout before first chunk")

// dispositionError projects known causal and progress classifications through errors.As.
type dispositionError struct {
	err         error
	disposition session.RetryDisposition
	progress    session.StreamProgress
}

func (e *dispositionError) Error() string                              { return e.err.Error() }
func (e *dispositionError) Unwrap() error                              { return e.err }
func (e *dispositionError) RetryDisposition() session.RetryDisposition { return e.disposition }
func (e *dispositionError) StreamProgress() session.StreamProgress     { return e.progress }

func explicitRetryDecision(err error) (retryable, explicit bool) {
	var decision interface{ Retryable() bool }
	if !errors.As(err, &decision) {
		return false, false
	}
	return decision.Retryable(), true
}

func dispositionOf(err error) session.RetryDisposition {
	if err == nil {
		return session.RetryDispositionUnknown
	}
	var classified port.RetryDispositionError
	if errors.As(err, &classified) {
		disposition := classified.RetryDisposition()
		if disposition.Valid() {
			return disposition
		}
		return session.RetryDispositionUnknown
	}
	return defaultDisposition(err)
}

func classifiedProgressError(err error, progress session.StreamProgress) error {
	d := dispositionOf(err)
	if err == nil {
		return nil
	}
	var classified port.RetryDispositionError
	var progressed port.StreamProgressError
	if errors.As(err, &classified) && errors.As(err, &progressed) && progressed.StreamProgress() == progress {
		return err
	}
	return &dispositionError{err: err, disposition: d, progress: progress}
}

// classifyVisibleError preserves the causal classification of a terminal
// mid-stream failure while attaching its visible progress.
func classifyVisibleError(err error) error {
	if err == nil {
		return nil
	}
	return classifiedProgressError(err, session.StreamProgressVisible)
}

// breakerState is the closed/open/half-open state machine, guarded by mu.
type breakerState struct {
	mu sync.Mutex
	// consecutiveFailures counts failed attempts since the last success.
	consecutiveFailures int
	// open is true while the breaker is open or half-open after cooldown.
	open bool
	// openedAt is when the breaker last opened.
	openedAt time.Time
	// halfOpen is true when a single trial is permitted after cooldown.
	halfOpen   bool
	generation uint64
	changed    chan struct{}
}

type resilientProvider struct {
	inner   port.LLMProvider
	cfg     Config
	breaker breakerState
}

// diag returns the configured Diagnostics sink, nil-safe (mirrors the engine's
// cfg.diag() helper). Wrap defaults cfg.Diagnostics to port.NopDiagnostics, but a
// resilientProvider constructed directly (zero Config in a test) may still hold a
// nil, so guard here too.
func (p *resilientProvider) diag() port.Diagnostics {
	if p.cfg.Diagnostics == nil {
		return port.NopDiagnostics{}
	}
	return p.cfg.Diagnostics
}

type attemptDecision string

const (
	decisionRetry    attemptDecision = "retry"
	decisionTerminal attemptDecision = "terminal"
)

type replaySuppressedReason string

const (
	replayVisibleOutput        replaySuppressedReason = "visible_output"
	replayAttemptsExhausted    replaySuppressedReason = "attempts_exhausted"
	replayPermanent            replaySuppressedReason = "permanent"
	replayUnknown              replaySuppressedReason = "unknown"
	replayClassifierVeto       replaySuppressedReason = "classifier_veto"
	replayProviderInternalVeto replaySuppressedReason = "provider_internal_veto"
	replayBreakerOpen          replaySuppressedReason = "breaker_open"
)

type attemptDiagnostic struct {
	ctx         context.Context
	model       string
	attempt     int
	maxAttempts int
	started     time.Time
	lease       *breakerLease
}

// logAttemptDecision is the single failed-attempt decision log path. It records
// causal classification and sanitized errors.As metadata without rendering the
// raw error, which may contain response bodies, URLs, headers, or credentials.
func (p *resilientProvider) logAttemptDecision(
	attempt attemptDiagnostic,
	err error,
	decision attemptDecision,
	progress session.StreamProgress,
	reason replaySuppressedReason,
	backoff time.Duration,
) {
	elapsed := p.cfg.Clock().Sub(attempt.started)
	if elapsed < 0 {
		elapsed = 0
	}
	metadata, metadataOK := attemptMetadata(err)
	observation := session.NetworkAttemptPayload{
		Attempt:          attempt.attempt,
		MaxAttempts:      attempt.maxAttempts,
		ElapsedMs:        elapsed.Milliseconds(),
		RetryDisposition: retryDispositionDiagnostic(dispositionOf(err)),
		StreamProgress:   streamProgressDiagnostic(progress),
		Decision:         string(decision),
		FailureClass:     attemptFailureClass(err, metadata),
	}
	if id, ok := port.SessionIDFromContext(attempt.ctx); ok {
		observation.SessionID = id
	}
	if serial, ok := port.RunSerialFromContext(attempt.ctx); ok {
		observation.RunSerial = serial
	}
	if turn, ok := port.TurnIndexFromContext(attempt.ctx); ok {
		observation.Turn = turn
	}
	if decision == decisionRetry {
		observation.BackoffMs = backoff.Milliseconds()
	} else {
		observation.SuppressionReason = string(reason)
	}
	if metadataOK {
		observation.HTTPStatus = metadata.httpStatus
		observation.InBandStatus = metadata.inBandStatus
		if digest, ok := session.NetworkCorrelationDigest(metadata.correlationKind, metadata.correlationID); ok {
			observation.CorrelationKind = metadata.correlationKind
			observation.CorrelationDigest = digest
		}
	}

	args := []any{
		"model", attempt.model,
		"attempt", observation.Attempt,
		"max_attempts", observation.MaxAttempts,
		"elapsed", elapsed,
		"retry_disposition", observation.RetryDisposition,
		"stream_progress", observation.StreamProgress,
		"decision", observation.Decision,
		"failure_class", observation.FailureClass,
	}
	if observation.SessionID != "" {
		args = append(args, "session", observation.SessionID)
	}
	if _, ok := port.RunSerialFromContext(attempt.ctx); ok {
		args = append(args, "run_serial", observation.RunSerial)
	}
	if _, ok := port.TurnIndexFromContext(attempt.ctx); ok {
		args = append(args, "turn", observation.Turn)
	}
	if decision == decisionRetry {
		args = append(args, "backoff", backoff)
	} else {
		args = append(args, "replay_suppressed_reason", observation.SuppressionReason)
	}
	if metadataOK {
		args = append(args, metadata.args()...)
	}
	port.ObserveAttempt(attempt.ctx, observation)

	level := port.LevelInfo
	message := "llm stream attempt failed; retrying"
	if decision == decisionTerminal {
		switch reason {
		case replayVisibleOutput:
			message = "llm stream failed mid-stream; ending turn"
		case replayAttemptsExhausted:
			message = "llm stream not established after all attempts"
		case replayPermanent:
			message = "llm stream failed with a non-retryable provider error; ending turn"
		case replayProviderInternalVeto:
			message = "llm provider recovery retry budget exhausted; ending turn"
		case replayBreakerOpen:
			message = "llm stream rejected by the open circuit breaker; ending turn"
		default:
			message = "llm stream failed without a safe retry; ending turn"
		}
	} else {
		level = port.LevelDebug
	}
	p.diag().Log(attempt.ctx, level, message, args...)
}

func retryDispositionDiagnostic(disposition session.RetryDisposition) string {
	switch disposition {
	case session.RetryDispositionRetryable:
		return "retryable"
	case session.RetryDispositionPermanent:
		return "permanent"
	default:
		return "unknown"
	}
}

func streamProgressDiagnostic(progress session.StreamProgress) string {
	switch progress {
	case session.StreamProgressPrecommit:
		return "precommit"
	case session.StreamProgressVisible:
		return "visible"
	case session.StreamProgressComplete:
		return "complete"
	default:
		return "unknown"
	}
}

type attemptErrorMetadata struct {
	httpStatus      int
	inBandStatus    int
	providerCode    string
	correlationKind string
	correlationID   string
}

func (m attemptErrorMetadata) valid() bool {
	if !validOptionalStatus(m.httpStatus) || !validOptionalStatus(m.inBandStatus) ||
		m.providerCode != "" && !validPrintableToken(m.providerCode, 128) {
		return false
	}
	if (m.correlationKind == "") != (m.correlationID == "") {
		return false
	}
	switch m.correlationKind {
	case "":
		return true
	case "request", "response", "trace", "completion", "message":
		return len(m.correlationID) <= 4096
	default:
		return false
	}
}

func validOptionalStatus(status int) bool {
	return status == 0 || status >= 100 && status <= 599
}

func validPrintableToken(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

func attemptMetadata(err error) (attemptErrorMetadata, bool) {
	var carrier port.ProviderErrorMetadataError
	if !errors.As(err, &carrier) {
		return attemptErrorMetadata{}, false
	}
	metadata := attemptErrorMetadata{
		httpStatus:      carrier.ProviderHTTPStatus(),
		inBandStatus:    carrier.ProviderInBandStatus(),
		providerCode:    carrier.ProviderErrorCode(),
		correlationKind: carrier.ProviderErrorCorrelationKind(),
		correlationID:   carrier.ProviderErrorCorrelationID(),
	}
	return metadata, metadata.valid()
}

func (m attemptErrorMetadata) args() []any {
	args := make([]any, 0, 8)
	if m.httpStatus != 0 {
		args = append(args, "http_status", m.httpStatus)
	}
	if m.inBandStatus != 0 {
		args = append(args, "in_band_status", m.inBandStatus)
	}
	if digest, ok := session.NetworkCorrelationDigest(m.correlationKind, m.correlationID); ok {
		args = append(args,
			"correlation_kind", m.correlationKind,
			"correlation_digest", digest)
	}
	return args
}

func attemptFailureClass(err error, metadata attemptErrorMetadata) string {
	if class := transportFailureClass(err); class != "" {
		return class
	}
	if metadata.httpStatus == 429 || metadata.inBandStatus == 429 {
		return "rate_limit"
	}
	if metadata.httpStatus != 0 {
		return "http"
	}
	if metadata.inBandStatus != 0 || metadata.providerCode != "" {
		return "provider"
	}
	return "unknown"
}

func transportFailureClass(err error) string {
	var breaker *BreakerError
	if errors.As(err, &breaker) {
		return "breaker"
	}
	if errors.Is(err, errFirstChunkTimeout) {
		return "timeout"
	}
	var idle *StreamIdleError
	if errors.As(err, &idle) {
		return "stream_idle"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns"
	}
	if isTLSError(err) {
		return "tls"
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return "connection_reset"
	}
	var op *net.OpError
	if errors.As(err, &op) && (op.Op == "dial" || op.Op == "connect") {
		return "connect"
	}
	var netErr net.Error
	if (errors.As(err, &netErr) && netErr.Timeout()) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return ""
}

func isTLSError(err error) bool {
	var recordHeader tls.RecordHeaderError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificateInvalid x509.CertificateInvalidError
	return errors.As(err, &recordHeader) || errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostname) || errors.As(err, &certificateInvalid)
}

// logMidStreamError records a failure after semantic visibility. Cancellation
// keeps its pre-existing non-failure diagnostic; every actual failure uses the
// centralized attempt-decision path.
func (p *resilientProvider) logMidStreamError(attempt attemptDiagnostic, err error) {
	if errors.Is(err, context.Canceled) {
		p.diag().Log(context.Background(), port.LevelInfo, "llm stream cancelled mid-stream; ending turn",
			"model", attempt.model)
		return
	}
	p.logAttemptDecision(attempt, err, decisionTerminal, session.StreamProgressVisible, replayVisibleOutput, 0)
}

// Capabilities forwards the wrapped provider's capabilities unchanged: the
// resilience decorator adds retries/breaker behaviour only and never alters what
// kinds of prompt input the underlying provider consumes.
func (p *resilientProvider) Capabilities() port.ProviderCapabilities {
	return p.inner.Capabilities()
}

// advancesVisible reports whether a chunk crosses the semantic commit boundary.
// Only meaningful assistant text becomes visible before clean completion. Every
// other current chunk is tentative: tool calls are dispatched only after a clean
// ChunkDone, and metadata/replay/accounting chunks can be discarded with a
// failed precommit attempt. Unknown future kinds are conservative and visible.
func advancesVisible(chunk port.Chunk) bool {
	switch chunk.Kind {
	case port.ChunkText:
		return strings.TrimSpace(chunk.Text) != ""
	case port.ChunkReasoning, port.ChunkReasoningItem, port.ChunkToolCall,
		port.ChunkUsage, port.ChunkPhase, port.ChunkProviderRoute, port.ChunkDone:
		return false
	default:
		return true
	}
}

// attemptResult is the result of pumping one attempt to a semantic boundary.
// progress is the single state model used by the pump and the relay: tentative
// chunks remain buffered at Precommit, Visible carries the first committed chunk
// and a continuation, and Complete flushes a clean buffered turn.
type attemptResult struct {
	progress       session.StreamProgress
	chunk          port.Chunk
	buffered       []port.Chunk
	discardedUsage session.Usage
	remaining      iter.Seq2[port.Chunk, error]
	stop           func()
	cancel         context.CancelFunc
}

func terminalWithUsage(usage session.Usage, err error) (iter.Seq2[port.Chunk, error], error) {
	if usage == (session.Usage{}) {
		return nil, err
	}
	return func(yield func(port.Chunk, error) bool) {
		if !yield(port.Chunk{Kind: port.ChunkUsage, Usage: &usage}, nil) {
			return
		}
		yield(port.Chunk{}, err)
	}, nil
}

// Stream pumps attempts under retry and breaker policy. Failed precommit
// attempts never escape; visible attempts return a continuation and are never
// replayed.
func (p *resilientProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	var lastErr error
	var discardedUsage session.Usage
	calls := 0
	source := "backoff"
	r := newRecoveryWindow(ctx)
	defer func() { r.stop(p.cfg.Clock()) }()
	transferred := false
	defer func() {
		if !transferred {
			r.cancel()
		}
	}()
	terminal := func(err error) (iter.Seq2[port.Chunk, error], error) {
		p.logRecovery(ctx, req.Model, "terminal", calls, 0, r, source)
		if callerErr := ctx.Err(); callerErr != nil {
			err = callerErr
		}
		return terminalWithUsage(discardedUsage, classifiedProgressError(err, session.StreamProgressPrecommit))
	}
	for {
		if err := ctx.Err(); err != nil {
			return terminal(err)
		}
		if r.expired(p.cfg.Clock()) {
			return terminal(lastErr)
		}
		lease, rejection, changed := p.allow(ctx, p.cfg.Clock())
		if rejection != nil {
			source = "breaker"
			if lastErr == nil {
				lastErr = rejection
			}
			r.start(p.cfg.Clock(), p.cfg.RecoveryBudget)
			if p.cfg.RecoveryBudget <= 0 || p.cfg.Clock().Add(rejection.RetryAfter).After(r.deadline) {
				return terminal(lastErr)
			}
			wait := rejection.RetryAfter
			if wait == 0 {
				wait = r.remaining(p.cfg.Clock())
			}
			p.logRecovery(ctx, req.Model, "wait", calls, wait, r, source)
			if err := waitRecovery(r.ctx, rejection.RetryAfter, changed); err != nil {
				return terminal(lastErr)
			}
			continue
		}
		if ctx.Err() != nil || r.expired(p.cfg.Clock()) {
			lease.release()
			return terminal(lastErr)
		}
		calls++
		diagnostic := attemptDiagnostic{ctx: ctx, model: req.Model, attempt: calls,
			maxAttempts: p.cfg.MaxAttempts, started: p.cfg.Clock(), lease: lease}
		head, err := p.establish(r.ctx, req, diagnostic)
		if err == nil {
			// Joining before returning the head linearizes commit against expiry,
			// including providers that ignore cancellation and yield text or Done.
			if r.stop(p.cfg.Clock()) || ctx.Err() != nil {
				discardedUsage = discardedUsage.Add(head.discardedUsage)
				if head.cancel != nil {
					head.cancel()
				}
				if head.stop != nil {
					head.stop()
				}
				lease.release()
				return terminal(lastErr)
			}
			if discardedUsage != (session.Usage{}) {
				head.buffered = append([]port.Chunk{{Kind: port.ChunkUsage, Usage: &discardedUsage}}, head.buffered...)
			}
			cancel := head.cancel
			head.cancel = func() {
				if cancel != nil {
					cancel()
				}
				r.cancel()
			}
			transferred = true
			if lastErr != nil {
				p.logRecovery(ctx, req.Model, "recovered", calls, 0, r, source)
			}
			return p.wrap(head, diagnostic), nil
		}
		if head != nil {
			discardedUsage = discardedUsage.Add(head.discardedUsage)
		}
		if ctx.Err() != nil || r.expired(p.cfg.Clock()) {
			lease.release()
			return terminal(lastErr)
		}
		if dispositionOf(err) == session.RetryDispositionRetryable {
			r.start(p.cfg.Clock(), p.cfg.RecoveryBudget)
		}
		if isTransientForBreaker(err) {
			lease.finish(breakerFailure)
		}
		lease.release()
		lastErr = err
		if errors.Is(err, errFirstChunkTimeout) {
			p.diag().Log(ctx, port.LevelDebug, "llm stream per-attempt timeout fired",
				"per_attempt_timeout", p.cfg.PerAttemptTimeout, "attempt", calls, "max_attempts", p.cfg.MaxAttempts)
		}
		reason := replaySuppressedReason("")
		switch {
		case providerVeto(err):
			reason = replayProviderInternalVeto
		case dispositionOf(err) == session.RetryDispositionPermanent:
			reason = replayPermanent
		case dispositionOf(err) != session.RetryDispositionRetryable:
			reason = replayUnknown
		case !p.cfg.Classifier(err):
			reason = replayClassifierVeto
		}
		if reason != "" {
			p.logAttemptDecision(diagnostic, err, decisionTerminal, session.StreamProgressPrecommit, reason, 0)
			return terminal(err)
		}
		if calls >= p.cfg.MaxAttempts {
			p.logAttemptDecision(diagnostic, err, decisionTerminal, session.StreamProgressPrecommit, replayAttemptsExhausted, 0)
			return terminal(&ExhaustedError{Attempts: calls, Err: classifiedProgressError(err, session.StreamProgressPrecommit), PerAttempt: p.cfg.PerAttemptTimeout})
		}
		now := p.cfg.Clock()
		at, waitSource, schedulable := retryAt(err, now, p.backoffDuration(calls-1), r)
		source = waitSource
		// This records the genuine failure, not a fabricated budget terminal.
		p.logAttemptDecision(diagnostic, err, decisionRetry, session.StreamProgressPrecommit, "", max(0, at.Sub(now)))
		if !schedulable {
			return terminal(lastErr)
		}
		wait := max(0, at.Sub(p.cfg.Clock()))
		p.logRecovery(ctx, req.Model, "wait", calls, wait, r, source)
		if err := waitRecovery(r.ctx, wait, nil); err != nil {
			return terminal(lastErr)
		}
	}
}

func providerVeto(err error) bool {
	retryable, explicit := explicitRetryDecision(err)
	return explicit && !retryable
}

// establish performs a single attempt. PerAttemptTimeout bounds connect plus the
// first raw chunk; pumpAttempt then continues under the idle watchdog until the
// attempt becomes semantically visible, completes, or fails.
//
// The establishment budget is enforced by a separate timer, not an absolute
// context deadline. The timer cancels a blocked first raw read, and is stopped
// and joined as soon as any chunk arrives. The deadline-free attempt context then
// remains governed by StreamIdleTimeout and the parent context, so a long active
// stream is never truncated by its establishment budget.
//
// The deadline-free ctx is intentionally left live on success: it is cancelled
// when the returned iterator finishes or the caller stops early (handled in
// wrap/restSeq).
func (p *resilientProvider) establish(ctx context.Context, req port.LLMRequest, diagnostic attemptDiagnostic) (*attemptResult, error) {
	attemptCtx := ctx
	var cancel context.CancelFunc
	if p.cfg.PerAttemptTimeout > 0 || p.cfg.StreamIdleTimeout > 0 {
		// A cancel handle is needed when EITHER bound is active: the establishment
		// timer fires it to abort a stalled first-chunk read, and the idle watchdog
		// fires it to abort a stalled mid-stream read. With BOTH disabled, keep the
		// plain ctx (cancel == nil) so the no-watchdog path is byte-identical.
		attemptCtx, cancel = context.WithCancel(ctx)
	}

	// Establishment timer: bounds connect + first chunk ONLY. It fires cancel()
	// (after setting estTimedOut) if the first chunk has not been pulled in time.
	// CRITICAL: the goroutine is stopped AND joined on every exit path of establish
	// (success after the first chunk, and all failure exits) so it cannot leak
	// (goleak in leakmain_test.go) and is gone before restSeq runs (sampled by
	// TestStreamIdleDisabledSpawnsNoWatchdogGoroutine).
	var estTimedOut atomic.Bool
	var estTimer *time.Timer
	estDone := make(chan struct{})
	estStop := make(chan struct{})
	// stopEstTimer stops the establishment timer, joins its goroutine, and reports
	// whether the timer had ALREADY fired (cancel() already called or in flight)
	// before it was stopped. The estTimedOut.Load() is ordered AFTER the join
	// (<-estDone): the goroutine's estTimedOut.Store(true) happens-before
	// close(estDone), which happens-before this receive — so a true here means
	// cancel() has run (or is guaranteed to before the goroutine exits) and
	// attemptCtx is dead.
	stopEstTimer := func() (fired bool) {
		if estTimer == nil {
			return false
		}
		estTimer.Stop()
		close(estStop) // wake the goroutine if it is still parked on the timer
		<-estDone      // join it before returning (no leak, gone before restSeq)
		estTimer = nil
		return estTimedOut.Load()
	}
	if p.cfg.PerAttemptTimeout > 0 {
		estTimer = time.NewTimer(p.cfg.PerAttemptTimeout)
		go func() {
			defer close(estDone)
			select {
			case <-estTimer.C:
				estTimedOut.Store(true)
				if cancel != nil {
					cancel()
				}
			case <-estStop:
			}
		}()
	}

	// establishmentFailure maps a failed first-chunk read to the error the
	// downstream classifier/breaker/diagnostics expect. When the establishment timer
	// fired (estTimedOut) the failure must look EXACTLY like the old absolute-deadline
	// path — `DeadlineExceeded: errFirstChunkTimeout` — regardless of what the inner
	// surfaced once cancelled (the inner ctx is a CANCEL context now, so on cancel an
	// adapter reports context.Canceled, which would otherwise be classified
	// non-retryable). This keeps the retry, the breaker-count, and the "per-attempt
	// timeout fired" debug line identical to today. Otherwise it annotates the inner
	// error with the per-attempt cause (a genuine parent cancel) read BEFORE cancel().
	establishmentFailure := func(innerErr error) error {
		if estTimedOut.Load() {
			return attemptError(context.DeadlineExceeded, errFirstChunkTimeout)
		}
		return attemptError(ctx.Err(), innerErr)
	}

	seq, err := p.inner.Stream(attemptCtx, req)
	if err != nil {
		failure := establishmentFailure(err)
		stopEstTimer()
		if cancel != nil {
			cancel()
		}
		return nil, failure
	}

	// Pump raw chunks to the semantic boundary. The establishment timer is stopped
	// by pumpAttempt on the first raw chunk, including tentative metadata.
	return p.pumpAttempt(ctx, diagnostic, seq, stopEstTimer, cancel, establishmentFailure)
}

func discardedChunkUsage(chunk port.Chunk) session.Usage {
	if chunk.Kind == port.ChunkUsage && chunk.Usage != nil {
		return *chunk.Usage
	}
	return session.Usage{}
}

// pumpAttempt drives one semantic attempt. It observes raw transport activity
// separately from semantic visibility, buffers tentative chunks in wire order,
// and returns only after meaningful text becomes visible or the attempt cleanly
// completes. A failure before either boundary returns no chunks, allowing the
// caller to discard the attempt and retry.
func (p *resilientProvider) pumpAttempt(
	ctx context.Context,
	diagnostic attemptDiagnostic,
	seq iter.Seq2[port.Chunk, error],
	stopEstTimer func() bool,
	cancel context.CancelFunc,
	establishmentFailure func(error) error,
) (*attemptResult, error) {
	next, stop := cancelablePull(ctx, seq)
	result := &attemptResult{progress: session.StreamProgressUnknown}
	rawSeen := false
	pull := func() (port.Chunk, bool, error) {
		if rawSeen && p.cfg.StreamIdleTimeout > 0 {
			return p.pullTentativeWithIdle(diagnostic.model, next, cancel)
		}
		chunk, err, ok := next()
		return chunk, ok, err
	}
	for {
		chunk, ok, cerr := pull()
		if ok {
			result.discardedUsage = result.discardedUsage.Add(discardedChunkUsage(chunk))
		}
		if !ok {
			timedOut := stopEstTimer()
			cause := ctx.Err()
			stop()
			if cancel != nil {
				cancel()
			}
			// Providers commonly swallow context cancellation and end their iterator.
			// Cancellation is not clean completion: discard every tentative chunk and
			// surface it without retrying or affecting the breaker.
			if cause != nil {
				return result, cause
			}
			if timedOut {
				return result, attemptError(context.DeadlineExceeded, errFirstChunkTimeout)
			}
			result.progress = session.StreamProgressComplete
			return result, nil
		}
		if cerr != nil {
			failure := establishmentFailure(cerr)
			stopEstTimer()
			stop()
			if cancel != nil {
				cancel()
			}
			return result, failure
		}

		if !rawSeen {
			rawSeen = true
			if stopEstTimer() {
				stop()
				if cancel != nil {
					cancel()
				}
				return result, attemptError(context.DeadlineExceeded, errFirstChunkTimeout)
			}
		}

		if cause := ctx.Err(); cause != nil {
			stop()
			if cancel != nil {
				cancel()
			}
			return result, cause
		}
		if chunk.Kind == port.ChunkDone {
			result.buffered = append(result.buffered, chunk)
			result.progress = session.StreamProgressComplete
			stop()
			if cancel != nil {
				cancel()
			}
			return result, nil
		}
		if !advancesVisible(chunk) {
			result.buffered = append(result.buffered, chunk)
			result.progress = session.StreamProgressPrecommit
			continue
		}

		result.progress = session.StreamProgressVisible
		result.chunk = chunk
		result.remaining = p.restSeq(diagnostic, next, stop, cancel)
		result.stop = stop
		result.cancel = cancel
		return result, nil
	}
}

// pullTentativeWithIdle bounds one raw read while the attempt is still
// semantically precommit. Every successfully received chunk resets the budget
// because this helper is called anew for each pull.
func (p *resilientProvider) pullTentativeWithIdle(
	model string,
	next func() (port.Chunk, error, bool),
	cancel context.CancelFunc,
) (port.Chunk, bool, error) {
	type result struct {
		chunk port.Chunk
		err   error
		ok    bool
	}
	results := make(chan result, 1)
	go func() {
		chunk, err, ok := next()
		results <- result{chunk: chunk, err: err, ok: ok}
	}()
	timer := time.NewTimer(p.cfg.StreamIdleTimeout)
	defer timer.Stop()
	select {
	case r := <-results:
		return r.chunk, r.ok, r.err
	case <-timer.C:
		if cancel != nil {
			cancel()
		}
		<-results
		p.diag().Log(context.Background(), port.LevelInfo, "llm stream stalled (idle timeout) before semantic commit",
			"model", model,
			"idle", p.cfg.StreamIdleTimeout)
		return port.Chunk{}, true, &StreamIdleError{Idle: p.cfg.StreamIdleTimeout}
	}
}

// restSeq builds the continuation iterator that yields the remainder of the
// inner stream after the buffered first chunk, cleaning up the pull iterator and
// per-attempt context when it finishes.
//
// When StreamIdleTimeout <= 0 it is the plain pull loop (no goroutine, no
// behaviour change). When StreamIdleTimeout > 0 each next() call is bounded by a
// per-iteration idle deadline: the read runs on a helper goroutine and a timer is
// reset to StreamIdleTimeout each iteration; if the timer fires first the wrapper
// cancels the per-attempt context (unblocking the inner stream.Next()) and yields
// a terminal *StreamIdleError. The helper goroutine is always drained after a
// cancel so it cannot leak.
func (p *resilientProvider) restSeq(diagnostic attemptDiagnostic, next func() (port.Chunk, error, bool), stop func(), cancel context.CancelFunc) iter.Seq2[port.Chunk, error] {
	if p.cfg.StreamIdleTimeout <= 0 {
		return p.restSeqUnbounded(diagnostic, next, stop, cancel)
	}
	return p.restSeqIdleBounded(diagnostic, next, stop, cancel)
}

// restSeqUnbounded is the restSeq variant with no idle timeout.
func (p *resilientProvider) restSeqUnbounded(diagnostic attemptDiagnostic, next func() (port.Chunk, error, bool), stop func(), cancel context.CancelFunc) iter.Seq2[port.Chunk, error] {
	return func(yield func(port.Chunk, error) bool) {
		defer stop()
		if cancel != nil {
			defer cancel()
		}
		for {
			c, e, ok := next()
			if !ok {
				return
			}
			if e != nil {
				// Logged BEFORE the yield: an ordinary consumer BREAKS its range loop on
				// the error, which makes yield return false — so a log placed after the
				// yield-false return is unreachable on the very path that matters.
				p.logMidStreamError(diagnostic, e)
				e = classifyVisibleError(e)
			}
			if !yield(c, e) {
				return
			}
			if e != nil {
				return
			}
		}
	}
}

// restSeqIdleBounded is the restSeq variant bounded by the stream-idle
// watchdog. A real time.NewTimer is used (NOT cfg.Clock — that drives breaker
// math only, consistent with backoff()).
func (p *resilientProvider) restSeqIdleBounded(diagnostic attemptDiagnostic, next func() (port.Chunk, error, bool), stop func(), cancel context.CancelFunc) iter.Seq2[port.Chunk, error] {
	return func(yield func(port.Chunk, error) bool) {
		defer stop()
		if cancel != nil {
			defer cancel()
		}

		type pull struct {
			c  port.Chunk
			e  error
			ok bool
		}
		// Single-shot channel per iteration; the helper goroutine writes exactly one
		// result then exits, so there is one goroutine in flight at a time.
		results := make(chan pull, 1)
		timer := time.NewTimer(p.cfg.StreamIdleTimeout)
		defer timer.Stop()

		for {
			go func() {
				c, e, ok := next()
				results <- pull{c: c, e: e, ok: ok}
			}()

			// Reset the idle timer for THIS read.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(p.cfg.StreamIdleTimeout)

			select {
			case r := <-results:
				if !r.ok {
					return
				}
				if r.e != nil {
					// See the non-idle variant above: logged BEFORE the yield because a
					// consumer that breaks on the error makes yield return false.
					p.logMidStreamError(diagnostic, r.e)
					r.e = classifyVisibleError(r.e)
				}
				if !yield(r.c, r.e) {
					return
				}
				if r.e != nil {
					return
				}
			case <-timer.C:
				// Idle budget elapsed. Cancel the per-attempt ctx to unblock the inner
				// stream.Next(); the adapters swallow the resulting ctx error (they yield
				// nothing on cancel), so we MUST synthesize the terminal error ourselves.
				if cancel != nil {
					cancel()
				}
				// Drain the in-flight helper goroutine so neither it nor the inner pull
				// coroutine leaks: the cancel above unblocks the helper's next() call,
				// which then returns and the goroutine sends + exits. We MUST read this
				// before returning — the deferred stop() unwinds the pull iterator, and
				// stop()/next() may not run concurrently, so the helper's next() has to
				// have completed first.
				<-results
				p.diag().Log(context.Background(), port.LevelInfo, "llm stream stalled (idle timeout); ending turn",
					"model", diagnostic.model,
					"idle", p.cfg.StreamIdleTimeout)
				idleErr := &StreamIdleError{Idle: p.cfg.StreamIdleTimeout}
				p.logAttemptDecision(diagnostic, idleErr, decisionTerminal, session.StreamProgressVisible, replayVisibleOutput, 0)
				yield(port.Chunk{}, classifiedProgressError(idleErr, session.StreamProgressVisible))
				return
			}
		}
	}
}

// attemptError annotates an establishment error with the per-attempt context's
// cause when a per-attempt deadline (or genuine caller-cancel) fired, so the
// classifier and caller-cancel check observe the right underlying cause.
//
// cause MUST be the per-attempt context's Err() read BEFORE the cleanup cancel()
// has run — never attemptCtx.Err() read afterwards. The cleanup cancel() we issue
// in establish() always sets attemptCtx.Err() to context.Canceled; reading it
// after cancel() would therefore mask EVERY real establishment error (e.g. a 400)
// as `context.Canceled: <real>`, which the loop then treats as a caller cancel and
// terminates as "cancelled" with the real provider message discarded.
//
// With the pre-cancel cause:
//   - cause == nil (no per-attempt deadline, no caller cancel): return err
//     VERBATIM so the real error surfaces (the classifier / loop see e.g. the 400
//     and report StopError with the provider message).
//   - cause != nil: wrap it as `cause: err` so a genuine per-attempt deadline stays
//     retryable (DefaultClassifier) and a genuine caller-cancel stays a cancel
//     (the loop's StopCancelled / wedge-recovery path).
func attemptError(cause, err error) error {
	if cause != nil && !errors.Is(err, cause) {
		return fmt.Errorf("%w: %w", cause, err)
	}
	return err
}

// wrap flushes tentative chunks at their semantic boundary and then relays the
// visible stream continuation, if any. A breaker success is recorded only after
// clean completion; a visible transient failure remains terminal and unhealthy.
func (p *resilientProvider) wrap(result *attemptResult, diagnostic attemptDiagnostic) iter.Seq2[port.Chunk, error] {
	return func(yield func(port.Chunk, error) bool) {
		defer diagnostic.lease.release()
		if result.cancel != nil {
			defer result.cancel()
		}
		cleanupRemaining := func() {
			if result.cancel != nil {
				result.cancel()
			}
			if result.stop != nil {
				result.stop()
			}
		}
		for _, buffered := range result.buffered {
			if !yield(buffered, nil) {
				cleanupRemaining()
				return
			}
		}
		switch result.progress {
		case session.StreamProgressComplete:
			if diagnostic.ctx.Err() == nil {
				diagnostic.lease.finish(breakerSuccess)
			}
			return
		case session.StreamProgressVisible:
			if !yield(result.chunk, nil) {
				cleanupRemaining()
				return
			}
			if result.remaining == nil {
				if diagnostic.ctx.Err() == nil {
					diagnostic.lease.finish(breakerSuccess)
				}
				return
			}
			clean := true
			consumed := true
			result.remaining(func(chunk port.Chunk, err error) bool {
				if err != nil {
					clean = false
					if diagnostic.ctx.Err() == nil && isTransientForBreaker(err) {
						diagnostic.lease.finish(breakerFailure)
					}
				}
				if !yield(chunk, err) {
					consumed = false
					return false
				}
				return err == nil
			})
			if clean && consumed && diagnostic.ctx.Err() == nil {
				diagnostic.lease.finish(breakerSuccess)
			}
		case session.StreamProgressUnknown, session.StreamProgressPrecommit:
			cleanupRemaining()
		}
	}
}

// backoffDuration computes the full-jitter backoff for the given zero-based
// attempt index: a random value in [0, min(MaxBackoff, Base*2^attempt)].
func (p *resilientProvider) backoffDuration(attempt int) time.Duration {
	if p.cfg.BaseBackoff <= 0 {
		return 0
	}
	// Exponential growth with overflow guard.
	exp := p.cfg.BaseBackoff
	for i := 0; i < attempt; i++ {
		if exp > time.Duration(1<<63-1)/2 {
			exp = time.Duration(1<<63 - 1)
			break
		}
		exp *= 2
		if p.cfg.MaxBackoff > 0 && exp >= p.cfg.MaxBackoff {
			exp = p.cfg.MaxBackoff
			break
		}
	}
	if p.cfg.MaxBackoff > 0 && exp > p.cfg.MaxBackoff {
		exp = p.cfg.MaxBackoff
	}
	if exp <= 0 {
		return 0
	}
	// Full jitter. A weak RNG is appropriate: jitter only spreads retries to
	// avoid thundering herds; it carries no security requirement.
	return time.Duration(rand.Int64N(int64(exp)) + 1) //nolint:gosec // jitter, not crypto
}

// DefaultClassifier is the legacy compatibility retry-policy projection of the
// provider-neutral tri-state classification. It returns true only for causally
// retryable failures. Config.Classifier is invoked after that classification as a
// tighten-only veto, so neither this function nor a custom classifier can upgrade
// unknown or permanent failures in Stream.
func DefaultClassifier(err error) bool {
	if retryable, explicit := explicitRetryDecision(err); explicit {
		return retryable
	}
	return DefaultDisposition(err) == session.RetryDispositionRetryable
}

// DefaultDisposition classifies the causal provider failure without deciding
// whether policy permits another attempt. Unknown is conservative and is never
// promoted to permanent.
func DefaultDisposition(err error) session.RetryDisposition {
	return dispositionOf(err)
}

func defaultDisposition(err error) session.RetryDisposition {
	if err == nil || errors.Is(err, context.Canceled) {
		return session.RetryDispositionUnknown
	}
	if errors.Is(err, ErrCredentials) {
		return session.RetryDispositionPermanent
	}

	var h2Err *http2.StreamError
	if errors.As(err, &h2Err) {
		return session.RetryDispositionRetryable
	}
	var apiErr *oai.Error
	if errors.As(err, &apiErr) {
		return statusDisposition(apiErr.StatusCode)
	}
	var sc interface{ StatusCode() int }
	if errors.As(err, &sc) {
		return statusDisposition(sc.StatusCode())
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return session.RetryDispositionRetryable
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return session.RetryDispositionRetryable
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		return session.RetryDispositionRetryable
	}
	return session.RetryDispositionUnknown
}

func statusDisposition(code int) session.RetryDisposition {
	switch {
	case code == 408 || code == 409 || code == 429:
		return session.RetryDispositionRetryable
	case code >= 500 && code <= 599:
		return session.RetryDispositionRetryable
	case code >= 400 && code <= 499:
		return session.RetryDispositionPermanent
	default:
		return session.RetryDispositionUnknown
	}
}

// isTransientForBreaker is the breaker-health predicate: it reports whether an
// establishment failure is a sign the PROVIDER is unhealthy and should count
// toward the shared circuit breaker. It is DISTINCT from the retry classifier
// (cfg.Classifier): the breaker must NOT be coupled to the caller-injectable
// classifier, so it keeps its own status switch.
//
// The two predicates deliberately DIVERGE on HTTP 409: a request conflict is
// retryable per-request (retryableStatus returns true) but is NOT a sign the
// provider is unhealthy, so 409 must not trip a shared breaker and is excluded
// here. The breaker counts only transient provider-health failures:
//   - true: HTTP 408, 429, any 5xx; net.Error; bare context.DeadlineExceeded
//     (a per-attempt timeout); HTTP/2 stream errors (http2.StreamError — a
//     peer RST_STREAM / INTERNAL_ERROR transport reset).
//   - false: HTTP 409 (request-conflict ≠ provider-unhealthy), all other 4xx
//     (400/401/403/404/...), context.Canceled, unknown errors, nil.
//
// It mirrors DefaultClassifier's errors.As chain but writes its own status
// switch inline (rather than reusing retryableStatus) so the 409 divergence is
// explicit and self-documenting.
func isTransientForBreaker(err error) bool {
	if err == nil {
		return false
	}
	// Caller-style context cancellation is breaker-neutral.
	if errors.Is(err, context.Canceled) {
		return false
	}

	// A credential failure is breaker-neutral: it happens before the provider is
	// ever dialed, so it is no evidence about the provider's health, and letting
	// it open a SHARED breaker takes down every session on this provider over a
	// problem local to one caller's token. Checked BEFORE the transport arms for
	// the same reason as in DefaultClassifier — *url.Error satisfies net.Error.
	if errors.Is(err, ErrCredentials) {
		return false
	}

	// HTTP/2 stream resets are transient transport-level provider-health
	// signals, matching DefaultClassifier. All http2.StreamError codes count.
	var h2Err *http2.StreamError
	if errors.As(err, &h2Err) {
		return true
	}

	// breakerStatus is the breaker's OWN status switch, intentionally distinct
	// from retryableStatus: 408/429/5xx count as provider-health signals; 409
	// does NOT (request-conflict is not provider-unhealthy), and neither does any
	// other 4xx.
	breakerStatus := func(code int) bool {
		switch {
		case code == 408 || code == 429:
			return true
		case code >= 500 && code <= 599:
			return true
		default:
			return false
		}
	}

	// OpenAI typed API error: classify on HTTP status.
	var apiErr *oai.Error
	if errors.As(err, &apiErr) {
		return breakerStatus(apiErr.StatusCode)
	}

	// Generic status-bearing errors (interface escape hatch for non-openai
	// providers that expose a StatusCode).
	var sc interface{ StatusCode() int }
	if errors.As(err, &sc) {
		return breakerStatus(sc.StatusCode())
	}

	// Network errors and timeouts are transient provider-health signals.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// A bare DeadlineExceeded (a per-attempt timeout surfaced without a net.Error
	// wrapper) is transient.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// A truncated/malformed payload (partial SSE frame → *json.SyntaxError, or an
	// empty gateway body → unexpected EOF) is the SAME transient class
	// DefaultClassifier retries. It MUST also drive the breaker: otherwise a
	// PERSISTENTLY malformed/truncated gateway response is retried up to
	// MaxAttempts on every call but never opens the shared breaker (contradicting
	// the "bounded by MaxAttempts + the breaker" contract). Kept in lockstep with
	// DefaultClassifier's matching arm.
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	return false
}

// Compile-time assertion that the decorator satisfies the port.
var _ port.LLMProvider = (*resilientProvider)(nil)
