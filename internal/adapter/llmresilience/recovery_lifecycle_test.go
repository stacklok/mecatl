package llmresilience

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestServerProviderRecovery_Scenario3_ProbeReleaseAndStaleGenerationIsolation(t *testing.T) {
	t.Run("stale outcomes cannot change newer probe", func(t *testing.T) {
		clock := &manualClock{t: time.Unix(100, 0)}
		p := Wrap(&fakeProvider{}, Config{BreakerThreshold: 1, BreakerCooldown: time.Second, Clock: clock.Now}).(*resilientProvider)
		staleSuccess, _, _ := p.allow(context.Background(), clock.Now())
		staleFailure, _, _ := p.allow(context.Background(), clock.Now())
		first, _, _ := p.allow(context.Background(), clock.Now())
		first.finish(breakerFailure)
		first.release()
		_, _, beforeProbe := p.allow(context.Background(), clock.Now())
		clock.Advance(2 * time.Second)
		probe, err, _ := p.allow(context.Background(), clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		defer probe.release()
		select {
		case <-beforeProbe:
		default:
			t.Fatal("half-open transition did not notify waiters")
		}
		opened, generation := p.breaker.openedAt, p.breaker.generation
		staleSuccess.finish(breakerSuccess)
		staleSuccess.release()
		staleFailure.finish(breakerFailure)
		staleFailure.release()
		if !p.breaker.open || !p.breaker.halfOpen || p.breaker.openedAt != opened || p.breaker.generation != generation {
			t.Fatal("stale outcome changed probe")
		}
		_, rejected, changed := p.allow(context.Background(), clock.Now())
		if rejected == nil {
			t.Fatal("second probe admitted")
		}
		probe.release()
		select {
		case <-changed:
		default:
			t.Fatal("neutral release did not notify waiters")
		}
		next, err, _ := p.allow(context.Background(), clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		defer next.release()
		next.finish(breakerSuccess)
		if p.breaker.open {
			t.Fatal("successful probe did not close")
		}
	})
	for _, outcome := range []string{"abandon", "cancel before iteration", "cancel during iteration", "permanent", "transient", "complete"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				stopped := make(chan struct{})
				calls := 0
				inner := recoveryProviderFunc(func(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
					calls++
					if calls == 1 {
						return nil, apiErr(503)
					}
					return func(yield func(port.Chunk, error) bool) {
						defer close(stopped)
						if !yield(port.Chunk{Kind: port.ChunkText, Text: "visible"}, nil) {
							return
						}
						switch outcome {
						case "permanent":
							yield(port.Chunk{}, apiErr(400))
						case "transient":
							yield(port.Chunk{}, apiErr(503))
						case "cancel during iteration":
							cancel()
							<-ctx.Done()
						default:
							yield(port.Chunk{Kind: port.ChunkDone}, nil)
						}
					}, nil
				})
				cfg := recoveryConfig(1, time.Second)
				cfg.BreakerThreshold = 1
				p := Wrap(inner, cfg).(*resilientProvider)
				_, _ = p.Stream(context.Background(), port.LLMRequest{})
				seq, err := p.Stream(ctx, port.LLMRequest{})
				if err != nil {
					t.Fatal(err)
				}
				p.breaker.mu.Lock()
				half := p.breaker.halfOpen
				changed := p.breaker.changed
				p.breaker.mu.Unlock()
				if !half {
					t.Fatal("visibility released ownership")
				}
				switch outcome {
				case "cancel before iteration":
					cancel()
				case "abandon":
					seq(func(port.Chunk, error) bool { return false })
				default:
					_, _ = drain(t, seq)
				}
				synctest.Wait()
				select {
				case <-stopped:
				default:
					// Always unwind the old implementation's suspended iterator before failing.
					seq(func(port.Chunk, error) bool { return false })
					t.Fatal("cancellation retained the unconsumed iterator")
				}
				select {
				case <-changed:
				default:
					t.Fatal("probe release did not notify")
				}
				// The notification makes the cancellation callback's health result observable.
				p.breaker.mu.Lock()
				half = p.breaker.halfOpen
				open := p.breaker.open
				failures := p.breaker.consecutiveFailures
				p.breaker.mu.Unlock()
				if half {
					t.Fatal("completed operation retained probe")
				}
				if outcome == "complete" {
					if open || failures != 0 {
						t.Fatal("clean completion did not recover")
					}
				} else if !open {
					t.Fatal("non-success closed breaker")
				}
			})
		})
	}
}

func TestServerProviderRecovery_Scenario4_CancellationAndTerminalClassification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls int
		inner := recoveryProviderFunc(func(attemptCtx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
			calls++
			if calls == 1 {
				return nil, apiErr(503)
			}
			return func(func(port.Chunk, error) bool) {
				<-attemptCtx.Done()
				cancel()
			}, nil
		})
		_, err := recoveryDrain(ctx, t, Wrap(inner, recoveryConfig(2, time.Second)))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("simultaneous caller cancellation lost to local expiry: %v", err)
		}
		if calls != 2 {
			t.Fatalf("calls=%d want 2", calls)
		}
	})
	t.Run("budget expires while another probe owns admission", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			cfg := recoveryConfig(1, 15*time.Second)
			cfg.BreakerThreshold = 1
			f := &fakeProvider{steps: []step{{outerErr: apiErr(503)}, {chunks: textTurn("probe")}}}
			p := Wrap(f, cfg).(*resilientProvider)
			_, _ = p.Stream(context.Background(), port.LLMRequest{})
			seq, err := p.Stream(context.Background(), port.LLMRequest{})
			if err != nil {
				t.Fatal(err)
			}
			defer seq(func(port.Chunk, error) bool { return false })
			observations := 0
			ctx := port.WithAttemptObserver(context.Background(), func(session.NetworkAttemptPayload) { observations++ })
			_, err = p.Stream(ctx, port.LLMRequest{})
			requirePrecommitRetryable(t, err)
			var breaker *BreakerError
			if !errors.As(err, &breaker) || observations != 0 || f.Calls() != 2 {
				t.Fatalf("err=%v observations=%d calls=%d", err, observations, f.Calls())
			}
			p.breaker.mu.Lock()
			half, failures := p.breaker.halfOpen, p.breaker.consecutiveFailures
			p.breaker.mu.Unlock()
			if !half || failures != 1 {
				t.Fatal("local admission expiry changed probe health")
			}
		})
	})
	t.Run("breaker-only exhaustion", func(t *testing.T) {
		f := &fakeProvider{steps: []step{{outerErr: apiErr(503)}}}
		cfg := recoveryConfig(1, 10*time.Millisecond)
		cfg.BreakerThreshold = 1
		cfg.BreakerCooldown = time.Hour
		p := Wrap(f, cfg)
		_, _ = p.Stream(context.Background(), port.LLMRequest{})
		_, err := p.Stream(context.Background(), port.LLMRequest{})
		requirePrecommitRetryable(t, err)
		var breaker *BreakerError
		if !errors.As(err, &breaker) || f.Calls() != 1 {
			t.Fatalf("err=%v calls=%d", err, f.Calls())
		}
	})
	for _, tc := range []struct {
		name       string
		err        error
		classifier func(error) bool
	}{
		{"permanent", apiErr(400), nil}, {"unknown", errors.New("unknown"), nil},
		{"classifier veto", apiErr(503), func(error) bool { return false }},
		{"provider veto", &providerRetryVeto{err: apiErr(503)}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeProvider{steps: []step{{outerErr: tc.err}}}
			cfg := recoveryConfig(5, time.Second)
			cfg.Classifier = tc.classifier
			_, err := recoveryDrain(context.Background(), t, Wrap(f, cfg))
			if !errors.Is(err, tc.err) || f.Calls() != 1 {
				t.Fatalf("err=%v calls=%d", err, f.Calls())
			}
		})
	}
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint("caller wins ", deadline), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				expected := context.Canceled
				if deadline {
					ctx, cancel = context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					expected = context.DeadlineExceeded
				}
				f := &fakeProvider{steps: []step{{outerErr: apiErr(503)}, {block: true}}, onAttempt: func(_ context.Context, n int) {
					if n == 1 && !deadline {
						cancel()
					}
				}}
				cfg := recoveryConfig(3, time.Minute)
				cfg.BreakerThreshold = 2
				p := Wrap(f, cfg).(*resilientProvider)
				_, err := recoveryDrain(ctx, t, p)
				if !errors.Is(err, expected) {
					t.Fatalf("err=%v want %v", err, expected)
				}
				if p.breaker.consecutiveFailures != 1 || p.breaker.open {
					t.Fatal("local expiry/caller cancellation changed health")
				}
			})
		})
	}
}

type providerRetryVeto struct{ err error }

func (e *providerRetryVeto) Error() string { return e.err.Error() }
func (e *providerRetryVeto) Unwrap() error { return e.err }
func (*providerRetryVeto) Retryable() bool { return false }

func TestServerProviderRecovery_Scenario7_SanitizedOperationalLogsAndStableAttemptVocabulary(t *testing.T) {
	for _, mode := range []string{"provider wait", "breaker only", "budget cancels call", "recovered"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				diag := &recordingDiag{}
				cfg := recoveryConfig(3, 2*time.Minute)
				cfg.Diagnostics = diag
				f := &fakeProvider{steps: []step{{outerErr: apiErr(503)}, {chunks: textTurn("ok")}}}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "provider wait" {
					f.steps[0].outerErr = recoveryHintError{apiErr(429), time.Now().Add(2 * time.Minute)}
					// Cancel from the diagnostic sink, after observing the planned long wait.
					cfg.Diagnostics = &cancelWaitDiag{recordingDiag: diag, cancel: cancel}
				}
				if mode == "breaker only" {
					cfg.MaxAttempts = 1
					cfg.BreakerThreshold = 1
					cfg.BreakerCooldown = time.Hour
				}
				if mode == "budget cancels call" {
					cfg.RecoveryBudget = 15 * time.Second
					f.steps[1] = step{chunks: []port.Chunk{{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 9}}}, stallAfterChunks: true}
				}
				p := Wrap(f, cfg)
				if mode == "breaker only" {
					_, _ = p.Stream(ctx, port.LLMRequest{})
					diag.records = nil
				}
				var observed []session.NetworkAttemptPayload
				ctx = port.WithAttemptObserver(ctx, func(o session.NetworkAttemptPayload) { observed = append(observed, o) })
				got, err := recoveryDrain(ctx, t, p)
				if mode == "recovered" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil {
					t.Fatal("expected terminal")
				}
				if mode == "budget cancels call" {
					if len(got) != 1 || got[0].Usage.InputTokens != 9 {
						t.Fatalf("lost usage: %v", got)
					}
					requirePrecommitRetryable(t, err)
				}
				want := 1
				if mode == "breaker only" {
					want = 0
				}
				if len(observed) != want {
					t.Fatalf("observations=%+v", observed)
				}
				for _, o := range observed {
					if _, valid := session.CanonicalNetworkAttempt(o, "test", 1, 1); !valid {
						t.Fatalf("invalid observation: %+v", o)
					}
					if o.Attempt != 1 || o.SuppressionReason != "" {
						t.Fatalf("fabricated terminal: %+v", o)
					}
				}
				records := diag.find("llm provider recovery")
				if len(records) == 0 {
					t.Fatal("missing recovery decision")
				}
				for _, record := range records {
					if record.level != port.LevelInfo {
						t.Fatal("recovery was not INFO")
					}
					for _, key := range []string{"decision", "attempt", "max_attempts", "wait", "remaining_budget", "source"} {
						if argValue(record.args, key) == nil {
							t.Fatalf("missing %s", key)
						}
					}
					if strings.Contains(fmt.Sprint(record), "recovery_budget_exhausted") {
						t.Fatal("new serialized reason")
					}
				}
				if mode == "provider wait" && len(records) != 2 {
					t.Fatalf("want one wait and terminal, got %v", records)
				}
			})
		})
	}
}

type cancelWaitDiag struct {
	*recordingDiag
	cancel context.CancelFunc
}

func (d *cancelWaitDiag) Log(ctx context.Context, level port.Level, msg string, args ...any) {
	d.recordingDiag.Log(ctx, level, msg, args...)
	if msg == "llm provider recovery" && argValue(args, "decision") == "wait" {
		d.cancel()
	}
}

func TestRecoveryBudgetStartsBeforeHealthDiagnostics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeProvider{steps: []step{{outerErr: apiErr(503)}, {chunks: textTurn("too late")}}}
		cfg := recoveryConfig(2, time.Minute)
		cfg.BreakerThreshold = 1
		cfg.Diagnostics = &delayHealthDiag{recordingDiag: &recordingDiag{}}
		_, err := recoveryDrain(context.Background(), t, Wrap(f, cfg))
		requirePrecommitRetryable(t, err)
		if f.Calls() != 1 {
			t.Fatalf("calls=%d, budget slid past health log", f.Calls())
		}
	})
}

type delayHealthDiag struct {
	*recordingDiag
}

func (d *delayHealthDiag) Log(ctx context.Context, level port.Level, msg string, args ...any) {
	d.recordingDiag.Log(ctx, level, msg, args...)
	if msg == "llm circuit breaker opened" {
		<-time.After(2 * time.Minute)
	}
}

func TestRecoveryHalfOpenWaitIsOperationallyVisibleAndCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	diag := &recordingDiag{}
	cfg := recoveryConfig(1, 2*time.Second)
	cfg.BreakerThreshold = 1
	cfg.Diagnostics = &cancelWaitDiag{recordingDiag: diag, cancel: cancel}
	f := &fakeProvider{steps: []step{{chunks: textTurn("must not call")}}}
	p := Wrap(f, cfg).(*resilientProvider)
	p.recordOutcome(0, false, breakerFailure)
	lease, rejection, _ := p.allow(context.Background(), time.Now())
	if rejection != nil {
		t.Fatal(rejection)
	}
	defer lease.release()
	_, err := recoveryDrain(ctx, t, p)
	if !errors.Is(err, context.Canceled) || f.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, f.Calls())
	}
	records := diag.find("llm provider recovery")
	if len(records) != 2 || argValue(records[0].args, "decision") != "wait" || argValue(records[0].args, "source") != "breaker" {
		t.Fatalf("logs=%v", records)
	}
	if !p.breaker.halfOpen || p.breaker.consecutiveFailures != 1 {
		t.Fatal("canceled admission changed health or ownership")
	}
}

func TestRecoveryFirstChunkTimeoutRetainsReceivedUsage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		usage := session.Usage{InputTokens: 11}
		f := &fakeProvider{steps: []step{{chunks: []port.Chunk{{Kind: port.ChunkUsage, Usage: &usage}}, firstChunkAfterCancel: true}, {chunks: textTurn("ok")}}}
		cfg := recoveryConfig(2, time.Minute)
		cfg.PerAttemptTimeout = 10 * time.Second
		got, err := recoveryDrain(context.Background(), t, Wrap(f, cfg))
		if err != nil {
			t.Fatal(err)
		}
		total := session.Usage{}
		for _, c := range got {
			total = total.Add(discardedChunkUsage(c))
		}
		if total.InputTokens != 11 {
			t.Fatalf("usage=%+v", total)
		}
	})
}

func TestRecoveryTerminalCancellationDominatesDuringDiagnostic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := recoveryConfig(2, time.Second)
	cfg.Diagnostics = &terminalCancelDiag{recordingDiag: &recordingDiag{}, cancel: cancel}
	f := &fakeProvider{steps: []step{{outerErr: recoveryHintError{apiErr(429), time.Now().Add(time.Hour)}}}}
	_, err := recoveryDrain(ctx, t, Wrap(f, cfg))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want caller cancellation", err)
	}
}

type terminalCancelDiag struct {
	*recordingDiag
	cancel context.CancelFunc
}

func (d *terminalCancelDiag) Log(ctx context.Context, level port.Level, msg string, args ...any) {
	d.recordingDiag.Log(ctx, level, msg, args...)
	if msg == "llm provider recovery" && argValue(args, "decision") == "terminal" {
		d.cancel()
	}
}

func TestRecoveryVisibleCallerCancellationIsHealthNeutral(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := recoveryProviderFunc(func(context.Context, port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
		return func(yield func(port.Chunk, error) bool) {
			if !yield(port.Chunk{Kind: port.ChunkText, Text: "visible"}, nil) {
				return
			}
			cancel()
			yield(port.Chunk{}, apiErr(503))
		}, nil
	})
	cfg := recoveryConfig(2, time.Second)
	cfg.BreakerThreshold = 1
	p := Wrap(inner, cfg).(*resilientProvider)
	_, _ = recoveryDrain(ctx, t, p)
	if p.breaker.open || p.breaker.consecutiveFailures != 0 {
		t.Fatal("visible caller cancellation changed provider health")
	}
}

func TestRecoveryBackoffOverflowDoesNotBecomeImmediate(t *testing.T) {
	p := &resilientProvider{cfg: Config{BaseBackoff: 1 << 62}}
	if got := p.backoffDuration(2); got <= 0 {
		t.Fatalf("overflow shortened backoff to %s", got)
	}
}
