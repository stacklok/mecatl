package llmresilience

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestBufferedUsagePrecedesSemanticsAndIsNotDuplicated(t *testing.T) {
	a := session.Usage{InputTokens: 5, OutputTokens: 1}
	b := session.Usage{InputTokens: 7, OutputTokens: 3}
	inner := &fakeProvider{steps: []step{
		{chunks: []port.Chunk{{Kind: port.ChunkUsage, Usage: &a}}, midErr: context.DeadlineExceeded},
		{chunks: []port.Chunk{
			{Kind: port.ChunkReasoning, Text: "reason"},
			{Kind: port.ChunkToolCall},
			{Kind: port.ChunkUsage, Usage: &b},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		}},
	}}
	seq, err := Wrap(inner, Config{MaxAttempts: 2, Classifier: func(error) bool { return true }}).Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := drain(t, seq)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Kind != port.ChunkUsage || *got[0].Usage != a.Add(b) ||
		got[1].Kind != port.ChunkReasoning || got[2].Kind != port.ChunkToolCall || got[3].Kind != port.ChunkDone {
		t.Fatalf("buffered order/accounting = %+v", got)
	}
}

func TestBufferedUsageCancelNeverPullsVisibleContinuation(t *testing.T) {
	usage := session.Usage{InputTokens: 7, OutputTokens: 3}
	pulled := false
	inner := &fakeProvider{steps: []step{{chunks: []port.Chunk{
		{Kind: port.ChunkUsage, Usage: &usage},
		{Kind: port.ChunkText, Text: "visible"},
		{Kind: port.ChunkUsage, Usage: &usage},
	}, onChunk: func(i int) {
		if i == 2 {
			pulled = true
		}
	}}}}
	ctx, cancel := context.WithCancel(context.Background())
	seq, err := Wrap(inner, Config{MaxAttempts: 1}).Stream(ctx, port.LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	got, err := drain(t, seq)
	if len(got) != 1 || got[0].Kind != port.ChunkUsage || *got[0].Usage != usage || !errors.Is(err, context.Canceled) || pulled {
		t.Fatalf("cancelled stream chunks=%+v err=%v continuation pulled=%v", got, err, pulled)
	}
}

func TestUsageAcceptedImmediatelyBeforePumpCancellation(t *testing.T) {
	usage := session.Usage{InputTokens: 3, OutputTokens: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := &fakeProvider{steps: []step{{chunks: []port.Chunk{
		{Kind: port.ChunkUsage, Usage: &usage},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}, onChunk: func(i int) {
		if i == 0 {
			cancel()
		}
	}}}}
	seq, err := Wrap(inner, Config{MaxAttempts: 1}).Stream(ctx, port.LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := drain(t, seq)
	if len(got) != 1 || got[0].Kind != port.ChunkUsage || *got[0].Usage != usage || !errors.Is(err, context.Canceled) {
		t.Fatalf("pump cancellation chunks=%+v err=%v", got, err)
	}
}

func TestBufferedZeroUsageCancellationIsNotSuccess(t *testing.T) {
	inner := &fakeProvider{steps: []step{{chunks: []port.Chunk{{Kind: port.ChunkDone, Stop: session.StopEndTurn}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	provider := Wrap(inner, Config{MaxAttempts: 1, BreakerThreshold: 1}).(*resilientProvider)
	provider.breaker.open = true // half-open probe must not close on cancellation
	seq, err := provider.Stream(ctx, port.LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	got, err := drain(t, seq)
	if len(got) != 0 || !errors.Is(err, context.Canceled) || !provider.breaker.open {
		t.Fatalf("zero-usage cancel chunks=%+v err=%v breaker open=%v", got, err, provider.breaker.open)
	}
}

func TestBufferedDoneCancellationDoesNotCloseBreaker(t *testing.T) {
	inner := &fakeProvider{steps: []step{{chunks: []port.Chunk{{Kind: port.ChunkDone, Stop: session.StopEndTurn}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := Wrap(inner, Config{MaxAttempts: 1, BreakerThreshold: 1}).(*resilientProvider)
	provider.breaker.open = true // half-open probe must not close on cancellation
	seq, err := provider.Stream(ctx, port.LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}

	var got []port.Chunk
	var streamErr error
	seq(func(chunk port.Chunk, err error) bool {
		if err != nil {
			streamErr = err
			return false
		}
		got = append(got, chunk)
		if chunk.Kind == port.ChunkDone {
			cancel()
		}
		return true
	})
	if len(got) != 1 || got[0].Kind != port.ChunkDone || !errors.Is(ctx.Err(), context.Canceled) ||
		!errors.Is(streamErr, context.Canceled) || !provider.breaker.open {
		t.Fatalf("buffered done cancellation chunks=%+v ctx=%v err=%v breaker open=%v", got, ctx.Err(), streamErr, provider.breaker.open)
	}
}
