package skillfs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestSkillCommandSourceListMirrorsSkills pins ListCommands: it returns one
// prompt.Command per skill, name-sorted, de-duped, and ValidCommandName-
// filtered. The skill-name grammar (name.go: ^[a-z0-9][a-z0-9_-]{0,63}$) is a
// SUBSET of the command grammar (letters/digits/'-'/'_'/'.'), so the filter is
// belt-and-braces — exercised here with a name the skill grammar would reject
// were it not defended upstream (a skill with a '.' would be invocable as a
// command but not a valid skill name; the converse — a skill name invalid as a
// command — is exercised by feeding the source a bogus meta directly).
func TestSkillCommandSourceListMirrorsSkills(t *testing.T) {
	metas := []tool.SkillMeta{
		{Name: "zebra", Description: "z skill"},
		{Name: "alpha", Description: "a skill"},
		{Name: "alpha", Description: "dup — de-duped, first description kept"},
		{Name: "Bad/Name", Description: "invalid command name — dropped"},
	}
	src := NewSkillCommandSource(metas, stubActivator{})

	cmds, err := src.ListCommands(context.Background())
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	// Sorted, de-duped, filtered: [alpha, zebra].
	if len(cmds) != 2 {
		t.Fatalf("ListCommands = %+v, want 2 commands", cmds)
	}
	if cmds[0].Name != "alpha" || cmds[0].Description != "a skill" {
		t.Errorf("cmds[0] = %+v, want {alpha a skill}", cmds[0])
	}
	if cmds[1].Name != "zebra" || cmds[1].Description != "z skill" {
		t.Errorf("cmds[1] = %+v, want {zebra z skill}", cmds[1])
	}
	// Every listed name must itself be a valid command name (the contract).
	for _, c := range cmds {
		if !prompt.ValidCommandName(c.Name) {
			t.Errorf("ListCommands returned a non-invocable name %q", c.Name)
		}
	}
}

// TestSkillCommandSourceEmptyInventory pins the no-skills path: an empty
// (or nil) metas slice lists and expands nothing — never an error.
func TestSkillCommandSourceEmptyInventory(t *testing.T) {
	for _, metas := range [][]tool.SkillMeta{nil, {}} {
		src := NewSkillCommandSource(metas, stubActivator{})
		cmds, err := src.ListCommands(context.Background())
		if err != nil {
			t.Fatalf("ListCommands(nil/empty) err = %v", err)
		}
		if len(cmds) != 0 {
			t.Errorf("ListCommands(nil/empty) = %+v, want empty", cmds)
		}
		body, found, err := src.CommandBody(context.Background(), "anything")
		if err != nil || found || body != "" {
			t.Errorf("CommandBody over empty inventory = (%q, %v, %v), want (\"\", false, nil)", body, found, err)
		}
	}
}

// TestSkillCommandSourceCommandBodyRoundTripsBody pins CommandBody: an
// Activate over a known skill returns its (frontmatter-stripped) body verbatim
// with found=true. The skill body is ALREADY stripped (ParseSkill), so the
// expander's stripFrontmatter is a no-op on it.
func TestSkillCommandSourceCommandBodyRoundTripsBody(t *testing.T) {
	act := stubActivator{
		bodies: map[string]string{
			"deploy": "Deploy the service to $1.",
			"review": "Review $ARGUMENTS for correctness.",
		},
	}
	src := NewSkillCommandSource([]tool.SkillMeta{
		{Name: "deploy", Description: "deploy"},
		{Name: "review", Description: "review"},
	}, act)
	ctx := context.Background()

	for _, name := range []string{"deploy", "review"} {
		body, found, err := src.CommandBody(ctx, name)
		if err != nil {
			t.Fatalf("CommandBody(%q): %v", name, err)
		}
		if !found {
			t.Fatalf("CommandBody(%q) found = false, want true", name)
		}
		if want := act.bodies[name]; body != want {
			t.Errorf("CommandBody(%q) = %q, want %q", name, body, want)
		}
	}
}

// TestSkillCommandSourceUnknownNameIsNormal pins that an unknown name is the
// NORMAL found=false outcome (never an error) — the input passes through the
// expander unchanged.
func TestSkillCommandSourceUnknownNameIsNormal(t *testing.T) {
	src := NewSkillCommandSource(
		[]tool.SkillMeta{{Name: "deploy", Description: "deploy"}},
		stubActivator{bodies: map[string]string{"deploy": "Deploy."}},
	)
	body, found, err := src.CommandBody(context.Background(), "no-such-skill")
	if err != nil {
		t.Errorf("CommandBody(unknown) err = %v, want nil (unknown is NORMAL)", err)
	}
	if found || body != "" {
		t.Errorf("CommandBody(unknown) = (%q, %v), want (\"\", false)", body, found)
	}
}

// TestSkillCommandSourceActivationFaultIsError pins that a genuine activation
// fault (NOT ErrSkillNotFound) surfaces as an error, mirroring the Skill tool's
// addressable activation-error posture.
func TestSkillCommandSourceActivationFaultIsError(t *testing.T) {
	act := stubActivator{err: errTestActivateFault}
	src := NewSkillCommandSource(
		[]tool.SkillMeta{{Name: "broken", Description: "broken"}},
		act,
	)
	_, _, err := src.CommandBody(context.Background(), "broken")
	if err == nil {
		t.Fatal("a genuine activation fault must surface as an error")
	}
	if !errors.Is(err, errTestActivateFault) {
		t.Errorf("error should wrap the original fault, got %v", err)
	}
}

// TestSkillCommandSourceNotFoundClassIsNormal pins that an Activate returning a
// wrapped ErrSkillNotFound is treated as the normal found=false outcome (never
// an error) — the activator's not-found path is the command source's not-found
// path.
func TestSkillCommandSourceNotFoundClassIsNormal(t *testing.T) {
	act := stubActivator{err: fmt.Errorf("%w: %q", tool.ErrSkillNotFound, "ghost")}
	src := NewSkillCommandSource(
		[]tool.SkillMeta{{Name: "ghost", Description: "ghost"}},
		act,
	)
	body, found, err := src.CommandBody(context.Background(), "ghost")
	if err != nil {
		t.Errorf("CommandBody(ErrSkillNotFound) err = %v, want nil", err)
	}
	if found || body != "" {
		t.Errorf("CommandBody(ErrSkillNotFound) = (%q, %v), want (\"\", false)", body, found)
	}
}

// TestSkillCommandSourceEmptyBodyNotAnExpansion pins that a skill whose body is
// empty (after trim) does NOT expand: the input is left unchanged so the model
// sees its raw `/name` rather than a blank substitution.
func TestSkillCommandSourceEmptyBodyNotAnExpansion(t *testing.T) {
	src := NewSkillCommandSource(
		[]tool.SkillMeta{{Name: "blank", Description: "blank"}},
		stubActivator{bodies: map[string]string{"blank": "   "}},
	)
	body, found, err := src.CommandBody(context.Background(), "blank")
	if err != nil || found || body != "" {
		t.Errorf("CommandBody(blank) = (%q, %v, %v), want (\"\", false, nil)", body, found, err)
	}
}

// TestSkillCommandSourceNilActivatorIsNoOp pins that a nil activator (the
// no-skills / disabled seam) never expands — ListCommands returns the metas,
// CommandBody returns found=false without dereferencing the nil activator.
func TestSkillCommandSourceNilActivatorIsNoOp(t *testing.T) {
	src := NewSkillCommandSource(
		[]tool.SkillMeta{{Name: "deploy", Description: "deploy"}},
		nil, // nil activator
	)
	cmds, err := src.ListCommands(context.Background())
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Name != "deploy" {
		t.Fatalf("ListCommands = %+v, want [deploy]", cmds)
	}
	body, found, err := src.CommandBody(context.Background(), "deploy")
	if err != nil || found || body != "" {
		t.Errorf("CommandBody over nil activator = (%q, %v, %v), want (\"\", false, nil)", body, found, err)
	}
}

// --- mock Activator + helpers ---------------------------------------------

var errTestActivateFault = errors.New("test: activation fault")

// stubActivator is a minimal in-memory Activator for the command-source tests.
// A non-nil err short-circuits every Activate (simulating a fault); a body in
// bodies is returned for that name; anything else returns ErrSkillNotFound.
type stubActivator struct {
	bodies map[string]string
	err    error
}

func (a stubActivator) Activate(_ context.Context, name string) (Activation, error) {
	if a.err != nil {
		return Activation{}, a.err
	}
	if body, ok := a.bodies[name]; ok {
		return Activation{Body: body}, nil
	}
	return Activation{}, fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
}
