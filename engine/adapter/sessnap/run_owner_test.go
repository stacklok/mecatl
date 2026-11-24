package sessnap_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

// TestRunOwnerSnapshotRoundTrip pins that a run's owner persists beside its
// run ID, independent of the session's owner, so a restored run keeps the
// owner it started with.
func TestRunOwnerSnapshotRoundTrip(t *testing.T) {
	t.Parallel()

	s := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/w", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0).UTC())
	owner := &session.Principal{Issuer: "https://login.example.com", Subject: "bob-117", GrantType: session.GrantTypeUser}
	runOwner := &session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser}
	if err := s.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	s.BeginRunFor("run_1", runOwner)
	runOwner.Subject = "changed-after-begin"

	line, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatal(err)
	}
	want := session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser}
	if got.RunID() != "run_1" || got.RunOwner() == nil || *got.RunOwner() != want {
		t.Fatalf("restored run %q owner %+v, want run_1 owned by %+v", got.RunID(), got.RunOwner(), want)
	}
	if got.Owner == nil || *got.Owner != *owner {
		t.Fatalf("restored session owner %+v, want %+v", got.Owner, owner)
	}
	again, err := sessnap.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(line) {
		t.Errorf("snapshot not byte-identical across round-trip:\n got %s\nwant %s", again, line)
	}

	// BeginRun starts a run with no recorded owner, emitting no key.
	got.BeginRun("run_2")
	if got.RunOwner() != nil {
		t.Fatalf("BeginRun kept owner %+v", got.RunOwner())
	}
	line, err = sessnap.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(line, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["run_owner"]; ok {
		t.Errorf("a run with no owner emitted a \"run_owner\" key: %s", line)
	}
}

// TestRunOwnerSnapshotCompatibility pins that a snapshot written before run
// owners restores its run with no owner, and that an unsafe run owner is
// refused as an unsafe session owner is.
func TestRunOwnerSnapshotCompatibility(t *testing.T) {
	t.Parallel()

	const prefix = `{"id":"s1","state":"idle","mode":"default","limits":{},` +
		`"counters":{},"token_usage":{},"environment_ref":{"Kind":"local","ID":"/w","Revision":"in-tree-v1"},"created_at":"1970-01-01T00:00:00Z",` +
		`"owner":{"issuer":"https://login.example.com","subject":"alice-204","grant_type":"user"},"run_id":"run_1",`

	got, err := sessnap.Unmarshal([]byte(prefix + `"messages":[]}`))
	if err != nil {
		t.Fatalf("Unmarshal older snapshot: %v", err)
	}
	if got.RunID() != "run_1" || got.RunOwner() != nil {
		t.Fatalf("older snapshot restored run %q owner %+v, want run_1 with none", got.RunID(), got.RunOwner())
	}

	for _, runOwner := range []string{
		`{"issuer":"a\u0000b","subject":"c","grant_type":"user"}`,
		`{"issuer":"a","subject":"b\u0000c","grant_type":"user"}`,
	} {
		if _, err := sessnap.Unmarshal([]byte(prefix + `"run_owner":` + runOwner + `,"messages":[]}`)); err == nil {
			t.Errorf("Unmarshal accepted unsafe run owner %s", runOwner)
		}
	}
}

// TestBeginRunForRecordsNoOwnerItCannotRestore pins that a run owner is
// recorded only for a named run and a well-framed identity, so a saved
// snapshot always restores.
func TestBeginRunForRecordsNoOwnerItCannotRestore(t *testing.T) {
	t.Parallel()

	owner := &session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser}
	for name, tc := range map[string]struct {
		runID string
		owner *session.Principal
	}{
		"no run":             {runID: "", owner: owner},
		"NUL in the issuer":  {runID: "run_1", owner: &session.Principal{Issuer: "a\x00b", Subject: "c", GrantType: session.GrantTypeUser}},
		"NUL in the subject": {runID: "run_1", owner: &session.Principal{Issuer: "a", Subject: "b\x00c", GrantType: session.GrantTypeUser}},
	} {
		s := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/w", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0).UTC())
		s.BeginRunFor("run_0", owner)
		s.BeginRunFor(tc.runID, tc.owner)
		if got := s.RunOwner(); got != nil {
			t.Errorf("%s: run owner = %+v, want none", name, got)
		}
		line, err := sessnap.Marshal(s)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := sessnap.Unmarshal(line); err != nil {
			t.Errorf("%s: saved snapshot does not restore: %v", name, err)
		}
	}
}
