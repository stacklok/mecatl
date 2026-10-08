package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestBuildSamePlacementForkKeepsExaminedInstructions(t *testing.T) {
	ws := osfsWorkspaceForHierarchy(t)
	if err := os.WriteFile(filepath.Join(ws.Root(), "nested/draft.txt"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := hcConfiguredFiles(t, ws)
	cfg.Workspace = ws.Root()
	cfg.AllowAllTools, cfg.GuardrailsDisabled = true, true
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })},
		mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"nested/draft.txt"}`)}), mockllm.TextTurn("parent"), mockllm.TextTurn("fork"), mockllm.TextTurn("fresh"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	parent := harnessCreate(t, built, t.Context())
	harnessRun(t, built, t.Context(), parent, "read draft")
	if len(requests) != 2 || !strings.Contains(fmt.Sprint(requests[1].Messages), "NESTED-HIERARCHY") {
		t.Fatalf("parent requests=%v", requests)
	}
	if err := os.WriteFile(filepath.Join(ws.Root(), "AGENTS.md"), []byte("NEW-ROOT"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root(), "nested/CLAUDE.md"), []byte("NEW-NESTED"), 0600); err != nil {
		t.Fatal(err)
	}
	fork, err := built.Service.ForkSessionSuccessor(t.Context(), server.ForkSuccessorRequest{Source: parent})
	if err != nil {
		t.Fatal(err)
	}
	harnessRun(t, built, t.Context(), fork, "continue")
	if len(requests) != 3 {
		t.Fatalf("requests=%d", len(requests))
	}
	got := fmt.Sprint(requests[2].Messages)
	if !strings.Contains(got, "ROOT-HIERARCHY") || !strings.Contains(got, "NESTED-HIERARCHY") || strings.Contains(got, "NEW-ROOT") || strings.Contains(got, "NEW-NESTED") {
		t.Fatalf("fork lost source snapshot: %s", got)
	}
	fresh := harnessCreate(t, built, t.Context())
	harnessRun(t, built, t.Context(), fresh, "fresh")
	if !strings.Contains(fmt.Sprint(requests[3].Messages), "NEW-ROOT") {
		t.Fatalf("fresh session inherited stale snapshot: %v", requests[3].Messages)
	}
}

func TestForkCorrespondenceDoesNotReuseRetiredIncarnation(t *testing.T) {
	resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{mode: harnessModeCombine}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	reader := memfs.NewWorkspace("/source")
	if err := reader.Write(t.Context(), "AGENTS.md", []byte("current")); err != nil {
		t.Fatal(err)
	}
	root := prompt.RootAssembler{Source: reader, SourceID: "source", SourcePrefix: "."}
	parentBinding, releaseParent, err := resolver.Borrow(t.Context(), "reused", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	original := parentBinding.(*resolvedCommandBinding)
	original.context = &Config{harnessInstructions: root}
	bound, _ := bindInstructionRoots(root, original.generation.entry)
	oldID := bound.(prompt.RootAssembler).SourceID
	resolver.Retire("reused")
	releaseParent()
	if err := resolver.Activate(t.Context(), "reused", nil, ""); err != nil {
		t.Fatal(err)
	}
	currentBinding, releaseCurrent, err := resolver.Borrow(t.Context(), "reused", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseCurrent()
	current := currentBinding.(*resolvedCommandBinding)
	current.context = &Config{harnessInstructions: root}
	childBinding, releaseChild, err := resolver.Borrow(t.Context(), "child", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseChild()
	child := childBinding.(*resolvedCommandBinding)
	child.context = &Config{harnessInstructions: root}
	changed := root
	changed.SourceID = "replaced"
	child.context.harnessInstructions = changed
	if got := resolver.inheritedInstructionSources("reused", nil, "", child.generation.entry); len(got) != 0 {
		t.Fatalf("changed selected source mapped: %v", got)
	}
	child.context.harnessInstructions = root
	mapping := resolver.inheritedInstructionSources("reused", nil, "", child.generation.entry)
	if mapping[oldID] != "" {
		t.Fatalf("retired generation mapped into child: %v", mapping)
	}
	state := &session.InstructionSnapshot{Directories: []string{"."}, Scopes: []session.InstructionScope{{SourceID: oldID, Directory: ".", File: "AGENTS.md", Text: "retired", Examined: true}}}
	g := generationInstructions{harnessGeneration: child.generation, source: root, inherited: mapping}
	messages, _, err := g.Assemble(t.Context(), []string{"."}, state, 65536)
	if err != nil || strings.Contains(fmt.Sprint(messages), "retired") || !strings.Contains(fmt.Sprint(messages), "current") {
		t.Fatalf("retired snapshot reused: messages=%v err=%v", messages, err)
	}
	if got := resolver.inheritedInstructionSources("reused", &session.Principal{Issuer: "other", Subject: "other", GrantType: session.GrantTypeUser}, "", child.generation.entry); len(got) != 0 {
		t.Fatalf("unrelated principal mapped: %v", got)
	}
}

func TestBuildForkDiscardsRemovedBinding(t *testing.T) {
	execution := osfsWorkspaceForHierarchy(t)
	if err := os.WriteFile(filepath.Join(execution.Root(), "nested/draft.txt"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	old, replacement := memfs.NewWorkspace("/old"), memfs.NewWorkspace("/replacement")
	for _, row := range []struct {
		ws           *memfs.Workspace
		root, nested string
	}{{old, "OLD-ROOT", "OLD-NESTED"}, {replacement, "REPLACEMENT-ROOT", "REPLACEMENT-NESTED"}} {
		if err := row.ws.Write(t.Context(), "AGENTS.md", []byte(row.root)); err != nil {
			t.Fatal(err)
		}
		if err := row.ws.Write(t.Context(), "nested/AGENTS.md", []byte(row.nested)); err != nil {
			t.Fatal(err)
		}
	}
	cfg := hcConfiguredFiles(t, old)
	cfg.Workspace = execution.Root()
	cfg.AllowAllTools, cfg.GuardrailsDisabled = true, true
	cfg.HarnessInstructionSources[0].Scope = HarnessSourceScopePrincipal
	binds := 0
	cfg.HarnessInstructionSources[0].Bind = func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		binds++
		source := old
		if binds > 1 {
			source = replacement
		}
		return prompt.RootAssembler{Source: source, SourceID: "source", SourcePrefix: "."}, nil, nil
	}
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })},
		mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"nested/draft.txt"}`)}), mockllm.TextTurn("parent"), mockllm.TextTurn("fork"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	parent := harnessCreate(t, built, t.Context())
	harnessRun(t, built, t.Context(), parent, "read")
	if !strings.Contains(fmt.Sprint(requests[1].Messages), "OLD-NESTED") {
		t.Fatalf("parent failed to discover old nested: %v", requests[1].Messages)
	}
	fork, err := built.Service.ForkSessionSuccessor(t.Context(), server.ForkSuccessorRequest{Source: parent})
	if err != nil {
		t.Fatal(err)
	}
	harnessRun(t, built, t.Context(), fork, "continue")
	if binds < 2 || len(requests) != 3 {
		t.Fatalf("binds=%d requests=%d", binds, len(requests))
	}
	got := fmt.Sprint(requests[2].Messages)
	if strings.Contains(got, "OLD-ROOT") || strings.Contains(got, "OLD-NESTED") || !strings.Contains(got, "REPLACEMENT-ROOT") {
		t.Fatalf("removed source reused in fork: %s", got)
	}
}
