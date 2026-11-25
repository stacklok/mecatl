package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// refusingRecorder refuses every record.
type refusingRecorder struct{}

func (refusingRecorder) Record(context.Context, identitybroker.Record) error {
	return errors.New("sink unavailable")
}

// TestLoggedRecorderSummarisesWithoutSubjects pins that the identity broker's
// sink logs one debug line per accepted record with only its type, AuditID,
// outcome, and refusal, never its subjects, and passes a refusal on without
// logging the record.
func TestLoggedRecorderSummarisesWithoutSubjects(t *testing.T) {
	diag := &kvDiag{}
	mem := identitybroker.NewMemoryRecorder()
	rec := &identitybroker.DecisionRecord{
		RecordHeader: identitybroker.RecordHeader{AuditID: "rec_test", Outcome: identitybroker.OutcomeDenied, Code: identitybroker.CodePermissionDenied, Reason: identitybroker.ReasonDenied},
		SessionID:    "bs_secretsession", Owner: identitybroker.Owner{Issuer: "https://idp.example.com", Subject: "alice-204"},
		RunOwner: identitybroker.Owner{Issuer: "https://idp.example.com", Subject: "alice-204"}, OperationID: "op-1",
	}
	if err := (loggedRecorder{recorder: mem, diag: diag}).Record(context.Background(), rec); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(mem.Decisions()) != 1 {
		t.Fatal("the wrapped recorder did not keep the record")
	}
	diag.mu.Lock()
	msgs, args, lvl := diag.msgs, diag.args, diag.lvl
	diag.mu.Unlock()
	if len(msgs) != 1 || msgs[0] != "identity broker record" || lvl[0] != port.LevelDebug {
		t.Fatalf("logged %v at %v, want one debug summary", msgs, lvl)
	}
	fields := map[string]string{}
	for i := 0; i+1 < len(args[0]); i += 2 {
		fields[fmt.Sprint(args[0][i])] = fmt.Sprint(args[0][i+1])
	}
	want := map[string]string{
		"type": identitybroker.TypeToolDecision, "audit_id": "rec_test", "outcome": "denied",
		"code": string(identitybroker.CodePermissionDenied), "reason": string(identitybroker.ReasonDenied),
	}
	if fmt.Sprint(fields) != fmt.Sprint(want) {
		t.Fatalf("summary fields = %v, want only %v", fields, want)
	}

	refusing := &kvDiag{}
	if err := (loggedRecorder{recorder: refusingRecorder{}, diag: refusing}).Record(context.Background(), rec); err == nil {
		t.Fatal("a refused record was reported accepted")
	}
	if len(refusing.msgs) != 0 {
		t.Fatalf("a refused record was logged as accepted: %v", refusing.msgs)
	}
}

// TestShutdownSendsTheIdentityBrokersQueuedRecords pins that Built.Close
// drains the identity broker's queued close records into its sink, so none
// queued at shutdown is lost unreported, and an end-to-end test can read them
// after Close.
func TestShutdownSendsTheIdentityBrokersQueuedRecords(t *testing.T) {
	g := buildWithRealAS1(t, false, nil, nil)
	s, _, _ := g.open(t)
	if err := g.built.IdentityBroker.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	g.built.Close()
	closes := g.built.IdentityBrokerRecords().Closes()
	if len(closes) != 1 || closes[0].SessionID != s.ID {
		t.Fatalf("close records after shutdown = %+v, want the close of %s", closes, s.ID)
	}
}
