package identitybroker

import (
	"context"
	"fmt"
	"testing"
)

var testCall = callKey{
	operationID: "op-1", agent: "spiffe://agents.example.com/mecatl/agent/shared/inst/" + incMain,
	method: MethodToolsCall, server: "github", tool: "github.pull_request_read",
	argumentsSHA256: "abababababababababababababababababababababababababababababababab",
}

func TestMemoryAttemptsForgetTheOldestPastTheBound(t *testing.T) {
	m := newAttemptLedger()
	for i := range maxAttemptsPerSession + 1 {
		m.issueLocked("bs_a", fmt.Sprintf("att_%d", i), testCall)
	}
	if m.knownLocked("bs_a", "att_0") {
		t.Fatal("the oldest attempt is still known past the bound")
	}
	for _, id := range []string{"att_1", fmt.Sprintf("att_%d", maxAttemptsPerSession)} {
		if !m.knownLocked("bs_a", id) {
			t.Fatalf("attempt %s is not known", id)
		}
	}
	if got := len(m.bySession["bs_a"].order); got != maxAttemptsPerSession {
		t.Fatalf("the ledger keeps %d attempts, want %d", got, maxAttemptsPerSession)
	}
}

func TestMemoryAttemptsAreConsumedOnce(t *testing.T) {
	m := newAttemptLedger()
	m.issueLocked("bs_a", "att_1", testCall)
	if !m.knownLocked("bs_a", "att_1") || !m.consumeLocked("bs_a", "att_1", testCall) {
		t.Fatal("an issued attempt is not consumed")
	}
	if m.knownLocked("bs_a", "att_1") || m.consumeLocked("bs_a", "att_1", testCall) {
		t.Fatal("a consumed attempt is consumed again")
	}
	if m.knownLocked("bs_a", "att_never") || m.consumeLocked("bs_a", "att_never", testCall) {
		t.Fatal("an attempt never issued is consumed")
	}
}

func TestMemoryAttemptsMatchTheCall(t *testing.T) {
	m := newAttemptLedger()
	m.issueLocked("bs_a", "att_1", testCall)
	if m.knownLocked("bs_b", "att_1") || m.consumeLocked("bs_b", "att_1", testCall) {
		t.Fatal("another session consumed the attempt")
	}
	other := testCall
	other.tool = "github.create_pull_request"
	if m.consumeLocked("bs_a", "att_1", other) {
		t.Fatal("a report for another call consumed the attempt")
	}
	// The mismatched report used the attempt up.
	if m.consumeLocked("bs_a", "att_1", testCall) {
		t.Fatal("the attempt is consumed after a mismatched report used it up")
	}
}

func TestAttemptsAreDroppedWhenTheSessionCloses(t *testing.T) {
	b, err := New(testConfig(), nil, testKey(t, "attempts"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	workload := Workload{Name: "system:serviceaccount:mecatl:mecak8s"}
	owner := Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	s, err := b.OpenSession(ctx, workload, OpenSessionRequest{HostSessionID: "hostS42", Incarnation: incMain, Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	b.sessions.mu.Lock()
	b.sessions.attempts.issueLocked(s.ID, "att_1", testCall)
	b.sessions.mu.Unlock()
	if err := b.CloseSession(ctx, workload, CloseSessionRequest{SessionID: s.ID, Reason: ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	b.sessions.mu.Lock()
	defer b.sessions.mu.Unlock()
	if b.sessions.attempts.knownLocked(s.ID, "att_1") {
		t.Fatal("a closed session's attempt is still known")
	}
	if _, ok := b.sessions.attempts.bySession[s.ID]; ok {
		t.Fatal("a closed session's ledger is kept")
	}
}
