package llmresilience

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
)

// diagRecord is one captured diagnostics line.
type diagRecord struct {
	level port.Level
	msg   string
	args  []any
}

// recordingDiag is a slice-recording port.Diagnostics test double. It is
// concurrency-safe because the idle-watchdog path logs from the rest-loop while
// the caller drains on another goroutine.
type recordingDiag struct {
	mu      sync.Mutex
	records []diagRecord
}

func (d *recordingDiag) Log(_ context.Context, level port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, diagRecord{level: level, msg: msg, args: args})
}

// With returns a child recorder that prepends bound args to every record,
// modelling the real slog-backed sink (so a provider-tagged sink — as wired in
// internal/app/registry.go — carries the provider key onto every line).
func (d *recordingDiag) With(args ...any) port.Diagnostics {
	return &boundDiag{parent: d, bound: args}
}

// boundDiag is a child of recordingDiag with bound args, forwarding to the same
// underlying record slice.
type boundDiag struct {
	parent *recordingDiag
	bound  []any
}

func (b *boundDiag) Log(ctx context.Context, level port.Level, msg string, args ...any) {
	merged := make([]any, 0, len(b.bound)+len(args))
	merged = append(merged, b.bound...)
	merged = append(merged, args...)
	b.parent.Log(ctx, level, msg, merged...)
}

func (b *boundDiag) With(args ...any) port.Diagnostics {
	return &boundDiag{parent: b.parent, bound: append(append([]any{}, b.bound...), args...)}
}

// find returns the records whose msg contains sub.
func (d *recordingDiag) find(sub string) []diagRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []diagRecord
	for _, r := range d.records {
		if strings.Contains(r.msg, sub) {
			out = append(out, r)
		}
	}
	return out
}

// argValue returns the value following the given key in a flat key/value arg
// slice, or nil if absent.
func argValue(args []any, key string) any {
	for i := 0; i+1 < len(args); i += 2 {
		if k, ok := args[i].(string); ok && k == key {
			return args[i+1]
		}
	}
	return nil
}

// TestResilienceLogsRetry asserts a transient establishment failure that succeeds
// on retry emits exactly one DEBUG "retrying" line carrying the attempt and a
// clamped err.
func TestResilienceLogsRetry(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("refused")}
	diag := &recordingDiag{}
	f := &fakeProvider{steps: []step{
		{outerErr: conn},
		{chunks: textTurn("ok")},
	}}
	cfg := tinyBackoffCfg(3)
	cfg.Diagnostics = diag
	p := Wrap(f, cfg)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if _, derr := drain(t, seq); derr != nil {
		t.Fatalf("drain error: %v", derr)
	}

	rec := diag.find("retrying")
	if len(rec) != 1 {
		t.Fatalf("retry lines = %d, want 1 (%+v)", len(rec), diag.records)
	}
	if rec[0].level != port.LevelDebug {
		t.Errorf("retry line level = %v, want LevelDebug", rec[0].level)
	}
	if got := argValue(rec[0].args, "attempt"); got != 1 {
		t.Errorf("retry attempt arg = %v, want 1", got)
	}
	// The backoff/backoffWith split exists so the computed duration is logged;
	// guard it. With BaseBackoff=MaxBackoff=1ns the full-jitter window is [1,1ns].
	if got, ok := argValue(rec[0].args, "backoff").(time.Duration); !ok || got != time.Nanosecond {
		t.Errorf("retry backoff arg = %v (%T), want 1ns", argValue(rec[0].args, "backoff"), argValue(rec[0].args, "backoff"))
	}
	if got, _ := argValue(rec[0].args, "err").(string); got == "" || !strings.Contains(got, "refused") {
		t.Errorf("retry err arg = %q, want the clamped underlying error", got)
	}
}

// TestResilienceLogsIdleStall drives the post-first-chunk idle watchdog and
// asserts exactly one INFO at the idle seam carrying the idle budget.
func TestResilienceLogsIdleStall(t *testing.T) {
	diag := &recordingDiag{}
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkText, Text: "partial"}}, stallAfterChunks: true},
	}}
	cfg := Config{
		MaxAttempts:       1,
		StreamIdleTimeout: 50 * time.Millisecond,
		Diagnostics:       diag,
	}
	p := Wrap(f, cfg)

	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		seq, err := p.Stream(context.Background(), port.LLMRequest{})
		if err != nil {
			done <- outcome{err: err}
			return
		}
		_, derr := drain(t, seq)
		done <- outcome{err: derr}
	}()

	select {
	case o := <-done:
		var sie *StreamIdleError
		if !errors.As(o.err, &sie) {
			t.Fatalf("err = %v, want *StreamIdleError", o.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stream/drain hung past the idle watchdog deadline")
	}

	rec := diag.find("idle timeout")
	if len(rec) != 1 {
		t.Fatalf("idle-stall lines = %d, want 1 (%+v)", len(rec), diag.records)
	}
	if rec[0].level != port.LevelInfo {
		t.Errorf("idle-stall line level = %v, want LevelInfo", rec[0].level)
	}
	if got := argValue(rec[0].args, "idle"); got != 50*time.Millisecond {
		t.Errorf("idle-stall idle arg = %v, want 50ms", got)
	}
}

// TestResilienceLogsPermanentError asserts the FIRST of the two paths that end a turn
// TERMINALLY — a permanent, non-retryable establishment error — emits exactly one INFO
// carrying the attempt and a clamped err. Before issue #319 the recoverable lifecycle
// (retry / exhaustion / idle stall / breaker) was fully observable while both fatal paths
// logged at NO level, so an operator reading mecatui.log could not distinguish a
// permanent 4xx from a run that never called the provider at all.
func TestResilienceLogsPermanentError(t *testing.T) {
	diag := &recordingDiag{}
	f := &fakeProvider{steps: []step{{outerErr: apiErr(400)}}}
	cfg := tinyBackoffCfg(3)
	cfg.Diagnostics = diag
	p := Wrap(f, cfg)

	if _, err := p.Stream(context.Background(), port.LLMRequest{Model: "gpt-5.5-mini"}); err == nil {
		t.Fatal("Stream must surface the permanent error")
	}

	rec := diag.find("non-retryable")
	if len(rec) != 1 {
		t.Fatalf("non-retryable lines = %d, want 1 (%+v)", len(rec), diag.records)
	}
	if rec[0].level != port.LevelInfo {
		t.Errorf("non-retryable line level = %v, want LevelInfo", rec[0].level)
	}
	if got := argValue(rec[0].args, "attempt"); got != 1 {
		t.Errorf("non-retryable attempt arg = %v, want 1", got)
	}
	if got, _ := argValue(rec[0].args, "err").(string); got == "" {
		t.Errorf("non-retryable line must carry the clamped err, got %q", got)
	}
	// CORRELATION (issue #319's other half): without the model an operator reading a
	// busy server's log learns that A turn died, not whose. It is the finest correlation
	// this decorator can reach — it sees no session/run identity, and port.LLMRequest
	// must stay provider-neutral.
	if got, _ := argValue(rec[0].args, "model").(string); got != "gpt-5.5-mini" {
		t.Errorf("non-retryable line model arg = %q, want the request's model", got)
	}
	// A permanent error is NOT retried, so no retry line may accompany it.
	if n := len(diag.find("retrying")); n != 0 {
		t.Errorf("a permanent error must not be retried, got %d retry lines", n)
	}
}

// TestResilienceLogsMidStreamError asserts the SECOND terminal path — an error chunk
// yielded AFTER the first committing chunk, which the no-replay rule makes unretryable —
// emits exactly one INFO. It runs BOTH restSeq variants (idle-bounded and not): the
// production wiring sets StreamIdleTimeout, but ≤0 disables the watchdog, and the blind
// spot must not silently re-open on that path.
func TestResilienceLogsMidStreamError(t *testing.T) {
	for _, tc := range []struct {
		name string
		idle time.Duration
	}{
		{"idle watchdog on (the production wiring)", 5 * time.Second},
		{"idle watchdog off", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diag := &recordingDiag{}
			f := &fakeProvider{steps: []step{
				{chunks: []port.Chunk{{Kind: port.ChunkText, Text: "partial"}}, midErr: errors.New("upstream 502 mid-stream")},
			}}
			cfg := Config{MaxAttempts: 1, StreamIdleTimeout: tc.idle, Diagnostics: diag}
			p := Wrap(f, cfg)

			seq, err := p.Stream(context.Background(), port.LLMRequest{Model: "claude-opus-5"})
			if err != nil {
				t.Fatalf("Stream error: %v", err)
			}
			if _, derr := drain(t, seq); derr == nil {
				t.Fatal("drain must surface the mid-stream error")
			}

			rec := diag.find("mid-stream")
			if len(rec) != 1 {
				t.Fatalf("mid-stream lines = %d, want 1 (%+v)", len(rec), diag.records)
			}
			if rec[0].level != port.LevelInfo {
				t.Errorf("mid-stream line level = %v, want LevelInfo", rec[0].level)
			}
			if got, _ := argValue(rec[0].args, "err").(string); !strings.Contains(got, "502") {
				t.Errorf("mid-stream line must carry the clamped err, got %q", got)
			}
			// CORRELATION: the model is threaded Stream -> establish -> pullToCommit ->
			// restSeq for this line specifically (the mid-stream site had no request in
			// scope), so BOTH restSeq variants must carry it — a thread that reached only
			// the idle-bounded path would leave the disable-the-watchdog wiring blind.
			if got, _ := argValue(rec[0].args, "model").(string); got != "claude-opus-5" {
				t.Errorf("mid-stream line model arg = %q, want the request's model", got)
			}
		})
	}
}

// TestResilienceCleanStreamLogsNoTerminalLine is the negative guard for both new lines: a
// stream that establishes and completes cleanly must emit neither, so a non-empty match
// genuinely means "this turn died".
func TestResilienceCleanStreamLogsNoTerminalLine(t *testing.T) {
	diag := &recordingDiag{}
	f := &fakeProvider{steps: []step{{chunks: textTurn("ok")}}}
	cfg := Config{MaxAttempts: 1, StreamIdleTimeout: 5 * time.Second, Diagnostics: diag}
	p := Wrap(f, cfg)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if _, derr := drain(t, seq); derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
	if n := len(diag.find("mid-stream")) + len(diag.find("non-retryable")); n != 0 {
		t.Fatalf("a clean stream must emit no terminal-failure line, got %d (%+v)", n, diag.records)
	}
}

// TestResilienceLogsBreakerOpen asserts the breaker-open transition emits exactly
// one INFO at the crossing (not on subsequent fail-fast calls), at LevelInfo with
// the consecutive_failures and cooldown keys.
func TestResilienceLogsBreakerOpen(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("refused")}
	clk := &manualClock{t: time.Unix(1000, 0)}
	diag := &recordingDiag{}
	f := &fakeProvider{steps: []step{{outerErr: conn}}}
	cfg := Config{
		MaxAttempts:      1,
		BaseBackoff:      time.Nanosecond,
		MaxBackoff:       time.Nanosecond,
		BreakerThreshold: 3,
		BreakerCooldown:  30 * time.Second,
		Clock:            clk.Now,
		Diagnostics:      diag,
	}
	p := Wrap(f, cfg)

	// Three consecutive failures open the breaker; a fourth fails fast while open.
	for i := 0; i < 4; i++ {
		_, _ = p.Stream(context.Background(), port.LLMRequest{})
	}

	rec := diag.find("circuit breaker opened")
	if len(rec) != 1 {
		t.Fatalf("breaker-open lines = %d, want exactly 1 at the crossing (%+v)", len(rec), diag.records)
	}
	if rec[0].level != port.LevelInfo {
		t.Errorf("breaker-open line level = %v, want LevelInfo", rec[0].level)
	}
	if got := argValue(rec[0].args, "consecutive_failures"); got != 3 {
		t.Errorf("breaker-open consecutive_failures arg = %v, want 3", got)
	}
	if got := argValue(rec[0].args, "cooldown"); got != 30*time.Second {
		t.Errorf("breaker-open cooldown arg = %v, want 30s", got)
	}
}

// TestResilienceLogsBreakerReopen drives the half-open path: a transient burst
// opens the breaker, the cooldown elapses to admit a half-open trial, and that
// trial FAILS — re-opening the breaker. The re-open is an operator-meaningful
// event and MUST emit its own "circuit breaker opened" INFO. This test also
// makes the !wasOpen/wasHalfOpen guard in recordFailure load-bearing: the
// half-open trial calls recordFailure while open==true, so the naive mutation
// `opened := open` would emit on EVERY post-open failure (the existing fast-fail
// path never reaches recordFailure, which is why the original test could not
// catch the mutation).
func TestResilienceLogsBreakerReopen(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("refused")}
	clk := &manualClock{t: time.Unix(2000, 0)}
	diag := &recordingDiag{}
	// One step, reused: every establish fails with the transient conn error.
	f := &fakeProvider{steps: []step{{outerErr: conn}}}
	cfg := Config{
		MaxAttempts:      1,
		BaseBackoff:      time.Nanosecond,
		MaxBackoff:       time.Nanosecond,
		BreakerThreshold: 2,
		BreakerCooldown:  10 * time.Second,
		Clock:            clk.Now,
		Diagnostics:      diag,
	}
	p := Wrap(f, cfg)

	// Two consecutive failures open the breaker (crossing #1).
	for i := 0; i < 2; i++ {
		_, _ = p.Stream(context.Background(), port.LLMRequest{})
	}
	if got := len(diag.find("circuit breaker opened")); got != 1 {
		t.Fatalf("after initial open: opened lines = %d, want 1 (%+v)", got, diag.records)
	}
	// While open and within cooldown, a fast-fail does NOT reach recordFailure
	// and must not emit another line.
	_, _ = p.Stream(context.Background(), port.LLMRequest{})
	if got := len(diag.find("circuit breaker opened")); got != 1 {
		t.Fatalf("fast-fail while open emitted an extra opened line: %d (%+v)", got, diag.records)
	}

	// Cooldown elapses: the next call admits a half-open trial, which fails and
	// re-opens the breaker (crossing #2). It must emit exactly one more line.
	clk.Advance(11 * time.Second)
	_, _ = p.Stream(context.Background(), port.LLMRequest{})

	rec := diag.find("circuit breaker opened")
	if len(rec) != 2 {
		t.Fatalf("after half-open re-open: opened lines = %d, want 2 (initial + re-open) (%+v)", len(rec), diag.records)
	}
	if rec[1].level != port.LevelInfo {
		t.Errorf("re-open line level = %v, want LevelInfo", rec[1].level)
	}
}

// TestResilienceLogsBreakerClosedRecovered asserts a successful half-open trial
// emits the "circuit breaker closed (recovered)" INFO exactly once.
func TestResilienceLogsBreakerClosedRecovered(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("refused")}
	clk := &manualClock{t: time.Unix(3000, 0)}
	diag := &recordingDiag{}
	f := &fakeProvider{steps: []step{
		{outerErr: conn},
		{outerErr: conn},
		{chunks: textTurn("recovered")},
	}}
	cfg := Config{
		MaxAttempts:      1,
		BaseBackoff:      time.Nanosecond,
		MaxBackoff:       time.Nanosecond,
		BreakerThreshold: 2,
		BreakerCooldown:  10 * time.Second,
		Clock:            clk.Now,
		Diagnostics:      diag,
	}
	p := Wrap(f, cfg)

	for i := 0; i < 2; i++ {
		_, _ = p.Stream(context.Background(), port.LLMRequest{})
	}
	// Cooldown elapses; the half-open trial succeeds and closes the breaker.
	clk.Advance(11 * time.Second)
	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("half-open trial: unexpected error: %v", err)
	}
	if _, derr := drain(t, seq); derr != nil {
		t.Fatalf("half-open drain error: %v", derr)
	}

	rec := diag.find("closed (recovered)")
	if len(rec) != 1 {
		t.Fatalf("closed-recovered lines = %d, want 1 (%+v)", len(rec), diag.records)
	}
	if rec[0].level != port.LevelInfo {
		t.Errorf("closed-recovered line level = %v, want LevelInfo", rec[0].level)
	}
}

// TestResilienceLogsPerAttemptTimeout asserts a per-attempt establishment
// deadline emits the DEBUG "per-attempt timeout fired" line carrying the
// configured budget and attempt counters.
func TestResilienceLogsPerAttemptTimeout(t *testing.T) {
	diag := &recordingDiag{}
	// A step that blocks until its per-attempt ctx is cancelled, then returns the
	// ctx error — modelling an establishment that overruns the per-attempt budget.
	f := &fakeProvider{steps: []step{{block: true}}}
	cfg := Config{
		MaxAttempts:       1,
		PerAttemptTimeout: 30 * time.Millisecond,
		Diagnostics:       diag,
	}
	p := Wrap(f, cfg)

	_, _ = p.Stream(context.Background(), port.LLMRequest{})

	rec := diag.find("per-attempt timeout fired")
	if len(rec) != 1 {
		t.Fatalf("per-attempt-timeout lines = %d, want 1 (%+v)", len(rec), diag.records)
	}
	if rec[0].level != port.LevelDebug {
		t.Errorf("per-attempt-timeout line level = %v, want LevelDebug", rec[0].level)
	}
	if got := argValue(rec[0].args, "per_attempt_timeout"); got != 30*time.Millisecond {
		t.Errorf("per_attempt_timeout arg = %v, want 30ms", got)
	}
	if got := argValue(rec[0].args, "attempt"); got != 1 {
		t.Errorf("attempt arg = %v, want 1", got)
	}
}

// TestResilienceLogsExhaustion asserts that exhausting every attempt emits the
// INFO "not established after all attempts" line with the attempts count.
func TestResilienceLogsExhaustion(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("refused")}
	diag := &recordingDiag{}
	f := &fakeProvider{steps: []step{{outerErr: conn}}}
	cfg := tinyBackoffCfg(3)
	cfg.Diagnostics = diag
	p := Wrap(f, cfg)

	if _, err := p.Stream(context.Background(), port.LLMRequest{}); err == nil {
		t.Fatal("Stream should have failed after exhausting attempts")
	}

	rec := diag.find("not established after all attempts")
	if len(rec) != 1 {
		t.Fatalf("exhaustion lines = %d, want 1 (%+v)", len(rec), diag.records)
	}
	if rec[0].level != port.LevelInfo {
		t.Errorf("exhaustion line level = %v, want LevelInfo", rec[0].level)
	}
	if got := argValue(rec[0].args, "attempts"); got != 3 {
		t.Errorf("exhaustion attempts arg = %v, want 3", got)
	}
}

// TestResilienceNilDiagnosticsSafe asserts a zero Config (nil Diagnostics) drives
// a retry path without panicking — the nil-safe diag() guard holds even for a
// provider constructed via Wrap (which defaults) and never trips on the hot path.
func TestResilienceNilDiagnosticsSafe(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("refused")}
	f := &fakeProvider{steps: []step{
		{outerErr: conn},
		{chunks: textTurn("ok")},
	}}
	cfg := tinyBackoffCfg(3) // Diagnostics deliberately left nil
	p := Wrap(f, cfg)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if _, derr := drain(t, seq); derr != nil {
		t.Fatalf("drain error: %v", derr)
	}
}

// TestResilienceDiagnosticsProviderTagFlows asserts that a provider-tagged sink
// (as composition wires via cfg.diag().With("provider", id)) carries the provider
// key onto every resilience line. Models the registry.go binding without
// importing internal/app.
func TestResilienceDiagnosticsProviderTagFlows(t *testing.T) {
	conn := &net.OpError{Op: "dial", Err: errors.New("refused")}
	diag := &recordingDiag{}
	f := &fakeProvider{steps: []step{
		{outerErr: conn},
		{chunks: textTurn("ok")},
	}}
	cfg := tinyBackoffCfg(3)
	cfg.Diagnostics = diag.With("provider", "openai") // the registry.go binding
	p := Wrap(f, cfg)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if _, derr := drain(t, seq); derr != nil {
		t.Fatalf("drain error: %v", derr)
	}

	rec := diag.find("retrying")
	if len(rec) != 1 {
		t.Fatalf("retry lines = %d, want 1 (%+v)", len(rec), diag.records)
	}
	if got := argValue(rec[0].args, "provider"); got != "openai" {
		t.Errorf("provider arg = %v, want openai (the bound tag must flow onto every line)", got)
	}
}

// compile-time check the recorders satisfy the port.
var (
	_ port.Diagnostics = (*recordingDiag)(nil)
	_ port.Diagnostics = (*boundDiag)(nil)
)

// TestResilienceLogsMidStreamCancellationToo pins the DELIBERATE breadth of the mid-stream
// line: it fires for EVERY non-nil mid-stream error, including context.Canceled when the
// operator cancels a run. That is intended — the line's job is "this turn ended here", and
// a cancelled turn ended just as terminally as a 502 — but an operator reading the log has
// to know to expect one per cancel, and a suite that only covers a clean stream and a 502
// has no opinion either way. Pin it so a future narrowing (e.g. skipping ctx.Canceled) is
// a deliberate change with a failing test, not a silent one.
func TestResilienceLogsMidStreamCancellationToo(t *testing.T) {
	diag := &recordingDiag{}
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkText, Text: "partial"}}, midErr: context.Canceled},
	}}
	cfg := Config{MaxAttempts: 1, StreamIdleTimeout: 5 * time.Second, Diagnostics: diag}
	p := Wrap(f, cfg)

	seq, err := p.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if _, derr := drain(t, seq); derr == nil {
		t.Fatal("drain must surface the cancellation")
	}

	rec := diag.find("mid-stream")
	if len(rec) != 1 {
		t.Fatalf("a mid-stream cancellation must emit exactly one line (the turn DID end there); got %d (%+v)", len(rec), diag.records)
	}
	if rec[0].level != port.LevelInfo {
		t.Errorf("mid-stream cancellation line level = %v, want LevelInfo", rec[0].level)
	}
	if got, _ := argValue(rec[0].args, "err").(string); !strings.Contains(got, "context canceled") {
		t.Errorf("the line must name the cancellation so an operator can tell it from a provider fault, got %q", got)
	}
}
