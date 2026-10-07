package prompt_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

func TestRootAssemblerMatchesDiscoverInstructions(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	if err := ws.Write(t.Context(), "AGENTS.md", []byte("be terse")); err != nil {
		t.Fatal(err)
	}
	want, _, err := prompt.DiscoverInstructions(t.Context(), ws, ".")
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := (prompt.RootAssembler{Source: ws, SourceID: "proj", SourcePrefix: "."}).Assemble(t.Context(), []string{"."}, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(got) != len(want) {
		t.Fatalf("got=%v want=%v err=%v", got, want, err)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Text != want[i].Text {
			t.Fatalf("got=%v want=%v", got, want)
		}
	}
}

func TestRootAssemblerEmptyWorkspace(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	got, _, err := (prompt.RootAssembler{Source: ws, SourceID: "proj", SourcePrefix: "."}).Assemble(t.Context(), nil, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(got) != 0 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestAssembleWithManifestPreservesMessagesAndReportsBuiltInProvenance(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	if err := ws.Write(context.Background(), "AGENTS.md", []byte("byte-stable instructions")); err != nil {
		t.Fatal(err)
	}
	a := prompt.NewMultiAssembler(prompt.RootAssembler{Source: ws, SourceID: "proj", SourcePrefix: "."})
	want, _, err := a.Assemble(t.Context(), []string{"."}, &session.InstructionSnapshot{}, 65536)
	if err != nil {
		t.Fatal(err)
	}
	got, manifest, err := prompt.AssembleWithManifest(t.Context(), a, []string{"."}, &session.InstructionSnapshot{}, 65536)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v err=%v", got, want, err)
	}
	if len(manifest) != 1 || manifest[0].SourceID != "proj" || !manifest[0].HasGuidance {
		t.Fatalf("manifest=%+v", manifest)
	}
}
