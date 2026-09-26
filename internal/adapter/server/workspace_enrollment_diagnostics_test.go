package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type observeDiagnosticRecord struct {
	level   port.Level
	message string
	fields  []any
}

type observeDiagnosticSink struct {
	mu      sync.Mutex
	records []observeDiagnosticRecord
	dropped int
}

func (d *observeDiagnosticSink) Log(ctx context.Context, level port.Level, message string, fields ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ctx.Err() != nil {
		d.dropped++
		return
	}
	d.records = append(d.records, observeDiagnosticRecord{level: level, message: message, fields: append([]any(nil), fields...)})
}

func (d *observeDiagnosticSink) With(...any) port.Diagnostics { return d }

func (d *observeDiagnosticSink) snapshot() ([]observeDiagnosticRecord, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]observeDiagnosticRecord(nil), d.records...), d.dropped
}

func diagnosticField(record observeDiagnosticRecord, key string) (string, bool) {
	for i := 0; i+1 < len(record.fields); i += 2 {
		if got, ok := record.fields[i].(string); ok && got == key {
			value, ok := record.fields[i+1].(string)
			return value, ok
		}
	}
	return "", false
}

func diagnosticRecordFor(records []observeDiagnosticRecord, message string) (observeDiagnosticRecord, bool) {
	for _, record := range records {
		if record.message == message {
			return record, true
		}
	}
	return observeDiagnosticRecord{}, false
}

func TestWorkspaceEnrollmentObserveFailureLogsSafeCauseAfterCancellation(t *testing.T) {
	svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
	diagnostics := &observeDiagnosticSink{}
	svc.cfg.Diagnostics = diagnostics
	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || started.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("begin enrollment = (%+v, %v)", started, err)
	}
	entered := make(chan struct{})
	broker.attachment.observe = func(ctx context.Context, _ brokercontract.WorkspaceEnrollmentRef) (brokercontract.WorkspaceEnrollmentResult, error) {
		close(entered)
		<-ctx.Done()
		return brokercontract.WorkspaceEnrollmentResult{}, ctx.Err()
	}

	ctx, cancel := context.WithCancel(t.Context())
	observed := make(chan error, 1)
	go func() {
		_, observeErr := svc.ConnectWorkspaceServices(ctx, created.ID)
		observed <- observeErr
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ObserveWorkspaceEnrollment was not entered")
	}
	cancel()
	select {
	case err := <-observed:
		if err == nil {
			t.Fatal("ConnectWorkspaceServices succeeded after canceled observation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled observation did not return")
	}

	records, dropped := diagnostics.snapshot()
	if dropped != 0 {
		t.Fatalf("diagnostic sink dropped %d canceled-context record(s)", dropped)
	}
	record, found := diagnosticRecordFor(records, "workspace enrollment observe failed")
	if !found {
		t.Fatalf("diagnostics = %#v, want delivered observe failure", records)
	}
	for key, want := range map[string]string{
		"cause":      "canceled",
		"session":    string(created.ID),
		"enrollment": string(started.Ref.ID),
	} {
		if got, ok := diagnosticField(record, key); !ok || got != want {
			t.Fatalf("diagnostic field %s = %q, %v; want %q", key, got, ok, want)
		}
	}
	if strings.Contains(fmt.Sprint(record.fields), "credential") {
		t.Fatalf("diagnostic leaked non-classified error details: %#v", record.fields)
	}
}

func TestWorkspaceEnrollmentObserveDiagnosticsSeparateMalformedAndMismatchedResponses(t *testing.T) {
	for _, test := range []struct {
		name      string
		message   string
		cause     string
		mutate    func(brokercontract.WorkspaceEnrollmentResult) brokercontract.WorkspaceEnrollmentResult
		forbidden string
	}{
		{
			name: "malformed response", message: "workspace enrollment observe response malformed", cause: "malformed_response", forbidden: "credential-secret",
			mutate: func(result brokercontract.WorkspaceEnrollmentResult) brokercontract.WorkspaceEnrollmentResult {
				result.Status = brokercontract.WorkspaceEnrollmentStatus("credential-secret")
				return result
			},
		},
		{
			name: "reference mismatch", message: "workspace enrollment observe reference mismatch", cause: "reference_mismatch", forbidden: "credential-secret",
			mutate: func(result brokercontract.WorkspaceEnrollmentResult) brokercontract.WorkspaceEnrollmentResult {
				result.Ref.ID = "credential-secret"
				return result
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
			diagnostics := &observeDiagnosticSink{}
			svc.cfg.Diagnostics = diagnostics
			started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
			if err != nil || started.Status != brokercontract.WorkspaceEnrollmentPending {
				t.Fatalf("begin enrollment = (%+v, %v)", started, err)
			}
			broker.attachment.mu.Lock()
			broker.attachment.result = test.mutate(brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentPending})
			broker.attachment.mu.Unlock()
			if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err == nil {
				t.Fatal("invalid broker observation succeeded")
			}
			records, dropped := diagnostics.snapshot()
			record, found := diagnosticRecordFor(records, test.message)
			if dropped != 0 || !found {
				t.Fatalf("diagnostics = %#v, dropped = %d; want delivered %q record", records, dropped, test.message)
			}
			if got, _ := diagnosticField(record, "cause"); got != test.cause {
				t.Fatalf("diagnostic cause = %q, want %q", got, test.cause)
			}
			if strings.Contains(fmt.Sprint(record.fields), test.forbidden) {
				t.Fatalf("diagnostic leaked malformed response details: %#v", record.fields)
			}
		})
	}
}

func TestWorkspaceEnrollmentObserveFailureOmitsUntrustedBrokerError(t *testing.T) {
	svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
	diagnostics := &observeDiagnosticSink{}
	svc.cfg.Diagnostics = diagnostics
	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || started.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("begin enrollment = (%+v, %v)", started, err)
	}
	brokerErr := errors.New("credential-secret provider response")
	broker.attachment.observe = func(context.Context, brokercontract.WorkspaceEnrollmentRef) (brokercontract.WorkspaceEnrollmentResult, error) {
		return brokercontract.WorkspaceEnrollmentResult{}, brokerErr
	}
	if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); !errors.Is(err, ErrFailedPrecondition) || strings.Contains(err.Error(), "credential-secret") {
		t.Fatalf("observe returned unsafe or unexpected error: %v", err)
	}

	records, dropped := diagnostics.snapshot()
	record, found := diagnosticRecordFor(records, "workspace enrollment observe failed")
	if dropped != 0 || !found || record.level != port.LevelWarn {
		t.Fatalf("diagnostics = %#v, dropped = %d; want delivered WARN record", records, dropped)
	}
	for key, want := range map[string]string{
		"cause":      "observe_failed",
		"session":    string(created.ID),
		"enrollment": string(started.Ref.ID),
	} {
		if got, ok := diagnosticField(record, key); !ok || got != want {
			t.Fatalf("diagnostic field %s = %q, %v; want %q", key, got, ok, want)
		}
	}
	if strings.Contains(record.message+fmt.Sprint(record.fields), "credential-secret") {
		t.Fatalf("diagnostic leaked untrusted broker error: %#v", record)
	}
}

var _ port.Diagnostics = (*observeDiagnosticSink)(nil)
