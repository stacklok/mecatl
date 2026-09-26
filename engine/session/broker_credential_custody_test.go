package session

import (
	"bytes"
	"encoding/base64"
	"slices"
	"testing"
	"time"
)

func testBrokerCredentialCustody(t *testing.T, s *Session) BrokerCredentialCustody {
	t.Helper()
	var owner, workload, profile [32]byte
	owner[0], workload[0], profile[0] = 1, 2, 3
	custody, err := NewBrokerCredentialCustody(base64.RawURLEncoding.EncodeToString(make([]byte, 32)), s.Incarnation(), owner, workload, profile, []string{"alpha", "beta"}, time.Now().Add(time.Hour).UTC())
	if err != nil {
		t.Fatalf("NewBrokerCredentialCustody: %v", err)
	}
	return custody
}

func TestBrokerCredentialCustodyValidationAndDefensiveCopies(t *testing.T) {
	s := newEnrollmentSession(t)
	custody := testBrokerCredentialCustody(t, s)
	providers := custody.Providers()
	providers[0] = "changed"
	if got := custody.Providers()[0]; got != "alpha" {
		t.Fatalf("Providers leaked mutable slice: %q", got)
	}
	if custody.OwnerPartition()[0] != 1 || !custody.Valid() || !custody.Active(time.Now()) {
		t.Fatal("valid custody rejected")
	}
	if _, err := NewBrokerCredentialCustody("bad", s.Incarnation(), custody.OwnerPartition(), custody.WorkloadPartition(), custody.ProfileDigest(), custody.Providers(), custody.ExpiresAt()); err == nil {
		t.Fatal("invalid recovery reference accepted")
	}
}

func TestBrokerCredentialCustodyAggregateLifecycle(t *testing.T) {
	s := newEnrollmentSession(t)
	pending := testWorkspaceEnrollment()
	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatal(err)
	}
	custody := testBrokerCredentialCustody(t, s)
	if err := s.InstallBrokerCredentialCustody(pending, custody, time.Now()); err != nil {
		t.Fatalf("InstallBrokerCredentialCustody: %v", err)
	}
	if err := s.AbortWorkspaceEnrollment(pending.ID); err == nil {
		t.Fatal("aborted pending enrollment while custody installed")
	}
	if err := s.InstallBrokerCredentialCustody(pending, custody, time.Now()); err != nil {
		t.Fatalf("idempotent install: %v", err)
	}
	if _, err := s.ClearBrokerCredentialCustody("other"); err == nil {
		t.Fatal("cleared different custody")
	}
	got, ok := s.BrokerCredentialCustody()
	if !ok || got.RecoveryReference() != custody.RecoveryReference() {
		t.Fatal("missing custody")
	}
	if _, err := s.ClearBrokerCredentialCustody(custody.RecoveryReference()); err != nil {
		t.Fatalf("ClearBrokerCredentialCustody: %v", err)
	}
	if err := s.AbortWorkspaceEnrollment(pending.ID); err != nil {
		t.Fatalf("Abort after clear: %v", err)
	}
}

func TestBrokerCredentialCustodyRejectsInvalidAggregateStates(t *testing.T) {
	s := newEnrollmentSession(t)
	pending := testWorkspaceEnrollment()
	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatal(err)
	}
	custody := testBrokerCredentialCustody(t, s)
	wrongPending := pending
	wrongPending.ID = "other"
	wrongIncarnation, err := NewBrokerCredentialCustody(custody.RecoveryReference(), NewIncarnationID(), custody.OwnerPartition(), custody.WorkloadPartition(), custody.ProfileDigest(), custody.Providers(), custody.ExpiresAt())
	if err != nil {
		t.Fatal(err)
	}
	expired, err := NewBrokerCredentialCustody(custody.RecoveryReference(), s.Incarnation(), custody.OwnerPartition(), custody.WorkloadPartition(), custody.ProfileDigest(), custody.Providers(), time.Now().Add(-time.Second).UTC())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		pending PendingWorkspaceEnrollment
		custody BrokerCredentialCustody
	}{
		{"wrong pending", wrongPending, custody},
		{"wrong incarnation", pending, wrongIncarnation},
		{"expired", pending, expired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.InstallBrokerCredentialCustody(tc.pending, tc.custody, time.Now()); err == nil {
				t.Fatal("InstallBrokerCredentialCustody accepted invalid state")
			}
			if _, ok := s.BrokerCredentialCustody(); ok {
				t.Fatal("rejected installation changed custody")
			}
		})
	}
	if err := s.InstallBrokerCredentialCustody(pending, custody, time.Now()); err != nil {
		t.Fatal(err)
	}
	other := custody.Clone()
	other.recoveryReference = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	if err := s.InstallBrokerCredentialCustody(pending, other, time.Now()); err == nil {
		t.Fatal("InstallBrokerCredentialCustody replaced existing custody")
	}
	if err := s.BeginWorkspaceEnrollment(testWorkspaceEnrollment()); err == nil {
		t.Fatal("BeginWorkspaceEnrollment accepted custody")
	}

	unbound := New("unbound", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, Limits{}, time.Unix(1, 0).UTC())
	unboundCustody := testBrokerCredentialCustody(t, unbound)
	if err := unbound.RestoreBrokerCredentialCustody(unboundCustody); err == nil {
		t.Fatal("RestoreBrokerCredentialCustody accepted unbound authority")
	}

	invalidRestored := newEnrollmentSession(t)
	invalidRestored.pendingWorkspaceEnrollment = &pending
	invalidRestored.Conversation.Append(NewUserMessage("history"))
	invalidCustody := testBrokerCredentialCustody(t, invalidRestored)
	if err := invalidRestored.RestoreBrokerCredentialCustody(invalidCustody); err == nil {
		t.Fatal("RestoreBrokerCredentialCustody accepted pending custody with history")
	}
}

func TestAdoptRecoveredBrokerCatalogue(t *testing.T) {
	s := newEnrollmentSession(t)
	pending := testWorkspaceEnrollment()
	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatal(err)
	}
	custody := testBrokerCredentialCustody(t, s)
	if err := s.InstallBrokerCredentialCustody(pending, custody, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteWorkspaceEnrollment(pending, []string{"old"}); err != nil {
		t.Fatal(err)
	}
	s.ExternalBinding = "old-binding"
	if err := s.AdoptRecoveredBrokerCatalogue(custody.RecoveryReference(), "old-binding", "new-binding", []string{"fresh"}); err != nil {
		t.Fatalf("AdoptRecoveredBrokerCatalogue: %v", err)
	}
	if s.ExternalBinding != "new-binding" || len(s.Authority.CapabilitySet.Tools) != 1 || s.Authority.CapabilitySet.Tools[0] != "fresh" {
		t.Fatal("adoption did not atomically replace binding/catalogue")
	}
	beforeBinding := s.ExternalBinding
	beforeTools := append([]string(nil), s.Authority.CapabilitySet.Tools...)
	for _, tc := range []struct {
		name, ref, expected, replacement string
		tools                            []string
	}{
		{"wrong recovery reference", "other", "new-binding", "another", []string{"other"}},
		{"wrong expected binding on idempotent retry", custody.RecoveryReference(), "old-binding", "new-binding", []string{"fresh"}},
		{"empty replacement", custody.RecoveryReference(), "new-binding", "", []string{"other"}},
		{"invalid tools", custody.RecoveryReference(), "new-binding", "another", []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.AdoptRecoveredBrokerCatalogue(tc.ref, ExternalBinding(tc.expected), ExternalBinding(tc.replacement), tc.tools); err == nil {
				t.Fatal("AdoptRecoveredBrokerCatalogue accepted invalid adoption")
			}
			if s.ExternalBinding != beforeBinding || !slices.Equal(s.Authority.CapabilitySet.Tools, beforeTools) {
				t.Fatal("failed adoption changed binding or catalogue")
			}
		})
	}
	if err := s.AdoptRecoveredBrokerCatalogue(custody.RecoveryReference(), "new-binding", "new-binding", []string{"fresh"}); err != nil {
		t.Fatalf("idempotent adoption: %v", err)
	}
	if err := s.AdoptRecoveredBrokerCatalogue(custody.RecoveryReference(), "new-binding", "new-binding", []string{"changed"}); err == nil {
		t.Fatal("adoption accepted a non-fresh replacement binding")
	}
	if s.ExternalBinding != "new-binding" || !slices.Equal(s.Authority.CapabilitySet.Tools, []string{"fresh"}) {
		t.Fatal("failed adoption changed binding or catalogue")
	}
}
