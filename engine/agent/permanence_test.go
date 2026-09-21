package agent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// permanentTestError is a typed permanent provider error.
type permanentTestError struct {
	msg string
}

type typedFailureError struct {
	disposition session.RetryDisposition
	progress    session.StreamProgress
}

func (*typedFailureError) Error() string                                { return "typed failure" }
func (e *typedFailureError) RetryDisposition() session.RetryDisposition { return e.disposition }
func (e *typedFailureError) StreamProgress() session.StreamProgress     { return e.progress }

func (e *permanentTestError) Error() string { return e.msg }
func (*permanentTestError) RetryDisposition() session.RetryDisposition {
	return session.RetryDispositionPermanent
}

var _ interface {
	RetryDisposition() session.RetryDisposition
} = (*permanentTestError)(nil)

// TestRunPermanentProviderErrorMarkedOnResult asserts that typed permanent
// provider classification is projected into canonical retry metadata.
func TestRunPermanentProviderErrorMarkedOnResult(t *testing.T) {
	permErr := &permanentTestError{msg: "invalid_encrypted_content: replay rejected"}
	llm := mockllm.New(mockllm.ErrorTurn(permErr))
	cat := catalogWith(t, loopTool())
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "test"}))
	res := lastResult(t, evs)

	if res.Stop != session.StopError {
		t.Fatalf("stop = %q, want StopError", res.Stop)
	}
	want := session.RetryMetadata{Disposition: session.RetryDispositionPermanent}
	if got := (session.RetryMetadata{Disposition: res.Disposition, Progress: res.Progress}); got != want {
		t.Fatalf("result retry metadata = %+v, want %+v", got, want)
	}
	if !strings.Contains(res.Error, permErr.Error()) {
		t.Fatalf("ResultPayload.Error = %q, want it to contain %q", res.Error, permErr.Error())
	}
	if got := sess.FailureMetadata(); got != want {
		t.Fatalf("session failure metadata = %+v, want %+v", got, want)
	}
}

// TestRunTransientErrorNotPermanent asserts an unclassified stream error leaves
// canonical retry metadata conservative and unknown.
func TestRunTransientErrorNotPermanent(t *testing.T) {
	transientErr := fmt.Errorf("upstream 503: service unavailable")
	llm := mockllm.New(mockllm.ErrorTurn(transientErr))
	cat := catalogWith(t, loopTool())
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "test"}))
	res := lastResult(t, evs)

	if res.Stop != session.StopError {
		t.Fatalf("stop = %q, want StopError", res.Stop)
	}
	if res.Disposition != session.RetryDispositionUnknown || res.Progress != session.StreamProgressUnknown {
		t.Fatalf("result retry metadata = (%v,%v), want unknown", res.Disposition, res.Progress)
	}
	if got := sess.FailureMetadata(); got != (session.RetryMetadata{}) {
		t.Fatalf("session failure metadata = %+v, want zero", got)
	}
}

func TestRunTypedFailureMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    session.RetryDisposition
		p    session.StreamProgress
	}{
		{"unknown", session.RetryDispositionUnknown, session.StreamProgressUnknown},
		{"retryable precommit", session.RetryDispositionRetryable, session.StreamProgressPrecommit},
		{"retryable visible", session.RetryDispositionRetryable, session.StreamProgressVisible},
		{"permanent", session.RetryDispositionPermanent, session.StreamProgressPrecommit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := &typedFailureError{disposition: tc.d, progress: tc.p}
			e := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ErrorTurn(err)), Catalog: catalogWith(t, loopTool())})
			sess := newSession(t, session.Limits{})
			res := lastResult(t, drain(e.Run(context.Background(), sess, agent.EnvForWS(memfs.NewWorkspace("/ws"), nil), agent.RunRequest{Text: "test"})))
			if res.Disposition != tc.d || res.Progress != tc.p {
				t.Fatalf("result metadata = (%v,%v), want (%v,%v)", res.Disposition, res.Progress, tc.d, tc.p)
			}
			want := session.RetryMetadata{Disposition: tc.d, Progress: tc.p}
			if got := sess.FailureMetadata(); got != want {
				t.Fatalf("session metadata = %+v, want %+v", got, want)
			}
		})
	}
}

// Clean stops carry complete progress without a failure disposition.
func TestRunCleanStopNotPermanent(t *testing.T) {
	llm := mockllm.New(
		mockllm.TextTurn("all done."),
	)
	cat := catalogWith(t, loopTool())
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "test"}))
	res := lastResult(t, evs)

	if res.Stop == session.StopError {
		t.Fatal("stop = StopError, want clean terminal")
	}
	if res.Disposition != session.RetryDispositionUnknown {
		t.Fatalf("ResultPayload.Disposition = %v, want unknown for clean terminal", res.Disposition)
	}
	if res.Progress != session.StreamProgressComplete {
		t.Fatalf("ResultPayload.Progress = %v, want complete", res.Progress)
	}
}

// TestRunChunkDoneStopErrorNotPermanent asserts that a provider-reported terminal
// condition on ChunkDone has unknown disposition because no Go error exists to classify.
func TestRunChunkDoneStopErrorNotPermanent(t *testing.T) {
	llm := mockllm.New(
		mockllm.EmptyTurnWithStop(session.StopError),
	)
	cat := catalogWith(t, loopTool())
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "test"}))
	res := lastResult(t, evs)

	if res.Stop != session.StopError {
		t.Fatalf("stop = %q, want StopError", res.Stop)
	}
	if res.Disposition != session.RetryDispositionUnknown {
		t.Fatalf("ResultPayload.Disposition = %v, want unknown (ChunkDone StopError has no Go error to classify)", res.Disposition)
	}
}
