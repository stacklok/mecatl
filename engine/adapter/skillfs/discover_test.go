package skillfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates <dir>/<name>/SKILL.md with the given content. It is an
// offline helper — everything is on the test's temp filesystem, no network.
func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", sub, err)
	}
	if err := os.WriteFile(filepath.Join(sub, SkillFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

const validSkill = `---
name: commit-style
description: How to write conventional commit messages for this repo.
---

# Commit style

Use the Conventional Commits format: type(scope): subject.
Wrap the body at 72 columns.
`

func TestDiscoverValidSkills(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "commit-style", validSkill)
	writeSkill(t, dir, "review", `---
name: review
description: Run a structured code review.
---
Look for correctness, then style.
`)

	got, skips, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if len(got) != 2 {
		t.Fatalf("Discover returned %d skills, want 2", len(got))
	}
	// Sorted by name: commit-style before review.
	if got[0].Name != "commit-style" || got[1].Name != "review" {
		t.Fatalf("skills not sorted by name: %q, %q", got[0].Name, got[1].Name)
	}
	if got[0].Description != "How to write conventional commit messages for this repo." {
		t.Errorf("description = %q", got[0].Description)
	}
	// Body excludes the frontmatter and is trimmed.
	if want := "# Commit style"; got[0].Body[:len(want)] != want {
		t.Errorf("body should start with the markdown heading, got %q", got[0].Body)
	}
	if got[0].Path == "" {
		t.Error("Path not populated")
	}
}

func TestDiscoverMissingDirIsNotError(t *testing.T) {
	got, skips, err := Discover(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(got) != 0 || len(skips) != 0 {
		t.Errorf("missing dir should yield nothing: skills=%d skips=%d", len(got), len(skips))
	}
}

func TestDiscoverEmptyDirArg(t *testing.T) {
	got, _, err := Discover("")
	if err != nil || got != nil {
		t.Errorf("empty dir arg: got=%v err=%v, want nil/nil", got, err)
	}
}

func TestDiscoverMalformedFrontmatterIsSkipped(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "good", validSkill)
	// No frontmatter at all.
	writeSkill(t, dir, "no-frontmatter", "just a body, no header\n")
	// Frontmatter present but missing required name.
	writeSkill(t, dir, "no-name", `---
description: has a description but no name
---
body
`)
	// Frontmatter opened but never closed.
	writeSkill(t, dir, "unterminated", `---
name: x
description: y
body without closing delimiter
`)
	// Broken YAML inside the frontmatter.
	writeSkill(t, dir, "bad-yaml", `---
name: [unclosed
---
body
`)

	got, skips, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0].Name != "commit-style" {
		t.Fatalf("only the valid skill should survive, got %d: %+v", len(got), got)
	}
	if len(skips) != 4 {
		t.Fatalf("expected 4 skips, got %d: %v", len(skips), skips)
	}
	// Each skip must carry a path and a reason (so the operator can fix it).
	for _, s := range skips {
		if s.Path == "" || s.Reason == "" {
			t.Errorf("skip missing path/reason: %+v", s)
		}
	}
}

func TestDiscoverDuplicateNameKeepsFirst(t *testing.T) {
	dir := t.TempDir()
	// Two directories whose frontmatter declares the SAME name.
	writeSkill(t, dir, "a-dir", `---
name: dup
description: first
---
first body
`)
	writeSkill(t, dir, "b-dir", `---
name: dup
description: second
---
second body
`)

	got, skips, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("duplicate name should yield 1 skill, got %d", len(got))
	}
	// a-dir sorts first, so it wins.
	if got[0].Description != "first" {
		t.Errorf("expected first definition kept, got %q", got[0].Description)
	}
	if len(skips) != 1 {
		t.Fatalf("expected 1 duplicate skip, got %d", len(skips))
	}
}

func TestDiscoverIgnoresNonSkillDirsAndFiles(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "real", validSkill)
	// A subdirectory with no SKILL.md must be silently ignored (no skip).
	if err := os.MkdirAll(filepath.Join(dir, "notaskill"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A stray top-level file must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, skips, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(got))
	}
	if len(skips) != 0 {
		t.Errorf("subdir without SKILL.md and stray file must not produce skips: %v", skips)
	}
}

func TestDiscoverCapsOversizedDescription(t *testing.T) {
	dir := t.TempDir()
	longDesc := strings.Repeat("a", maxDescriptionBytes*2)
	writeSkill(t, dir, "verbose", fmt.Sprintf("---\nname: verbose\ndescription: %s\n---\nbody\n", longDesc))

	got, skips, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// The skill is KEPT (oversize description is non-fatal), but trimmed.
	if len(got) != 1 {
		t.Fatalf("oversize description should not drop the skill, got %d", len(got))
	}
	if len(got[0].Description) > maxDescriptionBytes {
		t.Errorf("description not capped: %d bytes > %d", len(got[0].Description), maxDescriptionBytes)
	}
	if !strings.HasSuffix(got[0].Description, "…") {
		t.Error("truncated description should end with an ellipsis")
	}
	// A warning diagnostic must be emitted so the author gets a signal.
	if !hasReasonContaining(skips, "description") || !hasReasonContaining(skips, "truncated") {
		t.Errorf("expected a description-truncation warning, got %v", skips)
	}
}

func TestDiscoverWarnsOnOversizedBody(t *testing.T) {
	dir := t.TempDir()
	bigBody := strings.Repeat("x", MaxOutputBytes+1000)
	writeSkill(t, dir, "huge", "---\nname: huge\ndescription: a big skill\n---\n"+bigBody+"\n")

	got, skips, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// The body is NOT trimmed at discovery (it is trimmed on activation); the
	// skill is kept whole, but a warning is recorded.
	if len(got) != 1 {
		t.Fatalf("oversize body should not drop the skill, got %d", len(got))
	}
	if len(got[0].Body) <= MaxOutputBytes {
		t.Error("discovery must not pre-trim the body (it is trimmed on activation)")
	}
	if !hasReasonContaining(skips, "body") || !hasReasonContaining(skips, "truncated") {
		t.Errorf("expected an oversized-body warning, got %v", skips)
	}
}

// hasReasonContaining reports whether any diagnostic's Reason contains sub.
func hasReasonContaining(skips []SkipError, sub string) bool {
	for _, s := range skips {
		if strings.Contains(s.Reason, sub) {
			return true
		}
	}
	return false
}

func TestDirSourceImplementsSource(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "commit-style", validSkill)
	// DirSource is the concrete Source for one local directory; it must produce
	// the same skills as the Discover wrapper, and carry its Label unchanged.
	var src Source = DirSource{Dir: dir, Label: "project"}
	got, skips, err := src.Skills(context.Background())
	if err != nil {
		t.Fatalf("DirSource.Skills: %v", err)
	}
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if len(got) != 1 || got[0].Name != "commit-style" {
		t.Fatalf("DirSource produced %+v", got)
	}
}

func TestDirSourceEmptyAndMissing(t *testing.T) {
	// Empty Dir and a missing Dir both yield nothing, no error (opt-in).
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "nope")} {
		got, skips, err := DirSource{Dir: dir}.Skills(context.Background())
		if err != nil {
			t.Errorf("Dir=%q: unexpected error %v", dir, err)
		}
		if len(got) != 0 || len(skips) != 0 {
			t.Errorf("Dir=%q: expected nothing, got skills=%d skips=%d", dir, len(got), len(skips))
		}
	}
}

func TestDiscoverCRLFFrontmatter(t *testing.T) {
	dir := t.TempDir()
	crlf := "---\r\nname: win\r\ndescription: CRLF line endings\r\n---\r\nbody here\r\n"
	writeSkill(t, dir, "win", crlf)
	got, skips, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skips) != 0 {
		t.Fatalf("CRLF frontmatter should parse cleanly, skips=%v", skips)
	}
	if len(got) != 1 || got[0].Name != "win" {
		t.Fatalf("CRLF skill not parsed: %+v", got)
	}
}
