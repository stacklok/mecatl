package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
)

// project_ingestion_test.go pins the composition-layer project-tier-INGESTION
// admission seam (issue #359 redesign): the two positive grants — TrustProject
// (the workspace-trust fold) AND ProjectIngestionGranted (the root-aware opt-in)
// — gate every ingestion consumer, and applyPosture raises the grant per the
// root-aware ladder (an explicit --trust-project on BOTH roots; the interactive
// ladder at auto/yolo; HEADLESS does NOT grant via the ladder).

// TestProjectIngestionAdmittedTruthTable pins the single expression every
// ingestion consumer reads: admit the project's repo-resident steering only when
// the workspace is trusted AND the ingestion grant is raised.
func TestProjectIngestionAdmittedTruthTable(t *testing.T) {
	cases := []struct {
		name             string
		trustProject     bool
		ingestionGranted bool
		want             bool
	}{
		{"trusted, granted", true, true, true},
		{"trusted, not granted", true, false, false},
		{"untrusted, granted", false, true, false},
		{"untrusted, not granted", false, false, false},
	}
	for _, tc := range cases {
		got := projectIngestionAdmitted(Config{TrustProject: tc.trustProject, ProjectIngestionGranted: tc.ingestionGranted})
		if got != tc.want {
			t.Errorf("%s: projectIngestionAdmitted(TrustProject=%v, ProjectIngestionGranted=%v) = %v, want %v",
				tc.name, tc.trustProject, tc.ingestionGranted, got, tc.want)
		}
	}
}

// TestHeadlessAutoDoesNotGrantIngestionWithoutTrustProject is the FAIL-SAFE
// proof (issue #359 redesign): on a HEADLESS root, --posture auto does NOT raise
// the ingestion grant (ProjectIngestionGranted) NOR the TrustProject floor (=
// the shell gate) unless the operator explicitly passed --trust-project. So a
// dark factory over a freshly-cloned untrusted repo ingests NONE of the repo's
// steering AND gets no subagent shell (its `.git` is not vouched).
func TestHeadlessAutoDoesNotGrantIngestionWithoutTrustProject(t *testing.T) {
	got := applyPosture(Config{Headless: true, Posture: PostureAuto, TrustProject: false})
	if got.ProjectIngestionGranted {
		t.Error("headless auto must NOT raise ProjectIngestionGranted without --trust-project (the fail-safe default)")
	}
	if got.TrustProject {
		t.Error("headless auto must NOT raise the TrustProject floor (the ladder is interactive-only; the shell gate reads it)")
	}
	// Ingestion is not admitted: the explicit opt-in is the only path on headless.
	if projectIngestionAdmitted(got) {
		t.Error("headless auto without --trust-project must not admit project ingestion")
	}
}

// TestInteractiveAutoGrantsIngestion is the INTERACTIVE counterpart: on an
// interactive root, --posture auto grants ingestion via the ladder (the dev
// default ingests the operator's own CLAUDE.md) AND raises the TrustProject
// floor (so the subagent shell stays on, matching main), even without
// --trust-project.
func TestInteractiveAutoGrantsIngestion(t *testing.T) {
	got := applyPosture(Config{Headless: false, Posture: PostureAuto, TrustProject: false})
	if !got.ProjectIngestionGranted {
		t.Error("interactive auto MUST raise ProjectIngestionGranted (the ladder grants ingestion at auto/yolo)")
	}
	if !got.TrustProject {
		t.Error("interactive auto MUST raise the TrustProject floor (the subagent shell reads it; this is main's behavior)")
	}
}

// TestApplyPostureNeverLowersGrants pins that applyPosture only RAISES the
// ingestion grant: a pre-set ProjectIngestionGranted survives a posture tier
// that would not itself raise it (it is never cleared).
func TestApplyPostureNeverLowersGrants(t *testing.T) {
	for _, posture := range []Posture{PostureStrict, PostureTrusted, PostureAuto, PostureYolo} {
		got := applyPosture(Config{Posture: posture, ProjectIngestionGranted: true})
		if !got.ProjectIngestionGranted {
			t.Errorf("%s: applyPosture must never LOWER ProjectIngestionGranted; got false", posture)
		}
	}
}

// TestBuildInstructionAssemblerNoRootOmitsAGENTS pins the AGENTS.md/CLAUDE.md
// gate (issue #359 redesign): when project ingestion is not admitted (noRoot=true)
// the RootAssembler is omitted, so a repo's AGENTS.md is NOT ingested — and when
// no other assembler is wired the result is an honest no-op (Assemble → nil,nil),
// not a typed-nil/panic. It drives the REAL buildInstructionAssembler path against
// a memfs workspace that carries an AGENTS.md, so it fails if the gate is dropped.
// The bool semantics are unchanged (true = omit RootAssembler).
func TestBuildInstructionAssemblerNoRootOmitsAGENTS(t *testing.T) {
	ctx := context.Background()
	ws := memfs.NewWorkspace("/repo")
	if err := ws.Write(ctx, "AGENTS.md", []byte("# Repo steering the model must NOT see\n")); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}

	// Default (noRoot=false): AGENTS.md IS ingested.
	def := buildInstructionAssembler(nil, nil, nil, nil, false)
	msgs, err := def.Assemble(ctx, ws)
	if err != nil {
		t.Fatalf("default Assemble: %v", err)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text, "Repo steering the model must NOT see") {
		t.Fatalf("default path must ingest AGENTS.md; got %d messages: %+v", len(msgs), msgs)
	}

	// Pinned (noRoot=true): AGENTS.md is NOT ingested; the assembler is an honest
	// no-op (zero messages), never a typed-nil.
	pinned := buildInstructionAssembler(nil, nil, nil, nil, true)
	if pinned == nil {
		t.Fatal("noRoot with no other assembler must return an honest no-op assembler, not nil")
	}
	pmsgs, err := pinned.Assemble(ctx, ws)
	if err != nil {
		t.Fatalf("pinned Assemble: %v", err)
	}
	if len(pmsgs) != 0 {
		t.Fatalf("project ingestion not admitted must suppress AGENTS.md ingestion; got %d messages: %+v", len(pmsgs), pmsgs)
	}
}
