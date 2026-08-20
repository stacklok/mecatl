package session_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func newLabelSession(t *testing.T) *session.Session {
	t.Helper()
	return session.New("s1", session.ModeDefault, "/w", session.Limits{}, time.Unix(0, 0).UTC())
}

// TestRestoreLabelsIsWriteOnce pins the write-once contract of the owner label
// (ADR 0204 decision 4): a second RestoreLabels with a DIFFERENT owner is
// refused, so a restore path can never silently re-own a session.
func TestRestoreLabelsIsWriteOnce(t *testing.T) {
	t.Parallel()

	alice := &session.Principal{Issuer: "https://idp", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: "https://idp", Subject: "bob", GrantType: session.GrantTypeUser}

	s := newLabelSession(t)
	if err := s.RestoreLabels(alice, session.Authority{}); err != nil {
		t.Fatalf("first RestoreLabels: %v", err)
	}
	err := s.RestoreLabels(bob, session.Authority{})
	if !errors.Is(err, session.ErrOwnerAlreadySet) {
		t.Fatalf("second RestoreLabels with a different owner: got %v, want ErrOwnerAlreadySet", err)
	}
	if s.Owner.Subject != "alice" {
		t.Errorf("owner overwritten: got %q, want alice", s.Owner.Subject)
	}
	if _, bound := s.BoundAuthority(); bound {
		t.Error("owner restore unexpectedly bound authority")
	}
}

// TestRestoreLabelsIdempotentForSameOwner pins the other half: re-restoring the
// SAME owner (a re-read of the same snapshot) is not an error.
func TestRestoreLabelsIdempotentForSameOwner(t *testing.T) {
	t.Parallel()

	s := newLabelSession(t)
	p := session.Principal{Issuer: "https://idp", Subject: "alice", GrantType: session.GrantTypeUser}
	if err := s.RestoreLabels(&p, session.Authority{}); err != nil {
		t.Fatalf("first RestoreLabels: %v", err)
	}
	same := p // a distinct pointer with an equal value
	if err := s.RestoreLabels(&same, session.Authority{}); err != nil {
		t.Fatalf("idempotent RestoreLabels: %v", err)
	}
}

// TestRestoreLabelsNilOwnerLeavesOwnerUnset pins the ownerless path: restoring a
// nil owner (a pre-ship / no-auth session) leaves the label unset and is never
// an error — the byte-identical no-auth path of ADR 0204.
func TestRestoreLabelsNilOwnerLeavesOwnerUnset(t *testing.T) {
	t.Parallel()

	s := newLabelSession(t)
	if err := s.RestoreLabels(nil, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels(nil): %v", err)
	}
	if s.Owner != nil {
		t.Errorf("owner set from a nil restore: %+v", *s.Owner)
	}
	// A nil restore does not burn the write-once slot.
	if err := s.RestoreLabels(&session.Principal{Issuer: "i", Subject: "s", GrantType: session.GrantTypeSystem}, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels after a nil restore: %v", err)
	}
	if s.Owner == nil || s.Owner.Subject != "s" {
		t.Errorf("owner not set after a nil restore: %v", s.Owner)
	}
}

// TestPrincipalClone pins the shared "copy a *Principal across a boundary, nil
// stays nil" rule the ownership sites route through. Principal is all-strings
// today so a shallow copy is a deep copy; the method exists so the day it gains
// a slice or map field every site stays correct together instead of drifting
// into a silent aliasing bug.
func TestPrincipalClone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   *session.Principal
	}{
		{"nil stays nil", nil},
		{"zero value", &session.Principal{}},
		{"populated", &session.Principal{
			Issuer: "https://idp", Subject: "alice", GrantType: session.GrantTypeUser, Name: "Ada",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.in.Clone()
			if tc.in == nil {
				if got != nil {
					t.Fatalf("Clone of nil: got %+v, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("Clone returned nil for a non-nil principal")
			}
			if got == tc.in {
				t.Fatal("Clone returned the SAME pointer — not a copy")
			}
			if *got != *tc.in {
				t.Fatalf("Clone value mismatch: got %+v, want %+v", *got, *tc.in)
			}
			got.Subject = "attacker"
			if tc.in.Subject == "attacker" {
				t.Fatal("mutating the clone changed the original")
			}
		})
	}
}

// TestPrincipalGrantTypesAreTheThreeValues pins ADR 0204 decision 1: the grant
// type is a small closed enum of exactly user / client_credentials / system.
func TestPrincipalGrantTypesAreTheThreeValues(t *testing.T) {
	t.Parallel()

	want := map[session.GrantType]string{
		session.GrantTypeUser:              "user",
		session.GrantTypeClientCredentials: "client_credentials",
		session.GrantTypeSystem:            "system",
	}
	for gt, s := range want {
		if string(gt) != s {
			t.Errorf("grant type %v: got %q, want %q", gt, string(gt), s)
		}
		if !gt.Valid() {
			t.Errorf("grant type %q reported invalid", gt)
		}
	}
	for _, bad := range []session.GrantType{"", "anonymous", "User"} {
		if bad.Valid() {
			t.Errorf("grant type %q reported valid; the enum is closed", bad)
		}
	}
}

// TestADR_0212_VerifiedIssuerSubjectPairIsOwnerIdentity pins ADR 0212 decision
// 1: ownership is the exact verifier-emitted issuer/subject pair. Display and
// grant metadata do not select an owner, and issuer text is never normalized.
func TestADR_0212_VerifiedIssuerSubjectPairIsOwnerIdentity(t *testing.T) {
	t.Parallel()

	owner := &session.Principal{Issuer: "https://issuer.example/realm", Subject: "same", GrantType: session.GrantTypeUser, Name: "Alice"}
	for _, tc := range []struct {
		name string
		got  *session.Principal
		want bool
	}{
		{"same verified pair ignores display and grant", &session.Principal{Issuer: owner.Issuer, Subject: owner.Subject, GrantType: session.GrantTypeClientCredentials, Name: "forged display"}, true},
		{"same subject different issuer", &session.Principal{Issuer: "https://other.example/realm", Subject: owner.Subject, GrantType: owner.GrantType, Name: owner.Name}, false},
		{"alternate issuer spelling", &session.Principal{Issuer: "https://issuer.example/realm/", Subject: owner.Subject, GrantType: owner.GrantType, Name: owner.Name}, false},
		{"different subject", &session.Principal{Issuer: owner.Issuer, Subject: "other", GrantType: owner.GrantType, Name: owner.Name}, false},
		{"absent principal", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := owner.SameIdentity(tc.got); got != tc.want {
				t.Fatalf("SameIdentity(%+v) = %v, want %v", tc.got, got, tc.want)
			}
		})
	}
}
