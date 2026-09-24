package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func checkHarnessCommandAgreement(ctx context.Context, binding server.CommandSourceBinding, probes []string) error {
	listed, err := binding.List(ctx)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, command := range listed {
		names[command.Name] = true
		_, found, err := binding.Expand(ctx, "/"+command.Name)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("listed command %s has no expansion", command.Name)
		}
	}
	for _, name := range probes {
		_, found, err := binding.Expand(ctx, "/"+name)
		if err != nil {
			return err
		}
		if found != names[name] {
			return fmt.Errorf("command %s expansion disagrees with listing", name)
		}
	}
	return nil
}

type inconsistentHarnessCommands struct {
	server.CommandSourceBinding
	listed []prompt.Command
}

func (s inconsistentHarnessCommands) List(context.Context) ([]prompt.Command, error) {
	return s.listed, nil
}

func TestADR_0354_HarnessContext_Scenario2_PaletteAndExpansionAgree(t *testing.T) {
	kinds := harnessEmptyKinds()
	kinds.Commands = permconfig.HarnessContextKind{Sources: []string{"a", "b", "c"}, Mode: "combine", Overrides: []permconfig.HarnessContextOverride{{Name: "review", Winner: "c", Replaces: []string{"a"}}}}
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"a", "b", "c"}, Kinds: kinds})
	cfg.EnableCommands = true
	writeCommand(t, cfg.Workspace, ".mecatl/commands", "review", "UNSELECTED")
	for _, id := range []string{"a", "b", "c"} {
		marker := id
		cfg.HarnessCommandSources = append(cfg.HarnessCommandSources, HarnessSourceRegistration[server.CommandSourceBinding]{ID: HarnessSourceID(id), Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			return &hcCommands{values: map[string]string{"review": marker}}, nil, nil
		}})
	}
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.TextTurn("done"))
	b, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := harnessCreate(t, b, t.Context())
	commands, err := b.Service.ListCommandsForSession(t.Context(), id)
	if err != nil || len(commands) != 1 || commands[0].Name != "review" || commands[0].Description != "b" {
		t.Fatalf("palette=%v,%v", commands, err)
	}
	harnessRun(t, b, t.Context(), id, "/review")
	if len(requests) != 1 {
		t.Fatalf("requests=%d", len(requests))
	}
	last := ""
	for _, message := range requests[0].Messages {
		if message.Role == session.RoleUser {
			last = message.Text
		}
	}
	if last != "b" {
		t.Fatalf("actual engine expansion=%q want non-replaced blocker b", last)
	}
	valid := &hcCommands{values: map[string]string{"review": "body"}}
	if err := checkHarnessCommandAgreement(t.Context(), valid, []string{"review", "hidden"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []server.CommandSourceBinding{
		inconsistentHarnessCommands{CommandSourceBinding: valid, listed: []prompt.Command{{Name: "missing"}}},
		inconsistentHarnessCommands{CommandSourceBinding: valid},
	} {
		if err := checkHarnessCommandAgreement(t.Context(), bad, []string{"review", "hidden"}); err == nil {
			t.Fatal("stable invalid source passed listing/lookup conformance")
		}
	}
}

func TestADR_0354_HarnessContext_Scenario4_NoUnconfiguredAuthorityFallback(t *testing.T) {
	t.Run("required bind fails before model", func(t *testing.T) {
		source := memfs.NewWorkspace("/source")
		cfg := hcConfiguredFiles(t, source)
		if err := os.WriteFile(filepath.Join(cfg.Workspace, "AGENTS.md"), []byte("FORBIDDEN-FALLBACK"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.HarnessInstructionSources[0].Provenance.Fixed = "driver"
		cfg.HarnessInstructionSources[0].Bind = func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			return nil, nil, fs.ErrPermission
		}
		b, err := buildIsolated(t, t.Context(), cfg)
		if b != nil {
			b.Close()
		}
		if !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("required source failure=%v", err)
		}
	})
	t.Run("optional absence stays absent", func(t *testing.T) {
		source := memfs.NewWorkspace("/empty-source")
		cfg := hcConfiguredFiles(t, source)
		if err := os.WriteFile(filepath.Join(cfg.Workspace, "AGENTS.md"), []byte("FORBIDDEN-FALLBACK"), 0o600); err != nil {
			t.Fatal(err)
		}
		var requests []port.LLMRequest
		cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.TextTurn("done"))
		b, err := buildIsolated(t, t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close()
		id := harnessCreate(t, b, t.Context())
		harnessRun(t, b, t.Context(), id, "task")
		if len(requests) != 1 {
			t.Fatalf("requests=%d", len(requests))
		}
		for _, message := range requests[0].Messages {
			if strings.Contains(message.Text, "FORBIDDEN-FALLBACK") {
				t.Fatal("optional absence selected unrelated execution files")
			}
		}
	})
}

func TestADR_0354_HarnessContext_Scenario4_ConfiguredChainFailureSemantics(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, soft := range []bool{false, true} {
			t.Run(fmt.Sprintf("shared=%v/soft=%v", shared, soft), func(t *testing.T) {
				source := memfs.NewWorkspace("/selected")
				harnessSeed(t, source, "AGENTS.md", "NEXT-ADMITTED-SOURCE")
				reader := &harnessInstructionReads{Workspace: source}
				kinds := harnessEmptyKinds()
				kinds.Instructions = permconfig.HarnessContextKind{Sources: []string{"fault", "next"}, Mode: "combine"}
				cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"fault", "next"}, Kinds: kinds})
				if shared {
					cfg.PlacementProvider = harnessVirtualPlacement(source)
				}
				first := prompt.InstructionAssembler(prompt.RootAssembler{Source: &harnessInstructionReads{Workspace: source, fault: fs.ErrPermission}})
				firstTier := "project"
				if soft {
					first = prompt.RulesAssembler{Src: frozenHarnessRules{err: fs.ErrPermission}}
					firstTier = "driver"
				}
				cfg.HarnessInstructionSources = []HarnessSourceRegistration[prompt.InstructionAssembler]{
					{ID: "fault", Provenance: HarnessProvenancePolicy{Fixed: firstTier}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
						return first, nil, nil
					}},
					{ID: "next", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
						return prompt.RootAssembler{Source: reader}, nil, nil
					}},
				}
				var requests []port.LLMRequest
				cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.TextTurn("done"))
				b, err := buildIsolated(t, t.Context(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer b.Close()
				id := harnessCreate(t, b, t.Context())
				harnessRun(t, b, t.Context(), id, "task")
				if len(requests) != 1 {
					t.Fatalf("requests=%d", len(requests))
				}
				found := false
				for _, message := range requests[0].Messages {
					found = found || strings.Contains(message.Text, "NEXT-ADMITTED-SOURCE")
				}
				if found != soft {
					t.Fatalf("configured next source visible=%v want %v", found, soft)
				}
				wantReads := int32(0)
				if soft {
					wantReads = 1
				}
				if reader.reads.Load() != wantReads {
					t.Fatalf("next source reads=%d want %d", reader.reads.Load(), wantReads)
				}
			})
		}
	}
}
