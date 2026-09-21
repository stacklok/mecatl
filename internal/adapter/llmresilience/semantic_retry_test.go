package llmresilience

import (
	"context"
	"errors"
	"iter"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSemanticAttemptDiscardsTentativeFailedChunks(t *testing.T) {
	retryable := apiErr(503)
	call := session.ToolCall{ID: "call-1", Name: "Read"}
	tests := []struct {
		name      string
		tentative []port.Chunk
	}{
		{name: "whitespace", tentative: []port.Chunk{{Kind: port.ChunkText, Text: " \n"}}},
		{name: "metadata in wire order", tentative: []port.Chunk{
			{Kind: port.ChunkReasoning, Text: "reasoning"},
			{Kind: port.ChunkReasoningItem, Text: "replay"},
			{Kind: port.ChunkPhase, Text: "commentary"},
			{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 7}},
			{Kind: port.ChunkProviderRoute, Text: "route"},
		}},
		{name: "tool call", tentative: []port.Chunk{{Kind: port.ChunkToolCall, ToolCall: &call}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeProvider{steps: []step{
				{chunks: tc.tentative, midErr: retryable},
				{chunks: textTurn("successful attempt")},
			}}
			seq, err := Wrap(f, tinyBackoffCfg(2)).Stream(context.Background(), port.LLMRequest{})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			got, err := drain(t, seq)
			if err != nil {
				t.Fatalf("drain: %v", err)
			}
			if f.Calls() != 2 {
				t.Fatalf("calls = %d, want 2", f.Calls())
			}
			var successfulText int
			for _, chunk := range got {
				if chunk.Kind == port.ChunkText && chunk.Text == "successful attempt" {
					successfulText++
				}
				if chunk.Kind == port.ChunkReasoning || chunk.Kind == port.ChunkReasoningItem ||
					chunk.Kind == port.ChunkPhase || chunk.Kind == port.ChunkProviderRoute || chunk.Kind == port.ChunkToolCall {
					t.Fatalf("failed-attempt semantic chunk escaped: %+v", chunk)
				}
			}
			if successfulText != 1 {
				t.Fatalf("escaped chunks = %+v, want one successful text", got)
			}
		})
	}
}

func TestSemanticRetryAccumulatesDiscardedUsage(t *testing.T) {
	first := session.Usage{InputTokens: 5, OutputTokens: 2}
	second := session.Usage{InputTokens: 7, CacheReadTokens: 3}
	success := session.Usage{InputTokens: 11, OutputTokens: 4}
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{
			{Kind: port.ChunkReasoning, Text: "must stay hidden"},
			{Kind: port.ChunkUsage, Usage: &first},
			{Kind: port.ChunkUsage, Usage: &second},
		}, midErr: apiErr(503)},
		{chunks: []port.Chunk{
			{Kind: port.ChunkText, Text: "ok"},
			{Kind: port.ChunkUsage, Usage: &success},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		}},
	}}
	seq, err := Wrap(f, tinyBackoffCfg(2)).Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := drain(t, seq)
	if err != nil {
		t.Fatal(err)
	}
	var usage session.Usage
	for _, chunk := range got {
		if chunk.Kind == port.ChunkUsage && chunk.Usage != nil {
			usage = usage.Add(*chunk.Usage)
		}
		if strings.Contains(chunk.Text, "hidden") {
			t.Fatalf("failed-attempt content escaped: %+v", got)
		}
	}
	want := first.Add(second).Add(success)
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
}

func TestSemanticRetryExhaustionReturnsObservedUsage(t *testing.T) {
	u1 := session.Usage{InputTokens: 3, OutputTokens: 1}
	u2 := session.Usage{InputTokens: 4, OutputTokens: 2}
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkUsage, Usage: &u1}}, midErr: apiErr(503)},
		{chunks: []port.Chunk{{Kind: port.ChunkUsage, Usage: &u2}}, midErr: apiErr(503)},
	}}
	seq, err := Wrap(f, tinyBackoffCfg(2)).Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream returned outer error before usage could be observed: %v", err)
	}
	got, err := drain(t, seq)
	if err == nil {
		t.Fatal("drain succeeded, want exhausted error")
	}
	var exhausted *ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("error = %T %v, want ExhaustedError", err, err)
	}
	if len(got) != 1 || got[0].Kind != port.ChunkUsage || got[0].Usage == nil || *got[0].Usage != u1.Add(u2) {
		t.Fatalf("chunks = %+v, want aggregate usage %+v", got, u1.Add(u2))
	}
}

func TestSemanticAttemptFlushesEveryTentativeKindInExactOrder(t *testing.T) {
	call := session.ToolCall{ID: "call-1", Name: "Read", Args: []byte(`{"path":"a"}`)}
	usage := session.Usage{InputTokens: 7, OutputTokens: 3, CacheReadTokens: 2}
	chunks := []port.Chunk{
		{Kind: port.ChunkText, Text: " \n"},
		{Kind: port.ChunkReasoning, Text: "reasoning"},
		{Kind: port.ChunkReasoningItem, Text: "replay-item"},
		{Kind: port.ChunkPhase, Text: "commentary"},
		{Kind: port.ChunkProviderRoute, Text: "route-a"},
		{Kind: port.ChunkUsage, Usage: &usage},
		{Kind: port.ChunkToolCall, ToolCall: &call},
		{Kind: port.ChunkText, Text: "visible answer"},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}
	f := &fakeProvider{steps: []step{{chunks: chunks}}}
	seq, err := Wrap(f, Config{MaxAttempts: 2}).Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got, err := drain(t, seq)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !reflect.DeepEqual(got, chunks) {
		t.Fatalf("chunks differ\n got: %#v\nwant: %#v", got, chunks)
	}
}

func TestSemanticAttemptFirstVisibleTextHasNoTentativeBuffer(t *testing.T) {
	provider := &resilientProvider{cfg: Config{MaxAttempts: 1, Clock: time.Now}}
	inner := func(yield func(port.Chunk, error) bool) {
		yield(port.Chunk{Kind: port.ChunkText, Text: "visible"}, nil)
	}
	result, err := provider.pumpAttempt(
		context.Background(),
		attemptDiagnostic{},
		inner,
		func() bool { return false },
		nil,
		func(err error) error { return err },
	)
	if err != nil {
		t.Fatalf("pumpAttempt: %v", err)
	}
	defer func() {
		if result.cancel != nil {
			result.cancel()
		}
		result.stop()
	}()
	if result.progress != session.StreamProgressVisible || result.chunk.Text != "visible" {
		t.Fatalf("result = %+v, want directly visible text", result)
	}
	if result.buffered != nil {
		t.Fatalf("first visible text allocated a tentative buffer: %+v", result.buffered)
	}
}

func TestSemanticAttemptCleanCompletionFlushesTentativeTurn(t *testing.T) {
	call := session.ToolCall{ID: "call-1", Name: "Read"}
	tests := []struct {
		name   string
		chunks []port.Chunk
	}{
		{name: "whitespace only", chunks: []port.Chunk{
			{Kind: port.ChunkText, Text: " \n"},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		}},
		{name: "pure tool call", chunks: []port.Chunk{
			{Kind: port.ChunkToolCall, ToolCall: &call},
			{Kind: port.ChunkUsage, Usage: &session.Usage{OutputTokens: 2}},
			{Kind: port.ChunkDone, Stop: session.StopNone},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeProvider{steps: []step{{chunks: tc.chunks}}}
			seq, err := Wrap(f, Config{MaxAttempts: 2}).Stream(context.Background(), port.LLMRequest{})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			got, err := drain(t, seq)
			if err != nil {
				t.Fatalf("drain: %v", err)
			}
			if len(got) != len(tc.chunks) || got[len(got)-1].Kind != port.ChunkDone {
				t.Fatalf("chunks = %+v, want clean complete turn", got)
			}
			if f.Calls() != 1 {
				t.Fatalf("calls = %d, want 1", f.Calls())
			}
		})
	}
}

func TestRetryDispositionUnknownIsConservative(t *testing.T) {
	unknown := errors.New("unclassified")
	f := &fakeProvider{steps: []step{{outerErr: unknown}, {chunks: textTurn("must not run")}}}
	_, err := Wrap(f, Config{MaxAttempts: 2, Classifier: func(error) bool { return true }}).
		Stream(context.Background(), port.LLMRequest{})
	if !errors.Is(err, unknown) || f.Calls() != 1 {
		t.Fatalf("err = %v, calls = %d; want original unknown and one call", err, f.Calls())
	}
	var disposition port.RetryDispositionError
	if !errors.As(err, &disposition) || disposition.RetryDisposition() != session.RetryDispositionUnknown {
		t.Fatalf("unknown error disposition = %v; want explicit unknown", disposition)
	}
	var progress port.StreamProgressError
	if !errors.As(err, &progress) || progress.StreamProgress() != session.StreamProgressPrecommit {
		t.Fatalf("unknown error progress = %v; want precommit", progress)
	}
}

type dispositionOnlyError struct {
	disposition session.RetryDisposition
}

func (*dispositionOnlyError) Error() string { return "typed disposition" }
func (e *dispositionOnlyError) RetryDisposition() session.RetryDisposition {
	return e.disposition
}

func TestInvalidProviderDispositionNormalizesToUnknown(t *testing.T) {
	original := &dispositionOnlyError{disposition: session.RetryDisposition(99)}
	_, err := Wrap(&fakeProvider{steps: []step{{outerErr: original}}}, Config{MaxAttempts: 2}).
		Stream(context.Background(), port.LLMRequest{})
	if !errors.Is(err, original) {
		t.Fatalf("error did not retain provider cause: %v", err)
	}
	var classified port.RetryDispositionError
	if !errors.As(err, &classified) || classified.RetryDisposition() != session.RetryDispositionUnknown {
		t.Fatalf("disposition = %v, want conservative unknown", classified)
	}
}

func TestClassifiedErrorProjectsDirectInterfaces(t *testing.T) {
	tests := []struct {
		name        string
		disposition session.RetryDisposition
	}{
		{name: "unknown", disposition: session.RetryDispositionUnknown},
		{name: "retryable", disposition: session.RetryDispositionRetryable},
		{name: "permanent", disposition: session.RetryDispositionPermanent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := &dispositionOnlyError{disposition: tc.disposition}
			_, err := Wrap(&fakeProvider{steps: []step{{outerErr: original}}}, Config{MaxAttempts: 1}).
				Stream(context.Background(), port.LLMRequest{})
			var classified port.RetryDispositionError
			if !errors.As(err, &classified) {
				t.Fatalf("RetryDispositionError missing (err %v)", err)
			}
			if classified.RetryDisposition() != tc.disposition {
				t.Fatalf("disposition = %v, want %v", classified.RetryDisposition(), tc.disposition)
			}
		})
	}

	alreadyPermanent := &dispositionOnlyError{disposition: session.RetryDispositionPermanent}
	_, err := Wrap(&fakeProvider{steps: []step{{outerErr: alreadyPermanent}}}, Config{MaxAttempts: 1}).
		Stream(context.Background(), port.LLMRequest{})
	if !errors.Is(err, alreadyPermanent) {
		t.Fatalf("existing true permanent error missing from unwrap chain: got %T %v", err, err)
	}
	var progress port.StreamProgressError
	if !errors.As(err, &progress) || progress.StreamProgress() != session.StreamProgressPrecommit {
		t.Fatalf("existing permanent progress = %v, want precommit", progress)
	}
}

func TestDefaultDispositionDistinguishesPermanentRetryableAndUnknown(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want session.RetryDisposition
	}{
		{name: "permanent 4xx", err: apiErr(400), want: session.RetryDispositionPermanent},
		{name: "retryable 5xx", err: apiErr(503), want: session.RetryDispositionRetryable},
		{name: "unknown status", err: apiErr(0), want: session.RetryDispositionUnknown},
		{name: "cancellation is separate", err: context.Canceled, want: session.RetryDispositionUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultDisposition(tc.err); got != tc.want {
				t.Fatalf("DefaultDisposition = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAttemptExhaustionRetainsRetryableDisposition(t *testing.T) {
	failure := &net.OpError{Op: "read", Err: errors.New("reset")}
	f := &fakeProvider{steps: []step{{outerErr: failure}}}
	_, err := Wrap(f, tinyBackoffCfg(2)).Stream(context.Background(), port.LLMRequest{})
	var exhausted *ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("err = %v, want ExhaustedError", err)
	}
	var disposition port.RetryDispositionError
	if !errors.As(err, &disposition) || disposition.RetryDisposition() != session.RetryDispositionRetryable {
		t.Fatalf("retry disposition = %v, want retryable", disposition)
	}
}

func TestFirstRawTentativeChunkEndsEstablishmentTimer(t *testing.T) {
	f := &fakeProvider{steps: []step{{
		chunks: []port.Chunk{
			{Kind: port.ChunkReasoning, Text: "active"},
			{Kind: port.ChunkText, Text: "answer"},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		},
		chunkInterval: 30 * time.Millisecond,
	}}}
	seq, err := Wrap(f, Config{
		MaxAttempts:       1,
		PerAttemptTimeout: 10 * time.Millisecond,
		StreamIdleTimeout: 50 * time.Millisecond,
	}).Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := drain(t, seq); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

func TestIdleWatchdogResetsOnTentativeRawActivity(t *testing.T) {
	f := &fakeProvider{steps: []step{{
		chunks: []port.Chunk{
			{Kind: port.ChunkReasoning, Text: "r1"},
			{Kind: port.ChunkReasoningItem, Text: "r2"},
			{Kind: port.ChunkPhase, Text: "commentary"},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		},
		chunkInterval: 15 * time.Millisecond,
	}}}
	seq, err := Wrap(f, Config{MaxAttempts: 1, StreamIdleTimeout: 25 * time.Millisecond}).
		Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := drain(t, seq); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

type cancelAfterTentativeProvider struct {
	calls   atomic.Int32
	started chan struct{}
	once    sync.Once
}

func (*cancelAfterTentativeProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *cancelAfterTentativeProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	if p.calls.Add(1) > 1 {
		return func(yield func(port.Chunk, error) bool) {
			yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
		}, nil
	}
	return func(yield func(port.Chunk, error) bool) {
		if !yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &session.ToolCall{ID: "discard", Name: "Read"}}, nil) {
			return
		}
		p.once.Do(func() { close(p.started) })
		<-ctx.Done()
		// Deliberately swallow cancellation, as the production adapters do.
	}, nil
}

func TestCancellationAfterTentativeActivityDiscardsAndIsBreakerNeutral(t *testing.T) {
	inner := &cancelAfterTentativeProvider{started: make(chan struct{})}
	wrapped := Wrap(inner, Config{MaxAttempts: 3, BreakerThreshold: 1})
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		seq iter.Seq2[port.Chunk, error]
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		seq, err := wrapped.Stream(ctx, port.LLMRequest{})
		done <- outcome{seq: seq, err: err}
	}()
	<-inner.started
	cancel()
	got := <-done
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", got.err)
	}
	if got.seq != nil {
		t.Fatal("cancelled precommit attempt returned an iterator with tentative chunks")
	}
	if calls := inner.calls.Load(); calls != 1 {
		t.Fatalf("cancelled attempt calls = %d, want 1", calls)
	}

	seq, err := wrapped.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("breaker was affected by cancellation: %v", err)
	}
	chunks, err := drain(t, seq)
	if err != nil || len(chunks) != 1 || chunks[0].Kind != port.ChunkDone {
		t.Fatalf("follow-up chunks = %+v, err = %v", chunks, err)
	}
}

func TestPrecommitIdleStallRetriesWithoutEscapingFailedChunks(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	diag := &recordingDiag{}
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkReasoning, Text: "discard me"}}, stallAfterChunks: true, onChunk: func(int) { once.Do(func() { close(started) }) }},
		{chunks: textTurn("success")},
	}}
	cfg := tinyBackoffCfg(2)
	cfg.StreamIdleTimeout = 100 * time.Millisecond
	cfg.PerAttemptTimeout = time.Second
	cfg.Diagnostics = diag
	type outcome struct {
		chunks []port.Chunk
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		seq, err := Wrap(f, cfg).Stream(context.Background(), port.LLMRequest{})
		if err != nil {
			done <- outcome{err: err}
			return
		}
		chunks, err := drain(t, seq)
		done <- outcome{chunks: chunks, err: err}
	}()
	<-started
	got := <-done
	if got.err != nil {
		t.Fatalf("stream: %v", got.err)
	}
	if f.Calls() != 2 {
		t.Fatalf("attempts = %d, want 2", f.Calls())
	}
	want := textTurn("success")
	if !reflect.DeepEqual(got.chunks, want) {
		t.Fatalf("chunks = %#v, want %#v", got.chunks, want)
	}
	if lines := diag.find("per-attempt timeout fired"); len(lines) != 0 {
		t.Fatalf("precommit idle stall logged establishment timeout: %+v", lines)
	}
}

func TestRepeatedPrecommitIdleStallsExhaustRetryable(t *testing.T) {
	f := &fakeProvider{steps: []step{{
		chunks:           []port.Chunk{{Kind: port.ChunkToolCall, ToolCall: &session.ToolCall{ID: "discard", Name: "Read"}}},
		stallAfterChunks: true,
	}}}
	cfg := tinyBackoffCfg(2)
	cfg.StreamIdleTimeout = 100 * time.Millisecond
	seq, err := Wrap(f, cfg).Stream(context.Background(), port.LLMRequest{})
	if seq != nil {
		t.Fatal("exhausted precommit stalls returned chunks")
	}
	var exhausted *ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("err = %v, want ExhaustedError", err)
	}
	var disposition port.RetryDispositionError
	if !errors.As(err, &disposition) || disposition.RetryDisposition() != session.RetryDispositionRetryable {
		t.Fatalf("disposition = %v, want retryable", disposition)
	}
	var progress port.StreamProgressError
	if !errors.As(err, &progress) || progress.StreamProgress() != session.StreamProgressPrecommit {
		t.Fatalf("progress = %v, want precommit", progress)
	}
	if f.Calls() != 2 {
		t.Fatalf("attempts = %d, want 2", f.Calls())
	}
}

func TestUnknownChunkKindCommitsConservatively(t *testing.T) {
	failure := apiErr(503)
	unknown := port.Chunk{Kind: port.ChunkKind(255), Text: "future"}
	f := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{unknown}, midErr: failure},
		{chunks: textTurn("must not retry")},
	}}
	seq, err := Wrap(f, tinyBackoffCfg(2)).Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	chunks, err := drain(t, seq)
	if !errors.Is(err, failure) {
		t.Fatalf("drain err = %v, want %v", err, failure)
	}
	var disposition port.RetryDispositionError
	var progress port.StreamProgressError
	if !errors.As(err, &disposition) || disposition.RetryDisposition() != session.RetryDispositionRetryable ||
		!errors.As(err, &progress) || progress.StreamProgress() != session.StreamProgressVisible {
		t.Fatalf("visible failure metadata = (%v,%v), want retryable/visible", disposition, progress)
	}
	if len(chunks) != 1 || !reflect.DeepEqual(chunks[0], unknown) {
		t.Fatalf("chunks = %#v, want unknown chunk only", chunks)
	}
	if f.Calls() != 1 {
		t.Fatalf("attempts = %d, want 1", f.Calls())
	}
}

type flushCleanupProvider struct {
	stopped chan struct{}
}

func (*flushCleanupProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *flushCleanupProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		defer close(p.stopped)
		if !yield(port.Chunk{Kind: port.ChunkText, Text: " "}, nil) {
			return
		}
		if !yield(port.Chunk{Kind: port.ChunkReasoning, Text: "tentative"}, nil) {
			return
		}
		if !yield(port.Chunk{Kind: port.ChunkText, Text: "visible"}, nil) {
			return
		}
		<-ctx.Done()
	}, nil
}

func TestConsumerStopDuringBufferedFlushCleansUpInnerIterator(t *testing.T) {
	inner := &flushCleanupProvider{stopped: make(chan struct{})}
	seq, err := Wrap(inner, Config{MaxAttempts: 1, StreamIdleTimeout: time.Second}).
		Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range seq {
		break
	}
	select {
	case <-inner.stopped:
	case <-time.After(time.Second):
		t.Fatal("inner iterator was not stopped after consumer exited during buffered flush")
	}
}
