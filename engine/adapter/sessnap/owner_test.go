package sessnap_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

// TestCallerIdentity_Scenario0_OwnerSnapshotRoundTrip pins AC0.1 of
// docs/acceptance/caller-identity.md: Session.Owner and Session.Authority
// round-trip through Marshal/Unmarshal byte-identically, restored via the
// write-once aggregate method (RestoreLabels), with no RestoreState signature
// change.
func TestCallerIdentity_Scenario0_OwnerSnapshotRoundTrip(t *testing.T) {
	t.Parallel()

	s := session.New("s1", session.ModeDefault, "/w", session.Limits{}, time.Unix(0, 0).UTC())
	owner := &session.Principal{
		Issuer:    "https://idp.example.com",
		Subject:   "user-42",
		GrantType: session.GrantTypeUser,
		Name:      "Alice",
	}
	if err := s.RestoreLabels(owner, session.Authority("team-lead")); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}

	line, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Owner == nil {
		t.Fatalf("restored owner is nil, want %+v", *owner)
	}
	if *got.Owner != *owner {
		t.Errorf("owner round-trip: got %+v, want %+v", *got.Owner, *owner)
	}
	if got.Authority != session.Authority("team-lead") {
		t.Errorf("authority round-trip: got %q, want %q", got.Authority, "team-lead")
	}

	// Byte-identical: re-marshalling the restored session reproduces the line.
	again, err := sessnap.Marshal(got)
	if err != nil {
		t.Fatalf("re-Marshal: %v", err)
	}
	if string(again) != string(line) {
		t.Errorf("snapshot not byte-identical across round-trip:\n got %s\nwant %s", again, line)
	}

	// The identity pair is (Issuer, Subject) — a same-subject different-issuer
	// principal must not compare equal.
	other := *owner
	other.Issuer = "https://other.example.com"
	if other == *got.Owner {
		t.Error("principals from different issuers compared equal; identity must be the (iss, sub) pair")
	}
}

// TestCallerIdentity_Scenario0_PreShipSnapshotRestores pins AC0.2: a snapshot
// written before this plan (no "owner"/"authority" keys) restores to a nil
// owner and a zero authority — additive omitempty, never a parse failure.
func TestCallerIdentity_Scenario0_PreShipSnapshotRestores(t *testing.T) {
	t.Parallel()

	const preShip = `{"id":"s1","state":"idle","mode":"default","limits":{},` +
		`"counters":{},"workspace":"/w","created_at":"1970-01-01T00:00:00Z","messages":[]}`

	got, err := sessnap.Unmarshal([]byte(preShip))
	if err != nil {
		t.Fatalf("Unmarshal pre-ship snapshot: %v", err)
	}
	if got.Owner != nil {
		t.Errorf("pre-ship snapshot restored owner %+v, want nil", *got.Owner)
	}
	if got.Authority != (session.Authority("")) {
		t.Errorf("pre-ship snapshot restored authority %q, want the zero value", got.Authority)
	}

	// And the omitempty half: an ownerless session emits neither key, so a
	// snapshot stays byte-identical to a pre-ship one.
	line, err := sessnap.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(line, &keys); err != nil {
		t.Fatalf("decode keys: %v", err)
	}
	if _, ok := keys["owner"]; ok {
		t.Errorf("ownerless snapshot emitted an \"owner\" key: %s", line)
	}
	if _, ok := keys["authority"]; ok {
		t.Errorf("authority-less snapshot emitted an \"authority\" key: %s", line)
	}
}

// TestCallerIdentity_Scenario0_APICompatAdditive pins AC0.3: a consumer
// compiled against the new engine/session reads Owner and Authority with NO
// RestoreState call-site change — the additive-field, not-widened-signature
// contract (a trailing RestoreState parameter would be a Changed/breaking entry
// under engine/COMPATIBILITY.md).
func TestCallerIdentity_Scenario0_APICompatAdditive(t *testing.T) {
	t.Parallel()

	// The pre-existing eight-parameter call site, verbatim. If RestoreState's
	// signature widened, this file stops compiling — that is the assertion.
	s := session.New("s1", session.ModeDefault, "/w", session.Limits{}, time.Unix(0, 0).UTC())
	if err := sessnap.RestoreState(
		s,
		session.StateIdle,
		session.StopNone,
		nil,
		session.Counters{},
		session.Usage{},
		false,
		"",
	); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	// RestoreState touches neither label: they are restored by the aggregate
	// method, not the state driver.
	if s.Owner != nil || s.Authority != session.Authority("") {
		t.Errorf("RestoreState set the labels: owner=%v authority=%q", s.Owner, s.Authority)
	}

	// And the fields are readable off the aggregate by an external consumer.
	owner := &session.Principal{Issuer: "iss", Subject: "sub", GrantType: session.GrantTypeSystem}
	if err := s.RestoreLabels(owner, session.Authority("a")); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if s.Owner == nil || *s.Owner != *owner || s.Authority != session.Authority("a") {
		t.Errorf("labels not readable: owner=%v authority=%q", s.Owner, s.Authority)
	}
}
