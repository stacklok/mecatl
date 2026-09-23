package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
)

func writeFile(t *testing.T, ws tool.Workspace, path, content string) {
	t.Helper()
	if _, err := ws.CreateFile(context.Background(), path, []byte(content)); err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
}

// TestNoopExpanderReturnsInputUnchanged verifies the default expander never
// rewrites the input and never reports an expansion.
func TestNoopExpanderReturnsInputUnchanged(t *testing.T) {
	for _, in := range []string{"hello world", "/review foo.go", ""} {
		out, ok, err := prompt.NoopExpander{}.Expand(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Errorf("NoopExpander reported expanded=true for %q", in)
		}
		if out != in {
			t.Errorf("NoopExpander changed %q -> %q", in, out)
		}
	}
}

// TestDirCommandExpanderSubstitutes verifies $ARGUMENTS and positional ($1, $2)
// substitution against a seeded command file.
func TestDirCommandExpanderSubstitutes(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	writeFile(t, ws, ".mecatl/commands/review.md",
		"Please review $1 and also $2.\nAll args: $ARGUMENTS")

	exp := prompt.NewDirCommandExpander(ws)
	out, ok, err := exp.Expand(context.Background(), "/review foo.go bar.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("want expanded=true")
	}
	if !strings.Contains(out, "Please review foo.go and also bar.go.") {
		t.Errorf("positional substitution wrong: %q", out)
	}
	if !strings.Contains(out, "All args: foo.go bar.go") {
		t.Errorf("$ARGUMENTS substitution wrong: %q", out)
	}
}

// TestDirCommandExpanderLeavesUnknownPlaceholders verifies unknown $-tokens and
// out-of-range positionals are handled: unknown left intact, out-of-range empty.
func TestDirCommandExpanderLeavesUnknownPlaceholders(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	writeFile(t, ws, ".mecatl/commands/c.md", "keep $HOME but drop [$3] and use $1")

	exp := prompt.NewDirCommandExpander(ws)
	out, ok, err := exp.Expand(context.Background(), "/c only")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("want expanded=true")
	}
	want := "keep $HOME but drop [] and use only"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

// TestDirCommandExpanderStripsFrontmatter verifies a leading YAML frontmatter
// block is removed and never reaches the expanded body.
func TestDirCommandExpanderStripsFrontmatter(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	writeFile(t, ws, ".mecatl/commands/review.md",
		"---\ndescription: Review a file\n---\nReview $1 now.")

	exp := prompt.NewDirCommandExpander(ws)
	out, ok, err := exp.Expand(context.Background(), "/review foo.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("want expanded=true")
	}
	if strings.Contains(out, "description") || strings.Contains(out, "---") {
		t.Errorf("frontmatter not stripped: %q", out)
	}
	if out != "Review foo.go now." {
		t.Errorf("got %q, want %q", out, "Review foo.go now.")
	}
}

// TestDirCommandExpanderClaudeDir verifies the .claude/commands/ fallback dir is
// also searched.
func TestDirCommandExpanderClaudeDir(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	writeFile(t, ws, ".claude/commands/hi.md", "Hello $ARGUMENTS")

	exp := prompt.NewDirCommandExpander(ws)
	out, ok, err := exp.Expand(context.Background(), "/hi there")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("want expanded=true")
	}
	if out != "Hello there" {
		t.Errorf("got %q, want %q", out, "Hello there")
	}
}

// TestDirCommandExpanderUnknownCommand verifies an unknown command is left
// unchanged with expanded=false and no error (does not abort the run).
func TestDirCommandExpanderUnknownCommand(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	writeFile(t, ws, ".mecatl/commands/review.md", "body")

	exp := prompt.NewDirCommandExpander(ws)
	out, ok, err := exp.Expand(context.Background(), "/nope foo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Errorf("unknown command reported expanded=true")
	}
	if out != "/nope foo" {
		t.Errorf("unknown command changed input: %q", out)
	}
}

// TestDirCommandExpanderNonCommand verifies plain (non-slash) input is left
// unchanged with expanded=false.
func TestDirCommandExpanderNonCommand(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	writeFile(t, ws, ".mecatl/commands/review.md", "body")

	exp := prompt.NewDirCommandExpander(ws)
	for _, in := range []string{"just chatting", "look at /etc/hosts", "/", "/ space"} {
		out, ok, err := exp.Expand(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", in, err)
		}
		if ok {
			t.Errorf("non-command %q reported expanded=true", in)
		}
		if out != in {
			t.Errorf("non-command %q changed to %q", in, out)
		}
	}
}

// Compile-time assertions that both types satisfy the interface.
var (
	_ prompt.CommandExpander = prompt.NoopExpander{}
	_ prompt.CommandExpander = (*prompt.DirCommandExpander)(nil)
)

// stubExpander is a controllable CommandExpander for MultiExpander tests. It
// expands inputs equal to match into out (reporting expanded=true); otherwise it
// passes the input through. err, when set, is returned for any input.
type stubExpander struct {
	match string
	out   string
	err   error
}

func (s stubExpander) Expand(_ context.Context, input string) (string, bool, error) {
	if s.err != nil {
		return input, false, s.err
	}
	if input == s.match {
		return s.out, true, nil
	}
	return input, false, nil
}

func TestMultiExpanderFirstWins(t *testing.T) {
	// Both match "/x"; the first (highest precedence) should win and shadow the
	// second.
	exp := prompt.NewMultiExpander(
		stubExpander{match: "/x", out: "FIRST"},
		stubExpander{match: "/x", out: "SECOND"},
	)
	out, ok, err := exp.Expand(context.Background(), "/x")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !ok || out != "FIRST" {
		t.Errorf("Expand = (%q, %v), want (FIRST, true)", out, ok)
	}
}

func TestMultiExpanderFallsThrough(t *testing.T) {
	// Only the second matches; the first passes through to it.
	exp := prompt.NewMultiExpander(
		stubExpander{match: "/a", out: "A"},
		stubExpander{match: "/b", out: "B"},
	)
	out, ok, err := exp.Expand(context.Background(), "/b")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !ok || out != "B" {
		t.Errorf("Expand = (%q, %v), want (B, true)", out, ok)
	}
}

func TestMultiExpanderNoneMatches(t *testing.T) {
	exp := prompt.NewMultiExpander(
		stubExpander{match: "/a", out: "A"},
		stubExpander{match: "/b", out: "B"},
	)
	out, ok, err := exp.Expand(context.Background(), "/z")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if ok {
		t.Errorf("expected no expansion for /z")
	}
	if out != "/z" {
		t.Errorf("input changed: %q", out)
	}
}

func TestMultiExpanderDirShadowsLater(t *testing.T) {
	// A file-backed DirCommandExpander command shadows a later expander that would
	// also claim the same name — the file command wins because it is listed first.
	ws := memfs.NewWorkspace("/proj")
	writeFile(t, ws, ".mecatl/commands/dup.md", "FROM FILE")

	exp := prompt.NewMultiExpander(
		prompt.NewDirCommandExpander(ws), // highest precedence
		stubExpander{match: "/dup", out: "FROM STUB"},
	)
	out, ok, err := exp.Expand(context.Background(), "/dup")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !ok || out != "FROM FILE" {
		t.Errorf("Expand = (%q, %v), want (FROM FILE, true) — file command must shadow the later expander", out, ok)
	}
}

func TestMultiExpanderErrorStopsChain(t *testing.T) {
	sentinel := errStub("boom")
	exp := prompt.NewMultiExpander(
		stubExpander{err: sentinel},
		stubExpander{match: "/x", out: "X"}, // never reached
	)
	_, ok, err := exp.Expand(context.Background(), "/x")
	if err == nil {
		t.Fatalf("expected the chain to surface the first expander's error")
	}
	if ok {
		t.Errorf("expected expanded=false on error")
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }

// listerStub is a stubExpander that ALSO implements CommandLister, so the
// MultiExpander aggregation tests can exercise a child that enumerates.
type listerStub struct {
	cmds []prompt.Command
	err  error
}

func (listerStub) Expand(_ context.Context, input string) (string, bool, error) {
	return input, false, nil
}

func (s listerStub) List(_ context.Context) ([]prompt.Command, error) {
	return s.cmds, s.err
}

// TestNoopExpanderListsNothing verifies the default expander enumerates no
// commands.
func TestNoopExpanderListsNothing(t *testing.T) {
	got, err := prompt.NoopExpander{}.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("NoopExpander.List = %v, want empty", got)
	}
}

// TestDirCommandExpanderListDescriptions verifies List discovers names and
// derives descriptions from both a frontmatter description: field and the
// first-non-blank-line fallback, sorted by name.
func TestDirCommandExpanderListDescriptions(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	writeFile(t, ws, ".mecatl/commands/review.md",
		"---\ndescription: Review a pull request\nmodel: opus\n---\nPlease review $ARGUMENTS")
	writeFile(t, ws, ".mecatl/commands/fix.md",
		"Fix the failing test in $1\nmore body")

	cmds, err := prompt.NewDirCommandExpander(ws).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("List returned %d commands, want 2: %+v", len(cmds), cmds)
	}
	// Sorted by name: fix, review.
	if cmds[0].Name != "fix" || cmds[0].Description != "Fix the failing test in $1" {
		t.Errorf("cmds[0] = %+v, want {fix, first-line fallback}", cmds[0])
	}
	if cmds[1].Name != "review" || cmds[1].Description != "Review a pull request" {
		t.Errorf("cmds[1] = %+v, want {review, frontmatter description}", cmds[1])
	}
}

// TestDirCommandExpanderListDedupesAcrossDirs verifies a name present in the
// first dir shadows the same name in a later dir (matching Expand precedence).
func TestDirCommandExpanderListDedupesAcrossDirs(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	writeFile(t, ws, ".mecatl/commands/dup.md", "from mecatl")
	writeFile(t, ws, ".claude/commands/dup.md", "from claude")
	writeFile(t, ws, ".claude/commands/only.md", "claude-only command")

	cmds, err := prompt.NewDirCommandExpander(ws).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("List returned %d commands, want 2 (dup de-duped + only): %+v", len(cmds), cmds)
	}
	byName := map[string]prompt.Command{}
	for _, c := range cmds {
		byName[c.Name] = c
	}
	if got := byName["dup"].Description; got != "from mecatl" {
		t.Errorf("dup description = %q, want \"from mecatl\" (.mecatl dir wins)", got)
	}
	if _, ok := byName["only"]; !ok {
		t.Errorf("expected claude-only command to be listed")
	}
}

// TestDirCommandExpanderListEmptyWhenNoDirs verifies a workspace with no command
// files yields no commands and no error.
func TestDirCommandExpanderListEmptyWhenNoDirs(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	cmds, err := prompt.NewDirCommandExpander(ws).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cmds) != 0 {
		t.Errorf("List = %+v, want empty", cmds)
	}
}

// TestMultiExpanderListAggregatesAndDedupes verifies MultiExpander.List merges
// its children's lists, applies first-wins de-dup, sorts, and skips children
// that do not implement CommandLister.
func TestMultiExpanderListAggregatesAndDedupes(t *testing.T) {
	exp := prompt.NewMultiExpander(
		listerStub{cmds: []prompt.Command{{Name: "b", Description: "first b"}, {Name: "a", Description: "a"}}},
		stubExpander{match: "/x"}, // not a lister; contributes nothing
		listerStub{cmds: []prompt.Command{{Name: "b", Description: "second b (shadowed)"}, {Name: "c", Description: "c"}}},
	)
	cmds, err := exp.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cmds) != 3 {
		t.Fatalf("List returned %d, want 3 (a,b,c): %+v", len(cmds), cmds)
	}
	if cmds[0].Name != "a" || cmds[1].Name != "b" || cmds[2].Name != "c" {
		t.Errorf("not sorted by name: %+v", cmds)
	}
	if cmds[1].Description != "first b" {
		t.Errorf("b description = %q, want \"first b\" (earlier child wins)", cmds[1].Description)
	}
}

// TestMultiExpanderListErrorStops verifies a child List error stops aggregation.
func TestMultiExpanderListErrorStops(t *testing.T) {
	exp := prompt.NewMultiExpander(listerStub{err: errStub("boom")})
	if _, err := exp.List(context.Background()); err == nil {
		t.Fatalf("expected the child List error to surface")
	}
}

var _ prompt.CommandExpander = (*prompt.MultiExpander)(nil)
