package sessnap

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestExecutionCapabilitiesSnapshotRoundTrip(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "v1"}
	s := session.New("facts", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	s.ExecutionCapabilities = &session.ExecutionCapabilities{Files: true}
	encoded, err := Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Unmarshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ExecutionCapabilities == nil || *restored.ExecutionCapabilities != *s.ExecutionCapabilities {
		t.Fatalf("restored facts = %+v", restored.ExecutionCapabilities)
	}
	restored.ExecutionCapabilities.Files = false
	if !s.ExecutionCapabilities.Files {
		t.Fatal("snapshot aliased source execution facts")
	}
}

func TestInvariant_safe_placement_metadata_round_trips_with_snapshot(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/private/root", Revision: "exact-v1"}
	sess := session.New("placement-meta", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	sess.Placement = session.PlacementMetadata{Kind: "local", Label: "Primary", Branch: "main", Revision: "abc123"}
	snap, err := Of(sess)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if restored.Placement != sess.Placement {
		t.Fatalf("placement metadata = %+v, want %+v", restored.Placement, sess.Placement)
	}
}
