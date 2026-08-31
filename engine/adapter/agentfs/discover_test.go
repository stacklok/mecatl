package agentfs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
)

// writeDef writes a <name>.md file into dir, creating dir if needed.
func writeDef(t *testing.T, dir, file, content string) string {
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

func TestParseAgentDefValid(t *testing.T) {
	raw := []byte(`---
name: code-reviewer
description: Reviews a diff for correctness bugs.
tools: [Read, Grep, Glob]
disallowedTools: [Write]
model: sonnet
permissionMode: plan
maxTurns: 9
maxToolCalls: 25
color: blue
---
You are a meticulous code reviewer.
Inspect the change and report findings.`)

	def, perr, notes := parseAgentDef(raw, "code-reviewer.md")
	if perr != "" {
		t.Fatalf("unexpected parse error: %s", perr)
	}
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	if def.Name != "code-reviewer" || def.Description != "Reviews a diff for correctness bugs." {
		t.Fatalf("name/desc mismatch: %+v", def)
	}
	if got, want := strings.Join(def.Tools, ","), "Read,Grep,Glob"; got != want {
		t.Fatalf("tools = %q, want %q", got, want)
	}
	if got, want := strings.Join(def.DisallowedTools, ","), "Write"; got != want {
		t.Fatalf("disallowedTools = %q, want %q", got, want)
	}
	if def.Model != "sonnet" || def.PermissionMode != "plan" || def.MaxTurns != 9 || def.MaxToolCalls != 25 || def.Color != "blue" {
		t.Fatalf("optional fields mismatch: %+v", def)
	}
	if !strings.HasPrefix(def.Body, "You are a meticulous code reviewer.") {
		t.Fatalf("body mismatch: %q", def.Body)
	}
}

func TestGoccyYAMLMigration_SemanticMatrixAgentFrontmatter(t *testing.T) {
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
		outcome, ok := tc.Readers["agentfs"]
		if !ok {
			continue
		}
		seen = true
		if tc.Category != "frontmatter" {
			t.Fatalf("fixture %q has category %q, want frontmatter", tc.Name, tc.Category)
		}
		t.Run(tc.Name, func(t *testing.T) {
			_, parseError, _ := parseAgentDef([]byte(tc.Document), "semantic-matrix.md")
			if accepted, want := parseError == "", outcome == "accept"; accepted != want {
				t.Fatalf("parseAgentDef() accepted=%v, want %v (error=%q)", accepted, want, parseError)
			}
		})
	}
	if !seen {
		t.Fatal("semantic matrix declares no agentfs frontmatter case")
	}
}

// TestParseAgentDefProvider asserts the per-sub-agent-provider frontmatter field:
// a `provider:` value parses into AgentDef.Provider (trimmed), and its ABSENCE
// leaves Provider == "" (inherit). It is pure data — orthogonal to model.
func TestParseAgentDefProvider(t *testing.T) {
	withProvider := []byte(`---
name: x
description: d
provider:  openrouter
model: anthropic/claude-sonnet-4.5
---
body`)
	def, perr, _ := parseAgentDef(withProvider, "x.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if def.Provider != "openrouter" {
		t.Fatalf("provider = %q, want %q (trimmed)", def.Provider, "openrouter")
	}
	if def.Model != "anthropic/claude-sonnet-4.5" {
		t.Fatalf("model = %q, want the verbatim model id (orthogonal to provider)", def.Model)
	}

	noProvider := []byte(`---
name: y
description: d
model: sonnet
---
body`)
	def2, perr2, _ := parseAgentDef(noProvider, "y.md")
	if perr2 != "" {
		t.Fatalf("parse error: %s", perr2)
	}
	if def2.Provider != "" {
		t.Fatalf("absent provider = %q, want \"\" (inherit)", def2.Provider)
	}
}

// TestParseAgentDefToolsStringForm asserts Claude-Code compatibility: a `tools`
// scalar string ("Read, Edit Grep") parses identically to the array form.
func TestParseAgentDefToolsStringForm(t *testing.T) {
	raw := []byte(`---
name: x
description: d
tools: Read, Edit Grep
disallowedTools: Write
---
body`)
	def, perr, _ := parseAgentDef(raw, "x.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if got, want := strings.Join(def.Tools, ","), "Read,Edit,Grep"; got != want {
		t.Fatalf("tools = %q, want %q", got, want)
	}
	if got, want := strings.Join(def.DisallowedTools, ","), "Write"; got != want {
		t.Fatalf("disallowedTools = %q, want %q", got, want)
	}
}

// TestParseAgentDefSkillsMCPHooks asserts the Claude-Code-parity frontmatter
// fields parse: skills/mcpServers accept both array and scalar forms (like tools),
// and hooks parses as a phase→command map with empty entries dropped.
func TestParseAgentDefSkillsMCPHooks(t *testing.T) {
	raw := []byte(`---
name: specialist
description: a specialist
skills: refactoring, testing
mcpServers: [github, jira]
hooks:
  PreToolUse: "echo pre"
  PostToolUse: "  "
  "": "echo orphan"
---
body`)
	def, perr, _ := parseAgentDef(raw, "s.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if got, want := strings.Join(def.Skills, ","), "refactoring,testing"; got != want {
		t.Fatalf("skills = %q, want %q", got, want)
	}
	if got, want := strings.Join(mcpServerNames(def.MCPServers), ","), "github,jira"; got != want {
		t.Fatalf("mcpServers = %q, want %q", got, want)
	}
	for _, s := range def.MCPServers {
		if !s.IsReference() {
			t.Fatalf("scalar mcpServers entry %q should be a reference, got url=%q", s.Name, s.URL)
		}
	}
	// Empty-command and empty-key hook entries are dropped; only PreToolUse survives.
	if len(def.Hooks) != 1 || def.Hooks["PreToolUse"] != "echo pre" {
		t.Fatalf("hooks = %+v, want only PreToolUse=echo pre", def.Hooks)
	}
}

// TestParseAgentDefNoExtraFieldsNil asserts a def without the new fields carries
// nil slices/map (not empty non-nil), so the composition layer can treat absence
// uniformly.
func TestParseAgentDefNoExtraFieldsNil(t *testing.T) {
	def, perr, _ := parseAgentDef([]byte("---\nname: n\ndescription: d\n---\nbody"), "n.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if def.Skills != nil || def.MCPServers != nil || def.Hooks != nil {
		t.Fatalf("absent fields should be nil: skills=%v mcp=%v hooks=%v", def.Skills, def.MCPServers, def.Hooks)
	}
}

func TestParseAgentDefRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"no frontmatter", "just a body, no delimiters", "missing YAML frontmatter"},
		{"missing name", "---\ndescription: d\n---\nbody", "missing a non-empty \"name\""},
		{"missing description", "---\nname: n\n---\nbody", "missing a non-empty \"description\""},
		{"blank name", "---\nname: \"  \"\ndescription: d\n---\nbody", "missing a non-empty \"name\""},
		{"malformed yaml", "---\nname: [unterminated\n---\nbody", "malformed YAML frontmatter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, perr, _ := parseAgentDef([]byte(tc.raw), "f.md")
			if perr == "" || !strings.Contains(perr, tc.want) {
				t.Fatalf("err = %q, want containing %q", perr, tc.want)
			}
		})
	}
}

func TestParseAgentDefTruncatesDescriptionAndBody(t *testing.T) {
	longDesc := strings.Repeat("d", maxDescriptionBytes+50)
	longBody := strings.Repeat("b", maxPromptBodyBytes+50)
	raw := []byte("---\nname: n\ndescription: " + longDesc + "\n---\n" + longBody)
	def, perr, notes := parseAgentDef(raw, "n.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if len(def.Description) > maxDescriptionBytes {
		t.Fatalf("description not truncated: %d bytes", len(def.Description))
	}
	if len(def.Body) > maxPromptBodyBytes {
		t.Fatalf("body not truncated: %d bytes", len(def.Body))
	}
	if len(notes) != 2 {
		t.Fatalf("want 2 truncation notes, got %d: %v", len(notes), notes)
	}
	// Pin that the two notes are the DESCRIPTION-cap note and the BODY-cap note
	// (they differ: "always-in-context cap" vs "prompt-body cap"), so a
	// regression that emits the same note twice or drops one field's truncation
	// is caught — not just the count.
	var sawDescNote, sawBodyNote bool
	for _, n := range notes {
		switch {
		case strings.Contains(n, "always-in-context cap"):
			sawDescNote = true
		case strings.Contains(n, "prompt-body cap"):
			sawBodyNote = true
		}
	}
	if !sawDescNote {
		t.Fatalf("missing the description-cap note (\"always-in-context cap\"); notes = %v", notes)
	}
	if !sawBodyNote {
		t.Fatalf("missing the body-cap note (\"prompt-body cap\"); notes = %v", notes)
	}
}

// TestParseAgentDefBodyBoundary proves the body cap is exact: a body at exactly
// MaxAgentBodyBytes is kept untouched with no note; one byte over is truncated
// with a single note that names the byte cap.
func TestParseAgentDefBodyBoundary(t *testing.T) {
	atCap := strings.Repeat("b", maxPromptBodyBytes)
	raw := []byte("---\nname: n\ndescription: d\n---\n" + atCap)
	def, perr, notes := parseAgentDef(raw, "n.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if len(def.Body) != maxPromptBodyBytes {
		t.Fatalf("body at cap should be untouched: got %d bytes, want %d", len(def.Body), maxPromptBodyBytes)
	}
	if len(notes) != 0 {
		t.Fatalf("body at cap should produce no note, got %v", notes)
	}

	over := strings.Repeat("b", maxPromptBodyBytes+1)
	raw = []byte("---\nname: n\ndescription: d\n---\n" + over)
	def, perr, notes = parseAgentDef(raw, "n.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if len(def.Body) > maxPromptBodyBytes {
		t.Fatalf("body over cap not truncated: %d bytes", len(def.Body))
	}
	if len(notes) != 1 {
		t.Fatalf("body over cap should produce exactly one note, got %d: %v", len(notes), notes)
	}
	if want := strconv.Itoa(maxPromptBodyBytes); !strings.Contains(notes[0], want) {
		t.Fatalf("note should mention the cap %s bytes, got %q", want, notes[0])
	}
}

// TestParseAgentDefDescriptionBoundary proves the description cap is exact: a
// description at exactly MaxAgentDescriptionBytes is kept untouched with no
// note; one byte over is truncated with a single note that names the byte cap.
func TestParseAgentDefDescriptionBoundary(t *testing.T) {
	atCap := strings.Repeat("d", maxDescriptionBytes)
	raw := []byte("---\nname: n\ndescription: " + atCap + "\n---\nbody")
	def, perr, notes := parseAgentDef(raw, "n.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if len(def.Description) != maxDescriptionBytes {
		t.Fatalf("description at cap should be untouched: got %d bytes, want %d", len(def.Description), maxDescriptionBytes)
	}
	if len(notes) != 0 {
		t.Fatalf("description at cap should produce no note, got %v", notes)
	}

	over := strings.Repeat("d", maxDescriptionBytes+1)
	raw = []byte("---\nname: n\ndescription: " + over + "\n---\nbody")
	def, perr, notes = parseAgentDef(raw, "n.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if len(def.Description) > maxDescriptionBytes {
		t.Fatalf("description over cap not truncated: %d bytes", len(def.Description))
	}
	if len(notes) != 1 {
		t.Fatalf("description over cap should produce exactly one note, got %d: %v", len(notes), notes)
	}
	if want := strconv.Itoa(maxDescriptionBytes); !strings.Contains(notes[0], want) {
		t.Fatalf("note should mention the cap %s bytes, got %q", want, notes[0])
	}
}

// TestDirSourceSkipErrorFatalClassification is the structural oracle for the
// "dropped" vs "adjusted" log split (issue #156 Part B): a kept-but-truncated
// def yields a non-fatal SkipError (Fatal==false), while a def that cannot be
// admitted at all (malformed YAML, missing name, missing description, duplicate
// name) yields a fatal one (Fatal==true).
func TestDirSourceSkipErrorFatalClassification(t *testing.T) {
	dir := t.TempDir()
	longBody := strings.Repeat("b", maxPromptBodyBytes+1)
	writeDef(t, dir, "kept.md", "---\nname: kept\ndescription: d\n---\n"+longBody) // kept, adjusted
	writeDef(t, dir, "malformed.md", "---\nname: [unterminated\n---\nbody")        // dropped: parse
	writeDef(t, dir, "noname.md", "---\ndescription: d\n---\nbody")                // dropped: missing name
	writeDef(t, dir, "nodesc.md", "---\nname: nodesc\n---\nbody")                  // dropped: missing desc
	writeDef(t, dir, "dup-a.md", "---\nname: dup\ndescription: first\n---\nbody")  // kept (first)
	writeDef(t, dir, "dup-b.md", "---\nname: dup\ndescription: second\n---\nbody") // dropped: duplicate
	// cannot-read fatal site: a DANGLING symlink named like a def. os.ReadDir
	// reports it as a non-dir entry (so it passes the IsDir guard), but
	// os.ReadFile follows it to a nonexistent target and errors — far more
	// robust under CI/root than a mode-0000 file (root reads through 0000).
	if err := os.Symlink(filepath.Join(dir, "does-not-exist"), filepath.Join(dir, "dangling.md")); err != nil {
		t.Fatalf("symlink dangling.md: %v", err)
	}

	_, skips, err := DirSource{Dir: dir}.Agents(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The CLOSED set of fatal causes wired in discover.go/source.go. Asserting
	// every fatal reason matches one of these is the structural guard the panel
	// asked for: a NEW fatal site that forgets Fatal:true would land in the
	// non-fatal arm (failing the "unexpected non-fatal skip" check below), and a
	// fatal site with an unrecognised reason fails here.
	fatalReasonFragments := []string{"cannot read", "malformed YAML", "missing a non-empty", "duplicate agent name"}
	matchesAFatalCause := func(reason string) bool {
		for _, frag := range fatalReasonFragments {
			if strings.Contains(reason, frag) {
				return true
			}
		}
		return false
	}

	var sawAdjusted, sawFatal int
	sawCannotRead := false
	for _, s := range skips {
		if s.Fatal {
			sawFatal++
			if !matchesAFatalCause(s.Reason) {
				t.Fatalf("fatal skip has an unrecognised reason (a new fatal site?): %+v", s)
			}
			if strings.Contains(s.Reason, "cannot read") {
				sawCannotRead = true
			}
			continue
		}
		sawAdjusted++
		// The only non-fatal skip here is the truncation of kept.md.
		if !strings.Contains(s.Reason, "truncated") {
			t.Fatalf("unexpected non-fatal skip (a fatal site missing Fatal:true?): %+v", s)
		}
	}
	if sawAdjusted != 1 {
		t.Fatalf("want exactly 1 non-fatal (adjusted) skip, got %d: %+v", sawAdjusted, skips)
	}
	// cannot-read + malformed + noname + nodesc + duplicate = 5 fatal drops.
	if sawFatal != 5 {
		t.Fatalf("want 5 fatal (dropped) skips, got %d: %+v", sawFatal, skips)
	}
	if !sawCannotRead {
		t.Fatalf("missing the cannot-read fatal skip; skips = %+v", skips)
	}
}

// TestParseAgentDefTruncatesOnRuneBoundary locks the rune-safe truncation
// contract: a multi-byte rune straddling the cap is NOT split mid-rune (no
// mojibake), and the result stays within the byte cap. TruncateRunes backs
// both the description and body truncation.
func TestParseAgentDefTruncatesOnRuneBoundary(t *testing.T) {
	// "世" is 3 bytes (U+4E16). Build a body that overshoots the cap by an amount
	// that is NOT a multiple of 3, so the byte cap lands mid-rune and the
	// rune-boundary back-off must engage.
	body := strings.Repeat("世", maxPromptBodyBytes) // far over the byte cap
	raw := []byte("---\nname: n\ndescription: d\n---\n" + body)
	def, perr, notes := parseAgentDef(raw, "n.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if len(def.Body) > maxPromptBodyBytes {
		t.Fatalf("truncated body exceeds the byte cap: %d > %d", len(def.Body), maxPromptBodyBytes)
	}
	if !utf8.ValidString(def.Body) {
		t.Fatalf("truncated body is not valid UTF-8 (mid-rune split / mojibake): %q", def.Body)
	}
	if len(notes) != 1 {
		t.Fatalf("want 1 truncation note, got %d: %v", len(notes), notes)
	}

	// Same for the description.
	desc := strings.Repeat("世", maxDescriptionBytes)
	raw = []byte("---\nname: n\ndescription: " + desc + "\n---\nbody")
	def, perr, notes = parseAgentDef(raw, "n.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if len(def.Description) > maxDescriptionBytes {
		t.Fatalf("truncated description exceeds the byte cap: %d > %d", len(def.Description), maxDescriptionBytes)
	}
	if !utf8.ValidString(def.Description) {
		t.Fatalf("truncated description is not valid UTF-8 (mid-rune split): %q", def.Description)
	}
	if len(notes) != 1 {
		t.Fatalf("want 1 truncation note, got %d: %v", len(notes), notes)
	}
}

// TestParseAgentDefUnknownKeysIgnored proves forward-compat: an unknown
// frontmatter key does not fail the parse.
func TestParseAgentDefUnknownKeysIgnored(t *testing.T) {
	raw := []byte("---\nname: n\ndescription: d\nfutureField: whatever\n---\nbody")
	_, perr, _ := parseAgentDef(raw, "n.md")
	if perr != "" {
		t.Fatalf("unknown key should be ignored, got %q", perr)
	}
}

func TestDirSourceDiscovery(t *testing.T) {
	dir := t.TempDir()
	writeDef(t, dir, "alpha.md", "---\nname: alpha\ndescription: A\n---\nbody A")
	writeDef(t, dir, "beta.md", "---\nname: beta\ndescription: B\n---\nbody B")
	writeDef(t, dir, "broken.md", "no frontmatter here")
	writeDef(t, dir, "README.txt", "not a def") // wrong extension, ignored

	defs, skips, err := DirSource{Dir: dir}.Agents(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("want 2 defs, got %d: %+v", len(defs), defs)
	}
	if defs[0].Def.Name != "alpha" || defs[1].Def.Name != "beta" {
		t.Fatalf("defs not sorted by name: %+v", defs)
	}
	// Each Discovered entry carries the adapter-private locator (the bare path
	// for an unlabelled source) — the def value object itself carries none.
	if !strings.Contains(defs[0].Detail, "alpha.md") {
		t.Fatalf("Detail should carry the source path, got %q", defs[0].Detail)
	}
	// broken.md yields exactly one skip; README.txt is silently ignored.
	if len(skips) != 1 || !strings.Contains(skips[0].Path, "broken.md") {
		t.Fatalf("want 1 skip for broken.md, got %v", skips)
	}
}

func TestDirSourceMissingDirIsOptIn(t *testing.T) {
	defs, skips, err := DirSource{Dir: filepath.Join(t.TempDir(), "nope")}.Agents(t.Context())
	if err != nil || len(defs) != 0 || len(skips) != 0 {
		t.Fatalf("missing dir must be no-defs/no-error, got defs=%v skips=%v err=%v", defs, skips, err)
	}
	// Empty Dir is also opt-in.
	defs, _, err = DirSource{Dir: ""}.Agents(t.Context())
	if err != nil || len(defs) != 0 {
		t.Fatalf("empty dir must be no-defs, got %v err=%v", defs, err)
	}
}

// TestDirSourceDuplicateNameWithinDir asserts keep-first dedup by frontmatter
// name within one directory (the filename does not decide identity).
func TestDirSourceDuplicateNameWithinDir(t *testing.T) {
	dir := t.TempDir()
	// Both files declare name "dup"; sorted path order keeps a.md.
	writeDef(t, dir, "a.md", "---\nname: dup\ndescription: from a\n---\nbody")
	writeDef(t, dir, "b.md", "---\nname: dup\ndescription: from b\n---\nbody")

	defs, skips, err := DirSource{Dir: dir}.Agents(t.Context())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(defs) != 1 || defs[0].Def.Description != "from a" {
		t.Fatalf("want keep-first a.md, got %+v", defs)
	}
	if len(skips) != 1 || !strings.Contains(skips[0].Reason, "duplicate") {
		t.Fatalf("want duplicate skip, got %v", skips)
	}
}

// mcpServerNames projects an mcpServers list to its names, for assertions.
func mcpServerNames(in []AgentMCPServer) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, s.Name)
	}
	return out
}

// TestParseAgentDefMCPInlineAndReference asserts the extended mcpServers parsing:
// a YAML array MIXING a bare scalar (reference) with mappings (inline HTTP server,
// with and without headers), and that a nameless mapping and a stdio/non-HTTP entry
// are SKIPPED with diagnostics while the def is still kept.
func TestParseAgentDefMCPInlineAndReference(t *testing.T) {
	raw := []byte(`---
name: ops
description: an operator
mcpServers:
  - github
  - name: inline-http
    url: https://example.test/mcp
    headers:
      Authorization: Bearer tok
  - name: inline-bare
  - url: https://nameless.test/mcp
  - name: stdio-bad
    command: ./some-server
  - name: typed-bad
    type: stdio
---
body`)
	def, perr, notes := parseAgentDef(raw, "ops.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}

	byName := map[string]AgentMCPServer{}
	for _, s := range def.MCPServers {
		byName[s.Name] = s
	}

	// github = reference (no url).
	if ref, ok := byName["github"]; !ok || !ref.IsReference() {
		t.Fatalf("github should be a reference entry, got %+v (ok=%v)", ref, ok)
	}
	// inline-http = inline with url + header.
	in, ok := byName["inline-http"]
	if !ok || in.IsReference() || in.URL != "https://example.test/mcp" {
		t.Fatalf("inline-http should be inline with its url, got %+v (ok=%v)", in, ok)
	}
	if in.Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("inline-http headers = %v, want Authorization header", in.Headers)
	}
	// inline-bare = mapping with only a name => reference.
	if b, ok := byName["inline-bare"]; !ok || !b.IsReference() {
		t.Fatalf("inline-bare (name only) should collapse to a reference, got %+v (ok=%v)", b, ok)
	}

	// The nameless, stdio-command, and typed-stdio entries must be skipped.
	for _, bad := range []string{"stdio-bad", "typed-bad"} {
		if _, ok := byName[bad]; ok {
			t.Fatalf("entry %q should have been skipped (non-HTTP transport)", bad)
		}
	}
	if len(def.MCPServers) != 3 {
		t.Fatalf("want 3 usable mcpServers, got %d: %v", len(def.MCPServers), mcpServerNames(def.MCPServers))
	}

	// Diagnostics: one for the nameless entry, one per rejected stdio entry (3 total).
	var mcpNotes int
	for _, n := range notes {
		if strings.Contains(n, "mcpServers") {
			mcpNotes++
		}
	}
	if mcpNotes != 3 {
		t.Fatalf("want 3 mcpServers skip notes (nameless + stdio command + stdio type), got %d: %v", mcpNotes, notes)
	}
}

// TestParseMemoryFieldValidTiers asserts the two accepted persistent-memory tiers
// ("user"/"project", case-insensitively) parse onto AgentDef.Memory with no note.
func TestParseMemoryFieldValidTiers(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"user", "user"},
		{"project", "project"},
		{"  User  ", "user"},
		{"PROJECT", "project"},
	} {
		raw := []byte("---\nname: spec\ndescription: d\nmemory: " + tc.in + "\n---\nbody")
		def, perr, notes := parseAgentDef(raw, "spec.md")
		if perr != "" {
			t.Fatalf("memory:%q parse error: %s", tc.in, perr)
		}
		if def.Memory != tc.want {
			t.Fatalf("memory:%q => Memory=%q, want %q", tc.in, def.Memory, tc.want)
		}
		for _, n := range notes {
			if strings.Contains(n, "memory:") {
				t.Fatalf("memory:%q should produce no memory note, got %q", tc.in, n)
			}
		}
	}
}

// TestParseMemoryFieldRejectsUnknown asserts an unsupported tier (the deliberately
// deferred "local", plus any bogus value) is non-fatal: the def is KEPT, Memory is
// reset to "" (cold start), and a non-fatal note names the offending value.
func TestParseMemoryFieldRejectsUnknown(t *testing.T) {
	for _, bad := range []string{"local", "shared", "bogus"} {
		raw := []byte("---\nname: spec\ndescription: d\nmemory: " + bad + "\n---\nbody")
		def, perr, notes := parseAgentDef(raw, "spec.md")
		if perr != "" {
			t.Fatalf("memory:%q must be NON-fatal (def kept), got fatal: %s", bad, perr)
		}
		if def.Memory != "" {
			t.Fatalf("memory:%q => Memory=%q, want \"\" (cold start)", bad, def.Memory)
		}
		var found bool
		for _, n := range notes {
			if strings.Contains(n, "memory:") && strings.Contains(n, bad) {
				found = true
			}
		}
		if !found {
			t.Fatalf("memory:%q should produce a non-fatal note naming it, got %v", bad, notes)
		}
	}
}

// TestParseMemoryFieldAbsentIsUnset asserts a def with no memory: field carries the
// empty tier (no read, no prompt delta — byte-identical to today's behaviour).
func TestParseMemoryFieldAbsentIsUnset(t *testing.T) {
	def, perr, notes := parseAgentDef([]byte("---\nname: n\ndescription: d\n---\nbody"), "n.md")
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	if def.Memory != "" {
		t.Fatalf("absent memory: => Memory=%q, want \"\"", def.Memory)
	}
	for _, n := range notes {
		if strings.Contains(n, "memory:") {
			t.Fatalf("absent memory: should produce no memory note, got %q", n)
		}
	}
}

func TestStringOrSliceAcceptsResolvedScalarsAsText(t *testing.T) {
	t.Parallel()

	for _, scalar := range []string{"true", "42", "2026-08-28T12:00:00Z", "null"} {
		t.Run(scalar, func(t *testing.T) {
			def, reason, _ := parseAgentDef([]byte("---\nname: n\ndescription: d\ntools: "+scalar+"\n---\nbody"), "n.md")
			if reason != "" {
				t.Fatalf("parseAgentDef(%q) reason = %q", scalar, reason)
			}
			if got := strings.Join(def.Tools, ","); got != scalar {
				t.Fatalf("Tools = %q, want %q", got, scalar)
			}
		})
	}
}

func TestGoccyYAMLMigration_Scenario3_AgentFrontmatterCompatibility(t *testing.T) {
	t.Parallel()

	def, reason, _ := parseAgentDef([]byte(`---
name: reviewer
description: reviews changes
tools: Read, Grep
mcpServers:
  - github
  - name: issue-tracker
    url: https://mcp.example.test
    headers:
      Authorization: Bearer token
unknown-future-field: ignored
---
review the change`), "reviewer.md")
	if reason != "" {
		t.Fatalf("parse agent frontmatter: %s", reason)
	}
	if got, want := strings.Join(def.Tools, ","), "Read,Grep"; got != want {
		t.Fatalf("tools = %q, want %q", got, want)
	}
	if len(def.MCPServers) != 2 || !def.MCPServers[0].IsReference() || def.MCPServers[1].URL != "https://mcp.example.test" {
		t.Fatalf("mcpServers = %+v, want reference and inline HTTP server", def.MCPServers)
	}

	_, reason, _ = parseAgentDef([]byte(`---
name: invalid-header
description: invalid header type
mcpServers:
  - name: remote
    url: https://mcp.example.test
    headers:
      Authorization: [super-secret-token]
---
body`), "invalid-header.md")
	if !strings.Contains(reason, "malformed YAML frontmatter") || strings.Contains(reason, "super-secret-token") {
		t.Fatalf("invalid header reason = %q, want a value-free malformed-frontmatter diagnostic", reason)
	}

	_, reason, _ = parseAgentDef([]byte("---\nname: leaked-secret\ndescription: [unterminated\n---\nbody"), "bad.md")
	if !strings.Contains(reason, "malformed YAML frontmatter at line") {
		t.Fatalf("malformed frontmatter reason = %q, want safe location", reason)
	}
	if strings.Contains(reason, "leaked-secret") || strings.Contains(reason, "unterminated") {
		t.Fatalf("malformed frontmatter reason leaked YAML source: %q", reason)
	}
}
