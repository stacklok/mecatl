package session

import "testing"

func TestInstructionSnapshotMetadataBytes(t *testing.T) {
	snapshot := InstructionSnapshot{
		Directories: []string{".", "nested"},
		Scopes: []InstructionScope{
			{SourceID: "source", Directory: ".", File: "AGENTS.md"},
			{SourceID: "source", Directory: "nested", File: "CLAUDE.md"},
		},
	}
	want := (len(".") + 1) + (len("nested") + 1) +
		(len("source") + len(".") + len("AGENTS.md") + 1) +
		(len("source") + len("nested") + len("CLAUDE.md") + 1)
	if got := snapshot.MetadataBytes(); got != want {
		t.Fatalf("MetadataBytes() = %d, want %d", got, want)
	}
}
