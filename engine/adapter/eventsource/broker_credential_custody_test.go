package eventsource

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

func TestBrokerCredentialCustodyEventSourceRejectsPendingWithHistory(t *testing.T) {
	incarnation := session.NewIncarnationID()
	var owner, workload, profile [32]byte
	owner[0], workload[0], profile[0] = 1, 2, 3
	custody, err := session.NewBrokerCredentialCustody(base64.RawURLEncoding.EncodeToString(make([]byte, 32)), incarnation, owner, workload, profile, []string{"alpha"}, time.Now().Add(time.Hour).UTC())
	if err != nil {
		t.Fatal(err)
	}
	pending := session.PendingWorkspaceEnrollment{ID: "enroll-custody", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour).UTC()}
	authority := session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}, Provenance: "test"}
	_, err = Fold(SessionMeta{ID: "custody-history", Mode: session.ModeDefault, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ".", Revision: "test"}, Incarnation: incarnation, Authority: &authority, PendingWorkspaceEnrollment: &pending, BrokerCredentialCustody: &custody, CreatedAt: time.Now().UTC()}, func(yield func(session.Event, error) bool) {
		yield(session.Event{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "history"}}, nil)
	})
	if err == nil {
		t.Fatal("Fold accepted pending custody with history")
	}
}

func TestBrokerCredentialCustodyEventSourceMetaRoundTrip(t *testing.T) {
	incarnation := session.NewIncarnationID()
	var owner, workload, profile [32]byte
	owner[0], workload[0], profile[0] = 1, 2, 3
	custody, err := session.NewBrokerCredentialCustody(base64.RawURLEncoding.EncodeToString(make([]byte, 32)), incarnation, owner, workload, profile, []string{"alpha"}, time.Now().Add(time.Hour).UTC())
	if err != nil {
		t.Fatal(err)
	}
	pending := session.PendingWorkspaceEnrollment{ID: "enroll-custody", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour).UTC()}
	authority := session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}, Provenance: "test"}
	got, err := Fold(SessionMeta{ID: "custody", Mode: session.ModeDefault, Limits: session.Limits{}, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ".", Revision: "test"}, Incarnation: incarnation, Authority: &authority, PendingWorkspaceEnrollment: &pending, BrokerCredentialCustody: &custody, CreatedAt: time.Now().UTC()}, func(func(session.Event, error) bool) {})
	if err != nil {
		t.Fatal(err)
	}
	actual, ok := got.BrokerCredentialCustody()
	if !ok || actual.RecoveryReference() != custody.RecoveryReference() || actual.SessionIncarnation() != custody.SessionIncarnation() || actual.OwnerPartition() != custody.OwnerPartition() || actual.WorkloadPartition() != custody.WorkloadPartition() || actual.ProfileDigest() != custody.ProfileDigest() || len(actual.Providers()) != 1 || actual.Providers()[0] != "alpha" || !actual.ExpiresAt().Equal(custody.ExpiresAt()) {
		t.Fatalf("custody = %+v, %v", actual, ok)
	}
	actualPending, ok := got.PendingWorkspaceEnrollment()
	if !ok || actualPending != pending {
		t.Fatalf("pending = %+v, %v; want %+v, true", actualPending, ok, pending)
	}
}
