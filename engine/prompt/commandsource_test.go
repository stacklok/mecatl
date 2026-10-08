package prompt_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/prompt"
)

// mapCommandSource is an in-memory prompt.CommandSource over a fixed
// name → raw-template map, for the byte-parity tests.
type mapCommandSource struct {
	bodies map[string]string
	list   []prompt.Command
	err    error
}

func (m mapCommandSource) ListCommands(context.Context) ([]prompt.Command, error) {
	return m.list, m.err
}

func (m mapCommandSource) CommandBody(_ context.Context, name string) (string, bool, error) {
	if m.err != nil {
		return "", false, m.err
	}
	body, ok := m.bodies[name]
	return body, ok, nil
}

// TestSourceExpanderByteParityWithDirExpander pins the core contract: for the
// IDENTICAL raw template (frontmatter included) and the identical invocation,
// the SourceExpander's output is byte-for-byte the DirCommandExpander's — the
// shared parseCommand/stripFrontmatter/substitute path guarantees a template
// expands the same whichever backend serves it.
func TestSourceExpanderByteParityWithDirExpander(t *testing.T) {
	templates := map[string]string{
		// Frontmatter + $ARGUMENTS + positional refs (incl. out-of-range $3).
		"review": "---\ndescription: review a change\n---\nReview $1 then $2 (extra: $3).\nAll: $ARGUMENTS\nLiteral $HOME stays.",
		// No frontmatter, positional only.
		"fix": "Fix the bug in $1.",
		// Frontmatter with no closing delimiter (NOT valid frontmatter — kept).
		"odd": "---\nnot closed\nBody with $ARGUMENTS",
	}
	invocations := []string{
		"/review foo.go bar.go",
		"  /review one",
		"/fix pkg/thing.go",
		"/odd a b c",
		"/review",
	}

	ws := memfs.NewWorkspace("/proj")
	for name, body := range templates {
		writeFile(t, ws, ".mecatl/commands/"+name+".md", body)
	}
	dirExp := prompt.NewDirCommandExpander(ws)
	srcExp := prompt.NewSourceExpander(mapCommandSource{bodies: templates})

	for _, in := range invocations {
		fromDir, dirOK, dirErr := dirExp.Expand(context.Background(), in)
		fromSrc, srcOK, srcErr := srcExp.Expand(context.Background(), in)
		if dirErr != nil || srcErr != nil {
			t.Fatalf("Expand(%q) errs: dir=%v src=%v", in, dirErr, srcErr)
		}
		if dirOK != srcOK {
			t.Fatalf("Expand(%q) expanded: dir=%v src=%v", in, dirOK, srcOK)
		}
		if fromDir != fromSrc {
			t.Errorf("Expand(%q) diverged:\n dir: %q\n src: %q", in, fromDir, fromSrc)
		}
	}
}

// TestSourceExpanderPassThrough pins the two normal non-expansion outcomes:
// a non-command input and an unknown command name both return the input
// unchanged with expanded=false and NO error.
func TestSourceExpanderPassThrough(t *testing.T) {
	exp := prompt.NewSourceExpander(mapCommandSource{bodies: map[string]string{"known": "body"}})
	for _, in := range []string{"plain text", "/unknown-cmd args", "/", ""} {
		out, ok, err := exp.Expand(context.Background(), in)
		if err != nil {
			t.Fatalf("Expand(%q) err = %v, want nil", in, err)
		}
		if ok || out != in {
			t.Errorf("Expand(%q) = (%q, %v), want pass-through", in, out, ok)
		}
	}
}

// TestSourceExpanderBackendFault pins the error contract: a genuine backend
// fault on a COMMAND-shaped input surfaces as (input, false, err).
func TestSourceExpanderBackendFault(t *testing.T) {
	boom := errors.New("backend down")
	exp := prompt.NewSourceExpander(mapCommandSource{err: boom})
	out, ok, err := exp.Expand(context.Background(), "/cmd x")
	if !errors.Is(err, boom) {
		t.Fatalf("Expand err = %v, want the backend fault", err)
	}
	if ok || out != "/cmd x" {
		t.Errorf("Expand on fault = (%q, %v), want the unchanged input", out, ok)
	}
	// A NON-command input never consults the backend, so no error either.
	if _, ok, err := exp.Expand(context.Background(), "plain"); err != nil || ok {
		t.Errorf("non-command input must not consult the backend: ok=%v err=%v", ok, err)
	}
}

// TestSourceExpanderListPassthrough pins List as a pure passthrough of the
// source's live listing (the Workspace argument is ignored).
func TestSourceExpanderListPassthrough(t *testing.T) {
	want := []prompt.Command{
		{Name: "fix", Description: "fix a bug"},
		{Name: "review", Description: "review a change"},
	}
	exp := prompt.NewSourceExpander(mapCommandSource{list: want})
	got, err := exp.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}
}

// TestValidCommandName pins the shared invocation-grammar validator against
// the exact rune set parseCommand accepts.
func TestValidCommandName(t *testing.T) {
	valid := []string{"review", "fix-1", "a_b.c", "X9"}
	invalid := []string{"", "has space", "slash/y", "uni±code", "tab\tx", "/lead"}
	for _, n := range valid {
		if !prompt.ValidCommandName(n) {
			t.Errorf("ValidCommandName(%q) = false, want true", n)
		}
	}
	for _, n := range invalid {
		if prompt.ValidCommandName(n) {
			t.Errorf("ValidCommandName(%q) = true, want false", n)
		}
	}
}
