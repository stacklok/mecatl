package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
)

// TestProjectIngestionAdmitted pins the named semantic seam every project-tier
// consumer reads. TrustProject is the one final positive trust decision.
func TestProjectIngestionAdmitted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trusted bool
	}{
		{"trusted", true},
		{"untrusted", false},
	} {
		if got := projectIngestionAdmitted(Config{TrustProject: tc.trusted}); got != tc.trusted {
			t.Errorf("%s: projectIngestionAdmitted = %v, want %v", tc.name, got, tc.trusted)
		}
	}
}

// TestRootAwarePostureTrust proves the fail-safe root split: posture never trusts
// a headless workspace, while the interactive ladder preserves the dev default.
func TestRootAwarePostureTrust(t *testing.T) {
	headless := applyPosture(Config{Headless: true, Posture: PostureAuto})
	if headless.TrustProject || projectIngestionAdmitted(headless) {
		t.Fatal("headless auto without a trust source must admit neither project ingestion nor the child shell")
	}

	interactive := applyPosture(Config{Headless: false, Posture: PostureAuto})
	if !interactive.TrustProject || !projectIngestionAdmitted(interactive) {
		t.Fatal("interactive auto must trust the workspace and admit project ingestion")
	}

	explicit := applyPosture(Config{Headless: true, Posture: PostureStrict, TrustProject: true})
	if !explicit.TrustProject || !projectIngestionAdmitted(explicit) {
		t.Fatal("explicit trust must survive strict posture on a headless root")
	}
}

// TestBuildInstructionAssemblerNoRootOmitsAGENTS pins the AGENTS.md/CLAUDE.md
// gate: when project ingestion is not admitted (noRoot=true), RootAssembler is
// omitted. The bool semantics remain true = omit RootAssembler.
func TestBuildInstructionAssemblerNoRootOmitsAGENTS(t *testing.T) {
	ctx := context.Background()
	ws := memfs.NewWorkspace("/repo")
	if err := ws.Write(ctx, "AGENTS.md", []byte("# Repo steering the model must NOT see\n")); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}

	def := buildInstructionAssembler(nil, nil, nil, nil, false)
	msgs, err := def.Assemble(ctx, ws)
	if err != nil {
		t.Fatalf("default Assemble: %v", err)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text, "Repo steering the model must NOT see") {
		t.Fatalf("default path must ingest AGENTS.md; got %d messages: %+v", len(msgs), msgs)
	}

	pinned := buildInstructionAssembler(nil, nil, nil, nil, true)
	if pinned == nil {
		t.Fatal("noRoot with no other assembler must return an honest no-op assembler, not nil")
	}
	pmsgs, err := pinned.Assemble(ctx, ws)
	if err != nil {
		t.Fatalf("pinned Assemble: %v", err)
	}
	if len(pmsgs) != 0 {
		t.Fatalf("untrusted project must suppress AGENTS.md ingestion; got %d messages: %+v", len(pmsgs), pmsgs)
	}
}
