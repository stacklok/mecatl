package app

import (
	"context"
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
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

type changingPolicyGlobal struct {
	reads int
	empty bool
}

func (*changingPolicyGlobal) TargetScoped() bool { return false }
func (g *changingPolicyGlobal) Assemble(context.Context, []string, *session.InstructionSnapshot, int) ([]session.Message, []prompt.InstructionManifest, error) {
	g.reads++
	if g.empty {
		return nil, nil, nil
	}
	return []session.Message{session.NewUserMessage(fmt.Sprintf("global-%d", g.reads))}, []prompt.InstructionManifest{{Kind: prompt.InstructionKindTurn0, HasGuidance: true, Provenance: prompt.InstructionProvenanceCustom}}, nil
}

func TestBuildReplacePolicyGlobalStableAcrossReadDiscovery(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	kinds := harnessEmptyKinds()
	kinds.Instructions = permconfig.HarnessContextKind{Sources: []string{"project", "global"}, Mode: "replace"}
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"project", "global"}, Kinds: kinds})
	cfg.Workspace, cfg.AllowAllTools, cfg.GuardrailsDisabled = root, true, true
	global := &changingPolicyGlobal{}
	cfg.HarnessInstructionSources = []HarnessSourceRegistration[prompt.InstructionAssembler]{
		{ID: "project", Provenance: HarnessProvenancePolicy{Fixed: harnessProjectTier}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			return prompt.RootAssembler{Source: ws, SourceID: "project", SourcePrefix: "."}, nil, nil
		}},
		{ID: "global", Provenance: HarnessProvenancePolicy{Fixed: harnessDriverTier}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			return global, nil, nil
		}},
	}
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })},
		mockllm.ToolCallTurn(session.NewToolCall("read", "Read", []byte(`{"path":"nested/file.txt"}`))), mockllm.TextTurn("done"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	harnessRun(t, built, t.Context(), harnessCreate(t, built, t.Context()), "read nested")
	if len(requests) != 2 || !strings.Contains(harnessRequestText(requests[0]), "global-1") || !strings.Contains(harnessRequestText(requests[1]), "global-1") || global.reads != 1 {
		t.Fatalf("replace global changed on nested Read: requests=%d reads=%d", len(requests), global.reads)
	}
}

func TestReplacePolicyGlobalLivesForOneRun(t *testing.T) {
	ws := memfs.NewWorkspace("/repo")
	if err := ws.Write(t.Context(), "nested/AGENTS.md", []byte("project-nested")); err != nil {
		t.Fatal(err)
	}
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%v", empty), func(t *testing.T) {
			global := &changingPolicyGlobal{empty: empty}
			project := fixedInstructionAssembler{id: "project", inner: prompt.RootAssembler{Source: ws, SourceID: "repo", SourcePrefix: "."}, provenance: HarnessProvenancePolicy{Fixed: harnessProjectTier}, projectAdmitted: true}
			independent := fixedInstructionAssembler{id: "global", inner: global, provenance: HarnessProvenancePolicy{Fixed: harnessDriverTier}, projectAdmitted: true}
			policy := policyInstructionAssembler{mode: harnessModeReplace, sources: []prompt.InstructionAssembler{project, independent}, cache: &policyInstructionRunCache{}}
			ctx, cancel := context.WithCancel(t.Context())
			state := &session.InstructionSnapshot{}
			first, _, err := policy.Assemble(ctx, []string{"."}, state, 65536)
			if err != nil {
				t.Fatal(err)
			}
			if empty && len(first) != 0 || !empty && !strings.Contains(fmt.Sprint(first), "global-1") {
				t.Fatalf("initial guidance: %v", first)
			}
			next, _, err := policy.Assemble(ctx, []string{"nested"}, state, 65536)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(fmt.Sprint(next), "project-nested") || strings.Contains(fmt.Sprint(next), "global") {
				t.Fatalf("new project scope did not replace global: %v", next)
			}
			if global.reads != 1 {
				t.Fatalf("global reads after new directory = %d", global.reads)
			}
			cancel()
			fresh, freshCancel := context.WithCancel(t.Context())
			defer freshCancel()
			_, _, err = policy.Assemble(fresh, []string{"."}, &session.InstructionSnapshot{}, 65536)
			if err != nil || global.reads != 2 {
				t.Fatalf("new run global reads = %d, err=%v", global.reads, err)
			}
		})
	}
}

func TestReplacePolicyGlobalBodyStableAcrossNewDirectory(t *testing.T) {
	ws := memfs.NewWorkspace("/repo")
	if err := ws.Write(t.Context(), "nested/AGENTS.md", []byte("nested")); err != nil {
		t.Fatal(err)
	}
	global := &changingPolicyGlobal{}
	policy := policyInstructionAssembler{mode: harnessModeReplace, sources: []prompt.InstructionAssembler{
		fixedInstructionAssembler{id: "global", inner: global, provenance: HarnessProvenancePolicy{Fixed: harnessDriverTier}},
		fixedInstructionAssembler{id: "project", inner: prompt.RootAssembler{Source: ws, SourceID: "repo", SourcePrefix: "."}, provenance: HarnessProvenancePolicy{Fixed: harnessProjectTier}, projectAdmitted: true},
	}, cache: &policyInstructionRunCache{}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	state := &session.InstructionSnapshot{}
	first, _, err := policy.Assemble(ctx, []string{"."}, state, 65536)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := policy.Assemble(ctx, []string{"nested"}, state, 65536)
	if err != nil || fmt.Sprint(first) != fmt.Sprint(second) || global.reads != 1 {
		t.Fatalf("global changed: first=%v second=%v reads=%d err=%v", first, second, global.reads, err)
	}
}
