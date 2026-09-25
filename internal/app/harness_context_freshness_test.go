package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type harnessInstructionReads struct {
	tool.Workspace
	reads atomic.Int32
	fault error
}

func (w *harnessInstructionReads) Read(ctx context.Context, path string) ([]byte, error) {
	if path == "AGENTS.md" {
		w.reads.Add(1)
		if w.fault != nil {
			return nil, w.fault
		}
	}
	return w.Workspace.Read(ctx, path)
}

func TestADR_0359_HarnessContext_Scenario3_ProjectInstructionsPerRun(t *testing.T) {
	ws := memfs.NewWorkspace("/source")
	harnessSeed(t, ws, "AGENTS.md", "FIRST-CONTEXT")
	source := &harnessInstructionReads{Workspace: ws}
	cfg := hcConfiguredFiles(t, source)
	cfg.NoSoul = true
	cfg.AllowAllTools = true
	cfg.GuardrailsDisabled = true
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "data.txt"), []byte("execution"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"data.txt"}`)}), mockllm.TextTurn("one"), mockllm.TextTurn("two"))
	b, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := harnessCreate(t, b, t.Context())
	harnessRun(t, b, t.Context(), id, "first task")
	if source.reads.Load() != 1 {
		t.Fatalf("source reads after two model turns=%d", source.reads.Load())
	}
	harnessSeed(t, ws, "AGENTS.md", "SECOND-CONTEXT")
	harnessRun(t, b, t.Context(), id, "second task")
	if source.reads.Load() != 2 {
		t.Fatalf("source reads across two runs=%d", source.reads.Load())
	}
	if len(requests) != 3 {
		t.Fatalf("requests=%d", len(requests))
	}
	for i, request := range requests {
		want := "FIRST-CONTEXT"
		if i == 2 {
			want = "SECOND-CONTEXT"
		}
		if len(request.Messages) == 0 || !strings.Contains(request.Messages[0].Text, want) {
			t.Fatalf("run context did not refresh: request %d", i)
		}
	}
	stored, err := b.Service.GetSession(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range stored.Conversation.Messages {
		if prompt.IsInjectedTurn0Fragment(message.Text) {
			t.Fatal("source instruction persisted into conversation")
		}
	}
	a := policyInstructionAssembler{mode: harnessModeCombine, sources: []prompt.InstructionAssembler{fixedInstructionAssembler{inner: prompt.RootAssembler{Source: source}, provenance: HarnessProvenancePolicy{Fixed: "project"}, projectAdmitted: true}}}
	plain, err := a.Assemble(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	manifested, rows, err := prompt.AssembleWithManifest(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain, manifested) || len(rows) != 1 || rows[0].Kind != prompt.InstructionKindTurn0 || rows[0].Provenance != prompt.InstructionProvenanceProject {
		t.Fatalf("manifest provenance/message drift: %v", rows)
	}
}

func TestADR_0359_HarnessContext_Scenario3_RootDiscoveryCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, agents, claude, want string }{
		{"agents wins", " agents ", "claude", "agents"},
		{"whitespace fallback", " \t\n", " claude ", "claude"},
		{"missing agents", "", "claude", "claude"},
		{"both empty", " \t", " \n", ""},
		{"both absent", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := memfs.NewWorkspace("/root")
			if tc.agents != "" {
				harnessSeed(t, ws, "AGENTS.md", tc.agents)
			}
			if tc.claude != "" {
				harnessSeed(t, ws, "CLAUDE.md", tc.claude)
			}
			harnessSeed(t, ws, "nested/AGENTS.md", "NOT-ROOT")
			assembled, err := (prompt.RootAssembler{Source: ws}).Assemble(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			discovered, err := prompt.DiscoverInstructions(t.Context(), ws)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(assembled, discovered) {
				t.Fatal("source binding changed discovery framing")
			}
			if tc.want == "" {
				if len(assembled) != 0 {
					t.Fatal("empty root or nested file contributed")
				}
				return
			}
			if len(assembled) != 1 || !strings.HasSuffix(assembled[0].Text, tc.want) || !prompt.IsInjectedTurn0Fragment(assembled[0].Text) {
				t.Fatalf("root fragment=%v", assembled)
			}
		})
	}
	ws := memfs.NewWorkspace("/fault")
	harnessSeed(t, ws, "CLAUDE.md", "must not hide fault")
	source := &harnessInstructionReads{Workspace: ws, fault: fs.ErrPermission}
	if _, err := (prompt.RootAssembler{Source: source}).Assemble(t.Context()); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("real read fault became fallback: %v", err)
	}
	if messages, err := (prompt.RootAssembler{}).Assemble(t.Context()); err != nil || len(messages) != 0 {
		t.Fatal("optional nil source changed")
	}
}

func TestADR_0359_HarnessContext_Scenario2_SelectedCommandsRemainLive(t *testing.T) {
	for _, kind := range []string{"logical-api", "execution-files"} {
		t.Run(kind, func(t *testing.T) {
			source := memfs.NewWorkspace("/selected-live")
			harnessSeed(t, source, ".mecatl/commands/review.md", "FIRST")
			cfg := hcConfiguredFiles(t, source)
			cfg.NoSoul = true
			live := &hcCommands{values: map[string]string{"review": "FIRST"}}
			if kind == "logical-api" {
				cfg.HarnessCommandSources[0].Bind = func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
					return live, nil, nil
				}
			} else {
				cfg.PlacementProvider = harnessVirtualPlacement(source)
			}
			var requests []port.LLMRequest
			cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.TextTurn("one"), mockllm.TextTurn("two"), mockllm.TextTurn("three"))
			b, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			id := harnessCreate(t, b, t.Context())
			harnessRun(t, b, t.Context(), id, "/review")
			writeCommand(t, cfg.Workspace, ".mecatl/commands", "review", "UNSELECTED")
			harnessRun(t, b, t.Context(), id, "/review")
			live.mu.Lock()
			live.values["review"] = "SECOND"
			live.values["new"] = "NEW"
			live.mu.Unlock()
			harnessSeed(t, source, ".mecatl/commands/review.md", "SECOND")
			harnessSeed(t, source, ".mecatl/commands/new.md", "NEW")
			commands, err := b.Service.ListCommandsForSession(t.Context(), id)
			if err != nil || len(commands) != 2 {
				t.Fatalf("fresh list=%v,%v", commands, err)
			}
			harnessRun(t, b, t.Context(), id, "/review")
			if len(requests) != 3 {
				t.Fatalf("requests=%d", len(requests))
			}
			for i, request := range requests {
				last := ""
				for _, message := range request.Messages {
					if message.Role == session.RoleUser {
						last = message.Text
					}
				}
				want := "FIRST"
				if i == 2 {
					want = "SECOND"
				}
				if last != want {
					t.Fatalf("observation %d=%q want %q", i, last, want)
				}
			}
		})
	}
}
