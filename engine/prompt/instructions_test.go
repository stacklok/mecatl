package prompt_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

func TestRootAssemblerMatchesDiscoverInstructions(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	if err := ws.Write(t.Context(), "AGENTS.md", []byte("be terse")); err != nil {
		t.Fatal(err)
	}
	want, _, err := prompt.DiscoverInstructions(t.Context(), ws, ".")
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := (prompt.RootAssembler{Source: ws, SourceID: "proj", SourcePrefix: "."}).Assemble(t.Context(), []string{"."}, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(got) != len(want) {
		t.Fatalf("got=%v want=%v err=%v", got, want, err)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Text != want[i].Text {
			t.Fatalf("got=%v want=%v", got, want)
		}
	}
}

func TestRootAssemblerReservesFullSelectedPath(t *testing.T) {
	ws := &countedHierarchyWorkspace{Workspace: memfs.NewWorkspace("/budget")}
	if err := ws.Write(t.Context(), "prefix/sub/child/CLAUDE.md", []byte("child guidance")); err != nil {
		t.Fatal(err)
	}
	const sourceID, prefix, target = "source@revision-42", "prefix/sub", "child"
	dirs := []string{".", "prefix", "prefix/sub", "prefix/sub/child"}
	limit := len(target) + 1
	for _, dir := range dirs {
		file := ""
		if dir == "prefix/sub/child" {
			file = "prefix/sub/child/CLAUDE.md"
		}
		limit += len(sourceID) + len(dir) + len(file) + 1
	}
	a := prompt.RootAssembler{Source: ws, SourceID: sourceID, SourcePrefix: prefix}
	state := &session.InstructionSnapshot{}
	messages, rows, err := a.Assemble(t.Context(), []string{target}, state, limit)
	if err != nil || state.MetadataBytes() != limit || state.DiscoveryExhausted || len(rows) != 1 || rows[0].File != "prefix/sub/child/CLAUDE.md" || !strings.Contains(messages[0].Text, "child guidance") {
		t.Fatalf("exact cap: metadata=%d limit=%d scopes=%v rows=%v err=%v", state.MetadataBytes(), limit, state.Scopes, rows, err)
	}
	before := ws.reads
	short := &session.InstructionSnapshot{}
	_, _, err = a.Assemble(t.Context(), []string{target}, short, limit-1)
	if err != nil || !short.DiscoveryExhausted || short.MetadataBytes() > limit-1 || ws.reads-before > 2*(len(short.Scopes)) {
		t.Fatalf("short cap: metadata=%d scopes=%v reads=%d err=%v", short.MetadataBytes(), short.Scopes, ws.reads-before, err)
	}
	for _, scope := range short.Scopes {
		if scope.Directory == "prefix/sub/child" {
			t.Fatal("probed a scope without reserving its full selected path")
		}
	}
}

func TestRootAssemblerDeepTargetIsBudgetBoundedAndCancelable(t *testing.T) {
	ws := &countedHierarchyWorkspace{Workspace: memfs.NewWorkspace("/deep")}
	target := strings.TrimSuffix(strings.Repeat("x/", 30000), "/")
	a := prompt.RootAssembler{Source: ws, SourceID: "deep@1", SourcePrefix: "prefix"}
	const limit = 61000
	state := &session.InstructionSnapshot{}
	_, _, err := a.Assemble(t.Context(), []string{target}, state, limit)
	if err != nil || !state.DiscoveryExhausted || state.MetadataBytes() > limit || len(state.Scopes) > 200 || ws.reads > 2*len(state.Scopes) {
		t.Fatalf("deep target: metadata=%d scopes=%d reads=%d err=%v", state.MetadataBytes(), len(state.Scopes), ws.reads, err)
	}
	if allocs := testing.AllocsPerRun(1, func() {
		_, _, assembleErr := a.Assemble(t.Context(), []string{target}, &session.InstructionSnapshot{}, limit)
		if assembleErr != nil {
			panic(assembleErr)
		}
	}); allocs > 1000 {
		t.Fatalf("deep target allocated for unexamined ancestors: %.0f allocations", allocs)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := ws.reads
	_, _, err = a.Assemble(ctx, []string{target}, &session.InstructionSnapshot{}, limit)
	if err != context.Canceled || ws.reads != before {
		t.Fatalf("canceled deep target: reads=%d err=%v", ws.reads-before, err)
	}
}

func TestRootAssemblerHonorsHigherConfiguredContentLimit(t *testing.T) {
	ws := memfs.NewWorkspace("/large")
	body := strings.Repeat("x", 70000)
	if err := ws.Write(t.Context(), "AGENTS.md", []byte(body)); err != nil {
		t.Fatal(err)
	}
	state := &session.InstructionSnapshot{}
	messages, rows, err := (prompt.RootAssembler{Source: ws, SourceID: "large", SourcePrefix: "."}).Assemble(t.Context(), nil, state, 90000)
	if err != nil || len(messages) != 1 || len(rows) != 1 || rows[0].Partial || rows[0].Omitted || !strings.HasSuffix(messages[0].Text, body) || state.MetadataBytes() > 90000 {
		t.Fatalf("higher content limit not honored: metadata=%d rows=%v err=%v", state.MetadataBytes(), rows, err)
	}
}

func TestRootAssemblerEmptyWorkspace(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	got, _, err := (prompt.RootAssembler{Source: ws, SourceID: "proj", SourcePrefix: "."}).Assemble(t.Context(), nil, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(got) != 0 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestAssembleWithManifestPreservesMessagesAndReportsBuiltInProvenance(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	if err := ws.Write(context.Background(), "AGENTS.md", []byte("byte-stable instructions")); err != nil {
		t.Fatal(err)
	}
	a := prompt.NewMultiAssembler(prompt.RootAssembler{Source: ws, SourceID: "proj", SourcePrefix: "."})
	want, _, err := a.Assemble(t.Context(), []string{"."}, &session.InstructionSnapshot{}, 65536)
	if err != nil {
		t.Fatal(err)
	}
	got, manifest, err := prompt.AssembleWithManifest(t.Context(), a, []string{"."}, &session.InstructionSnapshot{}, 65536)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v err=%v", got, want, err)
	}
	if len(manifest) != 1 || manifest[0].SourceID != "proj" || !manifest[0].HasGuidance {
		t.Fatalf("manifest=%+v", manifest)
	}
}
