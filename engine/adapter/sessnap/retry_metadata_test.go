package sessnap

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestSnapshotRoundTripsTypedFailureMetadata(t *testing.T) {
	s := session.New("typed", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFailureMetadata(session.RetryDispositionRetryable, session.StreamProgressVisible); err != nil {
		t.Fatal(err)
	}
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Permanent {
		t.Fatal("retryable snapshot marked permanent")
	}
	wire, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	snap = decoded
	got, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if d, p := got.FailureMetadata(); d != session.RetryDispositionRetryable || p != session.StreamProgressVisible {
		t.Fatalf("restored metadata = (%v,%v)", d, p)
	}
}

func TestSnapshotRoundTripsPreparedFailedStepRetry(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "running"}[running], func(t *testing.T) {
			s := session.New("pending", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
			if err := s.BeginTurn(); err != nil {
				t.Fatal(err)
			}
			if err := s.Fail(); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordFailureMetadata(session.RetryDispositionRetryable, session.StreamProgressVisible); err != nil {
				t.Fatal(err)
			}
			if err := s.PrepareFailedStepRetry(); err != nil {
				t.Fatal(err)
			}
			if running {
				if err := s.BeginTurn(); err != nil {
					t.Fatal(err)
				}
			}
			snap, err := Of(s)
			if err != nil {
				t.Fatal(err)
			}
			got, err := snap.Restore()
			if err != nil {
				t.Fatal(err)
			}
			d, p, ok := got.FailedStepRetryPending()
			if !ok || d != session.RetryDispositionRetryable || p != session.StreamProgressVisible || got.State != s.State {
				t.Fatalf("restored pending = (%v,%v,%v), state=%s want %s", d, p, ok, got.State, s.State)
			}
		})
	}
}

func TestSnapshotRejectsInvalidRetryVocabulary(t *testing.T) {
	cases := []Snapshot{
		{ID: "bad-disposition", State: session.StateFailed, StopReason: session.StopError, RetryDisposition: session.RetryDisposition(99), CreatedAt: time.Now()},
		{ID: "bad-progress", State: session.StateFailed, StopReason: session.StopError, StreamProgress: session.StreamProgress(99), CreatedAt: time.Now()},
		{ID: "bad-pending", State: session.StateIdle, RetryPending: true, RetryPendingDisposition: session.RetryDisposition(99), RetryPendingProgress: session.StreamProgressPrecommit, CreatedAt: time.Now()},
	}
	for _, snap := range cases {
		if _, err := snap.Restore(); err == nil {
			t.Fatalf("Restore(%s) accepted invalid retry metadata", snap.ID)
		}
	}
}

func TestSnapshotNewPermanentSetsLegacyBool(t *testing.T) {
	s := session.New("permanent", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFailureMetadata(session.RetryDispositionPermanent, session.StreamProgressPrecommit); err != nil {
		t.Fatal(err)
	}
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Permanent || snap.RetryDisposition != session.RetryDispositionPermanent {
		t.Fatalf("snapshot permanent compatibility = %v, disposition=%v", snap.Permanent, snap.RetryDisposition)
	}
}

func TestSnapshotTypedDispositionWinsOverLegacyPermanent(t *testing.T) {
	snap := Snapshot{
		ID:               "conflict",
		State:            session.StateFailed,
		StopReason:       session.StopError,
		Permanent:        true,
		RetryDisposition: session.RetryDispositionRetryable,
		StreamProgress:   session.StreamProgressPrecommit,
		EnvironmentRef:   session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"},
		CreatedAt:        time.Now(),
	}
	got, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if d, p := got.FailureMetadata(); d != session.RetryDispositionRetryable || p != session.StreamProgressPrecommit || got.FailurePermanence() {
		t.Fatalf("conflicting metadata = (%v,%v), permanent=%v; typed retryable must win", d, p, got.FailurePermanence())
	}
}

func TestSnapshotLegacyPermanentRestoresTypedDisposition(t *testing.T) {
	snap := Snapshot{ID: "legacy", State: session.StateFailed, StopReason: session.StopError, Permanent: true, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, CreatedAt: time.Now()}
	got, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	d, p := got.FailureMetadata()
	if d != session.RetryDispositionPermanent || p != session.StreamProgressUnknown || !got.FailurePermanence() {
		t.Fatalf("legacy metadata = (%v,%v), permanent=%v", d, p, got.FailurePermanence())
	}
}
