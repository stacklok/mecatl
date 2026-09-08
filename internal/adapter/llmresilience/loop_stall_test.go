package llmresilience_test

import (
	"context"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
)

// stallingProvider streams one text chunk, signals started, then blocks until the
// context is cancelled and SWALLOWS the ctx error — it yields nothing further
// (never a ChunkDone). This mirrors the openai/anthropic adapters, which discard
// the ctx error on cancel. Without the post-first-chunk idle watchdog the agent
// loop would never observe a terminal chunk and would hang forever.
type stallingProvider struct {
	started chan struct{}
	once    sync.Once
}

func (*stallingProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

func (s *stallingProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		s.once.Do(func() { close(s.started) })
		if !yield(port.Chunk{Kind: port.ChunkText, Text: "thinking"}, nil) {
			return
		}
		<-ctx.Done()
		// Swallow the ctx error: yield nothing. The resilience wrapper must
		// synthesize the terminal timeout error itself.
	}, nil
}

// TestLoopMidStreamStallTerminatesAsError is the end-to-end guard for the
// mid-stream stall bug: an LLM stream that yields one chunk then stalls (no
// ChunkDone, ctx error swallowed) must, when wrapped with a StreamIdleTimeout,
// drive the agent loop to a terminal error result rather than hang. The whole run
// is bounded by a 5s context; a regression hangs the iterator until that deadline
// and the test fails for not terminating in time.
func TestLoopMidStreamStallTerminatesAsError(t *testing.T) {
	stall := &stallingProvider{started: make(chan struct{})}
	llm := llmresilience.Wrap(stall, llmresilience.Config{
		MaxAttempts:       1,
		StreamIdleTimeout: 50 * time.Millisecond,
	})

	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, ws, memledger.New(), nil)
	r := e.Run(ctx, newSession(t, session.Limits{}), env, agent.RunRequest{Text: "go"})

	type drained struct {
		evs []session.Event
	}
	done := make(chan drained, 1)
	go func() {
		done <- drained{evs: drain(r)}
	}()

	select {
	case d := <-done:
		res := lastResult(t, d.evs)
		if res.Stop != session.StopError {
			t.Fatalf("stop = %q, want error (the stalled stream must end the turn as an error)", res.Stop)
		}
		if res.Error == "" {
			t.Fatal("terminal error result has an empty error message")
		}
	case <-ctx.Done():
		t.Fatal("agent run did not terminate before its context deadline — the mid-stream stall was not bounded (regression)")
	}
}
