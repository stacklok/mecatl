package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// no_project_trust_test.go pins the composition-layer project-tier-INGESTION
// suppression seam (issue #359): the --no-project-trust pin suppresses the repo's
// repo-resident steering WITHOUT touching cfg.TrustProject (so the read-only
// subagent-shell trust gate keeps reading the real trust decision), and NO posture
// tier can re-raise the ingestion the pin dropped.

// TestIngestProjectTierTruthTable pins the single expression every ingestion
// consumer reads: admit the project's repo-resident steering only when the
// workspace is trusted AND the pin is not set.
func TestIngestProjectTierTruthTable(t *testing.T) {
	cases := []struct {
		name            string
		trustProject    bool
		noProjectIngest bool
		want            bool
	}{
		{"trusted, not suppressed", true, false, true},
		{"trusted, suppressed (the pin)", true, true, false},
		{"untrusted, not suppressed", false, false, false},
		{"untrusted, suppressed", false, true, false},
	}
	for _, tc := range cases {
		got := ingestProjectTier(Config{TrustProject: tc.trustProject, NoProjectIngest: tc.noProjectIngest})
		if got != tc.want {
			t.Errorf("%s: ingestProjectTier(TrustProject=%v, NoProjectIngest=%v) = %v, want %v",
				tc.name, tc.trustProject, tc.noProjectIngest, got, tc.want)
		}
	}
}

// TestPinWinsOverPostureAuto is the load-bearing invariant (issue #359): under
// --posture auto, applyPosture RAISES cfg.TrustProject (so the read-only subagent
// shell is built), but the pin's NoProjectIngest is NEVER touched by applyPosture —
// so ingestion stays suppressed while the trust/shell gate sees the real (raised)
// trust decision. A reviewer verifying option (a) over (b) starts here.
func TestPinWinsOverPostureAuto(t *testing.T) {
	for _, posture := range []Posture{PostureTrusted, PostureAuto, PostureYolo} {
		got := applyPosture(Config{Posture: posture, TrustProject: false, NoProjectIngest: true})
		if !got.TrustProject {
			t.Errorf("%s: applyPosture must still RAISE TrustProject (the subagent-shell gate reads it); got false", posture)
		}
		if !got.NoProjectIngest {
			t.Errorf("%s: applyPosture must NEVER clear NoProjectIngest (the pin wins over the posture floor); got false", posture)
		}
		if ingestProjectTier(got) {
			t.Errorf("%s: ingestion must stay SUPPRESSED under the pin even though TrustProject was raised", posture)
		}
	}
}

// TestApplyPostureNeverSetsNoProjectIngest pins the one-way door: no posture tier
// derives the suppression pin (it is an operator deployment decision, set only by
// the CLI flag / operator-tier YAML). If a future tier silently set NoProjectIngest
// the scheduler's "auto approvals + ingest the repo" combination would be lost.
func TestApplyPostureNeverSetsNoProjectIngest(t *testing.T) {
	for _, posture := range []Posture{PostureStrict, PostureTrusted, PostureAuto, PostureYolo} {
		got := applyPosture(Config{Posture: posture})
		if got.NoProjectIngest {
			t.Errorf("%s: applyPosture must not set NoProjectIngest (it is operator-set only)", posture)
		}
	}
}

// TestFoldOperatorNoProjectTrustCLIOutRanksYAML pins the precedence: a CLI
// --no-project-trust (NoProjectTrustFlagSet) wins over the operator-tier YAML, and
// the fold never fires when the flag was explicit. Mirrors
// TestFoldOperatorReasoningEffortCLIOutRanksYAML.
func TestFoldOperatorNoProjectTrustCLIOutRanksYAML(t *testing.T) {
	// CLI flag set → the fold is a no-op regardless of any resolver value.
	got := foldOperatorNoProjectTrust(Config{NoProjectIngest: true, NoProjectTrustFlagSet: true})
	if !got.NoProjectIngest {
		t.Fatal("CLI --no-project-trust must be preserved (fold is a no-op when the flag is explicit)")
	}
	// CLI flag explicitly false (operator passed --no-project-trust=false) → YAML
	// cannot raise it.
	got = foldOperatorNoProjectTrust(Config{NoProjectIngest: false, NoProjectTrustFlagSet: true})
	if got.NoProjectIngest {
		t.Fatal("an explicit CLI flag must out-rank the operator-tier YAML (YAML cannot raise it)")
	}
	// No flag, no resolver → the fold is a no-op (nil-safe).
	got = foldOperatorNoProjectTrust(Config{})
	if got.NoProjectIngest {
		t.Fatal("no flag + no resolver must leave NoProjectIngest false")
	}
}

// TestFoldOperatorNoProjectTrustYAMLRaises pins the positive YAML path: with no CLI
// flag, an operator-tier settings.yaml `no-project-trust: true` raises
// cfg.NoProjectIngest. (The CLI-wins direction is covered above.)
func TestFoldOperatorNoProjectTrustYAMLRaises(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npt.yaml")
	if err := os.WriteFile(path, []byte("no-project-trust: true\n"), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	if res == nil {
		t.Fatal("resolver should be non-nil")
	}
	if got := foldOperatorNoProjectTrust(Config{permResolver: res}); !got.NoProjectIngest {
		t.Fatal("operator-tier YAML no-project-trust: true must fold onto NoProjectIngest when no CLI flag is set")
	}
}

// TestBuildInstructionAssemblerNoRootOmitsAGENTS pins the AGENTS.md/CLAUDE.md gate
// (issue #359 decision): when the pin is set (noRoot=true) the RootAssembler is
// omitted, so a repo's AGENTS.md is NOT ingested — and when no other assembler is
// wired the result is an honest no-op (Assemble → nil,nil), not a typed-nil/panic.
// It drives the REAL buildInstructionAssembler path against a memfs workspace that
// carries an AGENTS.md, so it fails if the gate is dropped.
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
		t.Fatalf("the --no-project-trust pin must suppress AGENTS.md ingestion; got %d messages: %+v", len(pmsgs), pmsgs)
	}
}
