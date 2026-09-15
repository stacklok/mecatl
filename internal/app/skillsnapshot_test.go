package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// --- skillSnapshot (ListSkills projection) -----------------------------------

func TestSkillSnapshotEmpty(t *testing.T) {
	if got := skillSnapshot(nil); got != nil {
		t.Fatalf("nil input must yield a nil snapshot, got %+v", got)
	}
	if got := skillSnapshot([]skills.Skill{}); got != nil {
		t.Fatalf("empty input must yield a nil snapshot, got %+v", got)
	}
}

func TestSkillSnapshotProjectsNameAndDescriptionSorted(t *testing.T) {
	// Deliberately unsorted, with a body/path that must NOT leak into the snapshot.
	in := []skills.Skill{
		{Name: "deep-research", Description: "fan-out web research", Body: "BODY", Path: "/x/SKILL.md"},
		{Name: "code-review", Description: "review a diff", Body: "BODY2", Path: "/y/SKILL.md"},
	}
	snap := skillSnapshot(in)
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	// Name-sorted: "code-review" < "deep-research".
	if snap[0].GetName() != "code-review" || snap[1].GetName() != "deep-research" {
		t.Fatalf("snapshot order = %q,%q want code-review,deep-research",
			snap[0].GetName(), snap[1].GetName())
	}
	if snap[0].GetDescription() != "review a diff" {
		t.Fatalf("snap[0] description = %q", snap[0].GetDescription())
	}
	// Metadata only — the proto type has no body/path field, so verify the projection
	// carried exactly name+description (description does not accidentally hold the body).
	if strings.Contains(snap[0].GetDescription(), "BODY") {
		t.Fatalf("snapshot must project description only, not the body: %q", snap[0].GetDescription())
	}
}

// TestBuildWiresListSkillsEndToEnd proves the WHOLE discovery→snapshot→RPC chain
// is wired through the real harness: write two skills to a temp dir (deliberately
// NOT in sorted order), run the real app.Build with SkillsDirs pointed at it, and
// assert built.Service.ListSkills returns BOTH skills, name-SORTED, with the right
// name+description. Every other skills test stops at a seam (skillSnapshot called
// directly, server test injects a canned Config.Skills); only this one catches a
// dropped return value in build.go (e.g. registerSkills' discovered slice).
func TestBuildWiresListSkillsEndToEnd(t *testing.T) {
	dir := t.TempDir()
	// Write "zebra" BEFORE "alpha" so a no-sort regression would surface them in
	// discovery order, not name order.
	writeSkill(t, dir, "zebra", "the last skill", "ZEBRA BODY")
	writeSkill(t, dir, "alpha", "the first skill", "ALPHA BODY")

	ctx := context.Background()
	built, err := buildIsolated(t, ctx, Config{
		Workspace:  t.TempDir(),
		Model:      "mock",
		UseMock:    true,
		SkillsDirs: []string{dir},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	got := built.Service.ListSkills(ctx)
	if len(got) != 2 {
		t.Fatalf("ListSkills returned %d skills, want 2: %+v", len(got), got)
	}
	// Name-sorted: "alpha" < "zebra".
	if got[0].GetName() != "alpha" || got[1].GetName() != "zebra" {
		t.Fatalf("ListSkills order = %q,%q want alpha,zebra", got[0].GetName(), got[1].GetName())
	}
	if got[0].GetDescription() != "the first skill" {
		t.Fatalf("alpha description = %q, want %q", got[0].GetDescription(), "the first skill")
	}
	if got[1].GetDescription() != "the last skill" {
		t.Fatalf("zebra description = %q, want %q", got[1].GetDescription(), "the last skill")
	}
}
