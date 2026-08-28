package client

import (
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func retryDisposition(v mecatlv1.RetryDisposition) *mecatlv1.RetryDisposition { return &v }
func streamProgress(v mecatlv1.StreamProgress) *mecatlv1.StreamProgress       { return &v }

func TestResultTypedDispositionPrecedenceAndPresence(t *testing.T) {
	tests := []struct {
		name      string
		result    *mecatlv1.Result
		transient bool
		permanent bool
		present   bool
	}{
		{
			name: "typed retryable overrides legacy permanent and permanent-looking text",
			result: &mecatlv1.Result{Stop: "error", Error: "invalid request", Permanent: true,
				RetryDisposition: retryDisposition(mecatlv1.RetryDisposition_RETRY_DISPOSITION_RETRYABLE)},
			transient: true, present: true,
		},
		{
			name: "typed permanent overrides timeout text",
			result: &mecatlv1.Result{Stop: "error", Error: "deadline exceeded",
				RetryDisposition: retryDisposition(mecatlv1.RetryDisposition_RETRY_DISPOSITION_PERMANENT)},
			permanent: true, present: true,
		},
		{
			name: "typed unknown is conservative",
			result: &mecatlv1.Result{Stop: "error", Error: "deadline exceeded", Permanent: true,
				RetryDisposition: retryDisposition(mecatlv1.RetryDisposition_RETRY_DISPOSITION_UNKNOWN)},
			present: true,
		},
		{
			name:      "absent disposition keeps legacy display fallback",
			result:    &mecatlv1.Result{Stop: "error", Error: "deadline exceeded"},
			transient: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resultMsg(tc.result)
			if got.Transient != tc.transient || got.Permanent != tc.permanent || got.RetryDispositionPresent != tc.present {
				t.Fatalf("resultMsg = %+v, want transient=%v permanent=%v present=%v", got, tc.transient, tc.permanent, tc.present)
			}
		})
	}
}

func TestFailedStepRetryEligibleRequiresTypedRetryablePrecommit(t *testing.T) {
	eligible := ResultMsg{
		Stop: "error", RetryDispositionPresent: true, RetryDisposition: RetryDispositionRetryable,
		StreamProgressPresent: true, StreamProgress: StreamProgressPrecommit,
	}
	if !eligible.FailedStepRetryEligible() {
		t.Fatal("typed retryable precommit error must be failed-step retry eligible")
	}

	tests := map[string]ResultMsg{
		"non-error stop":      func() ResultMsg { r := eligible; r.Stop = "cancelled"; return r }(),
		"disposition absent":  func() ResultMsg { r := eligible; r.RetryDispositionPresent = false; return r }(),
		"unknown disposition": func() ResultMsg { r := eligible; r.RetryDisposition = RetryDispositionUnknown; return r }(),
		"permanent":           func() ResultMsg { r := eligible; r.RetryDisposition = RetryDispositionPermanent; return r }(),
		"progress absent":     func() ResultMsg { r := eligible; r.StreamProgressPresent = false; return r }(),
		"visible":             func() ResultMsg { r := eligible; r.StreamProgress = StreamProgressVisible; return r }(),
		"complete":            func() ResultMsg { r := eligible; r.StreamProgress = StreamProgressComplete; return r }(),
	}
	for name, result := range tests {
		t.Run(name, func(t *testing.T) {
			if result.FailedStepRetryEligible() {
				t.Fatalf("%+v must not be failed-step retry eligible", result)
			}
		})
	}
}

func TestModelRetryEventMapping(t *testing.T) {
	msg := EventToMsg(&mecatlv1.Event{Type: "model.retry", Text: "superseded", ModelRetry: &mecatlv1.ModelRetry{
		RetryDisposition: mecatlv1.RetryDisposition_RETRY_DISPOSITION_RETRYABLE,
		StreamProgress:   mecatlv1.StreamProgress_STREAM_PROGRESS_VISIBLE,
	}})
	got, ok := msg.(ModelRetryMsg)
	if !ok || got.Text != "superseded" {
		t.Fatalf("EventToMsg = %#v", msg)
	}
}

func TestSendRetryStartUsesExplicitFirstFrame(t *testing.T) {
	fs := newFakeStream()
	stream := NewStream(fs, fs)
	if err := stream.SendRetryStart("session-1"); err != nil {
		t.Fatalf("SendRetryStart: %v", err)
	}
	frames := fs.sentFrames()
	if len(frames) != 1 || frames[0].GetRetry().GetSessionId() != "session-1" || frames[0].GetPrompt() != nil {
		t.Fatalf("frames = %#v, want one RetryStart and no Prompt", frames)
	}
}

func TestResultMapperPreservesTypedProgressPresence(t *testing.T) {
	present := resultMsg(&mecatlv1.Result{StreamProgress: streamProgress(mecatlv1.StreamProgress_STREAM_PROGRESS_UNKNOWN)})
	if !present.StreamProgressPresent || present.StreamProgress != StreamProgressUnknown {
		t.Fatalf("explicit unknown progress lost: %+v", present)
	}
	absent := resultMsg(&mecatlv1.Result{})
	if absent.StreamProgressPresent {
		t.Fatalf("absent progress reported present: %+v", absent)
	}
}
