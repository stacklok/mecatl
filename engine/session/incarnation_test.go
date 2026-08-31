package session_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionIncarnationIsRandomIdentityNotMetadata(t *testing.T) {
	created := time.Unix(1700000000, 123).UTC()
	owner := &session.Principal{Issuer: "issuer", Subject: "subject", GrantType: session.GrantTypeUser}
	seen := make(map[session.IncarnationID]struct{}, 256)
	for range 256 {
		s := session.New("same-id", session.ModeDefault, "/same", session.Limits{}, created)
		if err := s.RestoreLabels(owner, session.Authority{}); err != nil {
			t.Fatal(err)
		}
		inc := s.Incarnation()
		if !inc.Valid() || !strings.HasPrefix(string(inc), "inc_") {
			t.Fatalf("invalid minted incarnation %q", inc)
		}
		if _, duplicate := seen[inc]; duplicate {
			t.Fatalf("duplicate incarnation for identical ID/time/owner: %q", inc)
		}
		seen[inc] = struct{}{}
	}
}

func TestSessionIncarnationPersistsAndLegacyIsDisjoint(t *testing.T) {
	created := time.Unix(1700000000, 123).UTC()
	owner := &session.Principal{Issuer: "issuer", Subject: "subject", GrantType: session.GrantTypeUser}
	s := session.New("same-id", session.ModeDefault, "/same", session.Limits{}, created)
	if err := s.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	snap, err := sessnap.Of(s)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if restored.Incarnation() != s.Incarnation() || session.DebugTargetFingerprint(restored) != session.DebugTargetFingerprint(s) {
		t.Fatal("persisted incarnation binding changed on restore")
	}

	snap.Incarnation = ""
	legacyA, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	legacyB, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if legacyA.Incarnation() != legacyB.Incarnation() || !strings.HasPrefix(string(legacyA.Incarnation()), "legacy_") || legacyA.Incarnation() == s.Incarnation() {
		t.Fatalf("legacy identity is not deterministic and prefix-disjoint: %q %q", legacyA.Incarnation(), legacyB.Incarnation())
	}
}
