package llmresilience

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type attemptMetadataError struct {
	err      error
	metadata attemptErrorMetadata
}

func (e *attemptMetadataError) Error() string             { return e.err.Error() }
func (e *attemptMetadataError) Unwrap() error             { return e.err }
func (e *attemptMetadataError) ProviderHTTPStatus() int   { return e.metadata.httpStatus }
func (e *attemptMetadataError) ProviderInBandStatus() int { return e.metadata.inBandStatus }
func (e *attemptMetadataError) ProviderErrorCode() string { return e.metadata.providerCode }
func (e *attemptMetadataError) ProviderErrorCorrelationKind() string {
	return e.metadata.correlationKind
}
func (e *attemptMetadataError) ProviderErrorCorrelationID() string { return e.metadata.correlationID }

func decisionRecords(d *recordingDiag) []diagRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	var records []diagRecord
	for _, record := range d.records {
		if argValue(record.args, "decision") != nil {
			records = append(records, record)
		}
	}
	return records
}

func requireDecisionFields(t *testing.T, record diagRecord, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if got := argValue(record.args, key); got != value {
			t.Errorf("%s = %#v (%T), want %#v (%T); args=%#v", key, got, got, value, value, record.args)
		}
	}
	for _, forbidden := range []string{"err", "error", "url", "headers", "response_body"} {
		if got := argValue(record.args, forbidden); got != nil {
			t.Errorf("forbidden %s field = %#v", forbidden, got)
		}
	}
}

type structuralAttemptProvider struct{ calls int }

func (*structuralAttemptProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *structuralAttemptProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.calls++
	call := p.calls
	return func(yield func(port.Chunk, error) bool) {
		if call == 1 {
			terminal := false
			port.ObserveAttempt(ctx, session.NetworkAttemptPayload{ProviderTerminalObserved: &terminal, StreamOutcome: "stream_error"})
			yield(port.Chunk{}, io.ErrUnexpectedEOF)
			return
		}
		terminal := true
		port.ObserveAttempt(ctx, session.NetworkAttemptPayload{ProviderTerminalObserved: &terminal, StreamOutcome: "complete"})
		yield(port.Chunk{Kind: port.ChunkText, Text: "ok"}, nil)
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

// deferredObserveProvider yields one visible chunk BEFORE reporting any
// structural evidence, then reports the terminal observation only when
// yielding its final chunk — mirroring a real adapter that cannot know the
// stream's structural outcome until the provider's own terminal marker
// arrives. Used to prove a chunk is observable while structural evidence
// remains unfinalized (ADR 0357 AC1.2).
type deferredObserveProvider struct{}

func (*deferredObserveProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (*deferredObserveProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		if !yield(port.Chunk{Kind: port.ChunkText, Text: "partial"}, nil) {
			return
		}
		terminal := true
		port.ObserveAttempt(ctx, session.NetworkAttemptPayload{ProviderTerminalObserved: &terminal, StreamOutcome: "complete"})
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

// outcomeReportingProvider yields one visible chunk, reports the given
// structural evidence, then ends the attempt with err.
type outcomeReportingProvider struct {
	terminal bool
	outcome  string
	err      error
}

func (*outcomeReportingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *outcomeReportingProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		terminal := p.terminal
		port.ObserveAttempt(ctx, session.NetworkAttemptPayload{ProviderTerminalObserved: &terminal, StreamOutcome: p.outcome})
		if !yield(port.Chunk{Kind: port.ChunkText, Text: "partial"}, nil) {
			return
		}
		yield(port.Chunk{}, p.err)
	}, nil
}

// TestADR_0357_Scenario1_PreservesStreaming proves AC1.2: the first model
// chunk is observable while the fixture source remains open (no
// whole-response buffering), and structural evidence is finalized only at
// attempt termination.
func TestADR_0357_Scenario1_PreservesStreaming(t *testing.T) {
	var observations []session.NetworkAttemptPayload
	ctx := port.WithAttemptObserver(context.Background(), func(row session.NetworkAttemptPayload) {
		observations = append(observations, row)
	})
	seq, err := Wrap(&deferredObserveProvider{}, Config{MaxAttempts: 1}).Stream(ctx, port.LLMRequest{Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	sawTextBeforeFinalization := false
	for c, streamErr := range seq {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		if c.Kind == port.ChunkText {
			text += c.Text
			sawTextBeforeFinalization = len(observations) == 0
		}
	}
	if text != "partial" {
		t.Fatalf("visible text = %q", text)
	}
	if !sawTextBeforeFinalization {
		t.Fatal("first chunk was not observable before structural evidence was finalized")
	}
	if len(observations) != 1 || observations[0].StreamOutcome != "complete" ||
		observations[0].ProviderTerminalObserved == nil || !*observations[0].ProviderTerminalObserved {
		t.Fatalf("structural evidence finalized at attempt termination = %+v", observations)
	}
}

// TestADR_0357_Scenario2_IncompleteErrorCancelled proves AC2.1: truncated,
// errored, and cancelled attempts produce the approved closed outcomes with
// ProviderTerminalObserved=false when the adapter determines no recognized
// terminal semantic was accepted.
func TestADR_0357_Scenario2_IncompleteErrorCancelled(t *testing.T) {
	tests := []struct {
		name    string
		outcome string
		err     error
	}{
		{name: "truncated", outcome: "incomplete", err: errors.New("truncated mid-stream")},
		{name: "errored", outcome: "stream_error", err: errors.New("stream broke")},
		{name: "cancelled", outcome: "cancelled", err: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &outcomeReportingProvider{terminal: false, outcome: test.outcome, err: test.err}
			var observations []session.NetworkAttemptPayload
			ctx := port.WithAttemptObserver(context.Background(), func(row session.NetworkAttemptPayload) {
				observations = append(observations, row)
			})
			seq, err := Wrap(provider, Config{MaxAttempts: 1}).Stream(ctx, port.LLMRequest{Model: "test"})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = drain(t, seq)
			if len(observations) != 1 {
				t.Fatalf("observations = %d, want exactly one final row: %+v", len(observations), observations)
			}
			got := observations[0]
			if got.StreamOutcome != test.outcome {
				t.Fatalf("stream outcome = %q, want %q", got.StreamOutcome, test.outcome)
			}
			if got.ProviderTerminalObserved == nil || *got.ProviderTerminalObserved {
				t.Fatalf("provider_terminal_observed = %v, want explicit false", got.ProviderTerminalObserved)
			}
		})
	}
}

// TestADR_0357_Scenario2_PreStreamFailureEvidence proves AC2.3: a pre-stream
// establishment failure retains the existing failure evidence and does not
// receive a fabricated structural completion summary.
func TestADR_0357_Scenario2_PreStreamFailureEvidence(t *testing.T) {
	var observations []session.NetworkAttemptPayload
	ctx := port.WithAttemptObserver(context.Background(), func(row session.NetworkAttemptPayload) {
		observations = append(observations, row)
	})
	_, err := Wrap(&fakeProvider{steps: []step{{outerErr: connectionFailure()}}}, Config{MaxAttempts: 1}).
		Stream(ctx, port.LLMRequest{Model: "test"})
	if err == nil {
		t.Fatal("Stream error = nil, want pre-stream establishment failure")
	}
	if len(observations) != 1 {
		t.Fatalf("observations = %d, want 1: %+v", len(observations), observations)
	}
	got := observations[0]
	if got.StreamOutcome != "" || got.ProviderTerminalObserved != nil {
		t.Fatalf("pre-stream failure fabricated structural evidence = %+v", got)
	}
	if got.Decision != "terminal" || got.FailureClass != "connect" {
		t.Fatalf("pre-stream failure lost existing failure evidence = %+v", got)
	}
}

// TestADR_0357_Scenario3_ObservationLifecycle proves AC3.2: the resilience
// wrapper emits at most one final structural summary for each outer
// attempt/decision slot. This is the regression test for the double-report
// bug: an ordinary consumer that BREAKS its range loop on a mid-stream error
// used to receive two terminal rows for the one outer attempt (logMidStreamError
// reporting it once, wrap's !consumed branch reporting it again).
func TestADR_0357_Scenario3_ObservationLifecycle(t *testing.T) {
	provider := &outcomeReportingProvider{terminal: false, outcome: "stream_error", err: errors.New("boom")}
	var observations []session.NetworkAttemptPayload
	ctx := port.WithAttemptObserver(context.Background(), func(row session.NetworkAttemptPayload) {
		observations = append(observations, row)
	})
	seq, err := Wrap(provider, Config{MaxAttempts: 1}).Stream(ctx, port.LLMRequest{Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, streamErr := range seq {
		if streamErr != nil {
			break
		}
	}
	if len(observations) != 1 {
		t.Fatalf("observations = %d, want at most one final structural summary per outer attempt: %+v", len(observations), observations)
	}
}

func TestADR_0357_Scenario3_RetryCorrelation(t *testing.T) {
	provider := &structuralAttemptProvider{}
	var observations []session.NetworkAttemptPayload
	ctx := port.WithAttemptObserver(context.Background(), func(row session.NetworkAttemptPayload) {
		observations = append(observations, row)
	})
	seq, err := Wrap(provider, Config{MaxAttempts: 2}).Stream(ctx, port.LLMRequest{Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(t, seq); err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 {
		t.Fatalf("observations = %d, want one final row per outer attempt", len(observations))
	}
	if observations[0].Attempt != 1 || observations[0].Decision != "retry" || observations[0].StreamOutcome != "stream_error" ||
		observations[1].Attempt != 2 || observations[1].Decision != "terminal" || observations[1].StreamOutcome != "complete" {
		t.Fatalf("retry observations = %+v", observations)
	}
	if observations[1].ProviderTerminalObserved == nil || !*observations[1].ProviderTerminalObserved {
		t.Fatalf("successful terminal evidence = %+v", observations[1])
	}
}

func TestAttemptDecisionRetryFieldsElapsedMetadataAndSession(t *testing.T) {
	clock := &manualClock{t: time.Unix(10, 0)}
	diag := &recordingDiag{}
	secretValues := []string{"sk-live-SECRET", "Bearer-SECRET", "trace-SECRET"}
	failure := &attemptMetadataError{
		err: &net.OpError{Op: "dial", Err: errors.New(secretValues[2])},
		metadata: attemptErrorMetadata{
			httpStatus:      503,
			inBandStatus:    429,
			providerCode:    secretValues[0],
			correlationKind: "request",
			correlationID:   secretValues[1],
		},
	}
	provider := &fakeProvider{
		steps: []step{{outerErr: failure}, {chunks: textTurn("ok")}},
		onAttempt: func(_ context.Context, attempt int) {
			if attempt == 0 {
				clock.Advance(250 * time.Millisecond)
			}
		},
	}
	cfg := Config{
		MaxAttempts: 2,
		BaseBackoff: 0,
		MaxBackoff:  0,
		Clock:       clock.Now,
		Diagnostics: diag,
	}
	var observations []session.NetworkAttemptPayload
	ctx := port.WithAttemptObserver(
		port.WithTurnIndex(port.WithRunSerial(port.WithSessionID(context.Background(), "session-409"), 17), 3),
		func(observation session.NetworkAttemptPayload) { observations = append(observations, observation) },
	)
	seq, err := Wrap(provider, cfg).Stream(ctx, port.LLMRequest{Model: "model-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(t, seq); err != nil {
		t.Fatal(err)
	}

	records := decisionRecords(diag)
	if len(records) != 1 {
		t.Fatalf("decision records = %d, want 1: %#v", len(records), records)
	}
	wantDigest, ok := session.NetworkCorrelationDigest("request", secretValues[1])
	if !ok {
		t.Fatal("correlation digest rejected test input")
	}
	requireDecisionFields(t, records[0], map[string]any{
		"model": "model-a", "attempt": 1, "max_attempts": 2,
		"elapsed": 250 * time.Millisecond, "retry_disposition": "retryable",
		"stream_progress": "precommit", "decision": "retry",
		"backoff": time.Duration(0), "session": session.SessionID("session-409"),
		"run_serial": int64(17), "turn": 3,
		"http_status": 503, "in_band_status": 429,
		"correlation_kind": "request", "correlation_digest": wantDigest,
	})
	for _, secret := range secretValues {
		if rendered := fmt.Sprint(records[0].args); strings.Contains(rendered, secret) {
			t.Fatalf("diagnostic leaked producer token %q: %q", secret, rendered)
		}
	}
	if len(observations) != 2 {
		t.Fatalf("attempt observations = %d, want retry and successful final rows", len(observations))
	}
	observation := observations[0]
	if observation.SessionID != "session-409" || observation.RunSerial != 17 || observation.Turn != 3 ||
		observation.Attempt != 1 || observation.MaxAttempts != 2 || observation.ElapsedMs != 250 ||
		observation.Decision != "retry" || observation.FailureClass != "connect" || observation.HTTPStatus != 503 ||
		observation.InBandStatus != 429 || observation.CorrelationKind != "request" || observation.CorrelationDigest != wantDigest {
		t.Fatalf("typed observation = %+v", observation)
	}
	if final := observations[1]; final.Attempt != 2 || final.StreamOutcome != "unavailable" || final.ProviderTerminalObserved != nil {
		t.Fatalf("successful final observation = %+v", final)
	}
	marshaled, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secretValues {
		if strings.Contains(fmt.Sprintf("%+v %s", observation, marshaled), secret) {
			t.Fatalf("typed observation leaked producer token %q: %+v / %s", secret, observation, marshaled)
		}
	}
	if argValue(records[0].args, "replay_suppressed_reason") != nil {
		t.Error("retry decision carried a terminal suppression reason")
	}
}

func TestAttemptDecisionTerminalReasons(t *testing.T) {
	connection := func() error { return &net.OpError{Op: "dial", Err: errors.New("secret-token=do-not-log")} }
	tests := []struct {
		name       string
		failure    error
		classifier func(error) bool
		wantReason replaySuppressedReason
		wantDisp   session.RetryDisposition
	}{
		{name: "permanent", failure: apiErr(400), wantReason: replayPermanent, wantDisp: session.RetryDispositionPermanent},
		{name: "unknown", failure: errors.New("secret unknown body"), wantReason: replayUnknown, wantDisp: session.RetryDispositionUnknown},
		{name: "classifier veto", failure: connection(), classifier: func(error) bool { return false }, wantReason: replayClassifierVeto, wantDisp: session.RetryDispositionRetryable},
		{name: "provider internal veto", failure: &explicitNoRetryTestError{err: connection()}, wantReason: replayProviderInternalVeto, wantDisp: session.RetryDispositionRetryable},
		{name: "attempts exhausted", failure: connection(), wantReason: replayAttemptsExhausted, wantDisp: session.RetryDispositionRetryable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diag := &recordingDiag{}
			clock := &manualClock{t: time.Unix(1, 0)}
			maxAttempts := 3
			if test.wantReason == replayAttemptsExhausted {
				maxAttempts = 1
			}
			cfg := Config{MaxAttempts: maxAttempts, Diagnostics: diag, Classifier: test.classifier, Clock: clock.Now}
			_, _ = Wrap(&fakeProvider{steps: []step{{outerErr: test.failure}}}, cfg).
				Stream(context.Background(), port.LLMRequest{Model: "model-terminal"})
			records := decisionRecords(diag)
			if len(records) != 1 {
				t.Fatalf("decision records = %d, want 1: %#v", len(records), records)
			}
			requireDecisionFields(t, records[0], map[string]any{
				"model": "model-terminal", "attempt": 1, "max_attempts": maxAttempts,
				"elapsed": time.Duration(0), "retry_disposition": retryDispositionDiagnostic(test.wantDisp),
				"stream_progress": "precommit", "decision": "terminal",
				"replay_suppressed_reason": string(test.wantReason),
			})
			if argValue(records[0].args, "backoff") != nil {
				t.Error("terminal decision carried backoff")
			}
			if strings.Contains(fmt.Sprint(records[0].args), "secret") {
				t.Fatalf("terminal diagnostic leaked raw error text: %#v", records[0].args)
			}
		})
	}
}

func TestAttemptDecisionVisibleAndBreakerOpen(t *testing.T) {
	t.Run("visible output", func(t *testing.T) {
		diag := &recordingDiag{}
		clock := &manualClock{t: time.Unix(1, 0)}
		provider := &fakeProvider{steps: []step{{chunks: []port.Chunk{{Kind: port.ChunkText, Text: "visible"}}, midErr: errors.New("secret body")}}}
		seq, err := Wrap(provider, Config{MaxAttempts: 4, Diagnostics: diag, Clock: clock.Now}).Stream(context.Background(), port.LLMRequest{Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = drain(t, seq)
		records := decisionRecords(diag)
		if len(records) != 1 {
			t.Fatalf("decision records = %d, want 1", len(records))
		}
		requireDecisionFields(t, records[0], map[string]any{
			"model": "m", "attempt": 1, "max_attempts": 4, "elapsed": time.Duration(0),
			"retry_disposition": "unknown", "stream_progress": "visible",
			"decision": "terminal", "replay_suppressed_reason": string(replayVisibleOutput),
		})
	})

	t.Run("breaker open", func(t *testing.T) {
		clock := &manualClock{t: time.Unix(1, 0)}
		diag := &recordingDiag{}
		provider := &fakeProvider{steps: []step{{outerErr: connectionFailure()}}}
		wrapped := Wrap(provider, Config{
			MaxAttempts: 1, BreakerThreshold: 1, BreakerCooldown: time.Hour,
			Clock: clock.Now, Diagnostics: diag,
		})
		_, _ = wrapped.Stream(context.Background(), port.LLMRequest{Model: "m"})
		before := len(decisionRecords(diag))
		_, _ = wrapped.Stream(context.Background(), port.LLMRequest{Model: "m"})
		records := decisionRecords(diag)
		if len(records) != before+1 {
			t.Fatalf("decision records grew by %d, want 1", len(records)-before)
		}
		record := records[len(records)-1]
		requireDecisionFields(t, record, map[string]any{
			"model": "m", "attempt": 1, "max_attempts": 1, "elapsed": time.Duration(0),
			"retry_disposition": "unknown", "stream_progress": "precommit",
			"decision": "terminal", "replay_suppressed_reason": string(replayBreakerOpen),
		})
	})
}

func TestAttemptFailureClassUsesSanitizedClosedVocabulary(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		metadata attemptErrorMetadata
		want     string
	}{
		{name: "dns", err: &net.DNSError{Err: "secret", Name: "private.example"}, want: "dns"},
		{name: "connect", err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, want: "connect"},
		{name: "tls", err: tls.RecordHeaderError{}, want: "tls"},
		{name: "timeout", err: context.DeadlineExceeded, want: "timeout"},
		{name: "connection reset", err: syscall.ECONNRESET, want: "connection_reset"},
		{name: "rate limit", err: errors.New("secret body"), metadata: attemptErrorMetadata{httpStatus: 429}, want: "rate_limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := attemptFailureClass(test.err, test.metadata); got != test.want {
				t.Fatalf("class = %q, want %q", got, test.want)
			}
		})
	}
}

func connectionFailure() error {
	return &net.OpError{Op: "dial", Err: errors.New("refused")}
}

func TestAttemptDecisionInvalidMetadataIsOmittedAndErrorContractSurvives(t *testing.T) {
	metadata := attemptErrorMetadata{
		httpStatus: 503, providerCode: "bad code",
		correlationKind: "unsupported", correlationID: "req-123",
	}
	failure := &attemptMetadataError{err: errors.New("raw secret"), metadata: metadata}
	diag := &recordingDiag{}
	_, returned := Wrap(&fakeProvider{steps: []step{{outerErr: failure}}}, Config{MaxAttempts: 2, Diagnostics: diag}).
		Stream(context.Background(), port.LLMRequest{})
	var carrier port.ProviderErrorMetadataError
	if !errors.As(returned, &carrier) || carrier.ProviderHTTPStatus() != metadata.httpStatus ||
		carrier.ProviderErrorCode() != metadata.providerCode {
		t.Fatalf("returned error lost metadata contract: %v", returned)
	}
	records := decisionRecords(diag)
	if len(records) != 1 {
		t.Fatalf("decision records = %d, want 1", len(records))
	}
	for _, key := range []string{"http_status", "in_band_status", "provider_code", "correlation_kind", "correlation_id", "correlation_digest"} {
		if got := argValue(records[0].args, key); got != nil {
			t.Errorf("invalid metadata field %s logged as %#v", key, got)
		}
	}
}

func TestAttemptMetadataValidation(t *testing.T) {
	valid := []attemptErrorMetadata{
		{},
		{httpStatus: 503},
		{inBandStatus: 429, providerCode: "rate_limit_exceeded"},
		{correlationKind: "request", correlationID: "req-123"},
		{correlationKind: "response", correlationID: "resp-123"},
		{correlationKind: "trace", correlationID: "trace-123"},
		{correlationKind: "completion", correlationID: "chatcmpl-123"},
		{correlationKind: "message", correlationID: "msg-123"},
		{correlationKind: "request", correlationID: "secret id"},
		{correlationKind: "request", correlationID: "https://private.example/?token=secret"},
	}
	for _, metadata := range valid {
		if !metadata.valid() {
			t.Errorf("metadata %+v should be valid", metadata)
		}
	}
	invalid := []attemptErrorMetadata{
		{httpStatus: 99}, {httpStatus: 600}, {inBandStatus: 42},
		{providerCode: "has space"}, {providerCode: "line\nbreak"},
		{providerCode: strings.Repeat("x", 129)},
		{correlationKind: "span", correlationID: "id"},
		{correlationKind: "request"}, {correlationID: "req-123"},
		{correlationKind: "request", correlationID: strings.Repeat("x", 4097)},
	}
	for _, metadata := range invalid {
		if metadata.valid() {
			t.Errorf("metadata %+v should be invalid", metadata)
		}
	}
}

func TestCancellationDoesNotEmitAttemptFailureDecision(t *testing.T) {
	diag := &recordingDiag{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = Wrap(&fakeProvider{steps: []step{{outerErr: context.Canceled}}}, Config{MaxAttempts: 2, Diagnostics: diag}).
		Stream(ctx, port.LLMRequest{})
	if records := decisionRecords(diag); len(records) != 0 {
		t.Fatalf("cancellation emitted attempt decisions: %#v", records)
	}
}
