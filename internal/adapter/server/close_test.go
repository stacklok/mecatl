package server_test

import (
	"context"
	"iter"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// blockingProvider is defined in lease_test.go (same package).

type settlementBlockingProvider struct {
	entered chan struct{}
	release chan struct{}
}

func (p settlementBlockingProvider) Stream(context.Context, port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		close(p.entered)
		<-p.release
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

func (settlementBlockingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func TestCloseRetainsOperationPinUntilRunActuallySettles(t *testing.T) {
	provider := settlementBlockingProvider{entered: make(chan struct{}), release: make(chan struct{})}
	var released atomic.Bool
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM: provider, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, permstore.New()), Model: "test-model",
		}),
		Store: memstore.New(),
		OperationPin: func(ctx context.Context) (context.Context, func(), error) {
			return ctx, func() { released.Store(true) }, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatal(err)
	}
	<-provider.entered
	closed := make(chan struct{})
	go func() { svc.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Service.Close did not finish cancellation")
	}
	if released.Load() {
		t.Fatal("Service.Close released the operation pin while provider work was still blocked")
	}
	close(provider.release)
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)
	if !released.Load() {
		t.Fatal("operation pin was not released after settlement")
	}
}

// TestCloseCancelsInFlightRun proves that Service.Close() cancels a run that is
// blocked in an LLM call. A blockingProvider streams nothing until ctx is
// cancelled; Close calls shutdownCancelFn + run.Cancel(), so the run unblocks
// and terminates with StopCancelled rather than hanging until the test timeout.
func TestCloseCancelsInFlightRun(t *testing.T) {
	store := memstore.New()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     blockingProvider{},
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  store,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// Do NOT call svc.Close in Cleanup — the test calls it explicitly.

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Close the service from a goroutine so the blocked run is cancelled.
	// Close must return promptly (the engine loop is empty, no per-session engines).
	closeDone := make(chan struct{})
	go func() {
		svc.Close()
		close(closeDone)
	}()

	// The run must terminate; drain its events until the channel closes.
	var stop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	svc.FinishRun(sess.ID, run)

	if stop != session.StopCancelled {
		t.Fatalf("run stop = %q, want %q (Close must cancel in-flight runs)", stop, session.StopCancelled)
	}

	// Close must return within a reasonable time (it had no engines to close,
	// only the run cancellation above).
	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within 5s (may be blocked on engine close loop)")
	}
}

// TestCloseEngineTimeoutBound proves that Service.Close() completes within the
// engineCloseTimeout when a per-session engine's close blocks forever. It triggers
// the per-session engine path by creating a session whose workspace differs from
// DefaultWorkspace (as the worktree-routing tests do), with a SessionEngine factory
// whose Close blocks until a release channel is signalled.
func TestCloseEngineTimeoutBound(t *testing.T) {
	// Override the engine-close timeout to a small value so the test completes in
	// milliseconds, not seconds.
	restore := server.SetEngineCloseTimeoutForTest(50 * time.Millisecond)
	defer restore()

	timeout := 50 * time.Millisecond
	margin := 500 * time.Millisecond

	// A release channel that we signal AFTER Close to unblock the stuck goroutine
	// so the test can clean up (the abandoned goroutine exits).
	release := make(chan struct{})

	store := memstore.New()
	ps := permstore.New()
	cat := tool.NewCatalog()
	sharedEngine := agent.NewEngine(agent.Deps{
		LLM:     blockingProvider{},
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})

	// A SessionEngine factory that returns an engine whose Close blocks until release.
	svc, err := newPlacementTestService(server.Config{
		Engine: sharedEngine,
		Store:  store,

		SharedEngineRoot: "/default-ws",
		SessionEngine: func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{
				Engine: agent.NewEngine(agent.Deps{
					LLM:     blockingProvider{},
					Catalog: tool.NewCatalog(),
					Policy:  permpolicy.NewPolicy(nil, permstore.New()),
					Model:   "test-model",
				}),
				Close: func() error {
					<-release // block until released
					return nil
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// An explicit selector requires a per-session engine without relying on
	// client-selected workspace placement.
	sess, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{ProviderID: "test"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !svc.HasSessionEngineForTest(sess.ID) {
		t.Fatal("CreateSession did not register a per-session engine — the test cannot assert the bounded close")
	}

	// Close must return within the bounded timeout + margin, even though
	// the session engine's Close blocks forever.
	start := time.Now()
	closeDone := make(chan struct{})
	go func() {
		svc.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		elapsed := time.Since(start)
		if elapsed > timeout+margin {
			t.Fatalf("Close took %v, want < %v (engine-close was not bounded)", elapsed, timeout+margin)
		}
	case <-time.After(timeout + margin + 2*time.Second):
		t.Fatal("Close did not return within the bounded timeout + margin (unbounded engine close)")
	}

	// Release the stuck goroutine so it can exit (cleanup).
	close(release)
}
