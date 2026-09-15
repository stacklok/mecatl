package jsonlstore

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

// TestMetaSnapshotTagsAreSessnapSubset is a reflection tripwire: metaSnapshot
// (in metalist.go) hand-mirrors the json tags of sessnap.Snapshot so the
// picker's cheap decode reads the same wire keys the snapshot writes. If
// sessnap.Snapshot gains/renames a field the picker needs, this test fails —
// the mirror cannot silently drift. It asserts every json-tagged field of
// metaSnapshot has a field with the SAME json tag name on sessnap.Snapshot.
// (The reverse is NOT required — metaSnapshot deliberately omits the heavy
// fields like messages/limits/usage; activity is intentionally carried by the
// outer currentSnapshot metadata wrapper so the canonical snapshot remains
// unchanged.)
//
// This is an INTERNAL test (package jsonlstore) because metaSnapshot is
// unexported.
func TestMetaSnapshotTagsAreSessnapSubset(t *testing.T) {
	metaType := reflect.TypeOf(metaSnapshot{})
	snapType := reflect.TypeOf(sessnap.Snapshot{})
	for i := 0; i < metaType.NumField(); i++ {
		metaField := metaType.Field(i)
		tag := metaField.Tag.Get("json")
		if tag == "" {
			t.Fatalf("metaSnapshot.%s has no json tag — every field must carry one (the wire key mirror)", metaField.Name)
		}
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("metaSnapshot.%s has a non-decoding json tag %q — the picker needs this key", metaField.Name, tag)
		}
		// Find a field on sessnap.Snapshot whose json tag name matches.
		found := false
		for j := 0; j < snapType.NumField(); j++ {
			snapField := snapType.Field(j)
			snapTag := snapField.Tag.Get("json")
			snapName := strings.Split(snapTag, ",")[0]
			if snapName == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("metaSnapshot.%s (json %q) has no matching field on sessnap.Snapshot — "+
				"the mirror has drifted; either add the field to sessnap.Snapshot or fix metaSnapshot", metaField.Name, name)
		}
	}
}

func TestCurrentSnapshotActivityIsMetadataOnly(t *testing.T) {
	currentType := reflect.TypeOf(currentSnapshot{})
	activity, ok := currentType.FieldByName("Activity")
	if !ok || strings.Split(activity.Tag.Get("json"), ",")[0] != "activity" {
		t.Fatal("current snapshot does not carry an activity metadata projection")
	}
	if _, found := reflect.TypeOf(sessnap.Snapshot{}).FieldByName("Activity"); found {
		t.Fatal("canonical sessnap.Snapshot must not duplicate activity metadata")
	}
}

// TestKnownStatesMatchSessionPackage is a tripwire: knownStates (in metalist.go)
// is a hand-mirrored copy of the session.State enum; if a new session.State
// lands in engine/session, knownStates would silently mark it invalid → the
// picker zeroes a valid snapshot. There is no exported IsValidState on
// session.State, so this test enumerates the known states itself and asserts:
// (1) every known session.State constant is present in knownStates (true), and
// (2) knownStates has NO extra entries that aren't real session states (no
// drifted/dead entries).
//
// This is an INTERNAL test (package jsonlstore) because knownStates is
// unexported. It is a test-only import of engine/session into the jsonlstore
// adapter (test files may import engine packages).
func TestKnownStatesMatchSessionPackage(t *testing.T) {
	realStates := []session.State{
		session.StateIdle,
		session.StateRunning,
		session.StateAwaiting,
		session.StateCompleted,
		session.StateFailed,
		session.StateCancelled,
	}
	// (1) Every real session.State constant must be present (true) in knownStates.
	for _, st := range realStates {
		if !knownStates[st] {
			t.Errorf("knownStates is missing real session.State %q — a new state landed in engine/session "+
				"and the picker will zero a valid snapshot carrying it", st)
		}
	}
	// (2) knownStates must have NO extra entries that aren't real session states.
	if got, want := len(knownStates), len(realStates); got != want {
		t.Errorf("knownStates has %d entries, want %d (the real session.State count) — "+
			"there is a drifted/dead entry; sync knownStates with engine/session", got, want)
	}
}
