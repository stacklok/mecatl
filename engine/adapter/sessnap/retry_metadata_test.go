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
	if err := s.RecordFailureMetadata(session.RetryMetadata{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible}); err != nil {
		t.Fatal(err)
	}
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
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
	want := session.RetryMetadata{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible}
	if got := got.FailureMetadata(); got != want {
		t.Fatalf("restored metadata = %+v, want %+v", got, want)
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
			if err := s.RecordFailureMetadata(session.RetryMetadata{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible}); err != nil {
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
			metadata, ok := got.FailedStepRetryPending()
			want := session.RetryMetadata{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible}
			if !ok || metadata != want || got.State != s.State {
				t.Fatalf("restored pending = (%+v,%v), state=%s want %s", metadata, ok, got.State, s.State)
			}
		})
	}
}

func TestSnapshotRejectsInvalidRetryVocabulary(t *testing.T) {
	env := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	cases := []Snapshot{
		{ID: "bad-disposition", State: session.StateFailed, StopReason: session.StopError, RetryDisposition: session.RetryDisposition(99), TokenUsage: map[session.UsageKind]session.TokenUsage{}, EnvironmentRef: env, CreatedAt: time.Now()},
		{ID: "bad-progress", State: session.StateFailed, StopReason: session.StopError, StreamProgress: session.StreamProgress(99), TokenUsage: map[session.UsageKind]session.TokenUsage{}, EnvironmentRef: env, CreatedAt: time.Now()},
		{ID: "bad-pending", State: session.StateIdle, RetryPending: true, RetryPendingDisposition: session.RetryDisposition(99), RetryPendingProgress: session.StreamProgressPrecommit, TokenUsage: map[session.UsageKind]session.TokenUsage{}, EnvironmentRef: env, CreatedAt: time.Now()},
	}
	for _, snap := range cases {
		if _, err := snap.Restore(); err == nil {
			t.Fatalf("Restore(%s) accepted invalid retry metadata", snap.ID)
		}
	}
}

func TestSnapshotRecordsPermanentRetryDisposition(t *testing.T) {
	s := session.New("permanent", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(); err != nil {
		t.Fatal(err)
	}
	want := session.RetryMetadata{Disposition: session.RetryDispositionPermanent, Progress: session.StreamProgressPrecommit}
	if err := s.RecordFailureMetadata(want); err != nil {
		t.Fatal(err)
	}
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	if snap.RetryDisposition != want.Disposition || snap.StreamProgress != want.Progress {
		t.Fatalf("snapshot retry metadata = (%v,%v), want %+v", snap.RetryDisposition, snap.StreamProgress, want)
	}
}

func TestSnapshotRejectsLegacyRetryAndUsageProjections(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire string
	}{
		{name: "permanent", wire: `{"token_usage":{},"permanent":true}`},
		{name: "usage", wire: `{"token_usage":{},"usage":{"InputTokens":1}}`},
		{name: "missing token usage", wire: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var snap Snapshot
			if err := json.Unmarshal([]byte(tc.wire), &snap); err == nil {
				t.Fatal("legacy snapshot projection accepted")
			}
		})
	}
}
