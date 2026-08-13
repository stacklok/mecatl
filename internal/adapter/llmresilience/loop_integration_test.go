package llmresilience_test

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
)

// These are the engine+llmresilience INTEGRATION tests: they drive a real
// agent.Engine through the resilience wrapper and assert the loop's terminal
// behavior. They live here (not in engine/agent) because the wrapper under
// test is this adapter — the engine tree stays self-contained, importing no
// internal/ package even from tests.

// --- local copies of the engine test helpers ---------------------------------

func newEngine(d agent.Deps) *agent.Engine {
	if d.Policy == nil {
		d.Policy = permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil)
	}
	if d.Model == "" {
		d.Model = "test-model"
	}
	return agent.NewEngine(d)
}

func newSession(t *testing.T, limits session.Limits) *session.Session {
	t.Helper()
	return session.New("s1", session.ModeDefault, "/ws", limits, time.Unix(0, 0))
}

func catalogWith(t *testing.T, tools ...tool.Tool) *tool.Catalog {
	t.Helper()
	c := tool.NewCatalog()
	for _, tl := range tools {
		c.MustRegister(tl)
	}
	return c
}

func drain(r *agent.Run) []session.Event {
	var evs []session.Event
	for ev := range r.Events() {
		evs = append(evs, ev)
	}
	return evs
}

func lastResult(t *testing.T, evs []session.Event) *session.ResultPayload {
	t.Helper()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == session.EvResult {
			return evs[i].Result
		}
	}
	types := make([]session.EventType, len(evs))
	for i, e := range evs {
		types[i] = e.Type
	}
	t.Fatalf("no result event in %v", types)
	return nil
}

// --- provider fakes -----------------------------------------------------------

// errProvider returns a real, NON-context error as the outer Stream error on
// every call — the shape of a provider 400 reaching the establishment seam.
type errProvider struct {
	err error
}

func (*errProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

func (p *errProvider) Stream(context.Context, port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return nil, p.err
}

// firstChunkErrProvider returns a NON-nil iterator whose FIRST yielded chunk
// carries a real, NON-context error — the shape of a provider 400 surfacing as
// the first SSE chunk rather than as the outer Stream error. This drives the
// "first-chunk cerr" establishment seam in llmresilience (distinct from the
// outer-Stream-error seam that errProvider exercises).
type firstChunkErrProvider struct {
	err error
}

func (*firstChunkErrProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *firstChunkErrProvider) Stream(context.Context, port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		yield(port.Chunk{}, p.err)
	}, nil
}

// --- tests --------------------------------------------------------------------

// TestEstablishErrorTerminatesStopError is the end-to-end reproduction of the
// reported symptom: a real establishment error (NOT a context error), wrapped
// through llmresilience with a per-attempt timeout, must terminate the turn as
// StopError with the provider message surfaced — NOT as StopCancelled with an
// empty/silent result.
func TestEstablishErrorTerminatesStopError(t *testing.T) {
	provErr := fmt.Errorf("upstream rejected request: bad tool schema (400)")
	inner := &errProvider{err: provErr}
	llm := llmresilience.Wrap(inner, llmresilience.Config{
		MaxAttempts:       1,
		PerAttemptTimeout: time.Second,
	})

	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, ws, nil)
	r := e.Run(context.Background(), newSession(t, session.Limits{}), env, agent.RunRequest{Text: "go"})

	res := lastResult(t, drain(r))
	if res.Stop != session.StopError {
		t.Fatalf("stop = %q, want error (NOT cancelled)", res.Stop)
	}
	if !strings.Contains(res.Error, "bad tool schema (400)") {
		t.Fatalf("result error = %q, want the provider message surfaced", res.Error)
	}
}

// streamStatusError is a minimal error type that implements StatusCode() int,
// mirroring the openai adapter's *responseStreamError so we can test the
// DefaultClassifier's StatusCode interface hook without importing the openai package
// (engine/agent and internal/adapter/llmresilience must not import each other's
// concrete adapters). The type is local to this test file only.
type streamStatusError struct {
	msg    string
	status int
}

func (e *streamStatusError) Error() string   { return e.msg }
func (e *streamStatusError) StatusCode() int { return e.status }

// TestStreamRateLimitErrorIsRetried verifies the end-to-end fix for the reported
// "no retry on rate_limit_exceeded" bug: a rate-limit arriving as a first-chunk
// error carrying StatusCode()==429 must be retried by DefaultClassifier, and the
// SECOND attempt succeeds. This covers the path where response.failed or a top-level
// "error" stream event (both now return *responseStreamError with StatusCode==429 for
// rate_limit_exceeded) surfaces as the first-chunk cerr in llmresilience.establish.
func TestStreamRateLimitErrorIsRetried(t *testing.T) {
	rateLimitErr := &streamStatusError{
		msg:    "response failed: rate_limit_exceeded: Too Many Requests",
		status: 429,
	}
	successChunks := []port.Chunk{
		{Kind: port.ChunkText, Text: "hello"},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}

	// First call: first-chunk yields the rate-limit error (pre-first-chunk seam).
	// Second call: succeeds with real chunks.
	inner := &firstChunkErrProvider{err: rateLimitErr}
	var callCount int32
	inner2 := &countingFirstChunkProvider{
		firstErr:      rateLimitErr,
		successChunks: successChunks,
		calls:         &callCount,
	}
	_ = inner // suppress unused var; we use inner2 below

	llm := llmresilience.Wrap(inner2, llmresilience.Config{
		MaxAttempts: 3,
		BaseBackoff: time.Millisecond, // fast for tests
	})

	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, ws, nil)
	r := e.Run(context.Background(), newSession(t, session.Limits{}), env, agent.RunRequest{Text: "go"})

	res := lastResult(t, drain(r))
	// The run must succeed, not fail.
	if res.Stop == session.StopError {
		t.Fatalf("stop = StopError (got %q), want StopEndTurn — rate_limit_exceeded must be retried", res.Stop)
	}
	// Must have made at least 2 calls (1 failing + 1 succeeding).
	if got := atomic.LoadInt32(&callCount); got < 2 {
		t.Errorf("calls = %d, want >= 2 (the rate-limit must trigger a retry)", got)
	}
}

// countingFirstChunkProvider fails with firstErr on the first call (as a
// first-chunk error), then succeeds on subsequent calls with successChunks.
type countingFirstChunkProvider struct {
	firstErr      error
	successChunks []port.Chunk
	calls         *int32
}

func (*countingFirstChunkProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *countingFirstChunkProvider) Stream(_ context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	n := int(atomic.AddInt32(p.calls, 1))
	if n == 1 {
		// First call: return a non-nil iterator whose first chunk is an error.
		err := p.firstErr
		return func(yield func(port.Chunk, error) bool) {
			yield(port.Chunk{}, err)
		}, nil
	}
	// Subsequent calls: yield success chunks.
	chunks := p.successChunks
	return func(yield func(port.Chunk, error) bool) {
		for _, c := range chunks {
			if !yield(c, nil) {
				return
			}
		}
	}, nil
}

// TestStreamRateLimitDefaultClassifier verifies that DefaultClassifier itself
// correctly classifies a StatusCode()==429 error as retryable. This is a direct
// unit test of the classifier without the full engine loop.
func TestStreamRateLimitDefaultClassifier(t *testing.T) {
	err429 := &streamStatusError{msg: "rate_limit_exceeded: Too Many Requests", status: 429}
	if !llmresilience.DefaultClassifier(err429) {
		t.Error("DefaultClassifier(status=429) = false, want true (rate_limit_exceeded must be retryable)")
	}

	// 5xx is also retryable.
	err503 := &streamStatusError{msg: "server_error: internal error", status: 503}
	if !llmresilience.DefaultClassifier(err503) {
		t.Error("DefaultClassifier(status=503) = false, want true (server_error must be retryable)")
	}

	// status=0 (unknown/permanent code) must NOT be retryable.
	err0 := &streamStatusError{msg: "content_filter: Content blocked", status: 0}
	if llmresilience.DefaultClassifier(err0) {
		t.Error("DefaultClassifier(status=0) = true, want false (permanent codes must not be retried)")
	}

	// status=400 (permanent) must NOT be retryable.
	err400 := &streamStatusError{msg: "invalid_request_error: bad request", status: 400}
	if llmresilience.DefaultClassifier(err400) {
		t.Error("DefaultClassifier(status=400) = true, want false (4xx non-408/409/429 must not be retried)")
	}
}

// TestFirstChunkErrorTerminatesStopError is the end-to-end counterpart of
// TestEstablishErrorTerminatesStopError for the OTHER establishment seam: here
// the inner Stream succeeds (non-nil iterator) but the FIRST CHUNK yields a real
// provider error (the "first-chunk cerr" path in llmresilience.establish). Wrapped
// through llmresilience with a per-attempt timeout, this too must terminate the
// turn as StopError with the provider message surfaced — NOT as StopCancelled.
// If the cerr-site masking fix (cause := attemptCtx.Err() read BEFORE cancel) is
// reverted, the cleanup cancel() masks the real error as context.Canceled, the
// loop treats it as a caller-cancel, and this test fails on StopCancelled.
func TestFirstChunkErrorTerminatesStopError(t *testing.T) {
	provErr := fmt.Errorf("upstream rejected request: bad tool schema (400)")
	inner := &firstChunkErrProvider{err: provErr}
	llm := llmresilience.Wrap(inner, llmresilience.Config{
		MaxAttempts:       1,
		PerAttemptTimeout: time.Second,
	})

	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, ws, nil)
	r := e.Run(context.Background(), newSession(t, session.Limits{}), env, agent.RunRequest{Text: "go"})

	res := lastResult(t, drain(r))
	if res.Stop != session.StopError {
		t.Fatalf("stop = %q, want error (NOT cancelled)", res.Stop)
	}
	if !strings.Contains(res.Error, "bad tool schema (400)") {
		t.Fatalf("result error = %q, want the provider message surfaced", res.Error)
	}
}

// preCommitReasoningProvider yields ChunkReasoning chunks on the first call then
// a retryable 503 error, and a full valid turn on subsequent calls.
type preCommitReasoningProvider struct {
	calls         *int32
	successChunks []port.Chunk
	firstErr      error
}

func (*preCommitReasoningProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *preCommitReasoningProvider) Stream(_ context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	n := int(atomic.AddInt32(p.calls, 1))
	if n == 1 {
		err := p.firstErr
		return func(yield func(port.Chunk, error) bool) {
			if !yield(port.Chunk{Kind: port.ChunkReasoning, Text: "thinking"}, nil) {
				return
			}
			yield(port.Chunk{}, err)
		}, nil
	}
	chunks := p.successChunks
	return func(yield func(port.Chunk, error) bool) {
		for _, c := range chunks {
			if !yield(c, nil) {
				return
			}
		}
	}, nil
}

// TestLoopPreCommitReasoningErrorRetriedAndSucceeds is the end-to-end test:
// a provider that on call 1 streams ChunkReasoning then a 503 *streamStatusError
// (retryable), and on call 2 streams a full valid turn, must complete StopEndTurn
// with 2 provider calls and no leaked goroutines.
func TestLoopPreCommitReasoningErrorRetriedAndSucceeds(t *testing.T) {
	var calls int32
	inner := &preCommitReasoningProvider{
		calls: &calls,
		firstErr: &streamStatusError{
			msg:    "server_error: 503 Service Unavailable",
			status: 503,
		},
		successChunks: []port.Chunk{
			{Kind: port.ChunkText, Text: "done"},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		},
	}
	llm := llmresilience.Wrap(inner, llmresilience.Config{
		MaxAttempts: 2,
		BaseBackoff: time.Millisecond,
	})

	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, ws, nil)
	r := e.Run(context.Background(), newSession(t, session.Limits{}), env, agent.RunRequest{Text: "go"})

	res := lastResult(t, drain(r))
	if res.Stop == session.StopError {
		t.Fatalf("stop = StopError (%q), want StopEndTurn — pre-commit reasoning error must be retried", res.Stop)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2 (1 failing with reasoning-only + 1 succeeding)", got)
	}
}
