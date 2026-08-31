package prompt_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/prompt"
)

// TestRootAssemblerMatchesDiscoverInstructions verifies the default assembler
// reproduces DiscoverInstructions byte-for-byte (P3 default-preserves-behaviour).
func TestRootAssemblerMatchesDiscoverInstructions(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	if err := ws.Write(context.Background(), "AGENTS.md", []byte("be terse")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	want, err := prompt.DiscoverInstructions(context.Background(), ws)
	if err != nil {
		t.Fatalf("DiscoverInstructions: %v", err)
	}
	got, err := prompt.RootAssembler{}.Assemble(context.Background(), ws)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len(Assemble)=%d, len(Discover)=%d", len(got), len(want))
	}
	for i := range want {
		if got[i].Text != want[i].Text || got[i].Role != want[i].Role {
			t.Fatalf("message[%d]: Assemble=%+v want %+v", i, got[i], want[i])
		}
	}
}

// TestRootAssemblerEmptyWorkspace verifies no instructions and no error when no
// instruction files are present.
func TestRootAssemblerEmptyWorkspace(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	got, err := prompt.RootAssembler{}.Assemble(context.Background(), ws)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d messages, want 0", len(got))
	}
}

func TestAssembleWithManifestPreservesMessagesAndReportsBuiltInProvenance(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	if err := ws.Write(context.Background(), "AGENTS.md", []byte("byte-stable instructions")); err != nil {
		t.Fatal(err)
	}
	assembler := prompt.NewMultiAssembler(prompt.RootAssembler{})
	want, err := assembler.Assemble(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	got, manifest, err := prompt.AssembleWithManifest(context.Background(), ws, assembler)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest assembly changed messages: got %+v want %+v", got, want)
	}
	if len(manifest) != 1 || manifest[0] != (prompt.InstructionManifest{
		Kind: prompt.InstructionKindTurn0, Provenance: prompt.InstructionProvenanceProject,
	}) {
		t.Fatalf("manifest = %+v", manifest)
	}
}
