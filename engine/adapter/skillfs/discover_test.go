package skillfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
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

func TestGoccyYAMLMigration_SemanticMatrixSkillFrontmatter(t *testing.T) {
	data, err := os.ReadFile("../../testdata/semantic-matrix.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var matrix struct {
		Cases []struct {
			Name     string            `yaml:"name"`
			Category string            `yaml:"category"`
			Document string            `yaml:"document"`
			Readers  map[string]string `yaml:"readers"`
		} `yaml:"cases"`
	}
	if err := yaml.Unmarshal(data, &matrix); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, tc := range matrix.Cases {
		outcome, ok := tc.Readers["skillfs"]
		if !ok {
			continue
		}
		seen = true
		if tc.Category != "frontmatter" {
			t.Fatalf("fixture %q has category %q, want frontmatter", tc.Name, tc.Category)
		}
		t.Run(tc.Name, func(t *testing.T) {
			_, parseError, _ := ParseSkill([]byte(tc.Document), "semantic-matrix.md")
			if accepted, want := parseError == "", outcome == "accept"; accepted != want {
				t.Fatalf("ParseSkill() accepted=%v, want %v (error=%q)", accepted, want, parseError)
			}
		})
	}
	if !seen {
		t.Fatal("semantic matrix declares no skillfs frontmatter case")
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
	writeSkill(t, dir, "commit-style", validSkill)
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
	// Under the dir-name-match rule two directories in the SAME parent cannot
	// both hold the same frontmatter name (each dir name must equal its skill
	// name), so the keep-first dedup lives at the CROSS-SOURCE layer. Exercise
	// it via MultiSource over two single-skill sources that both declare "dup".
	dirA := t.TempDir()
	writeSkill(t, dirA, "dup", `---
name: dup
description: first
---
first body
`)
	dirB := t.TempDir()
	writeSkill(t, dirB, "dup", `---
name: dup
description: second
---
second body
`)

	src := NewMultiSource(
		DirSource{Dir: dirA, Label: "a"},
		DirSource{Dir: dirB, Label: "b"},
	)
	got, skips, err := src.Skills(context.Background())
	if err != nil {
		t.Fatalf("MultiSource.Skills: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("duplicate name should yield 1 skill, got %d", len(got))
	}
	// The earlier (higher-precedence) source wins.
	if got[0].Description != "first" {
		t.Errorf("expected first definition kept, got %q", got[0].Description)
	}
	if len(skips) != 1 {
		t.Fatalf("expected 1 duplicate skip, got %d: %v", len(skips), skips)
	}
}

// TestValidSkillName pins the shared skill-name grammar
// (^[a-z0-9][a-z0-9_-]{0,63}$): a leading lowercase letter or digit, then
// lowercase letters/digits/underscore/hyphen, 1-64 chars. Underscore is
// DELIBERATELY allowed (the lax grammar the draft write path already used) — a
// future "tighten to the spec's hyphens-only form" change must trip this test.
func TestValidSkillName(t *testing.T) {
	valid := []string{"review", "commit-style", "deploy-to-staging", "api_v2", "a", "a1", "my_skill-2", "1skill"}
	for _, n := range valid {
		if !ValidSkillName(n) {
			t.Errorf("ValidSkillName(%q) = false, want true", n)
		}
	}
	invalid := []string{
		"",                            // empty
		"Review",                      // uppercase
		"UPPER",                       // uppercase
		"my skill",                    // space
		"-lead",                       // leading hyphen
		"_lead",                       // leading underscore
		"has.dot",                     // dot
		"a" + strings.Repeat("b", 64), // 65 chars, over the cap
	}
	for _, n := range invalid {
		if ValidSkillName(n) {
			t.Errorf("ValidSkillName(%q) = true, want false", n)
		}
	}
}

// TestParseSkillRejectsInvalidName pins that a name failing the shared grammar
// is a fatal parse skip (the skill is excluded), never silently accepted.
func TestParseSkillRejectsInvalidName(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "Bad Name", `---
name: Bad Name
description: uppercase and space
---
body
`)
	got, skips, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an invalid name must be skipped, got %+v", got)
	}
	if !hasReasonContaining(skips, "name") {
		t.Errorf("expected an invalid-name skip reason, got %v", skips)
	}
}

// TestDiscoveryRejectsDirNameMismatch pins the spec rule that the frontmatter
// name must equal the parent directory name (fail-soft: skip, don't abort).
func TestDiscoveryRejectsDirNameMismatch(t *testing.T) {
	dir := t.TempDir()
	// name: foo inside dir bar/ is skipped.
	writeSkill(t, dir, "bar", `---
name: foo
description: mismatched
---
body
`)
	// name: baz inside dir baz/ is accepted.
	writeSkill(t, dir, "baz", `---
name: baz
description: matched
---
body
`)
	got, skips, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0].Name != "baz" {
		t.Fatalf("only the dir-matched skill should survive, got %+v", got)
	}
	if !hasReasonContaining(skips, "does not match its directory") {
		t.Errorf("expected a dir-name-mismatch skip reason, got %v", skips)
	}
}

// TestDiscoveryAcceptsUnderscoreNames pins the deliberate lax-grammar decision:
// a name with an underscore (e.g. my_skill) in a matching dir IS accepted. This
// is the tripwire against a future "tighten to hyphens-only" change.
func TestDiscoveryAcceptsUnderscoreNames(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "my_skill", `---
name: my_skill
description: underscore name
---
body
`)
	got, skips, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0].Name != "my_skill" {
		t.Fatalf("an underscore name in a matching dir must be accepted, got %+v skips=%v", got, skips)
	}
	if len(skips) != 0 {
		t.Errorf("unexpected skips: %v", skips)
	}
}

func TestDiscoverIgnoresNonSkillDirsAndFiles(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "commit-style", validSkill)
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

func TestParseSkillCarriesOptionalFrontmatter(t *testing.T) {
	t.Run("round-trips into Skill and SkillMeta", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "licensed", "---\nname: licensed\ndescription: A skill with optional advisory frontmatter.\nlicense: MIT\ncompatibility: \"mecatl >= 0.1\"\nmetadata:\n  author: stacklok\n  version: \"1\"\n---\nbody\n")
		got, skips, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(skips) != 0 {
			t.Fatalf("optional fields should not produce skips: %v", skips)
		}
		if len(got) != 1 || got[0].Name != "licensed" {
			t.Fatalf("licensed skill not parsed: %+v", got)
		}
		sk := got[0]
		if sk.License != "MIT" {
			t.Errorf("Skill.License = %q, want %q", sk.License, "MIT")
		}
		if sk.Compatibility != "mecatl >= 0.1" {
			t.Errorf("Skill.Compatibility = %q, want %q", sk.Compatibility, "mecatl >= 0.1")
		}
		wantMeta := map[string]string{"author": "stacklok", "version": "1"}
		if !reflect.DeepEqual(sk.Metadata, wantMeta) {
			t.Errorf("Skill.Metadata = %v, want %v", sk.Metadata, wantMeta)
		}

		// The same values thread through the port-shaped SkillMeta produced by
		// NewFSSource (the FS source is the consumer that feeds the catalog).
		src, fskips, sErr := NewFSSource(context.Background(), DirSource{Dir: dir})
		if sErr != nil {
			t.Fatalf("NewFSSource: %v", sErr)
		}
		if len(fskips) != 0 {
			t.Fatalf("unexpected NewFSSource skips: %v", fskips)
		}
		metas, mErr := src.ListSkills(context.Background())
		if mErr != nil {
			t.Fatalf("ListSkills: %v", mErr)
		}
		if len(metas) != 1 || metas[0].Name != "licensed" {
			t.Fatalf("ListSkills = %+v", metas)
		}
		m := metas[0]
		if m.License != "MIT" {
			t.Errorf("SkillMeta.License = %q, want %q", m.License, "MIT")
		}
		if m.Compatibility != "mecatl >= 0.1" {
			t.Errorf("SkillMeta.Compatibility = %q, want %q", m.Compatibility, "mecatl >= 0.1")
		}
		if !reflect.DeepEqual(m.Metadata, wantMeta) {
			t.Errorf("SkillMeta.Metadata = %v, want %v", m.Metadata, wantMeta)
		}
	})

	t.Run("missing fields yield zero values with no skip and no note", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "commit-style", validSkill) // validSkill has only name+description
		got, skips, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(skips) != 0 {
			t.Fatalf("a skill omitting the optional fields must not produce notes: %v", skips)
		}
		if len(got) != 1 || got[0].Name != "commit-style" {
			t.Fatalf("plain skill not parsed: %+v", got)
		}
		if got[0].License != "" || got[0].Compatibility != "" || got[0].Metadata != nil {
			t.Errorf("omitted optional fields must be zero: License=%q Compatibility=%q Metadata=%v",
				got[0].License, got[0].Compatibility, got[0].Metadata)
		}
	})

	t.Run("oversized license and compatibility are clamped with a note", func(t *testing.T) {
		dir := t.TempDir()
		longLicense := strings.Repeat("L", MaxLicenseBytes*2)
		longCompat := strings.Repeat("C", MaxCompatibilityBytes*2)
		writeSkill(t, dir, "verbose", fmt.Sprintf("---\nname: verbose\ndescription: clamps advisory fields\nlicense: %s\ncompatibility: %s\n---\nbody\n", longLicense, longCompat))
		got, skips, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("oversized advisory fields should not drop the skill, got %d", len(got))
		}
		if len(got[0].License) > MaxLicenseBytes {
			t.Errorf("License not clamped: %d > %d", len(got[0].License), MaxLicenseBytes)
		}
		if len(got[0].Compatibility) > MaxCompatibilityBytes {
			t.Errorf("Compatibility not clamped: %d > %d", len(got[0].Compatibility), MaxCompatibilityBytes)
		}
		if !hasReasonContaining(skips, "license") {
			t.Errorf("expected a license-clamp warning, got %v", skips)
		}
		if !hasReasonContaining(skips, "compatibility") {
			t.Errorf("expected a compatibility-clamp warning, got %v", skips)
		}
	})

	t.Run("oversized metadata map drops to nil with a warning note", func(t *testing.T) {
		dir := t.TempDir()
		// Too many entries (> MaxMetadataEntries).
		var metaBlock strings.Builder
		metaBlock.WriteString("---\nname: big-meta\ndescription: too many metadata entries\nmetadata:\n")
		for i := 0; i < MaxMetadataEntries+1; i++ {
			fmt.Fprintf(&metaBlock, "  k%d: v%d\n", i, i)
		}
		metaBlock.WriteString("---\nbody\n")
		writeSkill(t, dir, "big-meta", metaBlock.String())
		got, skips, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		var sk Skill
		for _, s := range got {
			if s.Name == "big-meta" {
				sk = s
			}
		}
		if sk.Name != "big-meta" {
			t.Fatalf("big-meta skill not parsed: %+v", got)
		}
		if sk.Metadata != nil {
			t.Errorf("Metadata should be dropped to nil on entry-count overflow, got %v", sk.Metadata)
		}
		if !hasReasonContaining(skips, "metadata") {
			t.Errorf("expected a metadata-drop warning on entry overflow, got %v", skips)
		}
	})

	t.Run("oversized metadata value drops the whole map with a warning note", func(t *testing.T) {
		dir := t.TempDir()
		bigVal := strings.Repeat("V", MaxMetadataValueBytes+1)
		writeSkill(t, dir, "big-val", fmt.Sprintf("---\nname: big-val\ndescription: one huge metadata value\nmetadata:\n  author: %s\n---\nbody\n", bigVal))
		got, skips, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		var sk Skill
		for _, s := range got {
			if s.Name == "big-val" {
				sk = s
			}
		}
		if sk.Name != "big-val" {
			t.Fatalf("big-val skill not parsed: %+v", got)
		}
		if sk.Metadata != nil {
			t.Errorf("Metadata should be dropped to nil on value-overflow, got %v", sk.Metadata)
		}
		if !hasReasonContaining(skips, "metadata") {
			t.Errorf("expected a metadata-drop warning on value overflow, got %v", skips)
		}
	})
}

func TestParseSkillAllowedToolsAcceptsResolvedScalarsAsText(t *testing.T) {
	t.Parallel()

	for _, scalar := range []string{"true", "42", "2026-08-28T12:00:00Z", "null"} {
		t.Run(scalar, func(t *testing.T) {
			skill, reason, _ := ParseSkill([]byte("---\nname: tooling\ndescription: d\nallowed-tools: "+scalar+"\n---\nbody"), "tooling/SKILL.md")
			if reason != "" {
				t.Fatalf("ParseSkill(%q) reason = %q", scalar, reason)
			}
			if got := strings.Join(skill.AllowedTools, ","); got != scalar {
				t.Fatalf("AllowedTools = %q, want %q", got, scalar)
			}
		})
	}
}

// TestParseSkillAllowedTools pins the ADVISORY `allowed-tools` frontmatter
// (agentskills.io, Experimental; issue #419): the spec's space-separated STRING
// form splits into the SkillMeta field, extra whitespace is tolerated, a
// missing field yields nil (no skip), and oversized lists/names are truncated
// with a non-fatal warning note. A YAML LIST form is accepted too. A non-string
// scalar (e.g. allowed-tools: 123) is a FATAL parse error (fail-closed). The
// field is ADVISORY ONLY — it never grants permission; it is only surfaced as a
// note on activation.
func TestParseSkillAllowedTools(t *testing.T) {
	t.Run("space-separated string form splits into names", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "tooling", "---\nname: tooling\ndescription: a skill with allowed-tools\nallowed-tools: \"Shell Read Grep\"\n---\nbody\n")
		got, skips, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(skips) != 0 {
			t.Fatalf("a well-formed allowed-tools field must not produce notes: %v", skips)
		}
		if len(got) != 1 || got[0].Name != "tooling" {
			t.Fatalf("tooling skill not parsed: %+v", got)
		}
		want := []string{"Shell", "Read", "Grep"}
		if !reflect.DeepEqual(got[0].AllowedTools, want) {
			t.Errorf("AllowedTools = %v, want %v", got[0].AllowedTools, want)
		}

		// Threads through the port-shaped SkillMeta produced by NewFSSource.
		src, fskips, sErr := NewFSSource(context.Background(), DirSource{Dir: dir})
		if sErr != nil || len(fskips) != 0 {
			t.Fatalf("NewFSSource: %v skips=%v", sErr, fskips)
		}
		metas, _ := src.ListSkills(context.Background())
		if len(metas) != 1 || !reflect.DeepEqual(metas[0].AllowedTools, want) {
			t.Errorf("SkillMeta.AllowedTools = %v, want %v", metas[0].AllowedTools, want)
		}
	})

	t.Run("extra whitespace is tolerated", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "ws", "---\nname: ws\ndescription: lots of whitespace\nallowed-tools: \"  Shell   Read    Grep  \"\n---\nbody\n")
		got, _, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		want := []string{"Shell", "Read", "Grep"}
		if !reflect.DeepEqual(got[0].AllowedTools, want) {
			t.Errorf("AllowedTools = %v, want %v (extra whitespace should collapse)", got[0].AllowedTools, want)
		}
	})

	t.Run("YAML list form is accepted", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "listform", "---\nname: listform\ndescription: list form\nallowed-tools:\n  - Shell\n  - Read\n  - Grep\n---\nbody\n")
		got, skips, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(skips) != 0 {
			t.Fatalf("list form must parse cleanly, skips=%v", skips)
		}
		want := []string{"Shell", "Read", "Grep"}
		if !reflect.DeepEqual(got[0].AllowedTools, want) {
			t.Errorf("AllowedTools = %v, want %v", got[0].AllowedTools, want)
		}
	})

	t.Run("missing field yields nil with no skip and no note", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "commit-style", validSkill) // no allowed-tools
		got, skips, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(skips) != 0 {
			t.Fatalf("a skill omitting allowed-tools must not produce notes: %v", skips)
		}
		if got[0].AllowedTools != nil {
			t.Errorf("omitted allowed-tools must be nil, got %v", got[0].AllowedTools)
		}
	})

	t.Run("oversized list is truncated to the prefix with a warning note", func(t *testing.T) {
		dir := t.TempDir()
		// Build a space-separated list with MaxAllowedTools+1 names.
		var b strings.Builder
		for i := 0; i < MaxAllowedTools+1; i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, "t%d", i)
		}
		writeSkill(t, dir, "many", fmt.Sprintf("---\nname: many\ndescription: too many tools\nallowed-tools: %q\n---\nbody\n", b.String()))
		got, skips, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("oversized allowed-tools should not drop the skill, got %d", len(got))
		}
		if len(got[0].AllowedTools) != MaxAllowedTools {
			t.Errorf("AllowedTools not truncated to cap: %d (want %d)", len(got[0].AllowedTools), MaxAllowedTools)
		}
		// The parsed PREFIX is kept (first MaxAllowedTools names), in order.
		for i, name := range got[0].AllowedTools {
			want := fmt.Sprintf("t%d", i)
			if name != want {
				t.Errorf("AllowedTools[%d] = %q, want the parsed prefix %q", i, name, want)
			}
		}
		if !hasReasonContaining(skips, "allowed-tools") || !hasReasonContaining(skips, "truncated") {
			t.Errorf("expected an allowed-tools truncation warning, got %v", skips)
		}
	})

	t.Run("oversized name is truncated with a warning note", func(t *testing.T) {
		dir := t.TempDir()
		long := strings.Repeat("T", MaxAllowedToolNameBytes*2)
		writeSkill(t, dir, "longname", fmt.Sprintf("---\nname: longname\ndescription: one huge tool name\nallowed-tools: %q\n---\nbody\n", long))
		got, skips, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("oversized name should not drop the skill, got %d", len(got))
		}
		if len(got[0].AllowedTools) != 1 {
			t.Fatalf("expected exactly one allowed tool, got %v", got[0].AllowedTools)
		}
		if len(got[0].AllowedTools[0]) > MaxAllowedToolNameBytes {
			t.Errorf("name not truncated: %d > %d", len(got[0].AllowedTools[0]), MaxAllowedToolNameBytes)
		}
		if !hasReasonContaining(skips, "allowed-tools") {
			t.Errorf("expected an allowed-tools name-truncation warning, got %v", skips)
		}
	})

	t.Run("resolved scalar is preserved as text", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "scalar", "---\nname: scalar\ndescription: int scalar\nallowed-tools: 123\n---\nbody\n")
		got, skips, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(skips) != 0 || len(got) != 1 || !reflect.DeepEqual(got[0].AllowedTools, []string{"123"}) {
			t.Fatalf("resolved scalar = skills=%+v skips=%v, want allowed-tools [123] without skips", got, skips)
		}
	})
}

func TestGoccyYAMLMigration_Scenario3_SkillFrontmatterCompatibility(t *testing.T) {
	t.Parallel()

	skill, reason, _ := ParseSkill([]byte(`---
name: review
description: review changes
allowed-tools: [Read, "Grep Shell"]
metadata:
  audience: engineers
unknown-future-field: ignored
---
review the change`), "review/SKILL.md")
	if reason != "" {
		t.Fatalf("parse skill frontmatter: %s", reason)
	}
	if got, want := strings.Join(skill.AllowedTools, ","), "Read,Grep,Shell"; got != want {
		t.Fatalf("allowed-tools = %q, want %q", got, want)
	}
	if skill.Metadata["audience"] != "engineers" {
		t.Fatalf("metadata = %#v, want normalized metadata", skill.Metadata)
	}

	_, reason, _ = ParseSkill([]byte("---\nname: leaked-secret\ndescription: [unterminated\n---\nbody"), "bad/SKILL.md")
	if !strings.Contains(reason, "malformed YAML frontmatter at line") {
		t.Fatalf("malformed frontmatter reason = %q, want safe location", reason)
	}
	if strings.Contains(reason, "leaked-secret") || strings.Contains(reason, "unterminated") {
		t.Fatalf("malformed frontmatter reason leaked YAML source: %q", reason)
	}
}

func TestLegacyBashAllowedToolsNormalizesToShell(t *testing.T) {
	skill, reason, _ := ParseSkill([]byte("---\nname: legacy\ndescription: legacy tools\nallowed-tools: \"Bash Read\"\n---\nbody\n"), "legacy/SKILL.md")
	if reason != "" {
		t.Fatalf("ParseSkill reason = %q", reason)
	}
	if got, want := strings.Join(skill.AllowedTools, ","), "Shell,Read"; got != want {
		t.Fatalf("allowed tools = %q, want %q", got, want)
	}
}
