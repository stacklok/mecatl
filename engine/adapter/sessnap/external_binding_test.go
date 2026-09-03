package sessnap

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestExternalRuntimeBindingRoundTripsOpaque(t *testing.T) {
	original := session.New("bound", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	original.ExternalBinding = "opaque/process.binding:1"
	snap, err := Of(original)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if restored.ExternalBinding != original.ExternalBinding {
		t.Fatalf("ExternalBinding = %q, want %q", restored.ExternalBinding, original.ExternalBinding)
	}
}
