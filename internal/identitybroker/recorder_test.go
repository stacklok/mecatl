package identitybroker

import (
	"context"
	"fmt"
	"testing"
)

func TestMemoryRecorderBoundsAndCopies(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryRecorder()
	for i := range maxMemoryRecords + 1 {
		id := fmt.Sprintf("op-%d", i)
		if err := r.Record(ctx, &DecisionRecord{OperationID: id}); err != nil {
			t.Fatal(err)
		}
		if err := r.Record(ctx, &IDJAGRecord{OperationID: id}); err != nil {
			t.Fatal(err)
		}
	}
	decisions, idjags := r.Decisions(), r.IDJAGs()
	if len(decisions) != maxMemoryRecords || len(idjags) != maxMemoryRecords {
		t.Fatalf("recorder holds %d decisions and %d ID-JAGs, want %d of each", len(decisions), len(idjags), maxMemoryRecords)
	}
	last := fmt.Sprintf("op-%d", maxMemoryRecords)
	if decisions[0].OperationID != "op-1" || decisions[len(decisions)-1].OperationID != last ||
		idjags[0].OperationID != "op-1" || idjags[len(idjags)-1].OperationID != last {
		t.Fatal("the recorder did not drop the oldest record first")
	}
	decisions[0].OperationID, idjags[0].OperationID = "changed", "changed"
	if r.Decisions()[0].OperationID != "op-1" || r.IDJAGs()[0].OperationID != "op-1" {
		t.Fatal("changing a returned record changed the recorder's")
	}
}

func TestDecisionOutcomes(t *testing.T) {
	want := map[Code]DecisionOutcome{
		CodeInternal: OutcomeError, CodeUnavailable: OutcomeError,
		CodePermissionDenied: OutcomeDenied, CodeFailedPrecondition: OutcomeDenied, CodeUnauthenticated: OutcomeDenied,
	}
	for code, outcome := range want {
		if got := outcomeFor(code, ""); got != outcome {
			t.Errorf("outcomeFor(%s) = %s, want %s", code, got, outcome)
		}
	}
	// A record not accepted is an error, even for a converted open, which
	// reports it as CodeUnauthenticated; a close undoing an event is denied.
	if got := outcomeFor(CodeUnauthenticated, ReasonRecordNotAccepted); got != OutcomeError {
		t.Errorf("outcomeFor(record_not_accepted at a converted open) = %s, want %s", got, OutcomeError)
	}
	if got := outcomeFor(ReasonSessionClosed.Code(), ReasonSessionClosed); got != OutcomeDenied {
		t.Errorf("outcomeFor(session_closed) = %s, want %s", got, OutcomeDenied)
	}
}
