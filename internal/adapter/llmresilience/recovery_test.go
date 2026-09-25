package llmresilience

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type recoveryHintError struct {
	error
	at time.Time
}

func (e recoveryHintError) RetryNotBefore() (time.Time, bool) { return e.at, true }
func (e recoveryHintError) Unwrap() error                     { return e.error }

type safeRecoveryHint struct {
	recoveryHintError
	display string
}

func (e safeRecoveryHint) Error() string { return e.display }

func recoveryConfig(attempts int, budget time.Duration) Config {
	return Config{MaxAttempts: attempts, RecoveryBudget: budget}
}

func recoveryDrain(t *testing.T, p port.LLMProvider, ctx context.Context) ([]port.Chunk, error) {
	t.Helper()
	seq, err := p.Stream(ctx, port.LLMRequest{})
	if err != nil {
		return nil, err
	}
	return drain(t, seq)
}

func requirePrecommitRetryable(t *testing.T, err error) {
	t.Helper()
	var progress port.StreamProgressError
	if dispositionOf(err) != session.RetryDispositionRetryable || !errors.As(err, &progress) || progress.StreamProgress() != session.StreamProgressPrecommit {
		t.Fatalf("error = %T %v, want retryable+precommit", err, err)
	}
}

func TestServerProviderRecovery_Scenario1_VisibleFailureNeverReplays(t *testing.T) {
	f := &fakeProvider{steps: []step{{chunks: []port.Chunk{{Kind: port.ChunkText, Text: "visible"}}, midErr: apiErr(503)}}}
	got, err := recoveryDrain(t, Wrap(f, recoveryConfig(5, time.Second)), context.Background())
	if err == nil || len(got) != 1 || f.Calls() != 1 {
		t.Fatalf("chunks=%v err=%v calls=%d", got, err, f.Calls())
	}
	TestSemanticAttemptCleanCompletionFlushesTentativeTurn(t)
}

func TestServerProviderRecovery_Scenario1_TentativeAssemblyAndDiscardedUsageExactlyOnce(t *testing.T) {
	for _, outcome := range []string{"success", "exhaustion", "cancellation"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			usage := session.Usage{InputTokens: 7, OutputTokens: 3}
			tentative := []port.Chunk{{Kind: port.ChunkText, Text: " \n"}, {Kind: port.ChunkReasoning, Text: "hidden"}, {Kind: port.ChunkToolCall, ToolCall: &session.ToolCall{ID: "hidden", Name: "Write"}}, {Kind: port.ChunkUsage, Usage: &usage}}
			second := step{chunks: textTurn("ok")}
			if outcome == "exhaustion" {
				second = step{chunks: tentative, midErr: apiErr(503)}
			}
			if outcome == "cancellation" {
				second = step{chunks: tentative, onChunk: func(i int) {
					if i == len(tentative)-1 {
						cancel()
					}
				}}
			}
			f := &fakeProvider{steps: []step{{chunks: tentative, midErr: apiErr(503)}, second}}
			got, err := recoveryDrain(t, Wrap(f, recoveryConfig(2, time.Second)), ctx)
			if (err == nil) != (outcome == "success") {
				t.Fatalf("err=%v", err)
			}
			total := session.Usage{}
			for _, c := range got {
				total = total.Add(discardedChunkUsage(c))
				if c.Kind != port.ChunkUsage && c.Kind != port.ChunkDone && !(c.Kind == port.ChunkText && c.Text == "ok") {
					t.Fatalf("tentative chunk escaped: %+v", c)
				}
			}
			want := usage
			if outcome != "success" {
				want = usage.Add(usage)
			}
			if total != want {
				t.Fatalf("usage=%+v want=%+v", total, want)
			}
		})
	}
}

func TestServerProviderRecovery_Scenario2_EffectiveDelayNeverRetriesEarly(t *testing.T) {
	t.Run("maximum of provider and breaker", func(t *testing.T) {
		for _, providerDominates := range []bool{false, true} {
			providerDelay, breakerDelay := 10*time.Millisecond, 30*time.Millisecond
			if providerDominates {
				providerDelay, breakerDelay = breakerDelay, providerDelay
			}
			start := time.Now()
			eligible := start.Add(max(providerDelay, breakerDelay))
			f := &fakeProvider{steps: []step{{outerErr: recoveryHintError{apiErr(503), start.Add(providerDelay)}}, {chunks: textTurn("ok")}}, onAttempt: func(_ context.Context, n int) {
				if n > 0 && time.Now().Before(eligible) {
					t.Error("call before effective lower bound")
				}
			}}
			cfg := recoveryConfig(2, time.Second)
			cfg.BreakerThreshold = 1
			cfg.BreakerCooldown = breakerDelay
			_, err := recoveryDrain(t, Wrap(f, cfg), context.Background())
			if err != nil || f.Calls() != 2 {
				t.Fatalf("err=%v calls=%d", err, f.Calls())
			}
		}
	})
	t.Run("saturated date never becomes schedulable", func(t *testing.T) {
		now := time.Date(9999, 12, 30, 0, 0, 0, 0, time.UTC)
		r := &recoveryWindow{deadline: now.Add(time.Duration(1<<63 - 1))}
		_, _, ok := retryAt(recoveryHintError{apiErr(429), unschedulableRetry}, now, 0, r)
		if ok {
			t.Fatal("sentinel treated as eligible under enormous budget")
		}
	})
	t.Run("sanitized cause and target survive", func(t *testing.T) {
		raw := apiErr(429)
		raw.Message = "BODY-SECRET"
		raw.Request.URL.User = url.UserPassword("USER-SECRET", "PASSWORD-SECRET")
		raw.Request.URL.RawQuery = "token=QUERY-SECRET"
		display := port.AppendHTTPErrorDisplay("rate limited", raw.Request, "")
		failure := safeRecoveryHint{recoveryHintError: recoveryHintError{raw, time.Now().Add(time.Hour)}, display: display}
		diag := &recordingDiag{}
		cfg := recoveryConfig(2, time.Second)
		cfg.Diagnostics = diag
		var observed []session.NetworkAttemptPayload
		ctx := port.WithAttemptObserver(context.Background(), func(o session.NetworkAttemptPayload) { observed = append(observed, o) })
		f := &fakeProvider{steps: []step{{outerErr: failure}}}
		_, err := recoveryDrain(t, Wrap(f, cfg), ctx)
		requirePrecommitRetryable(t, err)
		if !errors.Is(err, raw) || !strings.Contains(err.Error(), "target: https://api.example/v1/responses") {
			t.Fatalf("lost safe cause/display: %v", err)
		}
		rendered := fmt.Sprint(err, diag.records, observed)
		if strings.Contains(rendered, "SECRET") {
			t.Fatalf("raw provider values escaped: %s", rendered)
		}
	})
	t.Run("provider lower bound", func(t *testing.T) {
		notBefore := time.Now().Add(25 * time.Millisecond)
		f := &fakeProvider{steps: []step{{outerErr: recoveryHintError{apiErr(429), notBefore}}, {chunks: textTurn("ok")}}, onAttempt: func(_ context.Context, n int) {
			if n > 0 && time.Now().Before(notBefore) {
				t.Error("retry before provider eligibility")
			}
		}}
		_, err := recoveryDrain(t, Wrap(f, recoveryConfig(2, time.Second)), context.Background())
		if err != nil || f.Calls() != 2 {
			t.Fatalf("err=%v calls=%d", err, f.Calls())
		}
	})
	for _, at := range []time.Time{time.Now().Add(time.Hour), time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)} {
		t.Run(at.String(), func(t *testing.T) {
			f := &fakeProvider{steps: []step{{outerErr: recoveryHintError{apiErr(429), at}}, {chunks: textTurn("early")}}}
			_, err := recoveryDrain(t, Wrap(f, recoveryConfig(2, time.Second)), context.Background())
			requirePrecommitRetryable(t, err)
			if f.Calls() != 1 {
				t.Fatalf("calls=%d", f.Calls())
			}
		})
	}
}

func TestServerProviderRecovery_Scenario4_NonSlidingBudgetAndActualCallCap(t *testing.T) {
	t.Run("initial call has no recovery deadline", func(t *testing.T) {
		f := &fakeProvider{steps: []step{{chunks: []port.Chunk{{Kind: port.ChunkReasoning, Text: "thinking"}, {Kind: port.ChunkText, Text: "visible"}, {Kind: port.ChunkDone}}, chunkInterval: 15 * time.Millisecond}}}
		_, err := recoveryDrain(t, Wrap(f, recoveryConfig(2, time.Millisecond)), context.Background())
		if err != nil || f.Calls() != 1 {
			t.Fatalf("err=%v calls=%d", err, f.Calls())
		}
	})
	t.Run("multiple failures share first deadline", func(t *testing.T) {
		clock := &manualClock{t: time.Unix(1, 0)}
		f := &fakeProvider{steps: []step{{outerErr: apiErr(503)}}, onAttempt: func(_ context.Context, n int) {
			if n > 0 {
				clock.Advance(400 * time.Millisecond)
			}
		}}
		cfg := recoveryConfig(10, time.Second)
		cfg.Clock = clock.Now
		_, err := recoveryDrain(t, Wrap(f, cfg), context.Background())
		requirePrecommitRetryable(t, err)
		if f.Calls() != 4 {
			t.Fatalf("calls=%d, budget slid", f.Calls())
		}
	})
	t.Run("reasoning activity does not slide", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		reasoning := make([]port.Chunk, 100)
		for i := range reasoning {
			reasoning[i] = port.Chunk{Kind: port.ChunkReasoning, Text: "active"}
		}
		f := &fakeProvider{steps: []step{{outerErr: apiErr(503)}, {chunks: reasoning, chunkInterval: 2 * time.Millisecond, stallAfterChunks: true}}}
		start := time.Now()
		_, err := recoveryDrain(t, Wrap(f, recoveryConfig(10, 25*time.Millisecond)), ctx)
		requirePrecommitRetryable(t, err)
		if f.Calls() != 2 || time.Since(start) > 500*time.Millisecond {
			t.Fatalf("calls=%d elapsed=%s", f.Calls(), time.Since(start))
		}
	})
	t.Run("actual call cap", func(t *testing.T) {
		f := &fakeProvider{steps: []step{{outerErr: apiErr(503)}}}
		_, err := recoveryDrain(t, Wrap(f, recoveryConfig(3, time.Second)), context.Background())
		requirePrecommitRetryable(t, err)
		if f.Calls() != 3 {
			t.Fatalf("calls=%d", f.Calls())
		}
	})
}

func TestServerProviderRecovery_Scenario4_RecoveryDeadlineCannotCancelVisibleStream(t *testing.T) {
	for _, kind := range []port.ChunkKind{port.ChunkText, port.ChunkDone} {
		t.Run(fmt.Sprint("expiry wins ", kind), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			f := &fakeProvider{steps: []step{{outerErr: apiErr(503)}, {chunks: []port.Chunk{{Kind: kind, Text: "too late"}}, firstChunkAfterCancel: true}}}
			got, err := recoveryDrain(t, Wrap(f, recoveryConfig(3, 20*time.Millisecond)), ctx)
			requirePrecommitRetryable(t, err)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost provider cause to local cancellation: %v", err)
			}
			if len(got) != 0 || f.Calls() != 2 {
				t.Fatalf("got=%v calls=%d", got, f.Calls())
			}
		})
	}
	t.Run("commit wins", func(t *testing.T) {
		f := &fakeProvider{steps: []step{{outerErr: apiErr(503)}, {chunks: textTurn("visible"), chunkInterval: 70 * time.Millisecond}}}
		got, err := recoveryDrain(t, Wrap(f, recoveryConfig(3, 30*time.Millisecond)), context.Background())
		if err != nil || len(got) != 3 || got[2].Kind != port.ChunkDone {
			t.Fatalf("got=%v err=%v", got, err)
		}
	})
}

func TestServerProviderRecovery_Scenario4_ZeroBudgetHonorsNotBeforeWithoutEarlyCall(t *testing.T) {
	t.Run("ordinary backoff already satisfies future hint", func(t *testing.T) {
		now := time.Now()
		backoff := 20 * time.Millisecond
		at, source, ok := retryAt(recoveryHintError{apiErr(429), now.Add(10 * time.Millisecond)}, now, backoff, &recoveryWindow{})
		if !ok || source != "backoff" || !at.Equal(now.Add(backoff)) {
			t.Fatalf("at=%v source=%s eligible=%v", at, source, ok)
		}
	})
	t.Run("manual action starts fresh", func(t *testing.T) {
		f := &fakeProvider{steps: []step{{outerErr: recoveryHintError{apiErr(429), unschedulableRetry}}, {chunks: textTurn("manual success")}}}
		p := Wrap(f, recoveryConfig(2, 0))
		_, err := recoveryDrain(t, p, context.Background())
		requirePrecommitRetryable(t, err)
		_, err = recoveryDrain(t, p, context.Background())
		if err != nil || f.Calls() != 2 {
			t.Fatalf("err=%v calls=%d", err, f.Calls())
		}
	})
	for _, future := range []bool{false, true} {
		at := time.Now().Add(-time.Second)
		if future {
			at = time.Now().Add(time.Hour)
		}
		f := &fakeProvider{steps: []step{{outerErr: recoveryHintError{apiErr(429), at}}, {chunks: textTurn("ok")}}}
		_, err := recoveryDrain(t, Wrap(f, recoveryConfig(2, 0)), context.Background())
		if future {
			requirePrecommitRetryable(t, err)
			if f.Calls() != 1 {
				t.Fatalf("early calls=%d", f.Calls())
			}
		} else if err != nil || f.Calls() != 2 {
			t.Fatalf("err=%v calls=%d", err, f.Calls())
		}
	}
}

func TestServerProviderRecovery_Scenario3_ExclusiveHalfOpenProbeAndNotifiedWaiters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var calls atomic.Int32
	entered := make(chan struct{}, 10)
	release := make(chan struct{})
	inner := recoveryProviderFunc(func(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
		if calls.Add(1) == 1 {
			return nil, apiErr(503)
		}
		entered <- struct{}{}
		return func(yield func(port.Chunk, error) bool) {
			if !yield(port.Chunk{Kind: port.ChunkText, Text: "visible probe"}, nil) {
				return
			}
			select {
			case <-release:
				yield(port.Chunk{Kind: port.ChunkDone}, nil)
			case <-ctx.Done():
			}
		}, nil
	})
	cfg := recoveryConfig(1, time.Second)
	cfg.BreakerThreshold = 1
	cfg.BreakerCooldown = 10 * time.Millisecond
	p := Wrap(inner, cfg)
	_, _ = recoveryDrain(t, p, ctx)
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Go(func() { _, err := recoveryDrain(t, p, ctx); errs <- err })
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("no probe admitted")
	}
	select {
	case <-entered:
		t.Error("multiple half-open probes")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("waiter failed: %v", err)
		}
	}
	if calls.Load() != 6 {
		t.Fatalf("calls=%d want 6", calls.Load())
	}
}

type recoveryProviderFunc func(context.Context, port.LLMRequest) (iter.Seq2[port.Chunk, error], error)

func (f recoveryProviderFunc) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return f(ctx, req)
}
func (recoveryProviderFunc) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}
