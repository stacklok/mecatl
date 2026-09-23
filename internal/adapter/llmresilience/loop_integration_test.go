package llmresilience_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
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
	return session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, limits, time.Unix(0, 0))
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

	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), EnableDurableEvidence: true})
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, ws, memledger.New(), nil)
	r := e.Run(context.Background(), newSession(t, session.Limits{}), env, agent.RunRequest{Text: "go"})

	events := drain(r)
	res := lastResult(t, events)
	var attempt *session.NetworkAttemptPayload
	attemptIndex, resultIndex := -1, -1
	for i, ev := range events {
		if ev.Type == session.EvNetworkAttempt {
			attempt = ev.NetworkAttempt
			attemptIndex = i
		}
		if ev.Type == session.EvResult {
			resultIndex = i
		}
	}
	if attempt == nil || attempt.SessionID != "s1" || attempt.Attempt != 1 || attempt.Decision != "terminal" || attempt.FailureClass != "unknown" {
		t.Fatalf("loop network attempt = %+v", attempt)
	}
	if attemptIndex < 0 || resultIndex < 0 || attemptIndex >= resultIndex {
		t.Fatalf("network attempt/result order = %d/%d, events=%v", attemptIndex, resultIndex, events)
	}
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
	msg             string
	status          int
	providerCode    string
	correlationKind string
	correlationID   string
}

func (e *streamStatusError) Error() string                        { return e.msg }
func (e *streamStatusError) StatusCode() int                      { return e.status }
func (e *streamStatusError) ProviderHTTPStatus() int              { return e.status }
func (*streamStatusError) ProviderInBandStatus() int              { return 0 }
func (e *streamStatusError) ProviderErrorCode() string            { return e.providerCode }
func (e *streamStatusError) ProviderErrorCorrelationKind() string { return e.correlationKind }
func (e *streamStatusError) ProviderErrorCorrelationID() string   { return e.correlationID }

// TestStreamRateLimitErrorIsRetried verifies the end-to-end fix for the reported
// "no retry on rate_limit_exceeded" bug: a rate-limit arriving as a first-chunk
// error carrying StatusCode()==429 must be retried by DefaultClassifier, and the
// SECOND attempt succeeds. This covers the path where response.failed or a top-level
// "error" stream event (both now return *responseStreamError with StatusCode==429 for
// rate_limit_exceeded) surfaces as the first-chunk cerr in llmresilience.establish.
func TestStreamRateLimitErrorIsRetried(t *testing.T) {
	secrets := []string{"sk-live-SECRET", "Bearer-SECRET"}
	rateLimitErr := &streamStatusError{
		msg:             "response failed: rate_limit_exceeded: Too Many Requests",
		status:          429,
		providerCode:    secrets[0],
		correlationKind: "request",
		correlationID:   secrets[1],
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

	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), EnableDurableEvidence: true})
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, ws, memledger.New(), nil)
	r := e.Run(context.Background(), newSession(t, session.Limits{}), env, agent.RunRequest{Text: "go"})

	events := drain(r)
	res := lastResult(t, events)
	// The run must succeed, not fail.
	if res.Stop == session.StopError {
		t.Fatalf("stop = StopError (got %q), want StopEndTurn — rate_limit_exceeded must be retried", res.Stop)
	}
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Fatalf("calls = %d, want exactly 2 (one retry then success)", got)
	}
	var attempts []session.NetworkAttemptPayload
	attemptIndex, firstVisibleRetryOutputIndex := -1, -1
	for i, ev := range events {
		if ev.Type == session.EvNetworkAttempt {
			attemptIndex = i
			if ev.NetworkAttempt == nil {
				t.Fatal("network.attempt has nil payload")
			}
			attempts = append(attempts, *ev.NetworkAttempt)
			continue
		}
		if attemptIndex >= 0 && firstVisibleRetryOutputIndex < 0 && (ev.Type == session.EvMessageDelta || ev.Type == session.EvReasoningDelta || ev.Type == session.EvToolCall) {
			firstVisibleRetryOutputIndex = i
		}
	}
	if len(attempts) != 1 {
		t.Fatalf("network attempts = %d, want exactly one retry event: %v", len(attempts), events)
	}
	attempt := attempts[0]
	wantDigest, _ := session.NetworkCorrelationDigest("request", secrets[1])
	if attempt.SessionID != "s1" || attempt.RunSerial < 1 || attempt.Turn != 0 ||
		attempt.Attempt != 1 || attempt.MaxAttempts != 3 || attempt.Decision != "retry" ||
		attempt.RetryDisposition != "retryable" || attempt.StreamProgress != "precommit" ||
		attempt.SuppressionReason != "" || attempt.HTTPStatus != 429 ||
		attempt.CorrelationKind != "request" || attempt.CorrelationDigest != wantDigest {
		t.Fatalf("retry attempt = %+v", attempt)
	}
	if attemptIndex < 0 || firstVisibleRetryOutputIndex < 0 || attemptIndex >= firstVisibleRetryOutputIndex {
		t.Fatalf("network attempt/first retry output order = %d/%d, events=%v", attemptIndex, firstVisibleRetryOutputIndex, events)
	}
	marshaled, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(marshaled), secret) {
			t.Fatalf("event stream leaked producer token %q: %s", secret, marshaled)
		}
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

	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), EnableDurableEvidence: true})
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, ws, memledger.New(), nil)
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
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, ws, memledger.New(), nil)
	r := e.Run(context.Background(), newSession(t, session.Limits{}), env, agent.RunRequest{Text: "go"})

	res := lastResult(t, drain(r))
	if res.Stop == session.StopError {
		t.Fatalf("stop = StopError (%q), want StopEndTurn — pre-commit reasoning error must be retried", res.Stop)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2 (1 failing with reasoning-only + 1 succeeding)", got)
	}
}

type semanticToolProvider struct {
	calls atomic.Int32
}

func (*semanticToolProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *semanticToolProvider) Stream(_ context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	attempt := p.calls.Add(1)
	return func(yield func(port.Chunk, error) bool) {
		switch attempt {
		case 1:
			yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &session.ToolCall{ID: "failed", Name: "Count", Args: json.RawMessage(`{}`)}}, nil)
			yield(port.Chunk{}, &streamStatusError{msg: "server_error: 503", status: 503})
		case 2:
			if !yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &session.ToolCall{ID: "successful", Name: "Count", Args: json.RawMessage(`{}`)}}, nil) {
				return
			}
			yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
		default:
			if !yield(port.Chunk{Kind: port.ChunkText, Text: "done"}, nil) {
				return
			}
			yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
		}
	}, nil
}

type countedTool struct {
	executions atomic.Int32
}

func (*countedTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Count", Description: "count executions", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*countedTool) ReadOnly() bool { return true }
func (t *countedTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.executions.Add(1)
	return session.NewToolResult(call.ID, "executed"), nil
}

func TestLoopDiscardsFailedTentativeToolCallAndDispatchesSuccessfulRetryOnce(t *testing.T) {
	inner := &semanticToolProvider{}
	llm := llmresilience.Wrap(inner, llmresilience.Config{MaxAttempts: 2, BaseBackoff: time.Millisecond})
	counter := &countedTool{}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, counter)})
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, ws, memledger.New(), nil)
	events := drain(e.Run(context.Background(), newSession(t, session.Limits{}), env, agent.RunRequest{Text: "go"}))

	if got := counter.executions.Load(); got != 1 {
		t.Fatalf("tool executions = %d, want 1", got)
	}
	var calls, results int
	for _, event := range events {
		switch event.Type {
		case session.EvToolCall:
			calls++
			if event.ToolCall == nil || event.ToolCall.ID != "successful" {
				t.Fatalf("escaped tentative tool-call event: %+v", event.ToolCall)
			}
		case session.EvToolResult:
			results++
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("tool events: calls=%d results=%d, want 1 each", calls, results)
	}
	res := lastResult(t, events)
	if res.Stop != session.StopEndTurn {
		t.Fatalf("stop = %q, error = %q", res.Stop, res.Error)
	}
}

func TestLoopAccountsRetriedUsageInSessionResultAndBudget(t *testing.T) {
	inner := &semanticUsageBudgetProvider{}
	llm := llmresilience.Wrap(inner, llmresilience.Config{MaxAttempts: 2, BaseBackoff: time.Millisecond})
	counter := &countedTool{}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, counter), MaxRunTokens: 10})
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, ws, memledger.New(), nil)
	sess := newSession(t, session.Limits{})
	result := lastResult(t, drain(e.Run(context.Background(), sess, env, agent.RunRequest{Text: "go"})))

	want := session.Usage{InputTokens: 9, OutputTokens: 3}
	if result.Stop != session.StopBudget {
		t.Fatalf("stop = %q, want budget", result.Stop)
	}
	if result.Usage != want || sess.UsageFor(session.UsageKindMain) != want {
		t.Fatalf("result usage=%+v session usage=%+v, want %+v", result.Usage, sess.UsageFor(session.UsageKindMain), want)
	}
	if inner.calls.Load() != 2 || counter.executions.Load() != 1 {
		t.Fatalf("provider calls=%d tool executions=%d, want 2 and 1", inner.calls.Load(), counter.executions.Load())
	}
}

func TestLoopRecordsUsageOnExhaustedStream(t *testing.T) {
	inner := &semanticUsageBudgetProvider{}
	llm := llmresilience.Wrap(inner, llmresilience.Config{MaxAttempts: 1})
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, ws, memledger.New(), nil)
	sess := newSession(t, session.Limits{})
	result := lastResult(t, drain(e.Run(context.Background(), sess, env, agent.RunRequest{Text: "go"})))

	want := session.Usage{InputTokens: 5, OutputTokens: 1}
	if result.Stop != session.StopError || result.Usage != want || sess.UsageFor(session.UsageKindMain) != want {
		t.Fatalf("stop=%q result usage=%+v session usage=%+v, want error and %+v", result.Stop, result.Usage, sess.UsageFor(session.UsageKindMain), want)
	}
}

type semanticUsageBudgetProvider struct {
	calls atomic.Int32
}

func (*semanticUsageBudgetProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *semanticUsageBudgetProvider) Stream(_ context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	attempt := p.calls.Add(1)
	return func(yield func(port.Chunk, error) bool) {
		if attempt == 1 {
			u1 := session.Usage{InputTokens: 2, OutputTokens: 1}
			u2 := session.Usage{InputTokens: 3}
			yield(port.Chunk{Kind: port.ChunkUsage, Usage: &u1}, nil)
			yield(port.Chunk{Kind: port.ChunkUsage, Usage: &u2}, nil)
			yield(port.Chunk{}, &streamStatusError{msg: "server_error: 503", status: 503})
			return
		}
		u := session.Usage{InputTokens: 4, OutputTokens: 2}
		yield(port.Chunk{Kind: port.ChunkUsage, Usage: &u}, nil)
		yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &session.ToolCall{ID: "successful", Name: "Count", Args: json.RawMessage(`{}`)}}, nil)
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

type cancelTentativeToolProvider struct {
	started chan struct{}
	once    sync.Once
}

func (*cancelTentativeToolProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *cancelTentativeToolProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		if !yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &session.ToolCall{ID: "cancelled", Name: "Count", Args: json.RawMessage(`{}`)}}, nil) {
			return
		}
		p.once.Do(func() { close(p.started) })
		<-ctx.Done()
	}, nil
}

func TestLoopCancellationAfterTentativeToolCallStopsCancelledWithoutDispatch(t *testing.T) {
	inner := &cancelTentativeToolProvider{started: make(chan struct{})}
	counter := &countedTool{}
	llm := llmresilience.Wrap(inner, llmresilience.Config{MaxAttempts: 2})
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, counter)})
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, ws, memledger.New(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	run := e.Run(ctx, newSession(t, session.Limits{}), env, agent.RunRequest{Text: "go"})
	<-inner.started
	cancel()
	events := drain(run)

	if got := counter.executions.Load(); got != 0 {
		t.Fatalf("tool executions = %d, want 0", got)
	}
	for _, event := range events {
		if event.Type == session.EvToolCall || event.Type == session.EvToolResult {
			t.Fatalf("tentative tool activity escaped as %s", event.Type)
		}
	}
	if stop := lastResult(t, events).Stop; stop != session.StopCancelled {
		t.Fatalf("stop = %q, want %q", stop, session.StopCancelled)
	}
}
