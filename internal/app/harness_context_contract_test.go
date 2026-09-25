package app

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
)

// Existing Service semantics permit closing local resources then loading the same
// durable session in the same process. The proposed Retire(ID) contract needs an
// explicit decision about whether this creates a new context-binding generation.
func TestHarnessContextCloseThenReloadCompatibility(t *testing.T) {
	source := memfs.NewWorkspace("/logical")
	if _, err := source.CreateFile(t.Context(), ".mecatl/commands/review.md", []byte("Review the change")); err != nil {
		t.Fatal(err)
	}
	cfg := hcConfiguredFiles(t, source)
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := built.Service.ListCommandsForSession(t.Context(), sess.ID)
	if err != nil || len(before) != 1 {
		t.Fatalf("before close: commands=%v err=%v", before, err)
	}
	built.Service.CloseSession(sess.ID)
	if _, err := built.Service.LoadSession(t.Context(), sess.ID); err != nil {
		t.Fatal(err)
	}
	after, err := built.Service.ListCommandsForSession(t.Context(), sess.ID)
	if err != nil || len(after) != 1 {
		t.Fatalf("after supported close/reload: commands=%v err=%v", after, err)
	}
}
