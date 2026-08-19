package llmresilience

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
)

// credentialFailure builds the error shape a token-source failure ACTUALLY
// arrives in: the transport wraps its cause with ErrCredentials, and net/http
// then wraps whatever the RoundTripper returned in *url.Error. Reproducing that
// wrapper matters — *url.Error is exactly what makes a credential failure
// indistinguishable from a network fault to the classifiers.
func credentialFailure(t *testing.T, cause error) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("%w: %w", ErrCredentials, cause)
	})}
	_, err := client.Get(srv.URL) //nolint:bodyclose // the transport never returns a response
	if err == nil {
		t.Fatal("expected the client to surface the transport error")
	}
	return err
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The wrapper this whole fix turns on: net/http hands back a *url.Error, which
// satisfies net.Error. If the classifiers reach their network arms, a credential
// failure is misread as a transient provider fault.
func TestCredentialFailureArrivesAsURLErrorSatisfyingNetError(t *testing.T) {
	err := credentialFailure(t, errors.New("no cached credential"))

	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("err = %T, want it to wrap *url.Error", err)
	}
	if !errors.Is(err, ErrCredentials) {
		t.Fatal("the credential sentinel must survive net/http's wrapping")
	}
}

func TestCredentialFailureIsNotRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"through net/http's url.Error wrapper", credentialFailure(t, errors.New("no cached credential"))},
		{"bare sentinel", ErrCredentials},
		{"wrapped once", fmt.Errorf("agent: start stream: %w", ErrCredentials)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if DefaultClassifier(tc.err) {
				t.Fatal("a credential failure must not be retryable: replaying the request cannot mint a token")
			}
		})
	}
}

func TestCredentialFailureIsBreakerNeutral(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"through net/http's url.Error wrapper", credentialFailure(t, errors.New("no cached credential"))},
		{"bare sentinel", ErrCredentials},
		{"wrapped once", fmt.Errorf("agent: start stream: %w", ErrCredentials)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if isTransientForBreaker(tc.err) {
				t.Fatal("a credential failure is no evidence about provider health and must not open the shared breaker")
			}
		})
	}
}

// End to end through Stream: one attempt, the cause surfaced verbatim, and the
// breaker left closed no matter how many turns fail this way.
func TestCredentialFailureSurfacesOnFirstAttemptAndNeverOpensBreaker(t *testing.T) {
	cause := errors.New("no cached ToolHive LLM gateway credential — run `thv llm setup` to log in")
	transportErr := credentialFailure(t, cause)

	f := &fakeProvider{steps: []step{
		{outerErr: transportErr}, {outerErr: transportErr}, {outerErr: transportErr},
		{outerErr: transportErr}, {outerErr: transportErr}, {outerErr: transportErr},
	}}
	cfg := tinyBackoffCfg(3)
	cfg.BreakerThreshold = 5
	cfg.BreakerCooldown = 30 * time.Second
	p := Wrap(f, cfg)

	// Six turns is past a threshold of 5, and each turn would burn 3 attempts if
	// the failure were classified retryable.
	for turn := range 6 {
		_, err := p.Stream(context.Background(), port.LLMRequest{})
		if err == nil {
			t.Fatalf("turn %d: expected the credential failure to surface", turn+1)
		}
		var breakerErr *BreakerError
		if errors.As(err, &breakerErr) {
			t.Fatalf("turn %d: a credential failure must never open the shared breaker, got %v", turn+1, err)
		}
		if !errors.Is(err, cause) {
			t.Fatalf("turn %d: the actionable cause must reach the caller, got %v", turn+1, err)
		}
	}

	// One attempt per turn: no retry budget spent on a failure retrying cannot fix.
	if got := f.Calls(); got != 6 {
		t.Fatalf("provider calls = %d, want 6 (exactly one attempt per turn)", got)
	}
}

// ── BreakerError carries the cause that opened it ────────────────────────────

func TestBreakerErrorCarriesOpeningCause(t *testing.T) {
	cause := apiErr(503)
	f := &fakeProvider{steps: []step{{outerErr: cause}, {outerErr: cause}}}
	cfg := tinyBackoffCfg(1)
	cfg.BreakerThreshold = 1
	cfg.BreakerCooldown = 30 * time.Second
	p := Wrap(f, cfg)

	// First turn trips the breaker (threshold 1).
	if _, err := p.Stream(context.Background(), port.LLMRequest{}); err == nil {
		t.Fatal("expected the first turn to fail")
	}

	// Second turn is rejected by the open breaker and must still name the cause.
	_, err := p.Stream(context.Background(), port.LLMRequest{})
	var breakerErr *BreakerError
	if !errors.As(err, &breakerErr) {
		t.Fatalf("err = %v, want a *BreakerError", err)
	}
	if breakerErr.Cause == nil {
		t.Fatal("BreakerError.Cause must name the failure that opened the breaker")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("the opening cause must be reachable through the rejection, got %v", err)
	}
	if !strings.Contains(err.Error(), "last failure") {
		t.Fatalf("the rendered message must mention the cause, got %q", err.Error())
	}
}

func TestBreakerErrorWithoutCauseKeepsOriginalMessage(t *testing.T) {
	e := &BreakerError{RetryAfter: 30 * time.Second}
	if got, want := e.Error(), "llmresilience: circuit breaker open, retry after 30s"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
