package sessnap

import (
	"encoding/base64"
	"slices"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func snapshotCustody(t *testing.T, s *session.Session) session.BrokerCredentialCustody {
	t.Helper()
	var owner, workload, profile [32]byte
	owner[0], workload[0], profile[0] = 1, 2, 3
	c, err := session.NewBrokerCredentialCustody(base64.RawURLEncoding.EncodeToString(make([]byte, 32)), s.Incarnation(), owner, workload, profile, []string{"alpha"}, time.Now().Add(time.Hour).UTC())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBrokerCredentialCustodySnapshotRoundTrip(t *testing.T) {
	s, pending := enrollmentSnapshotSession(t)
	want := snapshotCustody(t, s)
	if err := s.InstallBrokerCredentialCustody(pending, want, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteWorkspaceEnrollment(pending, []string{"Read"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUserPrompt("already used", nil); err != nil {
		t.Fatal(err)
	}
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	if snap.BrokerCredentialCustody == nil {
		t.Fatal("custody absent from snapshot")
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := restored.BrokerCredentialCustody()
	if !ok || got.RecoveryReference() != want.RecoveryReference() || got.SessionIncarnation() != want.SessionIncarnation() {
		t.Fatalf("custody = %+v, %v", got, ok)
	}
}

func TestBrokerCredentialCustodySnapshotRestoresPendingEnrollment(t *testing.T) {
	s, pending := enrollmentSnapshotSession(t)
	want := snapshotCustody(t, s)
	if err := s.InstallBrokerCredentialCustody(pending, want, time.Now()); err != nil {
		t.Fatal(err)
	}
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	gotPending, ok := restored.PendingWorkspaceEnrollment()
	if !ok || gotPending != pending {
		t.Fatalf("pending = %+v, %v; want %+v, true", gotPending, ok, pending)
	}
	got, ok := restored.BrokerCredentialCustody()
	if !ok || got.RecoveryReference() != want.RecoveryReference() || got.SessionIncarnation() != want.SessionIncarnation() || got.OwnerPartition() != want.OwnerPartition() || got.WorkloadPartition() != want.WorkloadPartition() || got.ProfileDigest() != want.ProfileDigest() || !slices.Equal(got.Providers(), want.Providers()) || !got.ExpiresAt().Equal(want.ExpiresAt()) {
		t.Fatalf("custody did not round trip all fields: got %+v", got)
	}
}

func TestBrokerCredentialCustodyLegacySnapshotHasNoAuthority(t *testing.T) {
	s, _ := enrollmentSnapshotSession(t)
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restored.BrokerCredentialCustody(); ok {
		t.Fatal("legacy snapshot acquired custody")
	}
}

func TestBrokerCredentialContinuity_Scenario2_LegacyWithoutCustody(t *testing.T) {
	s, _ := enrollmentSnapshotSession(t)
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restored.BrokerCredentialCustody(); ok {
		t.Fatal("legacy snapshot unexpectedly carries recovery authority")
	}
}

func TestBrokerCredentialCustodyMalformedSnapshotFailsClosed(t *testing.T) {
	s, pending := enrollmentSnapshotSession(t)
	c := snapshotCustody(t, s)
	if err := s.InstallBrokerCredentialCustody(pending, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	snap.BrokerCredentialCustody.OwnerPartition = "bad"
	if _, err := snap.Restore(); err == nil {
		t.Fatal("malformed custody restored")
	}
}
