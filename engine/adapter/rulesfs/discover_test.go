package rulesfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// writeRule writes a <name>.md file into dir, creating dir if needed.
func writeRule(t *testing.T, dir, file, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, file)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// TestParseRulePathsForms asserts the three accepted `paths:` forms parse
// identically: a YAML sequence, a single scalar glob, and a comma/space-
// separated scalar (the agentfs `tools` tolerance).
func TestParseRulePathsForms(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"sequence", "---\npaths:\n  - \"src/api/**/*.ts\"\n  - \"**/*.go\"\n---\nbody", []string{"src/api/**/*.ts", "**/*.go"}},
		{"inline sequence", "---\npaths: [\"**/*_test.go\", \"**/*.go\"]\n---\nbody", []string{"**/*_test.go", "**/*.go"}},
		{"single scalar glob", "---\npaths: \"**/*_test.go\"\n---\nbody", []string{"**/*_test.go"}},
		{"comma separated scalar", "---\npaths: \"**/*_test.go, **/*.go\"\n---\nbody", []string{"**/*_test.go", "**/*.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, perr, notes := parseRule([]byte(tc.raw), "r")
			if perr != "" {
				t.Fatalf("parse error: %s", perr)
			}
			if len(notes) != 0 {
				t.Fatalf("unexpected notes: %v", notes)
			}
			if strings.Join(r.Paths, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("paths = %v, want %v", r.Paths, tc.want)
			}
		})
	}
}

func TestGoccyYAMLMigration_SemanticMatrixRuleFrontmatter(t *testing.T) {
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
		outcome, ok := tc.Readers["rulesfs"]
		if !ok {
			continue
		}
		seen = true
		if tc.Category != "frontmatter" {
			t.Fatalf("fixture %q has category %q, want frontmatter", tc.Name, tc.Category)
		}
		t.Run(tc.Name, func(t *testing.T) {
			_, parseError, _ := parseRule([]byte(tc.Document), "semantic-matrix")
			if accepted, want := parseError == "", outcome == "accept"; accepted != want {
				t.Fatalf("parseRule() accepted=%v, want %v (error=%q)", accepted, want, parseError)
			}
		})
	}
	if !seen {
		t.Fatal("semantic matrix declares no rulesfs frontmatter case")
	}
}

// TestParseRuleBodyOnlyNoFrontmatter asserts a file with NO frontmatter parses
// to an UNCONDITIONAL rule (empty Paths) — the frontmatter is optional.
func TestParseRuleBodyOnlyNoFrontmatter(t *testing.T) {
	r, perr, notes := parseRule([]byte("Always run gofmt before committing."), "style")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	if r.Name != "style" {
		t.Fatalf("name = %q, want the filename stem", r.Name)
	}
	if len(r.Paths) != 0 {
		t.Fatalf("body-only file must yield empty Paths (unconditional), got %v", r.Paths)
	}
	if r.Body != "Always run gofmt before committing." {
		t.Fatalf("body = %q", r.Body)
	}
}

// TestParseRuleNameFieldIgnored asserts a `name:` frontmatter field is IGNORED
// (the filename stem is the source of truth; Claude Code's rules carry no
// `name:`). A stray one must neither fail the parse nor override the stem.
func TestParseRuleNameFieldIgnored(t *testing.T) {
	r, perr, _ := parseRule([]byte("---\nname: ignored\npaths: [a]\n---\nbody"), "stem")
	if perr != "" {
		t.Fatalf("a name: field must not fail the parse, got %s", perr)
	}
	if r.Name != "stem" {
		t.Fatalf("name = %q, want the filename stem (frontmatter name ignored)", r.Name)
	}
}

// TestParseRuleMalformedYAMLFatal asserts malformed frontmatter is a FATAL
// drop (the rule is excluded) and the scan continues to the next file.
func TestParseRuleMalformedYAMLFatal(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "good.md", "---\npaths: [a]\n---\nbody")
	writeRule(t, dir, "bad.md", "---\npaths: [unterminated\n---\nbody")

	discovered, skips, err := DirSource{Dir: dir}.Rules(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(discovered) != 1 || discovered[0].Rule.Name != "good" {
		t.Fatalf("malformed file must not abort the scan; discovered = %+v", discovered)
	}
	if len(skips) != 1 || !skips[0].Fatal || !strings.Contains(skips[0].Path, "bad.md") {
		t.Fatalf("want one FATAL skip for bad.md, got %+v", skips)
	}
	if !strings.Contains(skips[0].Reason, "malformed YAML frontmatter") {
		t.Fatalf("skip reason = %q, want the malformed-YAML reason", skips[0].Reason)
	}
}

// TestDirSourceDuplicateStemKeepFirst asserts duplicate filename stems within
// one directory resolve keep-first in sorted-path order. Two files cannot
// share a stem in one dir on a case-sensitive FS, so exercise the dedup via
// two dirs that differ only by a case variant — or, more portably, the
// keep-first discipline via two same-stem files on a case-insensitive-collision
// is NOT portable; instead pin the dedup map behaviour via two files whose
// stems collide after a normalisation the adapter does NOT do — the real
// duplicate case is two files literally named the same, which the FS forbids.
// So this test pins the dedup guard structurally: feed two Discovered entries
// with the same name through MultiSource and assert earlier-wins.
func TestDirSourceDuplicateStemKeepFirst(t *testing.T) {
	first := DirSource{Dir: t.TempDir(), Label: "first"}
	second := DirSource{Dir: t.TempDir(), Label: "second"}
	writeRule(t, first.Dir, "dup.md", "---\npaths: [first]\n---\nfrom first")
	writeRule(t, second.Dir, "dup.md", "---\npaths: [second]\n---\nfrom second")

	discovered, skips, err := NewMultiSource(first, second).Rules(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(discovered) != 1 || discovered[0].Rule.Body != "from first" {
		t.Fatalf("earlier source must win on a name collision; discovered = %+v", discovered)
	}
	if len(skips) != 1 || !skips[0].Fatal || !strings.Contains(skips[0].Reason, "shadowed") {
		t.Fatalf("want one shadowed skip, got %+v", skips)
	}
}

// TestDirSourceOverCapBodyTruncated asserts a body over prompt.MaxRuleBytes is
// truncated rune-safely (non-fatal) and the rule is KEPT.
func TestDirSourceOverCapBodyTruncated(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("b", maxRuleBodyBytes+50)
	writeRule(t, dir, "big.md", long)

	discovered, skips, err := DirSource{Dir: dir}.Rules(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("an over-cap body must be KEPT (adjusted), got %+v", discovered)
	}
	if len(discovered[0].Rule.Body) > maxRuleBodyBytes {
		t.Fatalf("body not truncated: %d bytes", len(discovered[0].Rule.Body))
	}
	if len(skips) != 1 || skips[0].Fatal || !strings.Contains(skips[0].Reason, "truncated") {
		t.Fatalf("want one NON-fatal truncation note, got %+v", skips)
	}
}

// TestDirSourceMissingDirIsNoRules asserts an absent or empty Dir is "no
// rules", never an error.
func TestDirSourceMissingDirIsNoRules(t *testing.T) {
	discovered, skips, err := DirSource{Dir: filepath.Join(t.TempDir(), "nope")}.Rules(t.Context())
	if err != nil || len(discovered) != 0 || len(skips) != 0 {
		t.Fatalf("missing dir must be no-rules/no-error, got discovered=%v skips=%v err=%v", discovered, skips, err)
	}
	discovered, _, err = DirSource{Dir: ""}.Rules(t.Context())
	if err != nil || len(discovered) != 0 {
		t.Fatalf("empty dir must be no-rules, got %v err=%v", discovered, err)
	}
}

// TestDirSourceIgnoresNonMarkdown asserts only flat <name>.md files are
// considered — a subdir or a non-.md file is silently ignored.
func TestDirSourceIgnoresNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "a.md", "body A")
	writeRule(t, dir, "README.txt", "not a rule")
	if err := os.MkdirAll(filepath.Join(dir, "sub.md"), 0o755); err != nil { // a DIR named *.md
		t.Fatalf("mkdir sub.md: %v", err)
	}

	discovered, _, err := DirSource{Dir: dir}.Rules(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(discovered) != 1 || discovered[0].Rule.Name != "a" {
		t.Fatalf("want only a.md, got %+v", discovered)
	}
}

func TestRulePathsAcceptResolvedScalarsAsText(t *testing.T) {
	t.Parallel()

	for _, scalar := range []string{"true", "42", "2026-08-28T12:00:00Z", "null"} {
		t.Run(scalar, func(t *testing.T) {
			rule, reason, _ := parseRule([]byte("---\npaths: "+scalar+"\n---\nbody"), "compatibility")
			if reason != "" {
				t.Fatalf("parseRule(%q) reason = %q", scalar, reason)
			}
			if got := strings.Join(rule.Paths, ","); got != scalar {
				t.Fatalf("Paths = %q, want %q", got, scalar)
			}
		})
	}
}

func TestGoccyYAMLMigration_Scenario3_RuleFrontmatterCompatibility(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"---\npaths: \"**/*.go, **/*_test.go\"\nunknown-future-field: ignored\n---\nbody",
		"---\npaths: [\"**/*.go\", \"**/*_test.go\"]\nunknown-future-field: ignored\n---\nbody",
	} {
		rule, reason, _ := parseRule([]byte(raw), "compatibility")
		if reason != "" {
			t.Fatalf("parse rule frontmatter: %s", reason)
		}
		if got, want := strings.Join(rule.Paths, ","), "**/*.go,**/*_test.go"; got != want {
			t.Fatalf("paths = %q, want %q", got, want)
		}
	}

	dir := t.TempDir()
	writeRule(t, dir, "good.md", "---\npaths: [\"**/*.go\"]\n---\nbody")
	writeRule(t, dir, "bad.md", "---\npaths: [leaked-secret\n---\nbody")
	discovered, skips, err := DirSource{Dir: dir}.Rules(t.Context())
	if err != nil || len(discovered) != 1 || len(skips) != 1 {
		t.Fatalf("per-file failure isolation: discovered=%+v skips=%+v err=%v", discovered, skips, err)
	}
	if reason := skips[0].Reason; !strings.Contains(reason, "malformed YAML frontmatter at line") || strings.Contains(reason, "leaked-secret") {
		t.Fatalf("malformed frontmatter reason = %q, want safe location without YAML source", reason)
	}
}
