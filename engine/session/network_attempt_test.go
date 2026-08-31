package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNetworkCorrelationDigestIsOneWayFixedAndDomainSeparated(t *testing.T) {
	secret := "Bearer-SECRET"
	request, ok := NetworkCorrelationDigest("request", secret)
	if !ok {
		t.Fatal("request digest rejected")
	}
	requestAgain, _ := NetworkCorrelationDigest("request", secret)
	trace, _ := NetworkCorrelationDigest("trace", secret)
	if len(request) != 64 || request != requestAgain || request == trace || strings.Contains(request, secret) {
		t.Fatalf("digests request=%q again=%q trace=%q", request, requestAgain, trace)
	}
	if _, ok := NetworkCorrelationDigest("arbitrary", secret); ok {
		t.Fatal("unsupported correlation kind accepted")
	}
}

func TestCanonicalNetworkAttemptValidatesWholePayloadAndBindsTrustedCorrelation(t *testing.T) {
	digest, _ := NetworkCorrelationDigest("request", "sk-live-SECRET")
	valid := NetworkAttemptPayload{
		SessionID: "forged", RunSerial: 999, Turn: 999,
		Attempt: 1, MaxAttempts: 2, ElapsedMs: 4, RetryDisposition: "retryable",
		StreamProgress: "precommit", Decision: "retry", BackoffMs: 8,
		FailureClass: "connect", HTTPStatus: 503,
		CorrelationKind: "request", CorrelationDigest: digest,
	}
	got, ok := CanonicalNetworkAttempt(valid, "trusted", 7, 3)
	if !ok {
		t.Fatal("valid attempt rejected")
	}
	if got.SessionID != "trusted" || got.RunSerial != 7 || got.Turn != 3 || got.CorrelationDigest != digest {
		t.Fatalf("canonical attempt = %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-live-SECRET", "Bearer-SECRET"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("canonical structure leaked %q: %s", secret, encoded)
		}
	}

	invalid := []NetworkAttemptPayload{
		{Attempt: 0, MaxAttempts: 2, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: "connect"},
		{Attempt: 1, MaxAttempts: 2, RetryDisposition: "sk-live-SECRET", StreamProgress: "precommit", Decision: "retry", FailureClass: "connect"},
		{Attempt: 1, MaxAttempts: 2, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", SuppressionReason: "permanent", FailureClass: "connect"},
		{Attempt: 1, MaxAttempts: 2, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: "connect", CorrelationKind: "request", CorrelationDigest: "Bearer-SECRET"},
		{Attempt: 1, MaxAttempts: 2, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: "trace-SECRET"},
	}
	for i, observation := range invalid {
		if _, ok := CanonicalNetworkAttempt(observation, "trusted", 7, 3); ok {
			t.Errorf("invalid observation %d accepted: %+v", i, observation)
		}
	}
}
