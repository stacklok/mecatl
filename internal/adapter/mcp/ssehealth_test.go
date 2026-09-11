package mcp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

// TestSSEMonitorRoundTripperClassifiesCompletedGETFailures proves that a
// completed standalone GET is hostile not only when it is an empty SSE stream,
// but also when the gateway rejects it or returns another representation. The
// monitor reports those responses without wrapping or closing their bodies;
// the SDK retains its normal response/body lifecycle.
func TestSSEMonitorRoundTripperClassifiesCompletedGETFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		contentType string
	}{
		{name: "non-200", status: http.StatusServiceUnavailable, contentType: "text/event-stream"},
		{name: "non-SSE", status: http.StatusOK, contentType: "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newSSEHealthTracker("rs", &recordingDiag{})
			var bodies []*closeTrackingBody
			monitor := &sseMonitorRoundTripper{
				base: testRoundTripper(func(*http.Request) (*http.Response, error) {
					body := &closeTrackingBody{Reader: strings.NewReader("gateway response")}
					bodies = append(bodies, body)
					return &http.Response{
						StatusCode: tc.status,
						Header:     http.Header{"Content-Type": []string{tc.contentType}},
						Body:       body,
					}, nil
				}),
				tracker: tracker,
			}

			for range earlyCloseThreshold {
				req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://mcp.example", nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Accept", "text/event-stream")
				resp, err := monitor.RoundTrip(req)
				if err != nil {
					t.Fatalf("RoundTrip: %v", err)
				}
				if _, ok := resp.Body.(*closeTrackingBody); !ok {
					t.Fatalf("response body was wrapped; completed non-SSE/non-200 responses must keep normal body handling")
				}
				if err := resp.Body.Close(); err != nil {
					t.Fatalf("response body Close: %v", err)
				}
			}
			if !tracker.Hostile() {
				t.Fatal("Hostile() = false after consecutive completed GET failures, want true")
			}
			for _, body := range bodies {
				if !body.closed {
					t.Fatal("response body was not closed by its normal caller")
				}
			}
		})
	}
}

// TestSSEMonitorRoundTripperIgnoresNonStandaloneRequests guards the classifier:
// POST traffic and ordinary GETs must neither be wrapped nor affect the
// standalone GET streak.
func TestSSEMonitorRoundTripperIgnoresNonStandaloneRequests(t *testing.T) {
	tracker := newSSEHealthTracker("rs", &recordingDiag{})
	monitor := &sseMonitorRoundTripper{
		base: testRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("no"))}, nil
		}),
		tracker: tracker,
	}
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		req, err := http.NewRequestWithContext(context.Background(), method, "https://mcp.example", nil)
		if err != nil {
			t.Fatal(err)
		}
		if method == http.MethodGet {
			req.Header.Set("Accept", "application/json")
		}
		for range earlyCloseThreshold {
			resp, err := monitor.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip(%s): %v", method, err)
			}
			_ = resp.Body.Close()
		}
	}
	if tracker.Hostile() {
		t.Fatal("Hostile() = true after non-standalone requests, want false")
	}
}

// TestSSEHealthTrackerTripsAfterConsecutiveEarlyCloses exercises
// sseHealthTracker.observe directly (no HTTP round-trip needed — the
// tracker's decision is pure over its observed (bytes, elapsed) inputs): it
// must NOT trip before earlyCloseThreshold consecutive zero-byte, fast
// closes, must trip exactly at the threshold, log exactly one WARN on the
// false->true transition, and never log a second one on further hostile
// observations (ADR 0327).
func TestSSEHealthTrackerTripsAfterConsecutiveEarlyCloses(t *testing.T) {
	diag := &recordingDiag{}
	tr := newSSEHealthTracker("rs", diag)

	for i := 0; i < earlyCloseThreshold-1; i++ {
		tr.observe(context.Background(), 0, time.Millisecond)
		if tr.Hostile() {
			t.Fatalf("tripped after only %d early close(s), want %d", i+1, earlyCloseThreshold)
		}
	}
	tr.observe(context.Background(), 0, time.Millisecond)
	if !tr.Hostile() {
		t.Fatalf("Hostile() = false after %d consecutive early closes, want true", earlyCloseThreshold)
	}
	if got := diag.count("mcp: standalone SSE stream auto-disabled"); got != 1 {
		t.Errorf("auto-disabled WARN lines = %d, want exactly 1", got)
	}

	// A further hostile observation on an already-tripped tracker must not
	// re-log — the CompareAndSwap gate is what makes this once-only.
	tr.observe(context.Background(), 0, time.Millisecond)
	if got := diag.count("mcp: standalone SSE stream auto-disabled"); got != 1 {
		t.Errorf("auto-disabled WARN lines after a further hostile observation = %d, want still 1", got)
	}
}

// TestSSEHealthTrackerResetsOnProgress proves the reset-on-progress branch:
// neither a byte-delivering observation nor a slow (but zero-byte) close
// counts toward the consecutive-hostility streak, so a single blip on an
// otherwise-healthy gateway never trips the verdict — the false-positive
// guard ADR 0327 relies on to justify auto-deciding at all.
func TestSSEHealthTrackerResetsOnProgress(t *testing.T) {
	diag := &recordingDiag{}
	tr := newSSEHealthTracker("rs", diag)

	// Two early closes, then a byte-delivering observation resets the streak,
	// so a lone early close after it must not, by itself, trip the verdict.
	tr.observe(context.Background(), 0, time.Millisecond)
	tr.observe(context.Background(), 0, time.Millisecond)
	tr.observe(context.Background(), 1, time.Millisecond) // progress: resets
	tr.observe(context.Background(), 0, time.Millisecond)
	if tr.Hostile() {
		t.Fatal("Hostile() = true after a progress reset; the byte-delivering observation should have cleared the streak")
	}

	// A stream that simply stays open past earlyCloseWindow — even
	// delivering zero bytes, e.g. an ordinary idle long-lived connection — is
	// not a hostile signal either.
	tr.observe(context.Background(), 0, earlyCloseWindow)
	if tr.Hostile() {
		t.Fatal("Hostile() = true for a slow zero-byte close; elapsed >= earlyCloseWindow must not count as hostile")
	}
	if got := diag.count("mcp: standalone SSE stream auto-disabled"); got != 0 {
		t.Errorf("auto-disabled WARN lines = %d, want 0 (never tripped)", got)
	}
}

// TestSSEHealthTrackerTripsOnConsecutiveTransportFailures exercises
// observeFailure directly: a transport-level failure (connection
// refused/reset/EOF — no response ever received) must count toward the
// hostility streak unconditionally, the same as observe's zero-byte-fast-
// close case, since a request that never completed carries no ambiguity
// about being a legitimate long-lived stream.
func TestSSEHealthTrackerTripsOnConsecutiveTransportFailures(t *testing.T) {
	diag := &recordingDiag{}
	tr := newSSEHealthTracker("rs", diag)

	for i := 0; i < earlyCloseThreshold-1; i++ {
		tr.observeFailure(context.Background())
		if tr.Hostile() {
			t.Fatalf("tripped after only %d failure(s), want %d", i+1, earlyCloseThreshold)
		}
	}
	tr.observeFailure(context.Background())
	if !tr.Hostile() {
		t.Fatalf("Hostile() = false after %d consecutive transport failures, want true", earlyCloseThreshold)
	}
	if got := diag.count("mcp: standalone SSE stream auto-disabled"); got != 1 {
		t.Errorf("auto-disabled WARN lines = %d, want exactly 1", got)
	}
}

// TestSSEHealthTrackerFailureAndCloseStreaksCompose proves observe and
// observeFailure share ONE streak (not two independent counters): a mix of
// the two hostile shapes still trips at earlyCloseThreshold total, and a
// progress observation in between resets both.
func TestSSEHealthTrackerFailureAndCloseStreaksCompose(t *testing.T) {
	diag := &recordingDiag{}
	tr := newSSEHealthTracker("rs", diag)

	tr.observeFailure(context.Background())
	tr.observe(context.Background(), 0, time.Millisecond)
	if tr.Hostile() {
		t.Fatal("Hostile() = true after only 2 mixed hostile observations, want false")
	}
	tr.observe(context.Background(), 1, time.Millisecond) // progress: resets both
	tr.observeFailure(context.Background())
	tr.observe(context.Background(), 0, time.Millisecond)
	if tr.Hostile() {
		t.Fatal("Hostile() = true after a reset plus only 2 more hostile observations, want false")
	}
	tr.observeFailure(context.Background())
	if !tr.Hostile() {
		t.Fatal("Hostile() = false after 3 consecutive mixed hostile observations, want true")
	}
}
