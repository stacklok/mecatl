// Package llmresilience provides a harness-level resilience decorator around any
// port.LLMProvider. It adds bounded retries with exponential backoff and jitter,
// a consecutive-failure circuit breaker, per-attempt timeouts, and pluggable
// error classification.
//
// The single load-bearing correctness rule is no-replay-after-first-committing-chunk:
// once any chunk that mutates session state has been observed, the turn cannot be
// safely replayed. ChunkText, ChunkToolCall, ChunkUsage, ChunkDone, and ChunkPhase
// are committing (assembled into session.Message or trigger dispatch). ChunkReasoning
// and ChunkReasoningItem are NOT committing — they are opaque blobs replayed verbatim
// on the NEXT turn's context and carry no partial session state mid-stream. This layer
// therefore retries failures that arrive before the first committing chunk: it buffers
// any leading non-committing chunks across a failed attempt and only promotes to the
// no-retry zone when a committing chunk is in hand. Once any committing chunk has been
// emitted to the caller, a subsequent mid-stream error is surfaced verbatim and never
// retried.
//
// Post-first-chunk reads are additionally bounded by StreamIdleTimeout: the gap
// between consecutive chunks AFTER the first is bounded so a mid-stream upstream
// stall cannot wedge the caller forever (the underlying adapters' stream.Next()
// blocks indefinitely on a stalled SSE connection). When a read exceeds the idle
// budget the wrapper cancels the per-attempt context and SYNTHESIZES a terminal
// *StreamIdleError — it cannot rely on the adapter to surface one, because the
// openai/anthropic adapters SWALLOW the ctx error on cancel (they yield nothing).
// A StreamIdleError is terminal and never retried: no-replay-after-first-chunk
// holds, so a mid-stream idle stall ends the turn as an error.
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
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	oai "github.com/openai/openai-go/v3"
	"golang.org/x/net/http2"

	"github.com/stacklok/mecatl/engine/port"
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
	// PerAttemptTimeout bounds each attempt's establishment (connect + first
	// committing chunk). 0 disables it. It never overrides a shorter caller deadline.
	PerAttemptTimeout time.Duration
	// StreamIdleTimeout bounds the gap between consecutive chunks AFTER the first
	// chunk has been observed. 0 disables it. A longer stall terminates the stream
	// with a classified *StreamIdleError (errors.Is(_, context.DeadlineExceeded)),
	// which is TERMINAL and never retried — no-replay-after-first-chunk holds. It
	// is distinct from PerAttemptTimeout, which bounds only establishment and the
	// FIRST chunk (and is retryable).
	StreamIdleTimeout time.Duration
	// BreakerThreshold is the number of consecutive failed attempts that opens
	// the breaker. Values < 1 disable the breaker.
	BreakerThreshold int
	// BreakerCooldown is how long the breaker stays open before half-opening.
	BreakerCooldown time.Duration
	// Classifier reports whether an error is retryable. nil selects
	// DefaultClassifier.
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

// StreamIdleError is the terminal error synthesized when a stream stalls
// mid-flight: no chunk arrived within StreamIdleTimeout AFTER the first chunk was
// already observed. The wrapper synthesizes it because the underlying adapters
// swallow the ctx error on cancel (they yield nothing once the context is done),
// so cancelling the per-attempt context unblocks the inner stream.Next() but
// surfaces no error of its own. It is TERMINAL and never retried
// (no-replay-after-first-chunk).
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

// logMidStreamError records the OTHER path that ends a turn terminally: an error
// chunk yielded AFTER the first committing chunk. It is never retried (the
// no-replay-after-first-chunk rule), so it is the end of the turn — and, like the
// non-retryable establishment error above, it previously logged at NO level (issue #319 /
// #318 diagnosis).
//
// The blind spot was exactly THREE emissions wide, and this comment used to describe it
// loosely enough to contradict its sibling 180 lines below. What WAS already observable
// pre-#319: the per-attempt retry (Debug), exhausted attempts (Info), the idle stall (Info)
// and the breaker's own state TRANSITIONS (Info). What was silent: this mid-stream error,
// the non-retryable establishment error, and the breaker REJECTION — the three paths that
// actually kill a turn. All three log at Info now.
//
// It is called from BOTH restSeq variants (idle-bounded and not) so disabling
// StreamIdleTimeout cannot silently re-open the blind spot.
//
// The ctx is deliberately context.Background(), mirroring the sibling idle-stall site:
// the request ctx may already be done by the time a mid-stream error surfaces, and a
// slog handler that honours ctx cancellation would drop the line.
//
// The model is CORRELATION, not decoration: on a busy server "llm stream failed
// mid-stream" alone tells an operator that *a* turn died, not whose — half of what issue
// #319 asked for. It is threaded down from establish's port.LLMRequest (Stream → establish
// → pullToCommit → restSeq) rather than read off a wider port: LLMRequest.Model is a bare
// opaque string and must stay so, and the wrapper deliberately sees no session/run
// identity at all (it is a provider decorator, not a run-scoped sink), so the model id is
// the finest correlation reachable here without widening port.LLMRequest.
// A CALLER CANCEL is not a failure and must not be logged as one. An operator pressing
// ctrl+c (or a client disconnecting) surfaces here as context.Canceled, and reporting
// "llm stream failed mid-stream" for it mislabels the single most common way a turn ends
// early — the same accuracy defect as the "denied by user" message that was never a user's
// decision. It is still logged, because "the turn ended and nothing else will arrive" is
// worth one line either way; only the wording (and the absence of an err= arg, which would
// just read "context canceled") changes.
func (p *resilientProvider) logMidStreamError(model string, err error) {
	if errors.Is(err, context.Canceled) {
		p.diag().Log(context.Background(), port.LevelInfo, "llm stream cancelled mid-stream; ending turn",
			"model", model)
		return
	}
	p.diag().Log(context.Background(), port.LevelInfo, "llm stream failed mid-stream; ending turn",
		"model", model,
		"err", clampErr(err))
}

// clampErr renders an error to a length-bounded string for a diagnostics arg.
// The wrapper sees only port.LLMRequest + errors, never prompt text, but a
// provider error body can still be large — clamp it so a single log line stays
// bounded.
func clampErr(err error) string {
	if err == nil {
		return ""
	}
	const maxLen = 256
	s := err.Error()
	if len(s) > maxLen {
		// Back off to the nearest rune boundary so we never split a multi-byte
		// UTF-8 rune (which would render as a replacement char in the log line).
		cut := maxLen
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return s[:cut] + "…"
	}
	return s
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
	// Cooldown elapsed: admit a single half-open trial.
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
	// A half-open trial leaves open=true (allow() sets halfOpen without clearing
	// open), so a failed trial is NOT a closed→open crossing by the open bit
	// alone. Treat a failed half-open trial as its own crossing: it is an
	// operator-meaningful "breaker re-opened" event. `crossing` is therefore the
	// UNION of (closed→open) and (half-open trial failed and re-opened).
	wasOpen := p.breaker.open
	wasHalfOpen := p.breaker.halfOpen
	p.breaker.consecutiveFailures++
	if p.breaker.halfOpen {
		// A failed trial re-opens the breaker and restarts the cooldown.
		p.breaker.halfOpen = false
		p.breaker.open = true
		p.breaker.openedAt = now
	} else if p.breaker.consecutiveFailures >= p.cfg.BreakerThreshold {
		p.breaker.open = true
		p.breaker.openedAt = now
	}
	// Emit on a fresh closed→open crossing OR on a half-open→open re-open.
	opened := p.breaker.open && (!wasOpen || wasHalfOpen)
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

// isCommitting reports whether a ChunkKind mutates session state. Once a
// committing chunk has been observed the stream cannot be safely retried.
// Non-committing kinds (ChunkReasoning, ChunkReasoningItem) carry opaque blobs
// replayed on the NEXT turn and are safe to discard on retry.
// Default: true — any unrecognised future kind is conservatively committing.
func isCommitting(kind port.ChunkKind) bool {
	switch kind {
	case port.ChunkReasoning, port.ChunkReasoningItem:
		return false
	default:
		return true
	}
}

// firstChunk is the buffered head of an attempt's stream: the first committing
// chunk the inner iterator produced, whether the stream was empty, any
// non-committing chunks buffered before that first committing chunk, and a
// continuation iterator (restSeq) that yields the remainder and performs cleanup.
type firstChunk struct {
	chunk     port.Chunk
	empty     bool
	noCommit  bool         // true when pre-commit chunks were buffered but no committing chunk arrived (clean close)
	preCommit []port.Chunk // non-committing chunks buffered before the first committing chunk
	restSeq   iter.Seq2[port.Chunk, error]
}

// Stream establishes the inner stream with retries and breaker protection, then
// returns an iterator that replays the buffered first chunk followed by the rest
// of the inner stream. Mid-stream errors (after the first chunk) are surfaced,
// never retried.
func (p *resilientProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	var lastErr error
	for attempt := 0; attempt < p.cfg.MaxAttempts; attempt++ {
		// Honour caller cancellation before doing any work.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// The THIRD of the five paths that end a turn terminally (and one of the two where the
		// provider is never called at all): the shared breaker is OPEN, so the request is
		// rejected outright. It logged at NO level, so an operator reading the log saw a turn
		// die with nothing at all in it — the exact blind spot issue #319 is about, whose
		// acceptance is that no terminal stream failure ends a turn without at least one
		// Info-level diagnostic. It was one of exactly THREE silent emissions, with the
		// non-retryable establishment error and the mid-stream error below; exhausted
		// attempts, the idle stall and the breaker's own state transitions were already
		// observable (see logMidStreamError for the full pre-#319 ledger). It carries the
		// model for the same correlation reason, and the error names the cooldown.
		if err := p.allow(p.cfg.Clock()); err != nil {
			p.diag().Log(ctx, port.LevelInfo, "llm stream rejected by the open circuit breaker; ending turn",
				"model", req.Model,
				"attempt", attempt+1,
				"err", clampErr(err))
			return nil, err
		}

		head, err := p.establish(ctx, req)
		if err == nil {
			// Stream established and (if non-empty) first chunk in hand.
			p.recordSuccess()
			return wrap(head), nil
		}

		lastErr = err

		// Caller cancellation is never retried and is breaker-neutral.
		if isCallerCanceled(ctx, err) {
			return nil, err
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
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			p.diag().Log(ctx, port.LevelDebug, "llm stream per-attempt timeout fired",
				"per_attempt_timeout", p.cfg.PerAttemptTimeout,
				"attempt", attempt+1,
				"max_attempts", p.cfg.MaxAttempts)
		}
		// Permanent (non-retryable) errors are surfaced verbatim, not retried. This
		// TERMINALLY ends the turn, so it is logged at Info: the recoverable lifecycle
		// (retry, exhaustion, idle stall, breaker transitions) was already observable
		// while this — one of the THREE silent paths that actually kill a turn, with the
		// mid-stream error and the breaker rejection — logged at NO level, so an operator
		// reading mecatui.log could not tell a permanent 4xx from a run that never called
		// the provider at all (issue #319 / #318 diagnosis).
		//
		// It carries the model for the same correlation reason logMidStreamError does:
		// on a busy server the bare message identifies that A turn died, not whose, and
		// the model id is the finest correlation this decorator can reach without
		// widening port.LLMRequest (which must stay provider-neutral).
		if !p.cfg.Classifier(err) {
			p.diag().Log(ctx, port.LevelInfo, "llm stream failed with a non-retryable provider error; ending turn",
				"model", req.Model,
				"attempt", attempt+1,
				"err", clampErr(err))
			return nil, err
		}
		// Backoff before the next attempt, unless this was the last one.
		if attempt < p.cfg.MaxAttempts-1 {
			d := p.backoffDuration(attempt)
			p.diag().Log(ctx, port.LevelDebug, "llm stream attempt failed; retrying",
				"attempt", attempt+1,
				"max_attempts", p.cfg.MaxAttempts,
				"backoff", d,
				"err", clampErr(err))
			if berr := p.backoffWith(ctx, d); berr != nil {
				return nil, berr
			}
		}
	}
	// The FOURTH of the FIVE terminal paths (the fifth is the post-first-chunk idle stall in
	// the stream watchdog below — a distinct emission on a distinct code path, and the shape
	// operators actually report as "thinking, then nothing"). It carries model + err for the
	// same correlation reason as its siblings: an operator who has learned to grep the log by
	// model must get every way a turn dies, not some of them, and the last attempt's error is
	// the only clue to WHY establishment never succeeded.
	p.diag().Log(ctx, port.LevelInfo, "llm stream not established after all attempts",
		"model", req.Model,
		"attempts", p.cfg.MaxAttempts,
		"err", clampErr(lastErr))
	return nil, &ExhaustedError{Attempts: p.cfg.MaxAttempts, Err: lastErr, PerAttempt: p.cfg.PerAttemptTimeout}
}

// establish performs a single attempt: it bounds ESTABLISHMENT (connect + the
// first committing chunk) by PerAttemptTimeout, calls the inner Stream, and pulls
// chunks until the first COMMITTING chunk is in hand so that a pre-committing-chunk
// error is observed here (and thus retryable). Non-committing chunks (ChunkReasoning,
// ChunkReasoningItem) are buffered and replayed on success. On success it returns the
// buffered head and a nil error. On failure it returns the error and cancels the
// per-attempt context.
//
// The per-attempt budget is enforced by a SEPARATE establishment time.Timer, NOT
// by an absolute context deadline: the inner stream rides a deadline-free
// context.WithCancel(ctx), and the timer's goroutine calls cancel() ONLY if the
// first committing chunk has not been pulled by PerAttemptTimeout. The timer stays
// live through any leading non-committing (reasoning) prefix. The timer is stopped
// and its goroutine fully joined the instant the first committing chunk is in hand
// (and on every failure exit). After that the streaming phase is governed solely by
// the idle watchdog (StreamIdleTimeout, run in restSeq on the same cancel handle)
// plus the parent ctx — so an actively-streaming long turn is NEVER cut at the
// per-attempt deadline (the bug an absolute deadline used to cause: the deadline
// stayed live through the whole stream and silently truncated a slow reasoning turn).
//
// The deadline-free ctx is intentionally left live on success: it is cancelled
// when the returned iterator finishes or the caller stops early (handled in
// wrap/restSeq).
func (p *resilientProvider) establish(ctx context.Context, req port.LLMRequest) (*firstChunk, error) {
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

	// Pull chunks until the first COMMITTING chunk is in hand. Non-committing
	// chunks (ChunkReasoning, ChunkReasoningItem) are buffered. The establishment
	// timer stays live through the reasoning prefix.
	return p.pullToCommit(ctx, req.Model, seq, stopEstTimer, cancel, establishmentFailure)
}

// pullToCommit drives the pull iterator from seq until the first COMMITTING chunk
// is available, buffering any non-committing prefix. It is split from establish()
// to keep establish's cyclomatic complexity within the lint budget.
func (p *resilientProvider) pullToCommit(
	ctx context.Context,
	model string,
	seq iter.Seq2[port.Chunk, error],
	stopEstTimer func() bool,
	cancel context.CancelFunc,
	establishmentFailure func(error) error,
) (*firstChunk, error) {
	next, stop := iter.Pull2(seq)
	var preCommit []port.Chunk
	for {
		chunk, cerr, ok := next()
		if !ok {
			// Stream ended (no committing chunk arrived — clean end or timer fired).
			timedOut := stopEstTimer()
			stop()
			if cancel != nil {
				cancel()
			}
			if timedOut && ctx.Err() == nil {
				return nil, attemptError(context.DeadlineExceeded, errFirstChunkTimeout)
			}
			// If non-committing chunks arrived before the clean close, forward them
			// rather than silently discarding them (empty = true short-circuits wrap).
			if len(preCommit) > 0 {
				return &firstChunk{preCommit: preCommit, noCommit: true}, nil
			}
			return &firstChunk{empty: true}, nil
		}
		if cerr != nil {
			// Error before any committing chunk: retryable establishment failure.
			failure := establishmentFailure(cerr)
			stopEstTimer()
			stop()
			if cancel != nil {
				cancel()
			}
			return nil, failure
		}

		if !isCommitting(chunk.Kind) {
			// Non-committing chunk: buffer it and keep pulling.
			preCommit = append(preCommit, chunk)
			continue
		}

		// First committing chunk in hand. The establishment budget is over: stop
		// and JOIN the timer goroutine NOW (before building rest) so it cannot fire
		// mid-stream and cannot leak. The streaming phase is governed by restSeq's
		// idle watchdog (StreamIdleTimeout) + the parent ctx only.
		if stopEstTimer() {
			// Late-fire race: the timer fired in the narrow window between pulling
			// this committing chunk and stopping the timer. It has already called
			// cancel(), so attemptCtx is dead and the streaming phase cannot proceed.
			// Treat it as a retryable establishment timeout. SAFE: the committing
			// chunk has NOT been yielded to the caller yet (we only buffered it here),
			// so no-replay-after-first-committing-chunk still holds.
			stop()
			if cancel != nil {
				cancel()
			}
			return nil, attemptError(context.DeadlineExceeded, errFirstChunkTimeout)
		}
		rest := p.restSeq(model, next, stop, cancel)
		return &firstChunk{chunk: chunk, preCommit: preCommit, restSeq: rest}, nil
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
func (p *resilientProvider) restSeq(model string, next func() (port.Chunk, error, bool), stop func(), cancel context.CancelFunc) iter.Seq2[port.Chunk, error] {
	if p.cfg.StreamIdleTimeout <= 0 {
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
					p.logMidStreamError(model, e)
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

	// Idle-bounded variant. A real time.NewTimer is used (NOT cfg.Clock — that
	// drives breaker math only, consistent with backoff()).
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
					p.logMidStreamError(model, r.e)
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
					"model", model,
					"idle", p.cfg.StreamIdleTimeout)
				yield(port.Chunk{}, &StreamIdleError{Idle: p.cfg.StreamIdleTimeout})
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

// wrap composes the buffered pre-commit chunks and the first committing chunk
// with the remainder of the inner stream.
func wrap(head *firstChunk) iter.Seq2[port.Chunk, error] {
	return func(yield func(port.Chunk, error) bool) {
		if head.empty {
			return
		}
		// Replay non-committing chunks buffered before the first committing chunk.
		for _, pc := range head.preCommit {
			if !yield(pc, nil) {
				if head.restSeq != nil {
					for range head.restSeq {
						break
					}
				}
				return
			}
		}
		// noCommit: only non-committing chunks were present (clean end before any
		// committing chunk) — do not yield the zero-value head.chunk.
		if head.noCommit {
			return
		}
		if !yield(head.chunk, nil) {
			// Caller stopped after the first committing chunk; drain the rest to
			// trigger its cleanup (stop + cancel) without yielding further.
			if head.restSeq != nil {
				for range head.restSeq {
					break
				}
			}
			return
		}
		if head.restSeq != nil {
			head.restSeq(yield)
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

// DefaultClassifier is the default retry policy. It retries:
//   - net errors and timeouts (net.Error, os timeouts),
//   - HTTP 408, 409, 429, and any 5xx from *openai.Error,
//   - context.DeadlineExceeded NOT tied to the caller (per-attempt timeouts),
//   - HTTP/2 stream errors (http2.StreamError — a peer RST_STREAM /
//     INTERNAL_ERROR transport reset, e.g. "stream error: stream ID 45;
//     INTERNAL_ERROR; received from peer").
//
// It does not retry:
//   - context.Canceled / context.DeadlineExceeded from the caller (handled
//     earlier in Stream, but also reported non-retryable here for safety),
//   - 4xx other than 408/429.
//
// Unknown errors are treated as non-retryable to avoid replaying ambiguous
// failures.
func DefaultClassifier(err error) bool {
	if err == nil {
		return false
	}
	// Caller-style context cancellation is never retryable.
	if errors.Is(err, context.Canceled) {
		return false
	}

	// HTTP/2 stream resets (peer RST_STREAM / INTERNAL_ERROR) are transient
	// transport-level failures — the bounded retry invariant caps the blast
	// radius. All http2.StreamError codes are treated as retryable, matching
	// net/http's own behaviour.
	var h2Err *http2.StreamError
	if errors.As(err, &h2Err) {
		return true
	}

	// OpenAI typed API error: classify on HTTP status.
	var apiErr *oai.Error
	if errors.As(err, &apiErr) {
		return retryableStatus(apiErr.StatusCode)
	}

	// Generic status-bearing errors (interface escape hatch for non-openai
	// providers that expose a StatusCode).
	var sc interface{ StatusCode() int }
	if errors.As(err, &sc) {
		return retryableStatus(sc.StatusCode())
	}

	// Network errors and timeouts are retryable.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// A bare DeadlineExceeded (e.g. a per-attempt timeout surfaced without a
	// net.Error wrapper) is retryable.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// A truncated or malformed payload surfaces as a JSON syntax error or an
	// unexpected EOF: a partial SSE frame (the openai-go ssestream decoder parses
	// each frame with encoding/json), or an empty/garbled error body some
	// OpenAI-compatible gateways return on a transient 5xx (the SDK's error path
	// then discards the status and hands back the raw json error, so it never
	// reaches the *oai.Error / StatusCode arms above). At establishment this is a
	// transient transport/proxy truncation, not a stable client error, so it is
	// retryable — bounded by MaxAttempts + the breaker.
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	return false
}

// retryableStatus reports whether an HTTP status code is retryable.
func retryableStatus(code int) bool {
	switch {
	case code == 408 || code == 409 || code == 429:
		return true
	case code >= 500 && code <= 599:
		return true
	default:
		return false
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
