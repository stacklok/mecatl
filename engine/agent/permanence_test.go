package agent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// permanentTestError is a test error implementing port.PermanentError.
type permanentTestError struct {
	msg string
}

func (e *permanentTestError) Error() string { return e.msg }
func (*permanentTestError) Permanent() bool { return true }

var _ port.PermanentError = (*permanentTestError)(nil)

// TestRunPermanentProviderErrorMarkedOnResult asserts that a stream error
// implementing port.PermanentError with Permanent()==true marks EvResult.Permanent
// and the session's FailurePermanence() as true, while the Error string still
// carries the message.
func TestRunPermanentProviderErrorMarkedOnResult(t *testing.T) {
	permErr := &permanentTestError{msg: "invalid_encrypted_content: replay rejected"}
	llm := mockllm.New(mockllm.ErrorTurn(permErr))
	cat := catalogWith(t, loopTool())
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, ws, "test"))
	res := lastResult(t, evs)

	if res.Stop != session.StopError {
		t.Fatalf("stop = %q, want StopError", res.Stop)
	}
	if !res.Permanent {
		t.Fatal("ResultPayload.Permanent = false, want true for permanent error")
	}
	if !strings.Contains(res.Error, permErr.Error()) {
		t.Fatalf("ResultPayload.Error = %q, want it to contain %q", res.Error, permErr.Error())
	}
	if !sess.FailurePermanence() {
		t.Fatal("session.FailurePermanence() = false, want true")
	}
}

// TestRunTransientErrorNotPermanent asserts that a stream error that does NOT
// implement port.PermanentError leaves EvResult.Permanent and the session's
// FailurePermanence() as false.
func TestRunTransientErrorNotPermanent(t *testing.T) {
	transientErr := fmt.Errorf("upstream 503: service unavailable")
	llm := mockllm.New(mockllm.ErrorTurn(transientErr))
	cat := catalogWith(t, loopTool())
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, ws, "test"))
	res := lastResult(t, evs)

	if res.Stop != session.StopError {
		t.Fatalf("stop = %q, want StopError", res.Stop)
	}
	if res.Permanent {
		t.Fatal("ResultPayload.Permanent = true, want false for transient error")
	}
	if sess.FailurePermanence() {
		t.Fatal("session.FailurePermanence() = true, want false")
	}
}

// TestRunCleanStopNotPermanent asserts that a clean StopEndTurn terminal has
// Permanent==false on the result payload.
func TestRunCleanStopNotPermanent(t *testing.T) {
	llm := mockllm.New(
		mockllm.TextTurn("all done."),
	)
	cat := catalogWith(t, loopTool())
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, ws, "test"))
	res := lastResult(t, evs)

	if res.Stop == session.StopError {
		t.Fatal("stop = StopError, want clean terminal")
	}
	if res.Permanent {
		t.Fatal("ResultPayload.Permanent = true, want false for clean terminal")
	}
}

// TestRunChunkDoneStopErrorNotPermanent asserts that a provider-reported terminal
// condition on ChunkDone (StopError via EmptyTurnWithStop, NOT a Go error) has
// Permanent==false — honest fail-open because no Go error exists to classify.
func TestRunChunkDoneStopErrorNotPermanent(t *testing.T) {
	llm := mockllm.New(
		mockllm.EmptyTurnWithStop(session.StopError),
	)
	cat := catalogWith(t, loopTool())
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, ws, "test"))
	res := lastResult(t, evs)

	if res.Stop != session.StopError {
		t.Fatalf("stop = %q, want StopError", res.Stop)
	}
	if res.Permanent {
		t.Fatal("ResultPayload.Permanent = true, want false (ChunkDone StopError has no Go error to classify)")
	}
}
