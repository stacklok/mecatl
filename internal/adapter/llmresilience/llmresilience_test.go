package llmresilience

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"golang.org/x/net/http2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	openaiadapter "github.com/stacklok/mecatl/provider/openai"
)

func TestEncryptedReasoningFallbackFailureIsNotReplayedByOuterResilience(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified or decrypted"}}`)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"server_error","message":"cleaned fallback unavailable"}}`)
	}))
	defer srv.Close()

	inner := openaiadapter.New(
		openaiadapter.WithAPIKey("test-key"),
		openaiadapter.WithBaseURL(srv.URL+"/v1"),
		openaiadapter.WithRequestOption(option.WithMaxRetries(0)),
	)
	wrapped := Wrap(inner, Config{
		MaxAttempts:      4,
		BreakerThreshold: 1,
		BreakerCooldown:  time.Hour,
		// The provider's exhausted internal-repair budget must outrank even an
		// operator classifier that would otherwise retry every error.
		Classifier: func(error) bool { return true },
	})
	assistant := session.NewAssistantMessage("", "opaque-blob", nil)
	assistant.ReasoningItemID = "rs_bad"
	req := port.LLMRequest{Model: "gpt-test", Messages: []session.Message{assistant}}

	seq, err := wrapped.Stream(context.Background(), req)
	if seq != nil || err == nil {
		t.Fatalf("Stream = (%v, %v), want terminal establishment error", seq, err)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("HTTP request count = %d, want exactly 2 despite MaxAttempts=4", got)
	}
	var permanent port.PermanentError
	if errors.As(err, &permanent) {
		t.Fatalf("error = %T %v, must not claim a cleaned 503 is permanent", err, err)
	}
	var apiErr *oai.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("error cause = %T %v, want unwrap-visible OpenAI 503", err, err)
	}

	// Non-retryable is a request replay decision, not a provider-health rewrite:
	// the unwrap-visible cleaned 503 still opens the breaker at threshold one.
	if _, secondErr := wrapped.Stream(context.Background(), req); secondErr == nil {
		t.Fatal("second Stream error = nil, want open breaker")
	} else {
		var breakerErr *BreakerError
		if !errors.As(secondErr, &breakerErr) {
			t.Fatalf("second Stream error = %T %v, want BreakerError", secondErr, secondErr)
		}
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("HTTP request count after breaker rejection = %d, want 2", got)
	}
}

type explicitNoRetryTestError struct{ err error }

func (e *explicitNoRetryTestError) Error() string { return e.err.Error() }
func (e *explicitNoRetryTestError) Unwrap() error { return e.err }
func (*explicitNoRetryTestError) Retryable() bool { return false }

func TestExplicitNoRetryDecisionIsNotPromotedToPermanentMidStream(t *testing.T) {
	inner := &oai.Error{StatusCode: http.StatusServiceUnavailable, Message: "temporary"}
	err := &explicitNoRetryTestError{err: inner}
	p := &resilientProvider{cfg: Config{Classifier: func(error) bool { return false }}}

	got := p.asPermanent(err)
	if got != err {
		t.Fatalf("asPermanent returned %T %v, want original explicit-decision error", got, got)
	}
	var permanent port.PermanentError
	if errors.As(got, &permanent) {
		t.Fatal("explicit no-retry decision was incorrectly promoted to port.PermanentError")
	}
}

// fakeProvider is a programmable port.LLMProvider for tests. Each call to Stream
// consumes the next entry in steps; if steps is exhausted the last entry is
// reused. A step either fails to establish (outerErr != nil), or yields the
// given chunks (optionally ending with a mid-stream error via midErr).
type fakeProvider struct {
	mu    sync.Mutex
	calls int32
	steps []step
	// caps is the capability set this fake advertises, so the forwarding test can
	// assert the decorator returns the inner provider's value verbatim.
	caps port.ProviderCapabilities
	// onAttempt, if set, is invoked at the start of each Stream call with the
	// (zero-based) call index, before the step is evaluated. Useful to observe
	// timing / ctx state.
	onAttempt func(ctx context.Context, n int)
}

type step struct {
	outerErr error        // non-nil: Stream returns this as the outer error.
	chunks   []port.Chunk // chunks to yield before midErr.
	midErr   error        // non-nil: yielded after chunks as a terminal error.
	// block, if true, blocks on ctx.Done() before yielding anything (to exercise
	// per-attempt timeout and caller cancellation).
	block    bool
	blockErr error // error returned after block unblocks (default ctx.Err()).
	// stallAfterChunks, if true, yields chunks then blocks on ctx.Done() WITHOUT
	// yielding anything further — mirroring the openai/anthropic adapters that
	// swallow the ctx error on cancel (they yield nothing). This exercises the
	// post-first-chunk idle watchdog: the stream never produces ChunkDone.
	stallAfterChunks bool
	// onChunk, if set, is invoked just before each chunk is yielded with the
	// zero-based chunk index. Used to sample runtime state (e.g. goroutine count)
	// at a deterministic point mid-stream.
	onChunk func(idx int)
	// chunkInterval, if > 0, sleeps this long BEFORE yielding each chunk after the
	// first (the first chunk is yielded immediately). The sleep aborts early on
	// ctx.Done() (mirroring a real adapter whose read unblocks on cancel). This lets
	// a fake yield chunk 0 immediately then space subsequent chunks across a wall-
	// clock deadline — to prove an actively-streaming turn is NOT cut at the
	// per-attempt deadline. Spacing under StreamIdleTimeout keeps the idle watchdog
	// from firing.
	chunkInterval time.Duration
	// firstChunkAfterCancel, if true, makes the fake wait for ctx.Done() BEFORE
	// yielding the FIRST chunk, then yield it anyway. This DETERMINISTICALLY
	// reproduces the establishment-timer late-fire window: the timer fires (calls
	// cancel → ctx.Done), and only THEN does the first chunk become available to
	// the establish() pull — so stopEstTimer() observes fired==true with a real
	// first chunk in hand. Used by TestEstablishmentTimerLateFireDoesNotTruncate.
	firstChunkAfterCancel bool
}

func (f *fakeProvider) Capabilities() port.ProviderCapabilities { return f.caps }

func (f *fakeProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	n := int(atomic.AddInt32(&f.calls, 1)) - 1
	if f.onAttempt != nil {
		f.onAttempt(ctx, n)
	}
	f.mu.Lock()
	var s step
	switch {
	case n < len(f.steps):
		s = f.steps[n]
	case len(f.steps) > 0:
		s = f.steps[len(f.steps)-1]
	}
	f.mu.Unlock()

	if s.block {
		<-ctx.Done()
		err := s.blockErr
		if err == nil {
			err = ctx.Err()
		}
		return func(yield func(port.Chunk, error) bool) {
			yield(port.Chunk{}, err)
		}, nil
	}
	if s.outerErr != nil {
		return nil, s.outerErr
	}
	return func(yield func(port.Chunk, error) bool) {
		for i, c := range s.chunks {
			if i == 0 && s.firstChunkAfterCancel {
				// Block until the establishment timer fires cancel(), then yield the
				// first chunk anyway — reproducing the late-fire race window.
				<-ctx.Done()
			}
			if i > 0 && s.chunkInterval > 0 {
				// Space chunks after the first by chunkInterval, aborting early if the
				// caller cancels (a real adapter's blocked read unblocks on cancel).
				t := time.NewTimer(s.chunkInterval)
				select {
				case <-t.C:
				case <-ctx.Done():
					t.Stop()
					return
				}
			}
			if s.onChunk != nil {
				s.onChunk(i)
			}
			if !yield(c, nil) {
				return
			}
		}
		if s.stallAfterChunks {
			// Mirror the real adapters: block until cancelled, then swallow the ctx
			// error (yield nothing). The wrapper's idle watchdog must synthesize the
			// terminal error itself.
			<-ctx.Done()
			return
		}
		if s.midErr != nil {
			yield(port.Chunk{}, s.midErr)
		}
	}, nil
}

func (f *fakeProvider) Calls() int { return int(atomic.LoadInt32(&f.calls)) }

// manualClock is a controllable time source for deterministic breaker tests.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func textTurn(s string) []port.Chunk {
	return []port.Chunk{
		{Kind: port.ChunkText, Text: s},
		{Kind: port.ChunkUsage, Usage: &session.Usage{}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}
}

func drain(t *testing.T, seq iter.Seq2[port.Chunk, error]) ([]port.Chunk, error) {
	t.Helper()
	var got []port.Chunk
	for c, err := range seq {
		if err != nil {
			return got, err
		}
		got = append(got, c)
	}
	return got, nil
}

// tinyBackoffCfg returns a Config with negligible backoff so retry tests do not
// sleep meaningfully.
func tinyBackoffCfg(maxAttempts int) Config {
	return Config{
		MaxAttempts: maxAttempts,
		BaseBackoff: time.Nanosecond,
		MaxBackoff:  time.Nanosecond,
	}
}

func TestSessionIDContextReachesEveryEstablishmentRetry(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	var got []session.SessionID
	f := &fakeProvider{
		steps: []step{
			{outerErr: conn},
			{chunks: textTurn("ok")},
		},
		onAttempt: func(ctx context.Context, _ int) {
			id, ok := port.SessionIDFromContext(ctx)
			if !ok {
				t.Error("session ID missing from retry context")
				return
			}
			got = append(got, id)
		},
	}
	p := Wrap(f, tinyBackoffCfg(2))
	ctx := port.WithSessionID(context.Background(), "session-retry-exact")

	seq, err := p.Stream(ctx, port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if _, err := drain(t, seq); err != nil {
		t.Fatalf("drain error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("observed session IDs = %q, want one for each of 2 attempts", got)
	}
	for i, id := range got {
		if id != "session-retry-exact" {
			t.Errorf("attempt %d session ID = %q, want %q", i+1, id, "session-retry-exact")
		}
	}
}

func TestFailsThenSucceedsWithinMaxAttempts(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	f := &fakeProvider{steps: []step{
		{outerErr: conn},
		{outerErr: conn},
		{chunks: textTurn("ok")},
	}}
	p := Wrap(f, tinyBackoffCfg(3))

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if len(got) != 3 || got[0].Text != "ok" {
		t.Fatalf("got %+v, want textTurn(ok)", got)
	}
	if f.Calls() != 3 {
		t.Fatalf("inner called %d times, want 3", f.Calls())
	}
}

// TestRetriesTruncatedFirstChunk proves a truncated first SSE frame — surfaced
// as a *json.SyntaxError yielded as the first (and only) chunk, the real
// establishment-time shape — is RETRIED, not surfaced. Without the classifier
// fix this failed on the first attempt with "unexpected end of JSON input".
func TestRetriesTruncatedFirstChunk(t *testing.T) {
	f := &fakeProvider{steps: []step{
		{midErr: emptyJSONErr()}, // no committing chunk yet ⇒ retryable establishment error
		{chunks: textTurn("ok")},
	}}
	p := Wrap(f, tinyBackoffCfg(3))

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream returned outer error: %v", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if len(got) != 3 || got[0].Text != "ok" {
		t.Fatalf("got %+v, want textTurn(ok)", got)
	}
	if f.Calls() != 2 {
		t.Fatalf("inner called %d times, want 2 (one retry after the truncated frame)", f.Calls())
	}
}

func TestExceedsMaxAttemptsReturnsExhausted(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	f := &fakeProvider{steps: []step{{outerErr: conn}}}
	var backoffs int
	clk := &manualClock{t: time.Unix(0, 0)}
	cfg := Config{
		MaxAttempts: 3,
		BaseBackoff: time.Nanosecond,
		MaxBackoff:  time.Nanosecond,
		Clock:       clk.Now,
	}
	// Count attempts via onAttempt; backoff itself uses real (tiny) timers.
	f.onAttempt = func(_ context.Context, _ int) { backoffs++ }

	p := Wrap(f, cfg)
	_, err := p.Stream(context.Background(), port.LLMRequest{})
	var ex *ExhaustedError
	if !errors.As(err, &ex) {
		t.Fatalf("err = %v, want *ExhaustedError", err)
	}
	if ex.Attempts != 3 {
		t.Fatalf("Attempts = %d, want 3", ex.Attempts)
	}
	if !errors.Is(err, conn) {
		t.Fatalf("ExhaustedError does not wrap the conn error: %v", err)
	}
	if f.Calls() != 3 {
		t.Fatalf("inner called %d times, want 3", f.Calls())
	}
}

func TestMidStreamErrorAfterFirstChunkNotRetried(t *testing.T) {
	boom := errors.New("transport blew up mid-stream")
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkText, Text: "partial"}}, midErr: boom},
		{chunks: textTurn("should-not-be-used")},
	}}
	p := Wrap(f, tinyBackoffCfg(3))

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream returned outer error: %v", err)
	}
	got, derr := drain(t, seq)
	if !errors.Is(derr, boom) {
		t.Fatalf("drain error = %v, want boom", derr)
	}
	if len(got) != 1 || got[0].Text != "partial" {
		t.Fatalf("got %+v, want one 'partial' chunk before error", got)
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1 (no replay after first chunk)", f.Calls())
	}
}

// TestStreamIdleTimeoutAfterFirstChunkTerminates is the core regression guard for
// the mid-stream stall bug: a stream that yields a first chunk then stalls (the
// adapter swallows the ctx error and yields nothing) must be terminated by the
// post-first-chunk idle watchdog with a *StreamIdleError, NOT hang forever. The
// stall is TERMINAL and never retried (no-replay-after-first-chunk). The whole
// test is deadline-guarded so a regression HANGS the iterator and fails here.
func TestStreamIdleTimeoutAfterFirstChunkTerminates(t *testing.T) {
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkText, Text: "partial"}}, stallAfterChunks: true},
	}}
	cfg := Config{
		MaxAttempts:       3,
		BaseBackoff:       time.Nanosecond,
		MaxBackoff:        time.Nanosecond,
		StreamIdleTimeout: 50 * time.Millisecond,
	}
	p := Wrap(f, cfg)

	type outcome struct {
		got []port.Chunk
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		seq, err := p.Stream(context.Background(), port.LLMRequest{})
		if err != nil {
			done <- outcome{err: err}
			return
		}
		got, derr := drain(t, seq)
		done <- outcome{got: got, err: derr}
	}()

	select {
	case o := <-done:
		if len(o.got) != 1 || o.got[0].Text != "partial" {
			t.Fatalf("got %+v, want one 'partial' chunk before the stall error", o.got)
		}
		if o.err == nil {
			t.Fatalf("drain returned nil error, want a stream-idle timeout")
		}
		if !errors.Is(o.err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want errors.Is(_, context.DeadlineExceeded)", o.err)
		}
		var sie *StreamIdleError
		if !errors.As(o.err, &sie) {
			t.Fatalf("err = %v, want *StreamIdleError", o.err)
		}
		if f.Calls() != 1 {
			t.Fatalf("inner called %d times, want 1 (idle stall is terminal, never retried)", f.Calls())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stream/drain hung past the idle watchdog deadline — the mid-stream stall was not bounded (regression)")
	}
}

// TestStreamIdleTimeoutDisabledWhenZero asserts the existing post-first-chunk loop
// is unchanged when StreamIdleTimeout == 0: a normal scripted turn completes
// verbatim (no goroutine, no behaviour change).
func TestStreamIdleTimeoutDisabledWhenZero(t *testing.T) {
	f := &fakeProvider{steps: []step{{chunks: textTurn("ok")}}}
	cfg := Config{MaxAttempts: 3, StreamIdleTimeout: 0}
	p := Wrap(f, cfg)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if len(got) != 3 || got[0].Text != "ok" {
		t.Fatalf("got %+v, want textTurn(ok)", got)
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1", f.Calls())
	}
}

// goroutineStackMarkerCount returns how many currently-live goroutines have
// marker somewhere in their stack trace. Unlike runtime.NumGoroutine(), this is
// immune to unrelated background goroutines (GC workers, the race detector's own
// bookkeeping, runtime housekeeping) that fluctuate independently of the code
// under test — that noise is exactly what made a raw NumGoroutine() differential
// flake under -race on a loaded CI runner (issue: disabled==armed was observed
// in CI despite the mechanism being correct). Naming the goroutine we're
// actually looking for is a direct, non-statistical witness instead.
func goroutineStackMarkerCount(marker string) int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), marker)
}

// TestStreamIdleDisabledSpawnsNoWatchdogGoroutine proves the StreamIdleTimeout<=0
// path takes the goroutine-FREE rest loop. restSeqIdleBounded (llmresilience.go)
// is the ONLY place that spawns a per-iteration helper goroutine; restSeqUnbounded
// (the StreamIdleTimeout<=0 path) never does. Both sample at the same
// deterministic point mid-stream (the SECOND chunk, read inside the rest loop) by
// taking a full stack dump and counting frames naming restSeqIdleBounded — the
// disabled config must show ZERO (a different function, restSeqUnbounded, is on
// the stack instead) and the armed config must show at least one (its own
// blocked-in-select frame plus the in-flight helper goroutine reading next()).
func TestStreamIdleDisabledSpawnsNoWatchdogGoroutine(t *testing.T) {
	const watchdogMarker = "restSeqIdleBounded"

	// sampleRestChunkMarkerCount drives a 3-chunk turn and returns the
	// watchdogMarker stack-frame count captured while the SECOND chunk (a
	// rest-loop read) is being produced by the inner provider.
	sampleRestChunkMarkerCount := func(idleTimeout time.Duration) int {
		var sampled int
		f := &fakeProvider{}
		f.steps = []step{{
			chunks: textTurn("ok"),
			onChunk: func(idx int) {
				// idx 0 is the first chunk (pulled in establish, inline either way);
				// idx 1 is read inside the rest loop — the path that differs.
				if idx == 1 {
					sampled = goroutineStackMarkerCount(watchdogMarker)
				}
			},
		}}
		p := Wrap(f, Config{MaxAttempts: 1, StreamIdleTimeout: idleTimeout})
		seq, err := p.Stream(context.Background(), port.LLMRequest{})
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		if _, derr := drain(t, seq); derr != nil {
			t.Fatalf("drain error: %v", derr)
		}
		return sampled
	}

	// Settle so a prior test's still-unwinding watchdog goroutine (async
	// teardown after an idle timeout or an abandoned iterator) is not still on
	// a stack when the disabled sample is taken. waitNoExtraGoroutines is the
	// wrong tool here: its baseline is runtime.NumGoroutine() sampled at call
	// time, so a stray watchdog already running gets baked into the baseline
	// itself and the check passes trivially without ever waiting for it to
	// exit. Poll the actual witness (the marker) instead.
	waitNoWatchdogGoroutine(t, watchdogMarker)

	if disabled := sampleRestChunkMarkerCount(0); disabled != 0 {
		t.Fatalf("disabled config: found %d restSeqIdleBounded stack frame(s), want 0 (no watchdog goroutine when StreamIdleTimeout<=0)", disabled)
	}
	if armed := sampleRestChunkMarkerCount(50 * time.Millisecond); armed < 1 {
		t.Fatalf("armed config: found %d restSeqIdleBounded stack frame(s), want >= 1", armed)
	}
}

// TestStreamIdleEarlyStopDrainsHelper exercises the watchdog-ARMED early-stop
// cleanup branch: a caller that ranges the stream and breaks AFTER the first chunk
// abandons the iterator mid-stream while the idle watchdog goroutine is in flight
// (the agent loop actually does this on a mid-stream error). The buffered first
// chunk must be delivered, the abandonment must unwind cleanly, and the watchdog
// helper goroutine must be drained — proven by the package goleak gate (TestMain).
// The test is deadline-guarded so a regression that wedges the cleanup fails here
// rather than hanging the suite.
func TestStreamIdleEarlyStopDrainsHelper(t *testing.T) {
	// One real first chunk, then a stall (no further chunk, ctx error swallowed) —
	// the same shape the adapters produce. The caller breaks after the first chunk,
	// so the rest-loop's single read is what arms (and then must unwind) the
	// watchdog goroutine.
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkText, Text: "first"}}, stallAfterChunks: true},
	}}
	cfg := Config{
		MaxAttempts:       1,
		StreamIdleTimeout: 50 * time.Millisecond,
	}
	p := Wrap(f, cfg)

	type outcome struct {
		first string
		count int
	}
	done := make(chan outcome, 1)
	go func() {
		seq, err := p.Stream(context.Background(), port.LLMRequest{})
		if err != nil {
			done <- outcome{}
			return
		}
		var o outcome
		for c, e := range seq {
			if e != nil {
				break
			}
			o.count++
			if o.count == 1 {
				o.first = c.Text
			}
			// Abandon the iterator right after the first chunk while the watchdog is
			// armed for the (stalled) remainder.
			break
		}
		done <- o
	}()

	select {
	case o := <-done:
		if o.count != 1 || o.first != "first" {
			t.Fatalf("got count=%d first=%q, want exactly the first chunk", o.count, o.first)
		}
		if f.Calls() != 1 {
			t.Fatalf("inner called %d times, want 1", f.Calls())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("early-stop cleanup wedged: the watchdog-armed abandon path did not unwind (regression)")
	}
	// Goroutine teardown is asynchronous after the break; let the watchdog helper +
	// pull coroutine unwind before the goleak gate samples at TestMain.
	waitNoExtraGoroutines(t, runtime.NumGoroutine())
}

// TestStreamIdleCallerCancelMidStallUnwinds exercises the watchdog-armed path when
// the PARENT context is cancelled WHILE the inner read is stalled: the loop's
// select observes neither a chunk nor its own idle timer first, but the helper's
// next() unblocks via the cancelled ctx and returns, so the iterator must unwind
// promptly (NOT wait out the full idle budget, and NOT leak). Deadline-guarded so
// a regression fails rather than hangs; the goleak gate proves no leak.
func TestStreamIdleCallerCancelMidStallUnwinds(t *testing.T) {
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkText, Text: "first"}}, stallAfterChunks: true},
	}}
	cfg := Config{
		MaxAttempts: 1,
		// Long idle budget: if the test passes, it must be the CALLER CANCEL — not the
		// watchdog timer — that unwinds the stalled read. A regression that ignored the
		// cancel would block until this budget (or forever) and trip the 5s deadline.
		StreamIdleTimeout: time.Hour,
	}
	p := Wrap(f, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		seq, err := p.Stream(ctx, port.LLMRequest{})
		if err != nil {
			return
		}
		for _, e := range seq {
			_ = e
		}
	}()

	// Let the first chunk flow and the inner read stall, then cancel the parent.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Unwound promptly via the caller cancel.
	case <-time.After(5 * time.Second):
		t.Fatal("caller-cancel mid-stall did not unwind the iterator (regression)")
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1", f.Calls())
	}
	waitNoExtraGoroutines(t, runtime.NumGoroutine())
}

// waitNoWatchdogGoroutine polls goroutineStackMarkerCount(marker) until it
// reads zero, so a caller never samples the stack while a prior test's
// watchdog goroutine is still mid-teardown. Unlike waitNoExtraGoroutines, the
// condition polled is the actual witness rather than a goroutine-count
// baseline captured at call time (which a still-live watchdog would already
// be part of).
func waitNoWatchdogGoroutine(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if goroutineStackMarkerCount(marker) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("a prior test's %s watchdog goroutine did not exit within budget", marker)
}

// waitNoExtraGoroutines waits (with a short settle budget) until the live
// goroutine count returns to at most baseline. Goroutine teardown after an
// abandoned/cancelled iterator is asynchronous, so a bare NumGoroutine() check
// would flake; this polls instead. It is a soft pre-check — the authoritative
// leak assertion is the package goleak gate in TestMain.
func waitNoExtraGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Not fatal here (goleak owns the verdict), but surface a hint if it never settled.
	t.Logf("goroutine count did not settle to <= baseline (%d) within budget; goleak gate is authoritative", baseline)
}

// TestPreFirstChunkStallStillUsesPerAttemptTimeout asserts the establishment
// window is unaffected by StreamIdleTimeout: a pre-first-chunk stall still trips
// the (retryable) PerAttemptTimeout path. With both knobs set, the first attempt
// blocks before any chunk (per-attempt deadline fires, retryable) and the second
// succeeds.
func TestPreFirstChunkStallStillUsesPerAttemptTimeout(t *testing.T) {
	f := &fakeProvider{steps: []step{
		{block: true}, // blocks BEFORE any chunk → per-attempt deadline → retryable
		{chunks: textTurn("recovered")},
	}}
	cfg := Config{
		MaxAttempts:       2,
		BaseBackoff:       time.Nanosecond,
		MaxBackoff:        time.Nanosecond,
		PerAttemptTimeout: 20 * time.Millisecond,
		StreamIdleTimeout: 5 * time.Second, // generous; must not interfere pre-first-chunk
	}
	p := Wrap(f, cfg)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if len(got) == 0 || got[0].Text != "recovered" {
		t.Fatalf("got %+v, want recovered", got)
	}
	if f.Calls() != 2 {
		t.Fatalf("inner called %d times, want 2 (pre-first-chunk stall retried)", f.Calls())
	}
}

// TestFirstChunkTimeoutSwallowedByAdapterIsRetriedAndExhausts pins the issue-#82
// root-cause fix (Fix C). It models the laundered path: the inner Stream connects
// (returns a non-nil seq, no outer error) but the model takes longer than the
// per-attempt budget to the FIRST chunk, and the adapter SWALLOWS the ctx error on
// cancel (yields NOTHING — modelled by stallAfterChunks with zero chunks). Before
// the fix this returned firstChunk{empty:true} with NO error → no retry, breaker
// untouched → a phantom clean (empty) completion. After the fix the per-attempt
// DeadlineExceeded is surfaced as a retryable establishment failure: the attempt is
// retried MaxAttempts times, the final error is *ExhaustedError wrapping
// DeadlineExceeded, and the breaker counted every (transient) timeout.
func TestFirstChunkTimeoutSwallowedByAdapterIsRetriedAndExhausts(t *testing.T) {
	// stallAfterChunks with NO chunks: the seq blocks on ctx.Done() during the first
	// next() and yields nothing once the per-attempt ctx is cancelled — exactly the
	// openai/anthropic swallow-on-cancel behaviour.
	f := &fakeProvider{steps: []step{{stallAfterChunks: true}}}
	diag := &recordingDiag{}
	cfg := Config{
		MaxAttempts:       3,
		BaseBackoff:       time.Nanosecond,
		MaxBackoff:        time.Nanosecond,
		PerAttemptTimeout: 20 * time.Millisecond,
		// Threshold == MaxAttempts: the breaker counts each timeout but only OPENS on
		// the third (after the run exhausts), so the run reaches *ExhaustedError and a
		// FOLLOW-UP call then fails fast — proving the timeouts were counted as transient.
		BreakerThreshold: 3,
		BreakerCooldown:  time.Hour,
		Diagnostics:      diag,
	}
	p := Wrap(f, cfg)

	_, err := p.Stream(context.Background(), port.LLMRequest{})
	var ex *ExhaustedError
	if !errors.As(err, &ex) {
		t.Fatalf("err = %v, want *ExhaustedError (a first-chunk timeout must surface, not phantom-done)", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ExhaustedError does not wrap context.DeadlineExceeded: %v", err)
	}
	if !errors.Is(err, errFirstChunkTimeout) {
		t.Fatalf("ExhaustedError does not wrap errFirstChunkTimeout: %v", err)
	}
	if f.Calls() != 3 {
		t.Fatalf("inner called %d times, want 3 (each first-chunk timeout retried)", f.Calls())
	}
	// The breaker counted the timeouts as transient: a follow-up call fails fast with
	// *BreakerError without touching the inner provider.
	if _, berr := p.Stream(context.Background(), port.LLMRequest{}); !errorsAsBreaker(berr) {
		t.Fatalf("follow-up err = %v, want *BreakerError (timeouts must count toward the breaker)", berr)
	}
	if f.Calls() != 3 {
		t.Fatalf("inner called %d times after breaker-open call, want still 3", f.Calls())
	}
	// The #81 per-attempt-timeout DEBUG line now actually fires on this path.
	if rec := diag.find("per-attempt timeout fired"); len(rec) == 0 {
		t.Fatalf("expected the per-attempt-timeout DEBUG line to fire on the first-chunk-timeout path; records=%+v", diag.records)
	}
	// The OPERATOR-FACING message (issue #82, MUST 2): the string that surfaces to
	// the TUI must read in plain language, name the timeout duration, name an action,
	// and NOT leak the internal "llmresilience:" package prefix.
	msg := err.Error()
	if strings.Contains(msg, "llmresilience:") {
		t.Errorf("operator message leaks the package prefix: %q", msg)
	}
	if !strings.Contains(msg, "did not start responding") {
		t.Errorf("operator message lacks the plain-language cause: %q", msg)
	}
	if !strings.Contains(msg, "--llm-per-attempt-timeout") {
		t.Errorf("operator message lacks the actionable hint: %q", msg)
	}
	if !strings.Contains(msg, "20ms") { // the configured PerAttemptTimeout, threaded in
		t.Errorf("operator message lacks the timeout duration: %q", msg)
	}
}

// TestGenuinelyEmptyStreamStaysSuccessAfterFix is the NEGATIVE guard for Fix C: an
// inner that yields zero chunks and FINISHES (no per-attempt deadline) must still
// take the empty-success path — no error, no retry. This guards against
// over-broadening the new DeadlineExceeded disambiguation. (Complements the
// PerAttemptTimeout=0 case in TestEmptyStreamIsSuccess by setting a generous, never-
// firing per-attempt budget so the cause is provably non-deadline.)
func TestGenuinelyEmptyStreamStaysSuccessAfterFix(t *testing.T) {
	f := &fakeProvider{steps: []step{{}}} // zero chunks, no error, no block: empty + done
	cfg := Config{
		MaxAttempts:       3,
		BaseBackoff:       time.Nanosecond,
		MaxBackoff:        time.Nanosecond,
		PerAttemptTimeout: time.Hour, // generous; never fires → cause stays nil
	}
	p := Wrap(f, cfg)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream error = %v, want nil (genuinely empty stream is success)", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if len(got) != 0 {
		t.Fatalf("got %d chunks, want 0", len(got))
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1 (no retry on a genuinely empty stream)", f.Calls())
	}
}

func TestCtxCancelDuringBackoffAbortsPromptly(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("refused")}
	f := &fakeProvider{steps: []step{{outerErr: conn}}}
	cfg := Config{
		MaxAttempts: 5,
		BaseBackoff: time.Hour, // long enough that only cancellation ends it.
		MaxBackoff:  time.Hour,
	}
	p := Wrap(f, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := p.Stream(ctx, port.LLMRequest{})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Stream took %s, want prompt abort on cancel", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestCallerCanceledNotRetried(t *testing.T) {
	f := &fakeProvider{steps: []step{{block: true}}}
	p := Wrap(f, tinyBackoffCfg(5))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := p.Stream(ctx, port.LLMRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1 (caller cancel not retried)", f.Calls())
	}
}

func TestPerAttemptTimeoutIsRetryable(t *testing.T) {
	// First attempt blocks until its per-attempt deadline; second succeeds.
	f := &fakeProvider{steps: []step{
		{block: true}, // returns ctx.Err() == DeadlineExceeded for the attempt ctx
		{chunks: textTurn("recovered")},
	}}
	cfg := Config{
		MaxAttempts:       2,
		BaseBackoff:       time.Nanosecond,
		MaxBackoff:        time.Nanosecond,
		PerAttemptTimeout: 20 * time.Millisecond,
	}
	p := Wrap(f, cfg)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if len(got) == 0 || got[0].Text != "recovered" {
		t.Fatalf("got %+v, want recovered", got)
	}
	if f.Calls() != 2 {
		t.Fatalf("inner called %d times, want 2", f.Calls())
	}
}

// TestRealEstablishErrorNotMaskedAsCanceled is the regression guard for the
// cleanup-cancel-masking bug: with a per-attempt timeout configured, a real
// establishment error (a 400) must NOT be reported as context.Canceled merely
// because establish() cancels the per-attempt context during cleanup. The real
// *oai.Error must remain recoverable via errors.As. Covers BOTH establish error
// paths — the outer Stream error and the first-chunk cerr.
func TestRealEstablishErrorNotMaskedAsCanceled(t *testing.T) {
	cfg := Config{MaxAttempts: 1, PerAttemptTimeout: time.Second}

	cases := []struct {
		name string
		step step
	}{
		{"outer error", step{outerErr: apiErr(400)}},
		{"first-chunk cerr", step{chunks: nil, midErr: apiErr(400)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeProvider{steps: []step{tc.step}}
			p := Wrap(f, cfg)
			_, err := p.Stream(context.Background(), port.LLMRequest{})
			if err == nil {
				t.Fatalf("Stream returned nil error, want the 400")
			}
			if errors.Is(err, context.Canceled) {
				t.Fatalf("err masked as context.Canceled: %v", err)
			}
			var apiErr *oai.Error
			if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
				t.Fatalf("err = %v, want recoverable 400 *oai.Error", err)
			}
			if f.Calls() != 1 {
				t.Fatalf("inner called %d times, want 1", f.Calls())
			}
		})
	}
}

// TestGenuineCallerCancelSurfacesCanceled asserts that a genuine caller-cancel,
// even WITH a per-attempt timeout configured, still surfaces as context.Canceled
// (preserving the loop's StopCancelled / wedge-recovery path). This guards against
// an over-correction of the masking fix.
func TestGenuineCallerCancelSurfacesCanceled(t *testing.T) {
	f := &fakeProvider{steps: []step{{block: true}}}
	cfg := Config{MaxAttempts: 5, PerAttemptTimeout: time.Hour}
	p := Wrap(f, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := p.Stream(ctx, port.LLMRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1 (caller cancel not retried)", f.Calls())
	}
}

// TestPerAttemptDeadlineChainHasDeadlineExceeded asserts that a genuine
// single-attempt per-attempt timeout produces an error chain that still satisfies
// errors.Is(_, context.DeadlineExceeded) — the property DefaultClassifier keys on
// to keep per-attempt timeouts retryable.
func TestPerAttemptDeadlineChainHasDeadlineExceeded(t *testing.T) {
	f := &fakeProvider{steps: []step{{block: true}}}
	cfg := Config{MaxAttempts: 1, PerAttemptTimeout: 20 * time.Millisecond}
	p := Wrap(f, cfg)

	_, err := p.Stream(context.Background(), port.LLMRequest{})
	if err == nil {
		t.Fatalf("Stream returned nil, want a per-attempt deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err chain = %v, want errors.Is(_, context.DeadlineExceeded)", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("err masked as context.Canceled: %v", err)
	}
}

func TestBreakerOpensFailsFastHalfOpensRecovers(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("refused")}
	clk := &manualClock{t: time.Unix(1000, 0)}
	f := &fakeProvider{}
	// Program: keep failing until we flip it.
	failing := step{outerErr: conn}
	f.steps = []step{failing}

	cfg := Config{
		MaxAttempts:      1, // one attempt per Stream so each call is one failure
		BaseBackoff:      time.Nanosecond,
		MaxBackoff:       time.Nanosecond,
		BreakerThreshold: 3,
		BreakerCooldown:  30 * time.Second,
		Clock:            clk.Now,
	}
	p := Wrap(f, cfg)

	// Three consecutive failures open the breaker.
	for i := 0; i < 3; i++ {
		if _, err := p.Stream(context.Background(), port.LLMRequest{}); err == nil {
			t.Fatalf("attempt %d: want error", i)
		}
	}
	callsAtOpen := f.Calls()
	if callsAtOpen != 3 {
		t.Fatalf("inner called %d times, want 3", callsAtOpen)
	}

	// Breaker open: fails fast with *BreakerError, inner not called.
	_, err := p.Stream(context.Background(), port.LLMRequest{})
	var be *BreakerError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want *BreakerError", err)
	}
	if f.Calls() != callsAtOpen {
		t.Fatalf("inner called during open breaker (calls=%d)", f.Calls())
	}

	// Advance past cooldown; breaker half-opens and admits one trial. Make the
	// next inner call succeed so the breaker resets.
	f.mu.Lock()
	f.steps = []step{{chunks: textTurn("back")}}
	f.calls = 0 // restart the step cursor for clarity
	f.mu.Unlock()
	clk.Advance(31 * time.Second)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("half-open trial: unexpected error: %v", err)
	}
	if _, derr := drain(t, seq); derr != nil {
		t.Fatalf("half-open drain error: %v", derr)
	}
	if f.Calls() != 1 {
		t.Fatalf("half-open: inner called %d times, want 1", f.Calls())
	}

	// After recovery the breaker is closed: subsequent calls flow through. Drain
	// the returned stream so the inner pull coroutine is unwound (an undrained seq
	// leaves the first-chunk coroutine parked at its yield — caught by goleak).
	seq, err = p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("post-recovery Stream error: %v", err)
	}
	if _, derr := drain(t, seq); derr != nil {
		t.Fatalf("post-recovery drain error: %v", derr)
	}
}

func TestBreakerHalfOpenFailureReopens(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("refused")}
	clk := &manualClock{t: time.Unix(0, 0)}
	f := &fakeProvider{steps: []step{{outerErr: conn}}}
	cfg := Config{
		MaxAttempts:      1,
		BaseBackoff:      time.Nanosecond,
		MaxBackoff:       time.Nanosecond,
		BreakerThreshold: 2,
		BreakerCooldown:  10 * time.Second,
		Clock:            clk.Now,
	}
	p := Wrap(f, cfg)

	for i := 0; i < 2; i++ {
		_, _ = p.Stream(context.Background(), port.LLMRequest{})
	}
	// Open now: fast-fail.
	if _, err := p.Stream(context.Background(), port.LLMRequest{}); !errorsAsBreaker(err) {
		t.Fatalf("want breaker error while open, got %v", err)
	}
	// Half-open: cooldown elapses, the trial fails, breaker reopens.
	clk.Advance(11 * time.Second)
	callsBefore := f.Calls()
	if _, err := p.Stream(context.Background(), port.LLMRequest{}); err == nil {
		t.Fatalf("half-open trial should have failed")
	}
	if f.Calls() != callsBefore+1 {
		t.Fatalf("half-open should admit exactly one trial")
	}
	// Reopened: fast-fail again without calling inner.
	c := f.Calls()
	if _, err := p.Stream(context.Background(), port.LLMRequest{}); !errorsAsBreaker(err) {
		t.Fatalf("want breaker error after reopen, got %v", err)
	}
	if f.Calls() != c {
		t.Fatalf("inner called while reopened")
	}
}

// TestBreakerHalfOpenPermanentErrorStaysOpen pins the designed (IMPLEMENTATION-NOTES)
// behaviour of a half-open trial that fails with a PERMANENT error: such an error is
// breaker-neutral, so recordFailure is never reached for it. The breaker therefore
// stays in its post-cooldown open&halfOpen shape — it is NOT hard-reopened with a fresh
// cooldown (the half-open reopen branch), NOR reset. Consequently the next allow() after
// cooldown re-admits a trial that reaches inner and surfaces the next programmed step,
// rather than wedging behind a permanent *BreakerError lockout.
func TestBreakerHalfOpenPermanentErrorStaysOpen(t *testing.T) {
	clk := &manualClock{t: time.Unix(1000, 0)}
	// Step 0..2: transient burst opens the breaker. Step 3: the half-open trial fails
	// with a PERMANENT 400. Step 4: the next half-open trial succeeds, proving the
	// breaker kept admitting trials (no permanent lockout).
	f := &fakeProvider{steps: []step{
		{outerErr: apiErr(503)},
		{outerErr: apiErr(503)},
		{outerErr: apiErr(503)},
		{outerErr: apiErr(400)},
		{chunks: textTurn("recovered")},
	}}
	cfg := Config{
		MaxAttempts:      1, // one attempt per Stream, so each call is one establishment
		BaseBackoff:      time.Nanosecond,
		MaxBackoff:       time.Nanosecond,
		BreakerThreshold: 3,
		BreakerCooldown:  30 * time.Second,
		Clock:            clk.Now,
	}
	p := Wrap(f, cfg)

	// 1. Three transient failures open the breaker.
	for i := 0; i < 3; i++ {
		if _, err := p.Stream(context.Background(), port.LLMRequest{}); err == nil {
			t.Fatalf("burst attempt %d: want error", i)
		}
	}
	if f.Calls() != 3 {
		t.Fatalf("inner called %d times during burst, want 3", f.Calls())
	}
	// Breaker is open: fails fast without calling inner.
	if _, err := p.Stream(context.Background(), port.LLMRequest{}); !errorsAsBreaker(err) {
		t.Fatalf("want *BreakerError while open, got %v", err)
	}
	if f.Calls() != 3 {
		t.Fatalf("inner called while breaker open (calls=%d, want 3)", f.Calls())
	}

	// 2. Advance past cooldown so the next allow() admits a HALF-OPEN trial.
	clk.Advance(31 * time.Second)

	// 3. The half-open trial (step 3) returns a PERMANENT 400.
	_, err := p.Stream(context.Background(), port.LLMRequest{})

	// Assert: the 400 surfaces VERBATIM, NOT wrapped as a *BreakerError.
	if errorsAsBreaker(err) {
		t.Fatalf("half-open permanent trial returned *BreakerError, want the verbatim 400: %v", err)
	}
	var got400 *oai.Error
	if !errors.As(err, &got400) || got400.StatusCode != 400 {
		t.Fatalf("half-open trial err = %v, want verbatim 400 *oai.Error", err)
	}
	// The 400 is a permanent client-side rejection — assert the PermanentError bit.
	var pe port.PermanentError
	if !errors.As(err, &pe) || !pe.Permanent() {
		t.Fatalf("400 establishment error must carry port.PermanentError; err = %v", err)
	}
	if f.Calls() != 4 {
		t.Fatalf("half-open permanent trial: inner called %d times, want 4 (trial WAS admitted)", f.Calls())
	}

	// 4. WITHOUT advancing the clock further beyond cooldown again, the breaker keeps
	// admitting trials — the permanent error neither re-armed (hard-reopened with a
	// fresh cooldown) nor reset it. The next Stream therefore reaches inner (step 4)
	// and surfaces the programmed success, not a permanent *BreakerError lockout.
	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if errorsAsBreaker(err) {
		t.Fatalf("breaker wedged after half-open permanent error (fast-failed instead of admitting a trial): %v", err)
	}
	if err != nil {
		t.Fatalf("post-permanent-trial Stream error: %v", err)
	}
	gotChunks, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if len(gotChunks) == 0 || gotChunks[0].Text != "recovered" {
		t.Fatalf("got %+v, want textTurn(recovered)", gotChunks)
	}
	if f.Calls() != 5 {
		t.Fatalf("post-permanent-trial: inner called %d times, want 5 (trial admitted again)", f.Calls())
	}
}

func errorsAsBreaker(err error) bool {
	var be *BreakerError
	return errors.As(err, &be)
}

// TestBreakerCountsTransientNotPermanent is the unit table for the breaker-health
// predicate isTransientForBreaker, distinct from DefaultClassifier (see ~499):
// 408/429/5xx/net/timeout are transient and count; 409 (the deliberate
// divergence), all other 4xx, caller cancel, unknown and nil are breaker-neutral.
func TestBreakerCountsTransientNotPermanent(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"429 transient", apiErr(429), true},
		{"408 transient", apiErr(408), true},
		{"500 transient", apiErr(500), true},
		{"503 transient", apiErr(503), true},
		{"net error transient", &net.OpError{Op: "dial", Err: errors.New("refused")}, true},
		{"deadline exceeded transient", context.DeadlineExceeded, true},
		{"http2 stream error transient", &http2.StreamError{StreamID: 45, Code: http2.ErrCodeInternal}, true},
		{"http2 protocol error transient", &http2.StreamError{StreamID: 1, Code: http2.ErrCodeProtocol}, true},
		{"json syntax error transient", emptyJSONErr(), true},
		{"wrapped json syntax error transient", fmt.Errorf("agent: start stream: %w", emptyJSONErr()), true},
		{"unexpected EOF transient", io.ErrUnexpectedEOF, true},
		{"400 permanent", apiErr(400), false},
		{"401 permanent", apiErr(401), false},
		{"403 permanent", apiErr(403), false},
		{"404 permanent", apiErr(404), false},
		{"409 permanent for breaker (divergence)", apiErr(409), false},
		{"context canceled neutral", context.Canceled, false},
		{"unknown neutral", errors.New("mystery"), false},
		{"nil neutral", nil, false},
		{"context-overflow status 0 breaker-neutral", &statusErr{status: 0, msg: "response failed: server_error: Your input exceeds the context window of this model"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientForBreaker(tc.err); got != tc.want {
				t.Fatalf("isTransientForBreaker(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestBreakerOpensOnTruncationBurst is the P2 regression: a PERSISTENTLY
// truncated/malformed gateway response must DRIVE the shared breaker, not merely
// retry MaxAttempts on every call forever. With MaxAttempts:1, three consecutive
// truncation failures open the breaker; the fourth call fails fast with a
// *BreakerError without touching the inner provider.
func TestBreakerOpensOnTruncationBurst(t *testing.T) {
	clk := &manualClock{t: time.Unix(1000, 0)}
	f := &fakeProvider{steps: []step{{outerErr: emptyJSONErr()}}} // last step is reused → always fails
	cfg := Config{
		MaxAttempts:      1, // one attempt per Stream so each call is one breaker failure
		BaseBackoff:      time.Nanosecond,
		MaxBackoff:       time.Nanosecond,
		BreakerThreshold: 3,
		BreakerCooldown:  30 * time.Second,
		Clock:            clk.Now,
	}
	p := Wrap(f, cfg)

	for i := 0; i < 3; i++ {
		if _, err := p.Stream(context.Background(), port.LLMRequest{}); err == nil {
			t.Fatalf("attempt %d: want a truncation error", i)
		}
	}
	callsAtOpen := f.Calls()
	if callsAtOpen != 3 {
		t.Fatalf("inner called %d times, want 3 (each truncation must count toward the breaker)", callsAtOpen)
	}

	// Breaker now open: fail fast with *BreakerError, inner NOT called again.
	_, err := p.Stream(context.Background(), port.LLMRequest{})
	var be *BreakerError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want *BreakerError (persistent truncation must open the breaker)", err)
	}
	if f.Calls() != callsAtOpen {
		t.Fatalf("inner called %d times after breaker open, want %d", f.Calls(), callsAtOpen)
	}
}

// TestBreakerOpensOnTransientBurst asserts a burst of TRANSIENT establishment
// failures opens the shared breaker: after BreakerThreshold failures the next
// Stream fails fast with *BreakerError without calling inner.
func TestBreakerOpensOnTransientBurst(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"429 burst", apiErr(429)},
		{"503 burst", apiErr(503)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := &manualClock{t: time.Unix(1000, 0)}
			f := &fakeProvider{steps: []step{{outerErr: tc.err}}}
			cfg := Config{
				MaxAttempts:      1,
				BaseBackoff:      time.Nanosecond,
				MaxBackoff:       time.Nanosecond,
				BreakerThreshold: 3,
				BreakerCooldown:  30 * time.Second,
				Clock:            clk.Now,
			}
			p := Wrap(f, cfg)

			for i := 0; i < 3; i++ {
				if _, err := p.Stream(context.Background(), port.LLMRequest{}); err == nil {
					t.Fatalf("attempt %d: want error", i)
				}
			}
			callsAtOpen := f.Calls()
			if callsAtOpen != 3 {
				t.Fatalf("inner called %d times, want 3", callsAtOpen)
			}

			_, err := p.Stream(context.Background(), port.LLMRequest{})
			var be *BreakerError
			if !errors.As(err, &be) {
				t.Fatalf("4th Stream err = %v, want *BreakerError", err)
			}
			if f.Calls() != callsAtOpen {
				t.Fatalf("inner called during open breaker (calls=%d, want %d)", f.Calls(), callsAtOpen)
			}
		})
	}
}

// TestHTTP2StreamErrorRetried asserts an http2.StreamError (a peer RST_STREAM /
// INTERNAL_ERROR transport reset) is RETRIED by the resilience layer and counts
// toward the shared breaker — the issue #208 regression. Mirrors the shape of
// TestBreakerOpensOnTransientBurst: a burst of http2.StreamError failures opens
// the breaker, and the 4th Stream fails fast with *BreakerError without calling
// inner.
func TestHTTP2StreamErrorRetried(t *testing.T) {
	clk := &manualClock{t: time.Unix(1000, 0)}
	streamErr := &http2.StreamError{StreamID: 45, Code: http2.ErrCodeInternal}
	f := &fakeProvider{steps: []step{
		{outerErr: streamErr},
		{outerErr: streamErr},
		{chunks: textTurn("ok")},
	}}
	p := Wrap(f, tinyBackoffCfg(3))

	// First two attempts fail with the stream error and are retried; the third
	// succeeds — proving http2.StreamError is classified retryable (a
	// non-retryable error would surface after a single attempt).
	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream err = %v, want success after retry", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if len(got) == 0 || got[0].Text != "ok" {
		t.Fatalf("got %+v, want textTurn(works)", got)
	}
	if c := f.Calls(); c != 3 {
		t.Fatalf("inner called %d times, want 3 (2 retries + 1 success)", c)
	}

	// Now prove it counts toward the breaker: a fresh provider with a threshold
	// of 3, fed nothing but stream errors, must open after 3 failures and fail
	// fast on the 4th.
	f2 := &fakeProvider{steps: []step{{outerErr: streamErr}}}
	cfg := Config{
		MaxAttempts:      1,
		BaseBackoff:      time.Nanosecond,
		MaxBackoff:       time.Nanosecond,
		BreakerThreshold: 3,
		BreakerCooldown:  30 * time.Second,
		Clock:            clk.Now,
	}
	p2 := Wrap(f2, cfg)
	for i := 0; i < 3; i++ {
		if _, err := p2.Stream(context.Background(), port.LLMRequest{}); err == nil {
			t.Fatalf("attempt %d: want error", i)
		}
	}
	callsAtOpen := f2.Calls()
	if callsAtOpen != 3 {
		t.Fatalf("inner called %d times, want 3", callsAtOpen)
	}
	_, err = p2.Stream(context.Background(), port.LLMRequest{})
	var be *BreakerError
	if !errors.As(err, &be) {
		t.Fatalf("4th Stream err = %v, want *BreakerError", err)
	}
	if f2.Calls() != callsAtOpen {
		t.Fatalf("inner called during open breaker (calls=%d, want %d)", f2.Calls(), callsAtOpen)
	}
}

// TestBreakerDoesNotOpenOnPermanentErrors is the core regression guard for the
// live bug: a burst of breaker-NEUTRAL client errors must NOT trip the shared
// breaker, so a working model still flows after the burst. This covers the
// permanent 4xx (400/401/403/404, e.g. a policy-blocked or unavailable model) AND
// the 409 divergence: 409 is RETRYABLE per cfg.Classifier yet breaker-neutral per
// isTransientForBreaker (a request-conflict is not provider-unhealthy), so a 409
// burst must likewise leave the breaker closed even though it is retryable. With
// MaxAttempts:1 each 409 surfaces in a single attempt like the other rows. This
// row goes red if 409 were added to the breaker's transient set.
func TestBreakerDoesNotOpenOnPermanentErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"400 burst", apiErr(400)},
		{"401 burst", apiErr(401)},
		{"403 burst", apiErr(403)},
		{"404 burst", apiErr(404)},
		{"409 burst (retryable but breaker-neutral)", apiErr(409)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := &manualClock{t: time.Unix(1000, 0)}
			f := &fakeProvider{steps: []step{{outerErr: tc.err}}}
			cfg := Config{
				MaxAttempts:      1,
				BaseBackoff:      time.Nanosecond,
				MaxBackoff:       time.Nanosecond,
				BreakerThreshold: 3,
				BreakerCooldown:  30 * time.Second,
				Clock:            clk.Now,
			}
			p := Wrap(f, cfg)

			// More than the threshold's worth of permanent errors.
			for i := 0; i < 5; i++ {
				if _, err := p.Stream(context.Background(), port.LLMRequest{}); err == nil {
					t.Fatalf("attempt %d: want error", i)
				}
			}
			callsAfterBurst := f.Calls()
			if callsAfterBurst != 5 {
				t.Fatalf("inner called %d times during burst, want 5", callsAfterBurst)
			}

			// Flip to a working model; it must succeed — the breaker stayed closed.
			f.mu.Lock()
			f.steps = []step{{chunks: textTurn("works")}}
			f.mu.Unlock()

			seq, err := p.Stream(context.Background(), port.LLMRequest{})
			if errorsAsBreaker(err) {
				t.Fatalf("working model blocked by breaker after permanent-error burst: %v", err)
			}
			if err != nil {
				t.Fatalf("working model Stream error: %v", err)
			}
			got, derr := drain(t, seq)
			if derr != nil {
				t.Fatalf("drain error: %v", derr)
			}
			if len(got) == 0 || got[0].Text != "works" {
				t.Fatalf("got %+v, want textTurn(works)", got)
			}
			if f.Calls() != callsAfterBurst+1 {
				t.Fatalf("working model: inner called %d times, want %d (inner WAS called)", f.Calls(), callsAfterBurst+1)
			}
		})
	}
}

// TestContextOverflowNotRetriedAndBreakerStaysClosed is the end-to-end
// regression guard for issue #207: a context-window-overflow error (now mapped
// to HTTP status 0 by the openai adapter) must be surfaced after a SINGLE
// attempt — NOT retried — and must NOT count toward the circuit breaker, so a
// subsequent working request still flows. This mirrors the live failure: under
// the old 503 mapping, the over-context prompt was replayed MaxAttempts times
// (identically failing each time) AND each failure fed the breaker until it
// wedged open. With status 0, DefaultClassifier returns false (no retry) and
// isTransientForBreaker returns false (breaker-neutral) — both pinned here.
func TestContextOverflowNotRetriedAndBreakerStaysClosed(t *testing.T) {
	clk := &manualClock{t: time.Unix(1000, 0)}
	overflowErr := &statusErr{
		status: 0,
		msg:    "response failed: server_error: Your input exceeds the context window of this model. Please adjust your input and try again.",
	}
	f := &fakeProvider{steps: []step{{outerErr: overflowErr}}}
	cfg := Config{
		MaxAttempts:      5, // a retry WOULD happen if misclassified as retryable
		BaseBackoff:      time.Nanosecond,
		MaxBackoff:       time.Nanosecond,
		BreakerThreshold: 3,
		BreakerCooldown:  30 * time.Second,
		Clock:            clk.Now,
	}
	p := Wrap(f, cfg)

	// A burst beyond the breaker threshold: each Stream must surface the
	// overflow error after a SINGLE attempt (no retry), and none may count
	// toward the breaker.
	for i := 0; i < 5; i++ {
		_, err := p.Stream(context.Background(), port.LLMRequest{})
		if err == nil {
			t.Fatalf("attempt %d: want the overflow error, got nil", i)
		}
		if !strings.Contains(err.Error(), "context window") {
			t.Fatalf("attempt %d: error %q does not carry the overflow message", i, err.Error())
		}
		if errorsAsBreaker(err) {
			t.Fatalf("attempt %d: overflow error surfaced as *BreakerError (breaker opened — it must be breaker-neutral): %v", i, err)
		}
	}
	// Exactly one inner call per Stream (5 bursts × 1 attempt each = 5). If the
	// error were misclassified retryable (status 5xx), each Stream would burn
	// MaxAttempts=5 attempts → 25 inner calls.
	if got := f.Calls(); got != 5 {
		t.Fatalf("inner called %d times across 5 overflow Streams, want 5 (no retry per Stream)", got)
	}

	// Flip to a working model; it must succeed — the breaker stayed closed.
	f.mu.Lock()
	f.steps = []step{{chunks: textTurn("works")}}
	f.mu.Unlock()

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if errorsAsBreaker(err) {
		t.Fatalf("working model blocked by breaker after overflow burst: %v", err)
	}
	if err != nil {
		t.Fatalf("working model Stream error: %v", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if len(got) == 0 || got[0].Text != "works" {
		t.Fatalf("got %+v, want textTurn(works)", got)
	}
	if f.Calls() != 6 {
		t.Fatalf("working model: inner called %d times, want 6 (one more for the working Stream)", f.Calls())
	}
}

// TestBreakerNeutralOnCallerCancel asserts caller cancellations are
// breaker-neutral: even more cancels than the threshold never open the breaker,
// and a subsequent working step succeeds.
func TestBreakerNeutralOnCallerCancel(t *testing.T) {
	clk := &manualClock{t: time.Unix(1000, 0)}
	f := &fakeProvider{steps: []step{{block: true}}}
	cfg := Config{
		MaxAttempts:      1,
		BaseBackoff:      time.Nanosecond,
		MaxBackoff:       time.Nanosecond,
		BreakerThreshold: 3,
		BreakerCooldown:  30 * time.Second,
		Clock:            clk.Now,
	}
	p := Wrap(f, cfg)

	// Exceed the threshold's worth of caller-cancels.
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()
		_, err := p.Stream(ctx, port.LLMRequest{})
		if errorsAsBreaker(err) {
			t.Fatalf("cancel %d returned *BreakerError: %v", i, err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel %d err = %v, want context.Canceled", i, err)
		}
		cancel()
	}

	// A subsequent working step succeeds — the breaker never opened.
	f.mu.Lock()
	f.steps = []step{{chunks: textTurn("works")}}
	f.mu.Unlock()

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if errorsAsBreaker(err) {
		t.Fatalf("working step blocked by breaker after caller-cancel burst: %v", err)
	}
	if err != nil {
		t.Fatalf("working step Stream error: %v", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if len(got) == 0 || got[0].Text != "works" {
		t.Fatalf("got %+v, want textTurn(works)", got)
	}
}

// apiErr builds an *oai.Error with enough populated for its Error() method to
// render without dereferencing nil Request/Response.
func apiErr(code int) *oai.Error {
	req, _ := http.NewRequest(http.MethodPost, "https://api.example/v1/responses", nil)
	return &oai.Error{
		StatusCode: code,
		Request:    req,
		Response:   &http.Response{StatusCode: code},
	}
}

// statusErr is a test stub for any error that carries an HTTP-status equivalent
// via the interface{ StatusCode() int } contract — the same contract the real
// *openai.responseStreamError (unexported, cross-package) satisfies and the one
// DefaultClassifier/isTransientForBreaker consume via errors.As. Using a local
// stub avoids importing the openai package into llmresilience tests while
// faithfully exercising the classifier's interface path. status 0 models the
// context-overflow classification from issue #207.
type statusErr struct {
	status int
	msg    string
}

func (e *statusErr) Error() string   { return e.msg }
func (e *statusErr) StatusCode() int { return e.status }

// emptyJSONErr returns the *json.SyntaxError the stdlib produces for empty input
// — the exact shape the openai-go ssestream decoder yields on a truncated SSE
// frame (json.Unmarshal(data, &chunk)), which is what surfaced as the occasional
// "agent: start stream: unexpected end of JSON input".
func emptyJSONErr() error {
	var v map[string]any
	return json.Unmarshal([]byte(""), &v)
}

func TestDefaultClassifier(t *testing.T) {
	mk := func(code int) error { return apiErr(code) }
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"500 retryable", mk(500), true},
		{"503 retryable", mk(503), true},
		{"429 retryable", mk(429), true},
		{"408 retryable", mk(408), true},
		{"409 retryable", mk(409), true},
		{"400 not", mk(400), false},
		{"401 not", mk(401), false},
		{"404 not", mk(404), false},
		{"connection error retryable", &net.OpError{Op: "dial", Err: errors.New("refused")}, true},
		{"deadline exceeded retryable", context.DeadlineExceeded, true},
		{"http2 stream error retryable", &http2.StreamError{StreamID: 45, Code: http2.ErrCodeInternal}, true},
		{"wrapped http2 stream error retryable", fmt.Errorf("x: %w", &http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}), true},
		{"context canceled not", context.Canceled, false},
		{"wrapped canceled not", fmt.Errorf("x: %w", context.Canceled), false},
		{"unknown not", errors.New("mystery"), false},
		{"nil not", nil, false},
		{"context-overflow status 0 not retryable", &statusErr{status: 0, msg: "response failed: server_error: Your input exceeds the context window of this model"}, false},
		// A truncated/malformed payload (partial SSE frame, or empty gateway error
		// body whose status the SDK discarded) is a transient truncation, retryable.
		{"json syntax error retryable", emptyJSONErr(), true},
		{"wrapped json syntax error retryable", fmt.Errorf("agent: start stream: %w", emptyJSONErr()), true},
		{"unexpected EOF retryable", io.ErrUnexpectedEOF, true},
		{"wrapped unexpected EOF retryable", fmt.Errorf("x: %w", io.ErrUnexpectedEOF), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultClassifier(tc.err); got != tc.want {
				t.Fatalf("DefaultClassifier(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestNonRetryableNotRetried(t *testing.T) {
	f := &fakeProvider{steps: []step{{outerErr: apiErr(400)}}}
	p := Wrap(f, tinyBackoffCfg(5))
	_, err := p.Stream(context.Background(), port.LLMRequest{})
	var apiErr *oai.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		t.Fatalf("err = %v, want 400 surfaced", err)
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1 (400 not retried)", f.Calls())
	}
}

func TestEmptyStreamIsSuccess(t *testing.T) {
	f := &fakeProvider{steps: []step{{}}} // no chunks, no error: empty stream
	p := Wrap(f, tinyBackoffCfg(3))
	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if len(got) != 0 {
		t.Fatalf("got %d chunks, want 0", len(got))
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1", f.Calls())
	}
}

func TestBackoffDurationRespectsMaxAndJitter(t *testing.T) {
	p := &resilientProvider{cfg: Config{BaseBackoff: 100 * time.Millisecond, MaxBackoff: 1 * time.Second}}
	for attempt := 0; attempt < 10; attempt++ {
		d := p.backoffDuration(attempt)
		if d <= 0 || d > p.cfg.MaxBackoff {
			t.Fatalf("attempt %d: backoff %s out of (0, %s]", attempt, d, p.cfg.MaxBackoff)
		}
	}
}

// TestCapabilitiesForwarded asserts the resilience decorator returns the wrapped
// provider's capabilities verbatim (it adds retries/breaker only, never alters
// what input the provider consumes).
func TestCapabilitiesForwarded(t *testing.T) {
	want := port.ProviderCapabilities{Image: true, Audio: false, EmbeddedContext: true}
	inner := &fakeProvider{caps: want}
	wrapped := Wrap(inner, Config{MaxAttempts: 1})
	if got := wrapped.Capabilities(); got != want {
		t.Fatalf("Capabilities() = %+v, want %+v", got, want)
	}
}

// multiTurnChunks returns n ChunkText chunks followed by a usage + ChunkDone, so
// a fake can stream n+2 chunks total. Used by the deadline-detach regression tests.
func multiTurnChunks(n int) []port.Chunk {
	cs := make([]port.Chunk, 0, n+2)
	for i := 0; i < n; i++ {
		cs = append(cs, port.Chunk{Kind: port.ChunkText, Text: "tok"})
	}
	cs = append(cs,
		port.Chunk{Kind: port.ChunkUsage, Usage: &session.Usage{}},
		port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	)
	return cs
}

// TestStreamNotTruncatedAtPerAttemptDeadline is the core regression guard: with a
// short PerAttemptTimeout, an actively-streaming turn whose chunks cross that wall-
// clock deadline must run to completion (the per-attempt budget bounds ONLY
// establishment + the first chunk, enforced by a separate timer stopped at the
// first chunk — never an absolute ctx deadline live through the whole stream).
//
// On the OLD code (context.WithTimeout) the inner reads ride an absolute deadline
// that fires mid-stream; the adapter swallows the ctx error and restSeq returns on
// !ok → the turn is silently truncated (no ChunkDone). This test FAILS there and
// passes after the fix.
func TestStreamNotTruncatedAtPerAttemptDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		idle time.Duration
	}{
		{"idle-disabled", 0},
		{"idle-large", 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// First chunk immediately, then ~6 more spaced 15ms apart (~90ms total),
			// well past the 40ms per-attempt budget but each gap under the idle budget.
			f := &fakeProvider{steps: []step{{
				chunks:        multiTurnChunks(6),
				chunkInterval: 15 * time.Millisecond,
			}}}
			cfg := Config{
				MaxAttempts:       1,
				PerAttemptTimeout: 40 * time.Millisecond,
				StreamIdleTimeout: tc.idle,
			}
			p := Wrap(f, cfg)

			seq, err := p.Stream(context.Background(), port.LLMRequest{})
			if err != nil {
				t.Fatalf("Stream returned error: %v", err)
			}
			got, derr := drain(t, seq)
			if derr != nil {
				t.Fatalf("drain error: %v — an actively-streaming turn was cut at the per-attempt deadline (regression)", derr)
			}
			// 6 text + usage + done = 8 chunks; the turn must be complete (ends in Done).
			if len(got) != 8 {
				t.Fatalf("got %d chunks, want 8 (turn truncated at the per-attempt deadline)", len(got))
			}
			if last := got[len(got)-1]; last.Kind != port.ChunkDone {
				t.Fatalf("last chunk kind = %v, want ChunkDone (turn did not complete)", last.Kind)
			}
		})
	}
}

// TestEstablishmentTimeoutStillFiresAndRetryable asserts the per-attempt budget
// still bounds ESTABLISHMENT: an attempt that never produces a first chunk times
// out, is retried, and surfaces as a retryable DeadlineExceeded wrapping
// errFirstChunkTimeout (classification identical to the old absolute-deadline path).
func TestEstablishmentTimeoutStillFiresAndRetryable(t *testing.T) {
	// Both attempts block forever (until their establishment timer cancels them).
	f := &fakeProvider{steps: []step{{block: true}, {block: true}}}
	cfg := Config{
		MaxAttempts:       2,
		BaseBackoff:       time.Nanosecond,
		MaxBackoff:        time.Nanosecond,
		PerAttemptTimeout: 30 * time.Millisecond,
	}
	p := Wrap(f, cfg)

	_, err := p.Stream(context.Background(), port.LLMRequest{})
	if err == nil {
		t.Fatal("Stream should fail when establishment never produces a first chunk")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err is not DeadlineExceeded: %v", err)
	}
	if !errors.Is(err, errFirstChunkTimeout) {
		t.Errorf("err does not wrap errFirstChunkTimeout: %v", err)
	}
	if f.Calls() != 2 {
		t.Errorf("inner called %d times, want 2 (establishment timeout must be retried)", f.Calls())
	}
}

// TestEstablishmentTimerStoppedAfterFirstChunk proves the establishment timer is
// stopped the instant the first chunk is in hand: the first chunk arrives at t=0,
// the second ~80ms later (past the 30ms per-attempt budget). If the timer were not
// stopped it would fire at 30ms and cancel the stream mid-flight; instead the turn
// completes cleanly.
func TestEstablishmentTimerStoppedAfterFirstChunk(t *testing.T) {
	f := &fakeProvider{steps: []step{{
		chunks:        multiTurnChunks(2), // first immediate, second after the interval
		chunkInterval: 80 * time.Millisecond,
	}}}
	cfg := Config{
		MaxAttempts:       1,
		PerAttemptTimeout: 30 * time.Millisecond,
	}
	p := Wrap(f, cfg)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v — establishment timer fired after the first chunk (not stopped)", derr)
	}
	if len(got) != 4 { // 2 text + usage + done
		t.Fatalf("got %d chunks, want 4 (turn truncated — timer fired post-first-chunk)", len(got))
	}
	if last := got[len(got)-1]; last.Kind != port.ChunkDone {
		t.Fatalf("last chunk kind = %v, want ChunkDone", last.Kind)
	}
}

// (The genuinely-empty-stream-with-a-generous-per-attempt-budget success path —
// zero chunks, timer never fires, parent live, no misclassification — is covered by
// the sibling TestGenuinelyEmptyStreamStaysSuccessAfterFix above; no separate test
// is duplicated for it here.)

// TestEstablishmentTimerLateFireDoesNotTruncate is the DETERMINISTIC guard for the
// late-fire race on the success path: the establishment timer can fire in the
// narrow window between pulling the first chunk and stopping the timer. When it
// does, it has already called cancel() — so attemptCtx is dead and the streaming
// phase (restSeq) cannot proceed; a naive success path would build restSeq on the
// dead context and the first rest-read would yield nothing → SILENT TRUNCATION (a
// partial chunk list with no ChunkDone and no error). establish() must instead
// detect the fired timer (stopEstTimer reporting fired==true) and treat it as a
// retryable establishment timeout.
//
// Determinism: firstChunkAfterCancel makes the fake wait for ctx.Done() BEFORE
// yielding the first chunk, so the timer is GUARANTEED to have fired (and called
// cancel) before establish() ever has a first chunk in hand — exercising exactly
// the window. With MaxAttempts:2 the second attempt streams normally and the call
// completes cleanly; the assertion is the disjunction the finding requires: EITHER
// a clean complete turn OR a surfaced errFirstChunkTimeout — but NEVER a truncated
// stream (chunks without a terminal ChunkDone and without an error).
//
// Against the pre-fix code (success path that ignored a late fire) the first
// attempt would build restSeq on the cancelled ctx and drain would return zero
// chunks with no error and no Done — failing the "never truncated" assertion below.
func TestEstablishmentTimerLateFireDoesNotTruncate(t *testing.T) {
	f := &fakeProvider{steps: []step{
		// Attempt 1: first chunk only becomes available AFTER the timer fires cancel;
		// chunkInterval makes every SUBSEQUENT read observe ctx.Done() (a real adapter
		// unblocks on cancel and yields nothing), so on the dead post-cancel ctx the
		// stream truncates after the buffered first chunk — the regression shape.
		{chunks: multiTurnChunks(2), firstChunkAfterCancel: true, chunkInterval: time.Millisecond},
		// Attempt 2 (the retry): streams normally to completion.
		{chunks: multiTurnChunks(2)},
	}}
	cfg := Config{
		MaxAttempts:       2,
		BaseBackoff:       time.Nanosecond,
		MaxBackoff:        time.Nanosecond,
		PerAttemptTimeout: 20 * time.Millisecond, // fires while attempt 1 is parked
	}
	p := Wrap(f, cfg)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		// Acceptable outcome: surfaced as a retryable establishment timeout.
		if !errors.Is(err, errFirstChunkTimeout) {
			t.Fatalf("Stream error = %v, want nil or errFirstChunkTimeout", err)
		}
		return
	}
	// Otherwise it must be a COMPLETE turn — never a truncated one.
	got, derr := drain(t, seq)
	if derr != nil {
		if !errors.Is(derr, errFirstChunkTimeout) {
			t.Fatalf("drain error = %v, want nil or errFirstChunkTimeout", derr)
		}
		return
	}
	if len(got) == 0 || got[len(got)-1].Kind != port.ChunkDone {
		t.Fatalf("got %d chunks ending in %v — late-fire produced a TRUNCATED stream (no ChunkDone, no error)",
			len(got), lastKind(got))
	}
}

func lastKind(cs []port.Chunk) any {
	if len(cs) == 0 {
		return "<none>"
	}
	return cs[len(cs)-1].Kind
}

// TestIsCommittingPredicate is the table test for the isCommitting predicate
// over all 7 ChunkKind values.
func TestIsCommittingPredicate(t *testing.T) {
	cases := []struct {
		kind port.ChunkKind
		want bool
	}{
		{port.ChunkText, true},
		{port.ChunkReasoning, false},
		{port.ChunkReasoningItem, false},
		{port.ChunkToolCall, true},
		{port.ChunkUsage, true},
		{port.ChunkDone, true},
		{port.ChunkPhase, true},
	}
	for _, tc := range cases {
		if got := isCommitting(tc.kind); got != tc.want {
			t.Errorf("isCommitting(%v) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// TestPreCommitReasoningOnlyErrorIsRetried verifies that a mid-reasoning error
// (before any committing chunk) is retried: attempt 1 yields ChunkReasoning
// then ChunkReasoningItem then a retryable net error; attempt 2 succeeds.
func TestPreCommitReasoningOnlyErrorIsRetried(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("refused")}
	reasoningChunks := []port.Chunk{
		{Kind: port.ChunkReasoning, Text: "thinking..."},
		{Kind: port.ChunkReasoningItem, Text: "blob"},
	}
	f := &fakeProvider{steps: []step{
		{chunks: reasoningChunks, midErr: conn},
		{chunks: textTurn("ok")},
	}}
	p := Wrap(f, tinyBackoffCfg(3))

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if f.Calls() != 2 {
		t.Fatalf("inner called %d times, want 2 (pre-commit error must be retried)", f.Calls())
	}
	// The successful attempt's chunks must be present (step 2: textTurn("ok")).
	if len(got) == 0 || got[0].Text != "ok" {
		t.Fatalf("got %+v, want textTurn(ok)", got)
	}
}

// TestPostCommitErrorNotRetried is the mutation guard: a retryable error that
// arrives AFTER a committing ChunkText must NOT be retried. If isCommitting were
// removed, the error would be treated as a pre-commit failure and retried (f.Calls()
// would exceed 1), which would make this test fail.
func TestPostCommitErrorNotRetried(t *testing.T) {
	boom := &net.OpError{Op: "dial", Err: errors.New("transport blew up")}
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkText, Text: "partial"}}, midErr: boom},
		{chunks: textTurn("should-not-be-used")},
	}}
	p := Wrap(f, tinyBackoffCfg(3))

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream outer error: %v", err)
	}
	got, derr := drain(t, seq)
	if !errors.Is(derr, boom) {
		t.Fatalf("drain error = %v, want boom", derr)
	}
	if len(got) != 1 || got[0].Text != "partial" {
		t.Fatalf("got %+v, want one 'partial' chunk before error", got)
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1 (no replay after committing chunk)", f.Calls())
	}
}

// TestMixedPreCommitThenCommittingThenErrorNotRetried asserts a mixed stream:
// non-committing chunks followed by a committing chunk followed by a retryable
// error — the committing chunk was already emitted so the error must NOT be retried.
func TestMixedPreCommitThenCommittingThenErrorNotRetried(t *testing.T) {
	boom := &net.OpError{Op: "dial", Err: errors.New("mid-stream failure")}
	f := &fakeProvider{steps: []step{
		{
			chunks: []port.Chunk{
				{Kind: port.ChunkReasoning, Text: "r"},
				{Kind: port.ChunkReasoningItem, Text: "b"},
				{Kind: port.ChunkText, Text: "partial"},
			},
			midErr: boom,
		},
		{chunks: textTurn("should-not-be-used")},
	}}
	p := Wrap(f, tinyBackoffCfg(3))

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream outer error: %v", err)
	}
	got, derr := drain(t, seq)
	if !errors.Is(derr, boom) {
		t.Fatalf("drain error = %v, want boom", derr)
	}
	// 3 chunks before the error: ChunkReasoning, ChunkReasoningItem, ChunkText.
	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3", len(got))
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1 (no retry after committing chunk)", f.Calls())
	}
}

// TestPreCommitChunksReplayedBeforeCommittingChunk asserts the pre-commit buffer
// is replayed in order before the first committing chunk.
func TestPreCommitChunksReplayedBeforeCommittingChunk(t *testing.T) {
	f := &fakeProvider{steps: []step{{
		chunks: []port.Chunk{
			{Kind: port.ChunkReasoning, Text: "r1"},
			{Kind: port.ChunkReasoningItem, Text: "b1"},
			{Kind: port.ChunkText, Text: "t1"},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		},
	}}}
	p := Wrap(f, Config{MaxAttempts: 1})

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if len(got) != 4 {
		t.Fatalf("got %d chunks, want 4", len(got))
	}
	if got[0].Kind != port.ChunkReasoning || got[0].Text != "r1" {
		t.Errorf("got[0] = %+v, want ChunkReasoning r1", got[0])
	}
	if got[1].Kind != port.ChunkReasoningItem || got[1].Text != "b1" {
		t.Errorf("got[1] = %+v, want ChunkReasoningItem b1", got[1])
	}
	if got[2].Kind != port.ChunkText || got[2].Text != "t1" {
		t.Errorf("got[2] = %+v, want ChunkText t1", got[2])
	}
	if got[3].Kind != port.ChunkDone {
		t.Errorf("got[3] = %+v, want ChunkDone", got[3])
	}
}

// TestPreCommitReasoningErrorExhaustsToExhaustedError asserts that when every
// attempt yields only reasoning chunks then a retryable error, the final error
// is *ExhaustedError with Attempts == MaxAttempts.
func TestPreCommitReasoningErrorExhaustsToExhaustedError(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("refused")}
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkReasoning, Text: "thinking"}}, midErr: conn},
	}}
	p := Wrap(f, tinyBackoffCfg(3))

	_, err := p.Stream(context.Background(), port.LLMRequest{})
	var ex *ExhaustedError
	if !errors.As(err, &ex) {
		t.Fatalf("err = %v, want *ExhaustedError", err)
	}
	if ex.Attempts != 3 {
		t.Fatalf("Attempts = %d, want 3", ex.Attempts)
	}
	if f.Calls() != 3 {
		t.Fatalf("inner called %d times, want 3", f.Calls())
	}
}

// TestPerAttemptTimeoutDuringPreCommitIsRetriedNotTruncated asserts that the
// per-attempt timer fires while the establish loop is blocked waiting for the
// first committing chunk (i.e., the provider is in its reasoning prefix and
// never yields a committing chunk within the budget). The result must be a
// clean retry — not truncation, not a phantom empty success.
//
// Setup: step 1 blocks before yielding any chunk (block:true); step 2 yields a
// full valid turn. The establishment timer fires during the blocked first-chunk
// read (same shape as TestPreFirstChunkStallStillUsesPerAttemptTimeout, but
// from the perspective of the pre-commit reasoning-prefix path rather than the
// outer-ctx-block path).
func TestPerAttemptTimeoutDuringPreCommitIsRetriedNotTruncated(t *testing.T) {
	f := &fakeProvider{steps: []step{
		{block: true},            // blocks before ANY chunk → per-attempt timer fires
		{chunks: textTurn("ok")}, // step 2: full valid turn
	}}
	cfg := Config{
		MaxAttempts:       2,
		BaseBackoff:       time.Nanosecond,
		MaxBackoff:        time.Nanosecond,
		PerAttemptTimeout: 20 * time.Millisecond,
	}
	p := Wrap(f, cfg)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream returned error: %v (want nil — timeout during pre-commit must retry, not surface)", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	// Step 2 is textTurn("ok"): text + usage + done = 3 chunks.
	if len(got) == 0 || got[0].Text != "ok" {
		t.Fatalf("got %+v, want textTurn(ok) from step 2", got)
	}
	if f.Calls() != 2 {
		t.Fatalf("inner called %d times, want 2 (pre-commit timeout retried once)", f.Calls())
	}
}

// TestPreCommitOnlyStreamCleanClose pins the behaviour when a stream yields
// only non-committing (reasoning) chunks and then ends cleanly — no error, no
// ChunkDone. With the pre-commit-forward fix the chunks are NOT silently
// discarded; they are forwarded to the caller. MaxAttempts=1 so no retry.
//
// This pins the current behaviour so a regression (panic, hang, or silent
// discard) is caught.
func TestPreCommitOnlyStreamCleanClose(t *testing.T) {
	reasoningChunks := []port.Chunk{
		{Kind: port.ChunkReasoning, Text: "r"},
		{Kind: port.ChunkReasoningItem, Text: "b"},
	}
	f := &fakeProvider{steps: []step{
		{chunks: reasoningChunks}, // yields 2 non-committing chunks then ends cleanly
	}}
	p := Wrap(f, Config{MaxAttempts: 1})

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	got, derr := drain(t, seq)
	if derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	// With the pre-commit-forward fix the reasoning chunks are forwarded.
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2 (reasoning chunks must be forwarded on a clean pre-commit close)", len(got))
	}
	if got[0].Kind != port.ChunkReasoning || got[0].Text != "r" {
		t.Errorf("got[0] = %+v, want ChunkReasoning r", got[0])
	}
	if got[1].Kind != port.ChunkReasoningItem || got[1].Text != "b" {
		t.Errorf("got[1] = %+v, want ChunkReasoningItem b", got[1])
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1 (no retry on a clean pre-commit-only close)", f.Calls())
	}
}

// TestPermanentError400Establishment asserts a 400 establishment error (non-retryable,
// non-transient) carries the port.PermanentError signal via errors.As so callers can
// distinguish a permanent client-side rejection from a transient failure.
func TestPermanentError400Establishment(t *testing.T) {
	f := &fakeProvider{steps: []step{{outerErr: apiErr(400)}}}
	p := Wrap(f, tinyBackoffCfg(3))
	_, err := p.Stream(context.Background(), port.LLMRequest{})

	// The underlying *oai.Error is still reachable through Unwrap.
	var apiErr *oai.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		t.Fatalf("err = %v, want 400 *oai.Error still reachable via errors.As", err)
	}
	// The PermanentError bit is set.
	var pe port.PermanentError
	if !errors.As(err, &pe) || !pe.Permanent() {
		t.Fatalf("400 establishment error must carry port.PermanentError; err = %v", err)
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1 (400 not retried)", f.Calls())
	}
}

// TestPermanentErrorAbsentFor429 asserts a 429 (retryable) does NOT carry the
// port.PermanentError signal: a rate limit is transient, not a permanent rejection.
func TestPermanentErrorAbsentFor429(t *testing.T) {
	f := &fakeProvider{steps: []step{{outerErr: apiErr(429)}}}
	p := Wrap(f, Config{MaxAttempts: 1, BaseBackoff: time.Nanosecond})
	_, err := p.Stream(context.Background(), port.LLMRequest{})

	var pe port.PermanentError
	if errors.As(err, &pe) {
		t.Fatalf("429 must NOT carry port.PermanentError; err = %v", err)
	}
	var apiErr *oai.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 {
		t.Fatalf("err = %v, want 429 *oai.Error surfaced verbatim", err)
	}
}

// TestPermanentErrorAbsentForCallerCancel asserts a caller-cancelled ctx does NOT
// carry the port.PermanentError signal.
func TestPermanentErrorAbsentForCallerCancel(t *testing.T) {
	f := &fakeProvider{steps: []step{{block: true}}}
	cfg := Config{MaxAttempts: 5, PerAttemptTimeout: time.Hour}
	p := Wrap(f, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := p.Stream(ctx, port.LLMRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var pe port.PermanentError
	if errors.As(err, &pe) {
		t.Fatalf("caller cancel must NOT carry port.PermanentError; err = %v", err)
	}
}

// TestPermanentErrorUnwrapIntact asserts the Unwrap chain of permanentError is
// intact: errors.As reaches the underlying typed error.
func TestPermanentErrorUnwrapIntact(t *testing.T) {
	f := &fakeProvider{steps: []step{{outerErr: apiErr(400)}}}
	p := Wrap(f, tinyBackoffCfg(3))
	_, err := p.Stream(context.Background(), port.LLMRequest{})

	// The specific *oai.Error must still be reachable.
	var apiErr *oai.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		t.Fatalf("errors.As(*oai.Error) must still work through the permanentError wrapper; err = %v", err)
	}
	// errors.Is must also work.
	if !errors.Is(err, apiErr) {
		t.Fatalf("errors.Is must still work through the permanentError wrapper; err = %v", err)
	}
}

// TestPermanentErrorMidStream400 asserts a mid-stream 400 (after the first
// committing chunk) carries port.PermanentError. Mid-stream errors are always
// terminal (no replay after first chunk), but a client-side rejection must
// still be distinguishable from a transient mid-stream failure.
func TestPermanentErrorMidStream400(t *testing.T) {
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkText, Text: "partial"}}, midErr: apiErr(400)},
	}}
	p := Wrap(f, Config{MaxAttempts: 1})

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream returned outer error: %v", err)
	}
	_, derr := drain(t, seq)
	if derr == nil {
		t.Fatal("expected a mid-stream error")
	}
	var pe port.PermanentError
	if !errors.As(derr, &pe) || !pe.Permanent() {
		t.Fatalf("mid-stream 400 must carry port.PermanentError; err = %v", derr)
	}
	var apiErr *oai.Error
	if !errors.As(derr, &apiErr) || apiErr.StatusCode != 400 {
		t.Fatalf("mid-stream 400 *oai.Error must still be reachable via errors.As; err = %v", derr)
	}
	if f.Calls() != 1 {
		t.Fatalf("inner called %d times, want 1 (400 mid-stream not retried)", f.Calls())
	}
}

// TestPermanentErrorMidStream429Absent asserts a mid-stream 429 (transient) does
// NOT carry port.PermanentError: a rate limit is not a permanent rejection even
// when it surfaces mid-stream.
func TestPermanentErrorMidStream429Absent(t *testing.T) {
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkText, Text: "partial"}}, midErr: apiErr(429)},
	}}
	p := Wrap(f, Config{MaxAttempts: 1})

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream returned outer error: %v", err)
	}
	_, derr := drain(t, seq)
	if derr == nil {
		t.Fatal("expected a mid-stream error")
	}
	var pe port.PermanentError
	if errors.As(derr, &pe) {
		t.Fatalf("mid-stream 429 must NOT carry port.PermanentError; err = %v", derr)
	}
	var apiErr *oai.Error
	if !errors.As(derr, &apiErr) || apiErr.StatusCode != 429 {
		t.Fatalf("mid-stream 429 *oai.Error must still be reachable; err = %v", derr)
	}
}
