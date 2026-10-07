package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

type fixedInstructions struct{}

func (fixedInstructions) TargetScoped() bool { return false }

func (fixedInstructions) Assemble(context.Context, []string, *session.InstructionSnapshot, int) ([]session.Message, []prompt.InstructionManifest, error) {
	return []session.Message{session.NewUserMessage("global guidance")}, []prompt.InstructionManifest{{Kind: prompt.InstructionKindTurn0, Provenance: prompt.InstructionProvenanceCustom}}, nil
}

func TestInertRootAssemblerDoesNotTrackScopesOrAddProtocol(t *testing.T) {
	var nilRoot *prompt.RootAssembler
	for _, tc := range []struct {
		name         string
		instructions prompt.InstructionAssembler
	}{
		{"default", nil},
		{"value", prompt.RootAssembler{}},
		{"pointer", &prompt.RootAssembler{}},
		{"nil pointer", nilRoot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine(Deps{Catalog: benchInternalCatalog(), Instructions: tc.instructions})
			r := &Run{diag: e.deps.Diagnostics}
			ws := memfs.NewWorkspace("/ws")
			sess := session.New("inert", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
			request := e.buildRequest(t.Context(), r, sess, testEnvironment(ws, nil))
			if e.deps.Instructions != nil || r.instructionScopes != nil || len(r.fragments) != 0 || strings.Contains(request.System.VolatileSuffix, "nested scopes discovered") {
				t.Fatalf("inert instructions enabled hierarchy: instructions=%v scopes=%v fragments=%v suffix=%q", e.deps.Instructions, r.instructionScopes, r.fragments, request.System.VolatileSuffix)
			}
			if e.deps.Instructions != nil && e.deps.Instructions.TargetScoped() {
				t.Fatal("inert source marked target-aware")
			}
		})
	}
}

func TestInertRootAssemblerMatchesNilInstructionAllocations(t *testing.T) {
	allocs := func(instructions prompt.InstructionAssembler) float64 {
		e := NewEngine(Deps{Catalog: benchInternalCatalog(), Instructions: instructions})
		r := &Run{diag: e.deps.Diagnostics}
		ws := memfs.NewWorkspace("/ws")
		sess := session.New("inert-allocs", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
		env := testEnvironment(ws, nil)
		return testing.AllocsPerRun(100, func() {
			_ = e.buildRequest(t.Context(), r, sess, env)
		})
	}

	if nilAllocs, inertAllocs := allocs(nil), allocs(prompt.RootAssembler{}); inertAllocs != nilAllocs {
		t.Fatalf("inert root allocations = %v, want nil-instruction path %v", inertAllocs, nilAllocs)
	}
}

func TestHierarchyGlobalContributorRunsOnceWhenScopesExpand(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(t.Context(), "nested/AGENTS.md", []byte("local")); err != nil {
		t.Fatal(err)
	}
	var calls int
	instructions := prompt.NewMultiAssembler(prompt.RootAssembler{Source: ws, SourceID: "ws@1", SourcePrefix: "."}, countingGlobalInstructions{calls: &calls})
	e := NewEngine(Deps{Catalog: benchInternalCatalog(), Instructions: instructions})
	r := &Run{diag: e.deps.Diagnostics}
	sess := session.New("global", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	_ = e.buildRequest(t.Context(), r, sess, testEnvironment(ws, nil))
	r.instructionScopes.mu.Lock()
	r.instructionScopes.dirs = append(r.instructionScopes.dirs, "nested")
	r.instructionScopes.dirty = true
	r.instructionScopes.mu.Unlock()
	request := e.buildRequest(t.Context(), r, sess, testEnvironment(ws, nil))
	if calls != 1 || len(request.Messages) != 2 || !strings.Contains(request.Messages[0].Text, "local") || request.Messages[1].Text != "global guidance" {
		t.Fatalf("global calls=%d messages=%v", calls, request.Messages)
	}
}

func TestHierarchyEmptyGlobalContributorRunsOnceAcrossScopes(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(t.Context(), "nested/AGENTS.md", []byte("local")); err != nil {
		t.Fatal(err)
	}
	var calls int
	instructions := prompt.NewMultiAssembler(prompt.NewMultiAssembler(countingEmptyInstructions{calls: &calls}), prompt.RootAssembler{Source: ws, SourceID: "ws@1", SourcePrefix: "."})
	e := NewEngine(Deps{Catalog: benchInternalCatalog(), Instructions: instructions})
	r := &Run{diag: e.deps.Diagnostics}
	sess := session.New("empty-global", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	_ = e.buildRequest(t.Context(), r, sess, testEnvironment(ws, nil))
	if r.instructionScopes == nil {
		t.Fatal("empty root disabled nested discovery")
	}
	r.instructionScopes.mu.Lock()
	r.instructionScopes.dirs = append(r.instructionScopes.dirs, "nested")
	r.instructionScopes.dirty = true
	r.instructionScopes.mu.Unlock()
	request := e.buildRequest(t.Context(), r, sess, testEnvironment(ws, nil))
	if calls != 1 || len(request.Messages) != 1 || !strings.Contains(request.Messages[0].Text, "local") {
		t.Fatalf("empty global calls=%d messages=%v", calls, request.Messages)
	}
}

type countingEmptyInstructions struct{ calls *int }

func (countingEmptyInstructions) TargetScoped() bool { return false }

func (c countingEmptyInstructions) Assemble(context.Context, []string, *session.InstructionSnapshot, int) ([]session.Message, []prompt.InstructionManifest, error) {
	*c.calls++
	return nil, nil, nil
}

type countingGlobalInstructions struct{ calls *int }

func (countingGlobalInstructions) TargetScoped() bool { return false }

func (c countingGlobalInstructions) Assemble(context.Context, []string, *session.InstructionSnapshot, int) ([]session.Message, []prompt.InstructionManifest, error) {
	*c.calls++
	return []session.Message{session.NewUserMessage("global guidance")}, []prompt.InstructionManifest{{Kind: prompt.InstructionKindTurn0, Provenance: prompt.InstructionProvenanceSoul, HasGuidance: true}}, nil
}

func TestInertRootInMultiKeepsGlobalContributor(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")
	instructions := prompt.NewMultiAssembler(prompt.RootAssembler{}, fixedInstructions{})
	e := NewEngine(Deps{Catalog: benchInternalCatalog(), Instructions: instructions})
	r := &Run{diag: e.deps.Diagnostics}
	sess := session.New("multi", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	request := e.buildRequest(t.Context(), r, sess, testEnvironment(ws, nil))
	if r.instructionScopes != nil || len(request.Messages) != 1 || request.Messages[0].Text != "global guidance" {
		t.Fatalf("global contributor lost or inert hierarchy enabled: scopes=%v messages=%v", r.instructionScopes, request.Messages)
	}
	if strings.Contains(request.System.VolatileSuffix, "nested scopes discovered") {
		t.Fatalf("inert root advertised scoped discovery: %q", request.System.VolatileSuffix)
	}
	active := prompt.NewMultiAssembler(instructions, &prompt.RootAssembler{Source: ws})
	if !active.TargetScoped() {
		t.Fatal("nested active root was not discovered")
	}
}
