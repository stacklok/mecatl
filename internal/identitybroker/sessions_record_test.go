package identitybroker

import (
	"context"
	"testing"
	"time"
)

func TestClosedSessionRecordKeepsIDOwnerAndClosingTime(t *testing.T) {
	b, err := New(testConfig(), nil, testKey(t, "closed record"))
	if err != nil {
		t.Fatal(err)
	}
	closedAt := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	later := closedAt.Add(time.Hour)
	clock := closedAt
	b.sessions.now = func() time.Time { return clock }

	ctx := context.Background()
	workload := Workload{Name: "system:serviceaccount:mecatl:mecak8s"}
	owner := Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	s, err := b.OpenSession(ctx, workload, OpenSessionRequest{HostSessionID: "hostS42", Incarnation: "inc_hostS42", Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	if rec := b.sessions.byID[s.ID]; !rec.closedAt.IsZero() {
		t.Fatalf("open session has closing time %v", rec.closedAt)
	}
	closeReq := CloseSessionRequest{SessionID: s.ID, Reason: ReasonSessionClosed}
	if err := b.CloseSession(ctx, workload, closeReq); err != nil {
		t.Fatal(err)
	}
	// Closing again keeps the first closing time, as it keeps the first reason.
	clock = later
	if err := b.CloseSession(ctx, workload, closeReq); err != nil {
		t.Fatal(err)
	}
	rec := b.sessions.byID[s.ID]
	if rec.ID != s.ID || rec.Owner != owner || rec.closed != ReasonSessionClosed || !rec.closedAt.Equal(closedAt) {
		t.Fatalf("closed record = {ID: %s, Owner: %+v, closed: %s, closedAt: %v}, want {%s, %+v, %s, %v}",
			rec.ID, rec.Owner, rec.closed, rec.closedAt, s.ID, owner, ReasonSessionClosed, closedAt)
	}
}
