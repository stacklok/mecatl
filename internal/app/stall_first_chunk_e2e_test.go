package app

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
)

// stallingProvider models the issue-#82 root-cause path at the provider boundary: the
// Stream STARTS (returns a non-nil seq, no outer error — the connection established),
// but the model produces NO first chunk and the adapter SWALLOWS the ctx error on
// cancel (it yields NOTHING once the per-attempt context is done). This is exactly the
// openai/anthropic swallow-on-cancel behaviour the llmresilience wrapper exists to
// surface. A genuinely empty cooperative stream (mockllm.EmptyTurn) finishes promptly
// with no usage — that path is the StopNoProgress contrast in
// engine/agent/noprogress_test.go; THIS provider never finishes on its own.
type stallingProvider struct{}

func (stallingProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

func (stallingProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(_ func(port.Chunk, error) bool) {
		<-ctx.Done() // block until the per-attempt deadline cancels us, then yield nothing.
	}, nil
}

// TestStallFirstChunkTimeoutTerminatesRunAsError is THE issue-#82 loop-level regression
// guard (MUST 3). It proves the JOIN of the two halves: a provider that stalls
// pre-first-chunk and swallows the timeout, wrapped by llmresilience.Wrap (Fix C),
// driven through ONE real Engine.Run turn, must terminate as session.StopError with the
// session FAILED — NOT the phantom StopNoProgress "done" the empty-stream path takes.
//
// Before Fix C the swallowed first-chunk timeout surfaced as a clean (empty) completion,
// the loop saw an empty turn, and the run ended StopNoProgress (a benign "done") — the
// laundered-timeout symptom. Contrast TestNoProgressTurnNudges* in
// engine/agent/noprogress_test.go, which proves a GENUINELY empty stream still reaches
// StopNoProgress. This test is the one that fails if a future refactor re-launders the
// timeout (verified by reverting Fix C: establish's empty branch then returns
// firstChunk{empty:true} with no error and this run ends StopNoProgress).
func TestStallFirstChunkTimeoutTerminatesRunAsError(t *testing.T) {
	wrapped := llmresilience.Wrap(stallingProvider{}, llmresilience.Config{
		MaxAttempts:       2,
		BaseBackoff:       time.Nanosecond,
		MaxBackoff:        time.Nanosecond,
		PerAttemptTimeout: 20 * time.Millisecond,
	})
	eng := agent.NewEngine(agent.Deps{LLM: wrapped, Catalog: tool.NewCatalog()})

	sess := session.New("stall-82", session.ModeDefault, "/ws", session.Limits{MaxTurns: 5}, time.Unix(0, 0))
	run := eng.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "do the task"})

	stop := session.StopNone
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}

	if stop == session.StopNoProgress {
		t.Fatalf("run terminated StopNoProgress — the swallowed first-chunk timeout was laundered into a phantom 'done' (issue #82); Fix C reverted?")
	}
	if stop != session.StopError {
		t.Fatalf("run stop = %q, want StopError (a stalled-pre-first-chunk turn is a terminal error)", stop)
	}
	if sess.State != session.StateFailed {
		t.Fatalf("session state = %q, want failed", sess.State)
	}
}
