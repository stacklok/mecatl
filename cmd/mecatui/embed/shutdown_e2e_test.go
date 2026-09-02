package embed

import (
	"context"
	"iter"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
	"github.com/stacklok/mecatl/internal/app"
)

// blockingProvider is a port.LLMProvider that BLOCKS its Stream call until the
// context is cancelled. It closes entered on the first Stream invocation so the
// test can synchronise on "the fire has started its LLM call".
type blockingProvider struct {
	entered chan struct{}
	calls   atomic.Int64
}

func (p *blockingProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.calls.Add(1)
	close(p.entered)
	return func(func(port.Chunk, error) bool) {
		<-ctx.Done()
	}, nil
}

func (*blockingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

// TestCloseShutdownBoundedWithBlockedScheduledFire is AC8 (issue #388): the
// embedded server's Close returns within the documented bound even when a
// scheduled fire is blocked mid-LLM-call — and the cancelled fire's session is
// persisted as terminal (StateCancelled, StopCancelled), with the schedule's
// LastFireSessionID reflecting the real id (not the "pending" sentinel).
//
// The test is OFFLINE (mock provider, jsonlstore, t.TempDir) and deterministic
// (entered channel, no blind sleeps). It FAILS if Close regresses to unbounded.
func TestCloseShutdownBoundedWithBlockedScheduledFire(t *testing.T) {
	// Shrink the embed-level timeouts so the test runs in milliseconds. The
	// scheduler's own StopFireGrace (default 10s) is a maximum, not a minimum —
	// once the fire's context is cancelled the engine unwind + persist + drain
	// completes well under the grace, so the select inside Stop takes the
	// firesWG.Wait() branch immediately.
	oldGraceful := gracefulStopTimeout
	oldComposition := compositionCloseTimeout
	gracefulStopTimeout = 50 * time.Millisecond
	// The composition close timeout must allow the scheduler's Stop() to drain
	// the cancelled fire, plus the rest of the close chain (refreshClose, svc.Close,
	// mcpClose, agentClose, storeClose). 2s is generous while still proving
	// boundedness — and vastly shorter than the pinned 30s + 10s unbounded old
	// posture this test exists to prevent returning to.
	compositionCloseTimeout = 2 * time.Second
	t.Cleanup(func() {
		gracefulStopTimeout = oldGraceful
		compositionCloseTimeout = oldComposition
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workspace := t.TempDir()
	storeDir := t.TempDir()

	// The mock provider that blocks until ctx is cancelled. The entered channel
	// lets the test know the fire has started its LLM call — we Close only after
	// the fire is genuinely in-flight.
	mock := &blockingProvider{entered: make(chan struct{})}

	// Seed a due one-shot into the jsonlstore BEFORE Start, matching the pattern
	// in TestScheduleTool_TuiEmbeddedSchedulerOn.
	seedStore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("seed jsonlstore: %v", err)
	}
	schedStore := seedStore.ScheduleStore()
	const schedName = "shutdown-oneshot"
	due := time.Now().Add(50 * time.Millisecond)
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:           schedName,
			Prompt:         "say hello from a blocking fire",
			Trigger:        port.TriggerSpec{OneShot: due},
			EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "local-default", Revision: "configured-v1"},
			PlacementScope: "deployment",
		},
		State: port.ScheduleState{NextFireAt: due, Enabled: true},
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	// Start the embedded server over the blocking mock provider + jsonlstore.
	srv, err := Start(ctx, app.Config{
		Workspace:             workspace,
		Model:                 "mock-model",
		UseMock:               true,
		MockProvider:          mock,
		Shell:                 "/bin/sh",
		Compaction:            "heuristic",
		Tokenizer:             "heuristic",
		StoreDir:              storeDir,
		SchedulerEnabled:      true,
		SchedulerTickInterval: 50 * time.Millisecond,
	}, PerfConfig{})
	if err != nil {
		t.Fatalf("embed.Start: %v", err)
	}

	// Wait for the scheduled fire to start its LLM call (the blocking provider
	// closes entered on the first Stream invocation). Don't race Close against
	// an un-started fire.
	select {
	case <-mock.entered:
		// The fire has started its LLM call and is now blocked.
	case <-time.After(5 * time.Second):
		t.Fatal("the scheduled fire never started its LLM call — the tick loop did not fire")
	}

	// Now Close while the fire is blocked mid-LLM-call. Measure wall time to
	// assert boundedness.
	closeStart := time.Now()
	if cerr := srv.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	closeDur := time.Since(closeStart)

	// The bound is gracefulStopTimeout + compositionCloseTimeout + scheduler drain
	// + teardown overhead. At ~2.05s nominal plus jitter, a 5s upper bound is
	// generous while staying well below the unbounded posture's 30s+10s=40s.
	bound := 5 * time.Second
	if closeDur > bound {
		t.Errorf("Close took %v, want <= %v (the shutdown must be bounded)", closeDur, bound)
	}

	// Verify the fire's session snapshot is persisted as terminal cancelled.
	fires, err := schedStore.ListFires(ctx, schedName)
	if err != nil || len(fires) == 0 {
		t.Fatalf("ListFires(%q) = (%v, %d), want at least one fire record", schedName, err, len(fires))
	}
	fire := fires[0]
	if fire.Stop != session.StopCancelled {
		t.Errorf("fire stop = %q, want cancelled (the blocked fire must be cancelled by shutdown)", fire.Stop)
	}
	if fire.SessionID == "" {
		t.Error("fire.SessionID is empty — want the real session id, not empty")
	}

	// Verify LastFireSessionID is the real id, not the "pending" sentinel.
	saved, err := schedStore.Load(ctx, schedName)
	if err != nil {
		t.Fatalf("Load(%q): %v", schedName, err)
	}
	if saved.State.LastFireSessionID == "" {
		t.Error("LastFireSessionID is empty after a recorded fire")
	}
	if saved.State.LastFireSessionID == port.PendingFireSessionID {
		t.Errorf("LastFireSessionID = %q, want the real session id (not the pending sentinel)", saved.State.LastFireSessionID)
	}
	if saved.State.LastFireSessionID != fire.SessionID {
		t.Errorf("LastFireSessionID = %q, fire.SessionID = %q — they must match", saved.State.LastFireSessionID, fire.SessionID)
	}

	// Verify the session itself is terminal cancelled by loading it from the
	// store.
	sess, err := seedStore.Load(ctx, fire.SessionID)
	if err != nil {
		t.Fatalf("Load session %q: %v", fire.SessionID, err)
	}
	if sess.State != session.StateCancelled {
		t.Errorf("session state = %v, want StateCancelled", sess.State)
	}

	// Verify the session is recoverable via Interrupt (the loadAndReopen seam
	// must be able to reopen a cancelled session for a follow-up run). We
	// can't run a full rehydration here, but the state+history must be
	// consistent: the conversation must have no dangling tool calls (the
	// Interrupt path repairs them).
	sess2, err := seedStore.Load(ctx, fire.SessionID)
	if err != nil {
		t.Fatalf("re-load session for pairing check: %v", err)
	}
	if err := session.ValidateToolPairing(sess2.Conversation.Messages); err != nil {
		t.Errorf("persisted conversation has invalid tool pairing (would cause provider 400 on resume): %v", err)
	}
}
