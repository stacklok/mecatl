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
// After BreakerThreshold transient failures it opens and Stream fails fast with
// *BreakerError for BreakerCooldown; it then half-opens to admit a single trial.
// Any success resets it. All breaker state is concurrency-safe.
//
// The package depends only on the standard library and engine/port; the
// classifier reaches *openai.Error via errors.As to read its StatusCode, which
// is acceptable for an adapter.
package llmresilience

import (
	"context"
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
	"time"

	oai "github.com/openai/openai-go/v3"
	"golang.org/x/net/http2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Config tunes the resilience decorator. The zero value is usable but inert
// (MaxAttempts <= 1 means a single attempt, no breaker); supply sensible values
// via Wrap.
type Config struct {
	// MaxAttempts is the total number of attempts for establishing the stream
	// (the initial call plus retries). Values < 1 are treated as 1.
	MaxAttempts int
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

// dispositionError projects a known causal classification through errors.As.
// Permanent classifications retain the older port.PermanentError projection.
type dispositionError struct {
	err         error
	disposition port.RetryDisposition
	progress    port.StreamProgress
}

func (e *dispositionError) Error() string                           { return e.err.Error() }
func (e *dispositionError) Unwrap() error                           { return e.err }
func (e *dispositionError) RetryDisposition() port.RetryDisposition { return e.disposition }
func (e *dispositionError) StreamProgress() port.StreamProgress     { return e.progress }

type permanentDispositionError struct{ *dispositionError }

func (*permanentDispositionError) Permanent() bool { return true }

func explicitRetryDecision(err error) (retryable, explicit bool) {
	var decision interface{ Retryable() bool }
	if !errors.As(err, &decision) {
		return false, false
	}
	return decision.Retryable(), true
}

func dispositionOf(err error) port.RetryDisposition {
	if err == nil {
		return port.RetryDispositionUnknown
	}
	var classified port.RetryDispositionError
	if errors.As(err, &classified) {
		disposition := classified.RetryDisposition()
		if disposition.Valid() {
			return disposition
		}
		return port.RetryDispositionUnknown
	}
	var permanent port.PermanentError
	if errors.As(err, &permanent) && permanent.Permanent() {
		return port.RetryDispositionPermanent
	}
	return defaultDisposition(err)
}

func classifiedProgressError(err error, progress port.StreamProgress) error {
	d := dispositionOf(err)
	if err == nil {
		return nil
	}
	var classified port.RetryDispositionError
	var progressed port.StreamProgressError
	if errors.As(err, &classified) && errors.As(err, &progressed) && progressed.StreamProgress() == progress {
		if d != port.RetryDispositionPermanent {
			return err
		}
		var permanent port.PermanentError
		if errors.As(err, &permanent) && permanent.Permanent() {
			return err
		}
	}
	base := &dispositionError{err: err, disposition: d, progress: progress}
	if d == port.RetryDispositionPermanent {
		return &permanentDispositionError{dispositionError: base}
	}
	return base
}

// classifyVisibleError preserves the causal classification of a terminal
// mid-stream failure while attaching its visible progress.
func classifyVisibleError(err error) error {
	if err == nil {
		return nil
	}
	return classifiedProgressError(err, port.StreamProgressVisible)
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
	halfOpen bool
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
}

// logAttemptDecision is the single failed-attempt decision log path. It records
// causal classification and sanitized errors.As metadata without rendering the
// raw error, which may contain response bodies, URLs, headers, or credentials.
func (p *resilientProvider) logAttemptDecision(
	attempt attemptDiagnostic,
	err error,
	decision attemptDecision,
	progress port.StreamProgress,
	reason replaySuppressedReason,
	backoff time.Duration,
) {
	elapsed := p.cfg.Clock().Sub(attempt.started)
	if elapsed < 0 {
		elapsed = 0
	}
	args := []any{
		"model", attempt.model,
		"attempt", attempt.attempt,
		"max_attempts", attempt.maxAttempts,
		"elapsed", elapsed,
		"retry_disposition", retryDispositionDiagnostic(dispositionOf(err)),
		"stream_progress", streamProgressDiagnostic(progress),
		"decision", string(decision),
	}
	if id, ok := port.SessionIDFromContext(attempt.ctx); ok && id != "" {
		args = append(args, "session", id)
	}
	if serial, ok := port.RunSerialFromContext(attempt.ctx); ok {
		args = append(args, "run_serial", serial)
	}
	if turn, ok := port.TurnIndexFromContext(attempt.ctx); ok {
		args = append(args, "turn", turn)
	}
	if decision == decisionRetry {
		args = append(args, "backoff", backoff)
	} else {
		args = append(args, "replay_suppressed_reason", string(reason))
	}
	args = append(args, attemptMetadataArgs(err)...)

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

func retryDispositionDiagnostic(disposition port.RetryDisposition) string {
	switch disposition {
	case port.RetryDispositionRetryable:
		return "retryable"
	case port.RetryDispositionPermanent:
		return "permanent"
	default:
		return "unknown"
	}
}

func streamProgressDiagnostic(progress port.StreamProgress) string {
	switch progress {
	case port.StreamProgressPrecommit:
		return "precommit"
	case port.StreamProgressVisible:
		return "visible"
	case port.StreamProgressComplete:
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
	if !validOptionalStatus(m.httpStatus) || !validOptionalStatus(m.inBandStatus) {
		return false
	}
	if m.providerCode != "" && !validPrintableToken(m.providerCode, 128) {
		return false
	}
	if (m.correlationKind == "") != (m.correlationID == "") {
		return false
	}
	switch m.correlationKind {
	case "":
		return true
	case "request", "response", "trace", "completion", "message":
		return validPrintableToken(m.correlationID, 256)
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
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func attemptMetadataArgs(err error) []any {
	var carrier port.ProviderErrorMetadataError
	if !errors.As(err, &carrier) {
		return nil
	}
	metadata := attemptErrorMetadata{
		httpStatus:      carrier.ProviderHTTPStatus(),
		inBandStatus:    carrier.ProviderInBandStatus(),
		providerCode:    carrier.ProviderErrorCode(),
		correlationKind: carrier.ProviderErrorCorrelationKind(),
		correlationID:   carrier.ProviderErrorCorrelationID(),
	}
	if !metadata.valid() {
		return nil
	}
	args := make([]any, 0, 10)
	if metadata.httpStatus != 0 {
		args = append(args, "http_status", metadata.httpStatus)
	}
	if metadata.inBandStatus != 0 {
		args = append(args, "in_band_status", metadata.inBandStatus)
	}
	if metadata.providerCode != "" {
		args = append(args, "provider_code", metadata.providerCode)
	}
	if metadata.correlationKind != "" {
		args = append(args,
			"correlation_kind", metadata.correlationKind,
			"correlation_id", metadata.correlationID)
	}
	return args
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
	p.logAttemptDecision(attempt, err, decisionTerminal, port.StreamProgressVisible, replayVisibleOutput, 0)
}

// Capabilities forwards the wrapped provider's capabilities unchanged: the
// resilience decorator adds retries/breaker behaviour only and never alters what
// kinds of prompt input the underlying provider consumes.
func (p *resilientProvider) Capabilities() port.ProviderCapabilities {
	return p.inner.Capabilities()
}

// allow checks the breaker before an attempt. It returns a *BreakerError if the
// breaker is open and the cooldown has not elapsed. When the cooldown has
// elapsed it transitions to half-open and admits the call.
func (p *resilientProvider) allow(now time.Time) error {
	if p.cfg.BreakerThreshold < 1 {
		return nil
	}
	p.breaker.mu.Lock()
	defer p.breaker.mu.Unlock()
	if !p.breaker.open {
		return nil
	}
	elapsed := now.Sub(p.breaker.openedAt)
	if elapsed < p.cfg.BreakerCooldown {
		return &BreakerError{RetryAfter: p.cfg.BreakerCooldown - elapsed}
	}
	// Cooldown elapsed: admit a half-open trial.
	p.breaker.halfOpen = true
	p.diag().Log(context.Background(), port.LevelInfo, "llm circuit breaker half-open; admitting a trial")
	return nil
}

// recordSuccess resets the breaker after a successful establishment.
func (p *resilientProvider) recordSuccess() {
	if p.cfg.BreakerThreshold < 1 {
		return
	}
	p.breaker.mu.Lock()
	wasOpen := p.breaker.open || p.breaker.halfOpen
	p.breaker.consecutiveFailures = 0
	p.breaker.open = false
	p.breaker.halfOpen = false
	p.breaker.mu.Unlock()
	if wasOpen {
		p.diag().Log(context.Background(), port.LevelInfo, "llm circuit breaker closed (recovered)")
	}
}

// recordFailure tallies a failed attempt and opens the breaker once the
// threshold is reached (or immediately again on a failed half-open trial).
// Stream calls it only for TRANSIENT establishment failures (HTTP 429/408/5xx,
// network errors, per-attempt timeouts — see isTransientForBreaker); permanent
// client errors (4xx other than 408/429) and caller cancellations are
// breaker-neutral and never reach here.
func (p *resilientProvider) recordFailure(now time.Time) {
	if p.cfg.BreakerThreshold < 1 {
		return
	}
	p.breaker.mu.Lock()
	// A failure from an attempt already in flight when another request opened the
	// breaker must not restart its cooldown or overwrite its opening state.
	if p.breaker.open && !p.breaker.halfOpen {
		p.breaker.mu.Unlock()
		return
	}
	wasHalfOpen := p.breaker.halfOpen
	p.breaker.consecutiveFailures++
	if wasHalfOpen {
		// A failed trial re-opens the breaker and restarts the cooldown.
		p.breaker.halfOpen = false
		p.breaker.open = true
		p.breaker.openedAt = now
	} else if p.breaker.consecutiveFailures >= p.cfg.BreakerThreshold {
		p.breaker.open = true
		p.breaker.openedAt = now
	}
	// Emit on a fresh closed→open crossing or on a failed half-open trial.
	opened := p.breaker.open && (wasHalfOpen || p.breaker.consecutiveFailures == p.cfg.BreakerThreshold)
	failures := p.breaker.consecutiveFailures
	p.breaker.mu.Unlock()
	// Log the open transition exactly once per crossing: the initial
	// closed→open, and again each time a failed half-open trial re-opens it.
	if opened {
		p.diag().Log(context.Background(), port.LevelInfo, "llm circuit breaker opened",
			"consecutive_failures", failures,
			"cooldown", p.cfg.BreakerCooldown)
	}
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
	progress       port.StreamProgress
	chunk          port.Chunk
	buffered       []port.Chunk
	discardedUsage session.Usage
	remaining      iter.Seq2[port.Chunk, error]
	abort          func()
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
	var lastDiagnostic attemptDiagnostic
	var discardedUsage session.Usage
	for attempt := 0; attempt < p.cfg.MaxAttempts; attempt++ {
		// Honour caller cancellation before doing any work.
		if err := ctx.Err(); err != nil {
			return terminalWithUsage(discardedUsage, classifiedProgressError(err, port.StreamProgressPrecommit))
		}
		started := p.cfg.Clock()
		diagnostic := attemptDiagnostic{
			ctx: ctx, model: req.Model, attempt: attempt + 1,
			maxAttempts: p.cfg.MaxAttempts, started: started,
		}
		lastDiagnostic = diagnostic
		if err := p.allow(started); err != nil {
			p.logAttemptDecision(diagnostic, err, decisionTerminal, port.StreamProgressPrecommit, replayBreakerOpen, 0)
			return terminalWithUsage(discardedUsage, classifiedProgressError(err, port.StreamProgressPrecommit))
		}

		head, err := p.establish(ctx, req, diagnostic)
		if err == nil {
			// Stream reached a semantic boundary. Breaker success is recorded only by
			// wrap after clean completion; visible output alone is not provider health.
			if discardedUsage != (session.Usage{}) {
				head.buffered = append([]port.Chunk{{Kind: port.ChunkUsage, Usage: &discardedUsage}}, head.buffered...)
			}
			return p.wrap(head, diagnostic), nil
		}
		if head != nil {
			discardedUsage = discardedUsage.Add(head.discardedUsage)
		}

		lastErr = err

		// Caller cancellation is never retried and is breaker-neutral.
		if isCallerCanceled(ctx, err) {
			return terminalWithUsage(discardedUsage, classifiedProgressError(err, port.StreamProgressPrecommit))
		}
		// Only TRANSIENT failures count toward the shared breaker; permanent
		// client errors (4xx) and caller cancels leave its counters untouched.
		// (A half-open trial that fails with a PERMANENT error therefore leaves
		// the breaker in open&halfOpen — the next allow re-admits a trial after
		// cooldown; permanent errors never drive breaker state.)
		if isTransientForBreaker(err) {
			p.recordFailure(p.cfg.Clock())
		}
		// A per-attempt timeout (establishment deadline) is a distinct, diagnosable
		// stall signal — surface it before backing off / retrying.
		if errors.Is(err, errFirstChunkTimeout) && ctx.Err() == nil {
			p.diag().Log(ctx, port.LevelDebug, "llm stream per-attempt timeout fired",
				"per_attempt_timeout", p.cfg.PerAttemptTimeout,
				"attempt", attempt+1,
				"max_attempts", p.cfg.MaxAttempts)
		}
		// A provider adapter may already have spent one narrowly safe internal
		// repair. Its explicit no-retry decision is a policy veto, not a rewrite of
		// the causal disposition.
		if retryable, explicit := explicitRetryDecision(err); explicit && !retryable {
			p.logAttemptDecision(diagnostic, err, decisionTerminal, port.StreamProgressPrecommit, replayProviderInternalVeto, 0)
			return terminalWithUsage(discardedUsage, classifiedProgressError(err, port.StreamProgressPrecommit))
		}

		disposition := dispositionOf(err)
		if disposition != port.RetryDispositionRetryable {
			reason := replayUnknown
			if disposition == port.RetryDispositionPermanent {
				reason = replayPermanent
			}
			p.logAttemptDecision(diagnostic, err, decisionTerminal, port.StreamProgressPrecommit, reason, 0)
			return terminalWithUsage(discardedUsage, classifiedProgressError(err, port.StreamProgressPrecommit))
		}
		if !p.cfg.Classifier(err) {
			p.logAttemptDecision(diagnostic, err, decisionTerminal, port.StreamProgressPrecommit, replayClassifierVeto, 0)
			return terminalWithUsage(discardedUsage, classifiedProgressError(err, port.StreamProgressPrecommit))
		}
		// Backoff before the next attempt, unless this was the last one.
		if attempt < p.cfg.MaxAttempts-1 {
			d := p.backoffDuration(attempt)
			p.logAttemptDecision(diagnostic, err, decisionRetry, port.StreamProgressPrecommit, "", d)
			if berr := p.backoffWith(ctx, d); berr != nil {
				return terminalWithUsage(discardedUsage, classifiedProgressError(berr, port.StreamProgressPrecommit))
			}
		}
	}
	p.logAttemptDecision(lastDiagnostic, lastErr, decisionTerminal, port.StreamProgressPrecommit, replayAttemptsExhausted, 0)
	exhausted := &ExhaustedError{Attempts: p.cfg.MaxAttempts, Err: classifiedProgressError(lastErr, port.StreamProgressPrecommit), PerAttempt: p.cfg.PerAttemptTimeout}
	return terminalWithUsage(discardedUsage, exhausted)
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
	next, stop := iter.Pull2(seq)
	result := &attemptResult{progress: port.StreamProgressUnknown}
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
			result.progress = port.StreamProgressComplete
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
		result.discardedUsage = result.discardedUsage.Add(discardedChunkUsage(chunk))
		if chunk.Kind == port.ChunkDone {
			result.buffered = append(result.buffered, chunk)
			result.progress = port.StreamProgressComplete
			stop()
			if cancel != nil {
				cancel()
			}
			return result, nil
		}
		if !advancesVisible(chunk) {
			result.buffered = append(result.buffered, chunk)
			result.progress = port.StreamProgressPrecommit
			continue
		}

		result.progress = port.StreamProgressVisible
		result.chunk = chunk
		result.remaining = p.restSeq(diagnostic, next, stop, cancel)
		result.abort = func() {
			if cancel != nil {
				cancel()
			}
			stop()
		}
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
				p.logAttemptDecision(diagnostic, idleErr, decisionTerminal, port.StreamProgressVisible, replayVisibleOutput, 0)
				yield(port.Chunk{}, classifiedProgressError(idleErr, port.StreamProgressVisible))
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
		cleanupRemaining := func() {
			if result.abort != nil {
				result.abort()
			}
		}
		for _, buffered := range result.buffered {
			if !yield(buffered, nil) {
				cleanupRemaining()
				return
			}
		}
		switch result.progress {
		case port.StreamProgressComplete:
			p.recordSuccess()
			return
		case port.StreamProgressVisible:
			if !yield(result.chunk, nil) {
				cleanupRemaining()
				return
			}
			if result.remaining == nil {
				p.recordSuccess()
				return
			}
			clean := true
			consumed := true
			result.remaining(func(chunk port.Chunk, err error) bool {
				if err != nil {
					clean = false
					if !isCallerCanceled(diagnostic.ctx, err) && isTransientForBreaker(err) {
						p.recordFailure(p.cfg.Clock())
					}
				}
				if !yield(chunk, err) {
					consumed = false
					return false
				}
				return err == nil
			})
			if clean && consumed {
				p.recordSuccess()
			}
		case port.StreamProgressUnknown, port.StreamProgressPrecommit:
			cleanupRemaining()
		}
	}
}

// backoffWith sleeps for the pre-computed duration d before the next attempt,
// honouring ctx (it aborts promptly on cancellation and returns ctx.Err()). The
// duration is computed by the caller (so it can also be logged) via
// backoffDuration.
func (*resilientProvider) backoffWith(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
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
		exp *= 2
		if p.cfg.MaxBackoff > 0 && exp >= p.cfg.MaxBackoff {
			exp = p.cfg.MaxBackoff
			break
		}
		if exp <= 0 { // overflow
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

// isCallerCanceled reports whether err stems from the caller's ctx being
// cancelled or deadline-exceeded (as opposed to a per-attempt timeout, which is
// a retryable failure).
func isCallerCanceled(ctx context.Context, err error) bool {
	if ctx.Err() == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
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
	return DefaultDisposition(err) == port.RetryDispositionRetryable
}

// DefaultDisposition classifies the causal provider failure without deciding
// whether policy permits another attempt. Unknown is conservative and is never
// promoted to permanent.
func DefaultDisposition(err error) port.RetryDisposition {
	return dispositionOf(err)
}

func defaultDisposition(err error) port.RetryDisposition {
	if err == nil || errors.Is(err, context.Canceled) {
		return port.RetryDispositionUnknown
	}
	if errors.Is(err, ErrCredentials) {
		return port.RetryDispositionPermanent
	}

	var h2Err *http2.StreamError
	if errors.As(err, &h2Err) {
		return port.RetryDispositionRetryable
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
		return port.RetryDispositionRetryable
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return port.RetryDispositionRetryable
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		return port.RetryDispositionRetryable
	}
	return port.RetryDispositionUnknown
}

func statusDisposition(code int) port.RetryDisposition {
	switch {
	case code == 408 || code == 409 || code == 429:
		return port.RetryDispositionRetryable
	case code >= 500 && code <= 599:
		return port.RetryDispositionRetryable
	case code >= 400 && code <= 499:
		return port.RetryDispositionPermanent
	default:
		return port.RetryDispositionUnknown
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
