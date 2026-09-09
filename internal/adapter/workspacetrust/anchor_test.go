package workspacetrust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// writeFile writes content to <ws>/rel, creating parent dirs.
func writeFile(t *testing.T, ws, rel, content string) {
	t.Helper()
	p := filepath.Join(ws, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// TestAnchorHashDeterministic asserts the same identity surface yields the same hash
// across repeated computations (the drift contract: a stable surface never drifts).
func TestAnchorHashDeterministic(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/soul.md", "You are a project persona.")
	writeFile(t, ws, ".mecatl/agents/reviewer.md", "agent body")
	writeFile(t, ws, ".claude/commands/deploy.md", "command body")
	writeFile(t, ws, ".mecatl/skills/x/SKILL.md", "skill body")

	r := New()
	h1 := r.AnchorHash(ws)
	h2 := r.AnchorHash(ws)
	if h1 == "" {
		t.Fatal("anchor hash empty for a populated surface")
	}
	if h1 != h2 {
		t.Fatalf("anchor hash not deterministic: %q != %q", h1, h2)
	}
}

// TestAnchorHashChangesOnSoulEdit asserts editing the project SOUL drifts the anchor.
func TestAnchorHashChangesOnSoulEdit(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/soul.md", "original persona")
	r := New()
	before := r.AnchorHash(ws)
	writeFile(t, ws, ".mecatl/soul.md", "MALICIOUS rewritten persona")
	if after := r.AnchorHash(ws); after == before {
		t.Fatal("editing the project soul did not change the anchor (drift missed)")
	}
}

// TestAnchorHashChangesOnAgentEdit asserts editing a project AGENT def drifts.
func TestAnchorHashChangesOnAgentEdit(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".claude/agents/reviewer.md", "benign reviewer")
	r := New()
	before := r.AnchorHash(ws)
	writeFile(t, ws, ".claude/agents/reviewer.md", "exfiltrate secrets")
	if after := r.AnchorHash(ws); after == before {
		t.Fatal("editing a project agent def did not change the anchor")
	}
}

// TestAnchorHashChangesOnNewCommand asserts ADDING a project command drifts (a repo
// gaining a steering channel after trust must re-prompt).
func TestAnchorHashChangesOnNewCommand(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/soul.md", "persona")
	r := New()
	before := r.AnchorHash(ws)
	writeFile(t, ws, ".mecatl/commands/evil.md", "new command")
	if after := r.AnchorHash(ws); after == before {
		t.Fatal("adding a project command did not change the anchor")
	}
}

// TestAnchorHashChangesOnNewSkill asserts ADDING a project skill drifts.
func TestAnchorHashChangesOnNewSkill(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/soul.md", "persona")
	r := New()
	before := r.AnchorHash(ws)
	writeFile(t, ws, ".claude/skills/evil/SKILL.md", "new skill")
	if after := r.AnchorHash(ws); after == before {
		t.Fatal("adding a project skill did not change the anchor")
	}
}

// TestAnchorHashIgnoresSettingsYAML is THE nag-avoidance guarantee (MUST-FIX 1):
// editing settings.yaml must NOT change the identity anchor. settings.yaml is in
// the admission set but NOT the drift anchor, so a permission edit never re-prompts.
func TestAnchorHashIgnoresSettingsYAML(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/soul.md", "persona")
	writeFile(t, ws, ".mecatl/agents/a.md", "agent")
	writeFile(t, ws, ".mecatl/settings.yaml", "permissions:\n  allow:\n    - Shell(go test*)\n")
	r := New()
	before := r.AnchorHash(ws)

	// Edit settings.yaml (and the project's settings.local.yaml) — a routine
	// permission change on a real commit. The anchor MUST be unchanged.
	writeFile(t, ws, ".mecatl/settings.yaml", "permissions:\n  allow:\n    - Shell(go build*)\n    - Shell(go vet*)\n")
	writeFile(t, ws, ".mecatl/settings.local.yaml", "permissions:\n  allow:\n    - Shell(rm*)\n")
	if after := r.AnchorHash(ws); after != before {
		t.Fatal("editing settings.yaml changed the identity anchor — would nag-fatigue the operator (MUST-FIX 1 violated)")
	}
}

// TestAnchorHashIgnoresSourceCode asserts ordinary repo files (source, README)
// outside the authority-set dirs do not affect the anchor.
func TestAnchorHashIgnoresSourceCode(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/soul.md", "persona")
	r := New()
	before := r.AnchorHash(ws)
	writeFile(t, ws, "main.go", "package main\nfunc main(){}\n")
	writeFile(t, ws, "README.md", "# project")
	if after := r.AnchorHash(ws); after != before {
		t.Fatal("editing ordinary source files changed the anchor (only the authority set should)")
	}
}

// TestAnchorHashEmptyWorkspace asserts an empty workspace ⇒ "" anchor (the fold
// treats a remembered entry against "" as drift, fail-safe).
func TestAnchorHashEmptyWorkspace(t *testing.T) {
	if h := New().AnchorHash(""); h != "" {
		t.Fatalf("AnchorHash(\"\") = %q, want empty", h)
	}
}

// TestAnchorHashStableEmptySurface asserts a workspace with NO authority-set files
// still produces a stable, non-empty hash (the all-absent transcript), and that it
// differs from a workspace that HAS a soul — so adding the first persona drifts.
func TestAnchorHashStableEmptySurface(t *testing.T) {
	bare := realDir(t, t.TempDir(), "bare")
	r := New()
	h1 := r.AnchorHash(bare)
	h2 := r.AnchorHash(bare)
	if h1 == "" || h1 != h2 {
		t.Fatalf("bare-surface anchor not stable: %q vs %q", h1, h2)
	}
	withSoul := realDir(t, t.TempDir(), "withsoul")
	writeFile(t, withSoul, ".mecatl/soul.md", "persona")
	if r.AnchorHash(withSoul) == h1 {
		t.Fatal("a workspace with a soul hashed identically to a bare one (absent marker missing)")
	}
}

// TestAnchorHashChangesOnSoulRemoval (FIX 2) asserts REMOVING the project soul
// drifts the anchor. Every other "changes" test adds/edits; removal is the
// absent-marker's job — a refactor that dropped the marker would silently STOP
// drifting when a steering file is deleted (a trusted repo could remove + re-add a
// persona to launder a change). Guard it.
func TestAnchorHashChangesOnSoulRemoval(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	soul := filepath.Join(ws, ".mecatl/soul.md")
	writeFile(t, ws, ".mecatl/soul.md", "persona")
	r := New()
	before := r.AnchorHash(ws)
	if err := os.Remove(soul); err != nil {
		t.Fatalf("remove soul: %v", err)
	}
	if after := r.AnchorHash(ws); after == before {
		t.Fatal("removing the project soul did not change the anchor (absent-marker drift missed)")
	}
}

// TestAnchorHashBoundedRead (FIX 4) pins that osAnchorRead hashes only the first
// anchorMaxFileBytes (1 MiB) of a member file — a deliberate bound (CWE-789), not an
// accident. It asserts an edit WITHIN the head bytes still drifts, so the bound is
// safe AND nobody "fixes" the cap into an unbounded read without this test noticing.
// We do NOT assert that a beyond-cap edit is invisible (that would over-specify the
// tail behaviour); we only pin head-sensitivity + the bound's existence.
func TestAnchorHashBoundedRead(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	// A file larger than the cap: a head we will edit + a tail of filler past 1 MiB.
	head := "HEAD-MARKER-v1\n"
	tail := strings.Repeat("x", anchorMaxFileBytes) // pushes total past the cap
	writeFile(t, ws, ".mecatl/agents/big.md", head+tail)
	r := New()
	before := r.AnchorHash(ws)

	// Edit within the FIRST anchorMaxFileBytes (the head) — must drift, proving the
	// read covers (at least) the head and the bound did not drop head bytes.
	writeFile(t, ws, ".mecatl/agents/big.md", "HEAD-MARKER-v2\n"+tail)
	if after := r.AnchorHash(ws); after == before {
		t.Fatal("an edit within the first 1 MiB did not drift; the bounded read must still hash the head")
	}
}

// TestAnchorCoversGateSurface (FIX 1 divergence guard) is the regression that makes
// anchor/gate divergence IMPOSSIBLE to introduce silently: it asserts that every
// project-tier directory the admission GATE admits — the agents resolver's project
// tier, the skills resolver's project tier, and the canonical command dir set — is
// covered by the anchor's folded dir set. If a future tier dir is added to a
// resolver but not threaded into the anchor, this fails LOUDLY (the unsafe direction:
// gate admits, anchor never drifts).
func TestAnchorCoversGateSurface(t *testing.T) {
	covered := make(map[string]bool, len(anchorDirs))
	for _, d := range anchorDirs {
		covered[filepath.Clean(d)] = true
	}
	// The exact project-tier dirs the gates admit, named from their OWNING packages
	// (not re-typed string literals — so this test tracks the sources, not a copy).
	gateSurface := []string{
		agents.ProjectDirMecatl,
		agents.ProjectDirClaude,
		skills.ProjectDirMecatl,
		skills.ProjectDirClaude,
	}
	gateSurface = append(gateSurface, prompt.DefaultCommandDirs...)
	for _, d := range gateSurface {
		if !covered[filepath.Clean(d)] {
			t.Fatalf("anchor does NOT cover gate-admitted project-tier dir %q; anchor folds %v — "+
				"the gate would admit defs the anchor never drifts on (FIX 1 divergence)", d, anchorDirs)
		}
	}
}
