package client

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// TestTransientStreamErr tables the stream-error classifier: the retryable gRPC
// codes and a context deadline are transient; a plain error whose text hits the
// vocabulary is transient; InvalidArgument / a bare "server_error" / nil are NOT.
func TestTransientStreamErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unavailable", status.Error(codes.Unavailable, "backend down"), true},
		{"deadline", status.Error(codes.DeadlineExceeded, "slow"), true},
		{"resource_exhausted", status.Error(codes.ResourceExhausted, "429"), true},
		{"invalid_argument", status.Error(codes.InvalidArgument, "bad model"), false},
		{"permission_denied", status.Error(codes.PermissionDenied, "nope"), false},
		{"ctx_deadline", context.DeadlineExceeded, true},
		{"vocab_plain_idle", errors.New("stream idle timeout after 180s"), true},
		{"vocab_plain_overloaded", errors.New("upstream overloaded"), true},
		{"vocab_plain_503", errors.New("HTTP 503 from provider"), true},
		{"vocab_status_message", status.Error(codes.Internal, "service_unavailable upstream"), true},
		{"bare_server_error", errors.New("server_error"), false},
		{"unrelated", errors.New("json parse failed"), false},
		// Digit-boundary: a code embedded in a port/model id is NOT transient.
		{"port_false_positive", errors.New("port 50378 refused"), false},
		{"model_id_false_positive", errors.New("unknown model 1230503"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TransientStreamErr(tc.err); got != tc.want {
				t.Errorf("TransientStreamErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestTransientResultError tables the result-error TEXT classifier: each vocabulary
// substring is transient; unrelated text and empty/whitespace are not; the bare
// "server_error" token is deliberately NOT transient (OpenRouter reuses it).
func TestTransientResultError(t *testing.T) {
	transient := []string{
		"idle timeout", "stream stalled", "stream idle", "backend unavailable",
		"model overloaded", "deadline exceeded", "too many requests", "got a 429",
		"502 bad gateway", "503 service", "504 timeout", "temporarily unreachable",
		"engine_overloaded", "service_unavailable",
		"OVERLOADED (upper-case)", // case-insensitive
	}
	for _, s := range transient {
		if !TransientResultError(s) {
			t.Errorf("TransientResultError(%q) = false, want true", s)
		}
	}
	nonTransient := []string{
		"", "   ", "server_error", "invalid request", "context length exceeded",
		"tool execution failed",
		// Numeric status codes are digit-bounded: a code embedded in a port number or
		// model id must NOT match (the bare-substring false-positive the reviewer
		// flagged — "503" in "port 50378" / "1230503" is not an HTTP 503).
		"port 50378 unreachable", "model 1230503 failed", "listening on 42900",
	}
	for _, s := range nonTransient {
		if TransientResultError(s) {
			t.Errorf("TransientResultError(%q) = true, want false", s)
		}
	}
}

// TestEventToMsgSetsResultTransient asserts retry disposition drives Transient.
func TestEventToMsgSetsResultTransient(t *testing.T) {
	retryable := mecatlv1.RetryDisposition_RETRY_DISPOSITION_RETRYABLE
	transientEv := &mecatlv1.Event{Type: "result", Result: &mecatlv1.Result{
		Stop: "error", Error: "engine_overloaded", RetryDisposition: &retryable}}
	m := EventToMsg(transientEv)
	rm, ok := m.(ResultMsg)
	if !ok {
		t.Fatalf("EventToMsg = %T, want ResultMsg", m)
	}
	if !rm.Transient {
		t.Errorf("transient result error must set Transient=true, got %+v", rm)
	}

	cleanEv := &mecatlv1.Event{Type: "result", Result: &mecatlv1.Result{Stop: "end_turn"}}
	if rm2 := EventToMsg(cleanEv).(ResultMsg); rm2.Transient {
		t.Errorf("clean end_turn must not be transient, got %+v", rm2)
	}

	hardEv := &mecatlv1.Event{Type: "result", Result: &mecatlv1.Result{
		Stop: "error", Error: "invalid request"}}
	if rm3 := EventToMsg(hardEv).(ResultMsg); rm3.Transient {
		t.Errorf("hard result error must not be transient, got %+v", rm3)
	}

	// The bare "server_error" carve-out (OpenRouter reuses it for non-transient
	// faults) must hold at the mapper boundary too, not only in the classifier table.
	serverErrEv := &mecatlv1.Event{Type: "result", Result: &mecatlv1.Result{
		Stop: "error", Error: "server_error"}}
	if rm4 := EventToMsg(serverErrEv).(ResultMsg); rm4.Transient {
		t.Errorf("bare server_error must not be transient at the mapper boundary, got %+v", rm4)
	}
}

// TestEventToMsgSetsResultPermanent asserts typed retry disposition drives presentation.
func TestEventToMsgSetsResultPermanent(t *testing.T) {
	permanent := mecatlv1.RetryDisposition_RETRY_DISPOSITION_PERMANENT
	permEv := &mecatlv1.Event{Type: "result", Result: &mecatlv1.Result{
		Stop: "error", Error: "engine_overloaded", RetryDisposition: &permanent}}
	rm := EventToMsg(permEv).(ResultMsg)
	if !rm.Permanent || rm.Transient {
		t.Errorf("permanent disposition mapped incorrectly: %+v", rm)
	}

	retryable := mecatlv1.RetryDisposition_RETRY_DISPOSITION_RETRYABLE
	retryEv := &mecatlv1.Event{Type: "result", Result: &mecatlv1.Result{
		Stop: "error", Error: "engine_overloaded", RetryDisposition: &retryable}}
	rm2 := EventToMsg(retryEv).(ResultMsg)
	if rm2.Permanent || !rm2.Transient {
		t.Errorf("retryable disposition mapped incorrectly: %+v", rm2)
	}

	absent := EventToMsg(&mecatlv1.Event{Type: "result", Result: &mecatlv1.Result{
		Stop: "error", Error: "engine_overloaded"}}).(ResultMsg)
	if absent.Permanent || absent.Transient {
		t.Errorf("absent disposition must be conservative: %+v", absent)
	}
}

// TestReadLoopSetsStreamErrTransient asserts ReadLoop classifies a transient Recv
// error onto StreamErrMsg.Transient (and leaves it false for a hard error).
func TestReadLoopSetsStreamErrTransient(t *testing.T) {
	t.Run("transient", func(t *testing.T) {
		fs := newFakeStream(resp(&mecatlv1.Event{Type: "session.init"}))
		fs.endErr = status.Error(codes.Unavailable, "backend down")
		st := NewStream(fs, fs)
		ch := make(chan tea.Msg, 8)
		go st.ReadLoop(context.Background(), ch)

		msgs := drain(ch)
		se, ok := msgs[len(msgs)-1].(StreamErrMsg)
		if !ok {
			t.Fatalf("last msg = %T, want StreamErrMsg", msgs[len(msgs)-1])
		}
		if !se.Transient {
			t.Errorf("Unavailable Recv error must set Transient=true, got %+v", se)
		}
	})
	t.Run("hard", func(t *testing.T) {
		fs := newFakeStream(resp(&mecatlv1.Event{Type: "session.init"}))
		fs.endErr = status.Error(codes.InvalidArgument, "bad request")
		st := NewStream(fs, fs)
		ch := make(chan tea.Msg, 8)
		go st.ReadLoop(context.Background(), ch)

		msgs := drain(ch)
		se, ok := msgs[len(msgs)-1].(StreamErrMsg)
		if !ok {
			t.Fatalf("last msg = %T, want StreamErrMsg", msgs[len(msgs)-1])
		}
		if se.Transient {
			t.Errorf("InvalidArgument Recv error must not be transient, got %+v", se)
		}
	})
}
