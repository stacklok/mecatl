package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestADR_0376_AgentsHierarchy_Scenario3_SourceMapping(t *testing.T) {
	repository := t.TempDir()
	website := filepath.Join(repository, "website")
	if err := os.MkdirAll(website, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(repository, "AGENTS.md"): "POISONED-PARENT",
		filepath.Join(website, "AGENTS.md"):    "WEBSITE-GUIDANCE",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	run := func(t *testing.T, cfg Config) port.LLMRequest {
		t.Helper()
		var requests []port.LLMRequest
		cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.TextTurn("done"))
		built, err := buildIsolated(t, t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer built.Close()
		harnessRun(t, built, t.Context(), harnessCreate(t, built, t.Context()), "follow project guidance")
		if len(requests) != 1 {
			t.Fatalf("requests=%d", len(requests))
		}
		return requests[0]
	}
	contains := func(request port.LLMRequest, needle string) bool {
		for _, message := range request.Messages {
			if strings.Contains(message.Text, needle) {
				return true
			}
		}
		return false
	}

	defaultRequest := run(t, Config{Workspace: website, UseMock: true, Headless: true, TrustProject: true, NoSoul: true, UserModelDir: t.TempDir()})
	if !contains(defaultRequest, "WEBSITE-GUIDANCE") || contains(defaultRequest, "POISONED-PARENT") {
		t.Fatalf("default subfolder source escaped its selected root: %v", defaultRequest.Messages)
	}

	broader, err := osfs.NewWorkspace(repository)
	if err != nil {
		t.Fatal(err)
	}
	cfg := hcConfiguredFiles(t, broader)
	cfg.HarnessInstructionSources[0].Bind = func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		return prompt.RootAssembler{Source: broader, SourceID: "broader", SourcePrefix: "website"}, nil, nil
	}
	cfg.Workspace = website
	cfg.NoSoul = true
	broaderRequest := run(t, cfg)
	if !contains(broaderRequest, "POISONED-PARENT") || !contains(broaderRequest, "WEBSITE-GUIDANCE") {
		t.Fatalf("explicit broader source did not contribute its root-to-subtree chain: %v", broaderRequest.Messages)
	}
}

func TestHierarchyFactorySelectedSource(t *testing.T) {
	source := osfsWorkspaceForHierarchy(t)
	cfg := hcConfiguredFiles(t, source)
	cfg.Workspace = source.Root()
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "nested/draft.txt"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })},
		mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"nested/draft.txt"}`)}),
		mockllm.TextTurn("done"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(t.Context(), sess.ID, "write draft")
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	has := func(r port.LLMRequest, s string) bool {
		for _, m := range r.Messages {
			if strings.Contains(m.Text, s) {
				return true
			}
		}
		return false
	}
	if !has(requests[0], "ROOT-HIERARCHY") || has(requests[0], "NESTED-HIERARCHY") || !has(requests[1], "NESTED-HIERARCHY") {
		t.Fatalf("factory delivery first=%v next=%v", requests[0].Messages, requests[1].Messages)
	}
	if !strings.Contains(requests[0].System.VolatileSuffix, "NEXT request") {
		t.Fatal("missing model-visible delivery limitation")
	}
	if _, err := os.Stat(filepath.Join(cfg.Workspace, "nested/draft.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestHierarchyFactorySelectedSeparateSource(t *testing.T) {
	execution := osfsWorkspaceForHierarchy(t)
	selected := memfs.NewWorkspace("/selected-independent")
	for name, body := range map[string]string{"AGENTS.md": "SELECTED-ROOT", "nested/AGENTS.md": "SELECTED-NESTED"} {
		if err := selected.Write(t.Context(), name, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	cfg := hcConfiguredFiles(t, selected)
	cfg.Workspace = execution.Root()
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "nested/draft.txt"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.AllowAllTools = true
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })},
		mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"nested/draft.txt"}`)}), mockllm.TextTurn("done"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	id := harnessCreate(t, built, t.Context())
	events := harnessRun(t, built, t.Context(), id, "read nested draft")
	if len(requests) != 2 {
		t.Fatalf("requests=%d, events=%v", len(requests), events)
	}
	contains := func(r port.LLMRequest, needle string) bool {
		for _, m := range r.Messages {
			if strings.Contains(m.Text, needle) {
				return true
			}
		}
		return false
	}
	if !contains(requests[0], "SELECTED-ROOT") || contains(requests[0], "SELECTED-NESTED") || !contains(requests[1], "SELECTED-NESTED") {
		t.Fatalf("selected separate source: first=%v next=%v", requests[0].Messages, requests[1].Messages)
	}
	if contains(requests[1], "NESTED-HIERARCHY") || contains(requests[1], "ROOT-HIERARCHY") {
		t.Fatal("execution-only instructions leaked into selected guidance")
	}
}

func TestHierarchyFactoryAbsentRootLoadsNestedThroughHostWrapper(t *testing.T) {
	selected := memfs.NewWorkspace("/selected-independent")
	if err := selected.Write(t.Context(), "nested/AGENTS.md", []byte("SELECTED-NESTED-ONLY")); err != nil {
		t.Fatal(err)
	}
	cfg := hcConfiguredFiles(t, selected)
	if !((policyInstructionAssembler{mode: harnessModeCombine, sources: []prompt.InstructionAssembler{
		fixedInstructionAssembler{inner: prompt.RootAssembler{Source: selected, SourceID: "source", SourcePrefix: "."}, provenance: HarnessProvenancePolicy{Fixed: "project"}, projectAdmitted: true},
	}}).TargetScoped()) {
		t.Fatal("host-selected root with no root file must retain target scope")
	}
	if err := os.MkdirAll(filepath.Join(cfg.Workspace, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "nested/draft.txt"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.AllowAllTools = true
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })},
		mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"nested/draft.txt"}`)}), mockllm.TextTurn("done"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	events := harnessRun(t, built, t.Context(), harnessCreate(t, built, t.Context()), "read nested draft")
	if len(requests) != 2 {
		t.Fatalf("requests=%d events=%v", len(requests), events)
	}
	for i, r := range requests {
		found := false
		for _, m := range r.Messages {
			found = found || strings.Contains(m.Text, "SELECTED-NESTED-ONLY")
		}
		if found != (i == 1) {
			t.Fatalf("request %d nested guidance=%v messages=%v", i, found, r.Messages)
		}
	}
}

type emptyHostInstructions struct{ calls *int }

func (emptyHostInstructions) TargetScoped() bool { return false }

func (a emptyHostInstructions) Assemble(context.Context, []string, *session.InstructionSnapshot, int) ([]session.Message, []prompt.InstructionManifest, error) {
	*a.calls++
	return nil, nil, nil
}

func TestHierarchyFactoryMixedHostSourceCachesEmptyGlobal(t *testing.T) {
	selected := memfs.NewWorkspace("/selected-independent")
	if err := selected.Write(t.Context(), "AGENTS.md", []byte("ROOT-STABLE")); err != nil {
		t.Fatal(err)
	}
	if err := selected.Write(t.Context(), "nested/AGENTS.md", []byte("SELECTED-NESTED-ONLY")); err != nil {
		t.Fatal(err)
	}
	cfg := hcConfiguredFiles(t, selected)
	var calls int
	cfg.HarnessInstructionSources[0].Bind = func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		return prompt.NewMultiAssembler(prompt.RootAssembler{Source: selected, SourceID: "source", SourcePrefix: "."}, emptyHostInstructions{calls: &calls}), nil, nil
	}
	if err := os.MkdirAll(filepath.Join(cfg.Workspace, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "nested/draft.txt"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.AllowAllTools = true
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
		requests = append(requests, r)
		if len(requests) == 1 {
			if err := selected.Write(t.Context(), "AGENTS.md", []byte("ROOT-CHANGED")); err != nil {
				t.Error(err)
			}
		}
	})}, mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"nested/draft.txt"}`)}), mockllm.TextTurn("done"), mockllm.TextTurn("again"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	id := harnessCreate(t, built, t.Context())
	harnessRun(t, built, t.Context(), id, "read nested draft")
	if calls != 1 {
		t.Fatalf("empty host-global contributor assembled %d times in one run", calls)
	}
	if len(requests) != 2 || !strings.Contains(fmt.Sprint(requests[0].Messages), "ROOT-STABLE") || !strings.Contains(fmt.Sprint(requests[1].Messages), "ROOT-STABLE") || !strings.Contains(fmt.Sprint(requests[1].Messages), "SELECTED-NESTED-ONLY") || strings.Contains(fmt.Sprint(requests[1].Messages), "ROOT-CHANGED") {
		t.Fatalf("project guidance was not session-stable across scope discovery: %v", requests)
	}
	harnessRun(t, built, t.Context(), id, "another task")
	if calls != 2 {
		t.Fatalf("host-global contributor did not refresh for next run: %d calls", calls)
	}
}

func TestDefaultSourceChildAndConversationForkInstructionSnapshots(t *testing.T) {
	ws := osfsWorkspaceForHierarchy(t)
	if err := os.WriteFile(filepath.Join(ws.Root(), "nested/draft.txt"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Workspace: ws.Root(), UseMock: true, TrustProject: true, Headless: true, NoSoul: true, NoShell: true, AllowAllTools: true, UserModelDir: t.TempDir()}
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
		requests = append(requests, r)
		// The parent has loaded nested guidance by its second request. Change the
		// source before forking to distinguish its snapshot from a fresh child.
		if len(requests) == 2 {
			if err := os.WriteFile(filepath.Join(ws.Root(), "nested/AGENTS.md"), []byte("NEW-NESTED"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	})},
		mockllm.ToolCallTurn(session.ToolCall{ID: "parent-read", Name: "Read", Args: json.RawMessage(`{"path":"nested/draft.txt"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "fork", Name: "Subagent", Args: json.RawMessage(`{"prompt":"inspect","fork":true}`)}),
		mockllm.TextTurn("fork done"),
		mockllm.ToolCallTurn(session.ToolCall{ID: "fresh", Name: "Subagent", Args: json.RawMessage(`{"prompt":"inspect"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "child-read", Name: "Read", Args: json.RawMessage(`{"path":"nested/draft.txt"}`)}),
		mockllm.TextTurn("fresh done"),
		mockllm.TextTurn("parent done"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	events := harnessRun(t, built, t.Context(), harnessCreate(t, built, t.Context()), "inspect")
	if len(requests) != 7 {
		t.Fatalf("requests=%d events=%v", len(requests), events)
	}
	has := func(i int, text string) bool { return strings.Contains(fmt.Sprint(requests[i].Messages), text) }
	if !has(1, "NESTED-HIERARCHY") || !has(2, "NESTED-HIERARCHY") || has(2, "NEW-NESTED") || has(4, "NESTED-HIERARCHY") || !has(5, "NEW-NESTED") || has(5, "NESTED-HIERARCHY") || !has(6, "NESTED-HIERARCHY") || has(6, "NEW-NESTED") {
		t.Fatalf("parent/fork/fresh snapshots: parent=%v fork=%v fresh=%v next=%v resumed parent=%v", requests[1].Messages, requests[2].Messages, requests[4].Messages, requests[5].Messages, requests[6].Messages)
	}
}

func TestHierarchyNoFSChildKeepsOnlyStartingGuidance(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprintf("configured=%v", configured), func(t *testing.T) {
			ws := osfsWorkspaceForHierarchy(t)
			cfg := Config{Workspace: ws.Root(), UseMock: true, TrustProject: true, Headless: true, NoSoul: true, NoShell: true, AllowAllTools: true, UserModelDir: t.TempDir()}
			prefix := "."
			if configured {
				// The execution root is below the source root. Both ancestors are
				// starting guidance, although only one has source-relative path '.'.
				prefix = "website"
				cfg = hcConfiguredFiles(t, ws)
				cfg.NoSoul, cfg.NoShell, cfg.AllowAllTools = true, true, true
				cfg.HarnessInstructionSources[0].Bind = func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
					return prompt.RootAssembler{Source: ws, SourceID: "source", SourcePrefix: prefix}, nil, nil
				}
				if err := ws.Write(t.Context(), "website/AGENTS.md", []byte("START-OLD")); err != nil {
					t.Fatal(err)
				}
			}
			if err := ws.Write(t.Context(), filepath.Join(prefix, "nested/AGENTS.md"), []byte("NESTED-POISON")); err != nil {
				t.Fatal(err)
			}
			var requests []port.LLMRequest
			cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
				requests = append(requests, r)
				if len(requests) == 1 {
					if err := ws.Write(t.Context(), "AGENTS.md", []byte("ROOT-NEW")); err != nil {
						t.Fatal(err)
					}
					if configured {
						if err := ws.Write(t.Context(), "website/AGENTS.md", []byte("START-NEW")); err != nil {
							t.Fatal(err)
						}
					}
				}
			})},
				mockllm.ToolCallTurn(session.ToolCall{ID: "delegate", Name: "Subagent", Args: json.RawMessage(`{"prompt":"inspect","fork":true}`)}),
				mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"nested/file.txt"}`)}),
				mockllm.TextTurn("child done"), mockllm.TextTurn("parent done"))
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			parent, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS)
			if err != nil {
				t.Fatal(err)
			}
			events := harnessRun(t, built, t.Context(), parent.ID, "delegate")
			if len(requests) != 4 {
				t.Fatalf("requests=%d events=%v", len(requests), events)
			}
			for _, i := range []int{1, 2} {
				text := harnessRequestText(requests[i])
				if !strings.Contains(text, "ROOT-HIERARCHY") || !strings.Contains(text, "scope mapping unavailable") || configured && !strings.Contains(text, "START-OLD") || strings.Contains(text, "NESTED-POISON") || strings.Contains(text, "ROOT-NEW") || strings.Contains(text, "START-NEW") {
					t.Fatalf("no-FS child lost starting guidance or gained nested mapping: %s", text)
				}
				for _, spec := range requests[i].Tools {
					if spec.Name == "Read" {
						t.Fatal("no-FS child gained file tools")
					}
				}
			}
		})
	}
}

func TestHierarchyDefaultSourceChildPlacementMapping(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TEMPLATE_DIR", t.TempDir())
	for _, sameWorkspace := range []bool{false, true} {
		t.Run(fmt.Sprintf("sameWorkspace=%v", sameWorkspace), func(t *testing.T) {
			selected := osfsWorkspaceForHierarchy(t)
			execution := selected
			if !sameWorkspace {
				wtInitRepo(t, selected.Root())
				wtRunGit(t, selected.Root(), "commit", "--allow-empty", "-m", "test fixture\n\nCo-authored-by: Mecatl <noreply@mecatl.dev>")
				otherRoot := filepath.Join(t.TempDir(), "other")
				wtRunGit(t, selected.Root(), "worktree", "add", "-b", "other", otherRoot)
				var err error
				execution, err = osfs.NewWorkspace(otherRoot)
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"AGENTS.md", "nested/CLAUDE.md"} {
					if err := execution.Write(t.Context(), name, []byte("UNSELECTED-CHECKOUT-GUIDANCE")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := execution.Write(t.Context(), "nested/draft.txt", []byte("execution draft")); err != nil {
				t.Fatal(err)
			}
			cfg := Config{Workspace: selected.Root(), UseMock: true, TrustProject: true, Headless: true, NoSoul: true, Shell: "/bin/sh", AllowAllTools: true, UserModelDir: t.TempDir()}
			var requests []port.LLMRequest
			cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })},
				mockllm.ToolCallTurn(session.ToolCall{ID: "parent-read", Name: "Read", Args: json.RawMessage(`{"path":"nested/draft.txt"}`)}),
				mockllm.ToolCallTurn(session.ToolCall{ID: "delegate", Name: "Subagent", Args: json.RawMessage(`{"prompt":"inspect","fork":true}`)}),
				mockllm.ToolCallTurn(session.ToolCall{ID: "child-read", Name: "Read", Args: json.RawMessage(`{"path":"nested/draft.txt"}`)}),
				mockllm.TextTurn("child done"), mockllm.TextTurn("parent done"))
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			// Supply an exact server-owned placement without a custom provider, which
			// would independently disable default mapping before the session-root gate.
			ref := configuredLocalPlacementRef(execution.Root())
			if !sameWorkspace {
				ref.Revision = wtHead(t, execution.Root())
			}
			placement := server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, execution, memledger.New(), nil)}
			parent, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithPlacementBinding(placement))
			if err != nil {
				t.Fatal(err)
			}
			events := harnessRun(t, built, t.Context(), parent.ID, "inspect")
			if len(requests) != 5 {
				t.Fatalf("requests=%d events=%v", len(requests), events)
			}
			if strings.Contains(harnessRequestText(requests[0]), "NESTED-HIERARCHY") || !strings.Contains(harnessRequestText(requests[1]), "NESTED-HIERARCHY") || !strings.Contains(harnessRequestText(requests[4]), "NESTED-HIERARCHY") {
				t.Fatal("parent did not discover and retain selected nested guidance before delegation")
			}
			for _, i := range []int{2, 3} {
				text := harnessRequestText(requests[i])
				if strings.Count(text, "ROOT-HIERARCHY") != 1 || strings.Contains(text, "NESTED-HIERARCHY") != sameWorkspace || strings.Contains(text, "scope mapping unavailable") == sameWorkspace || strings.Contains(text, "UNSELECTED-CHECKOUT-GUIDANCE") {
					t.Fatalf("child request %d lost starting guidance or used the wrong mapping: %s", i, text)
				}
				if !requestHasTool(requests[i], "Read") {
					t.Fatal("workspace child lost file tools")
				}
			}
			readSucceeded := false
			for _, message := range requests[3].Messages {
				if result := message.ToolResult; result != nil && result.CallID == "child-read" {
					readSucceeded = !result.IsError
				}
			}
			if !readSucceeded {
				t.Fatal("workspace child could not read its execution file")
			}
		})
	}
}

func osfsWorkspaceForHierarchy(t *testing.T) *osfs.Workspace {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"AGENTS.md": "ROOT-HIERARCHY", "nested/CLAUDE.md": "NESTED-HIERARCHY"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestHierarchyUnmappedChildWarningRealRun(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(t.Context(), "AGENTS.md", []byte("root guidance")); err != nil {
		t.Fatal(err)
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "test"}
	sess := session.New("unmapped", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	env := tool.MustEnvironment(ref, ws, memledger.New(), nil)
	var request port.LLMRequest
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { request = r })}, mockllm.TextTurn("done"))
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Instructions: childUnmappedInstructions{source: prompt.RootAssembler{Source: ws, SourceID: "root", SourcePrefix: "."}}})
	warnings := 0
	for ev := range engine.Run(t.Context(), sess, env, agent.RunRequest{Text: "go"}).Events() {
		if ev.Type == session.EvHook {
			if ev.Hook == nil || ev.Hook.Decision != session.HookAdvisory || ev.Text != "Project instructions: child workspace scope mapping unavailable; nested guidance is not automatically loaded. Selected root guidance remains available; ordinary tools remain available." {
				t.Fatalf("unmapped warning: %+v", ev)
			}
			warnings++
		}
	}
	if warnings != 1 || !strings.Contains(fmt.Sprint(request.Messages), "scope mapping unavailable") || !strings.Contains(fmt.Sprint(request.Messages), "root guidance") {
		t.Fatalf("warnings=%d model request=%v", warnings, request.Messages)
	}
}

func TestHierarchyUnmappedPartialRoot(t *testing.T) {
	ws := &policyFaultWorkspace{Workspace: memfs.NewWorkspace("/selected")}
	harnessSeed(t, ws.Workspace, "AGENTS.md", "ADMITTED-ROOT")
	state := session.InstructionSnapshot{
		Directories: []string{"nested"},
		Scopes:      []session.InstructionScope{{SourceID: "source", Directory: "bad/nested", File: "bad/nested/AGENTS.md", Text: "NESTED-POISON", Examined: true}},
	}
	a := childUnmappedInstructions{source: fixedInstructionAssembler{
		inner:      prompt.RootAssembler{Source: ws, SourceID: "source", SourcePrefix: "bad"},
		provenance: HarnessProvenancePolicy{Fixed: "project"}, projectAdmitted: true,
	}}
	messages, rows, err := a.Assemble(t.Context(), []string{"nested"}, &state, 65536)
	text := fmt.Sprint(messages)
	if !errors.Is(err, os.ErrPermission) || len(messages) != len(rows) || !strings.Contains(text, "ADMITTED-ROOT") || !strings.Contains(text, "scope mapping unavailable") || strings.Contains(text, "NESTED-POISON") || strings.Contains(text, "permission denied") {
		t.Fatalf("partial unmapped result: %v rows=%v err=%v", messages, rows, err)
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/selected", Revision: "test"}
	sess := session.New("partial-unmapped", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	var request port.LLMRequest
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { request = r })}, mockllm.TextTurn("done"))
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Instructions: a})
	unmapped := false
	for ev := range engine.Run(t.Context(), sess, tool.MustEnvironment(ref, ws, memledger.New(), nil), agent.RunRequest{Text: "go"}).Events() {
		if ev.Type == session.EvHook && ev.Hook != nil && ev.Hook.Decision == session.HookAdvisory && strings.Contains(ev.Text, "scope mapping unavailable") {
			unmapped = true
		}
	}
	if !unmapped || !strings.Contains(harnessRequestText(request), "ADMITTED-ROOT") || !strings.Contains(harnessRequestText(request), "scope mapping unavailable") {
		t.Fatalf("warning=%v request=%v", unmapped, request.Messages)
	}
}

type failingRefreshInstructions struct{ calls int }

func (*failingRefreshInstructions) TargetScoped() bool { return true }

func (a *failingRefreshInstructions) Assemble(context.Context, []string, *session.InstructionSnapshot, int) ([]session.Message, []prompt.InstructionManifest, error) {
	a.calls++
	if a.calls > 1 {
		return nil, nil, errors.New("PRIVATE-SOURCE-FAILURE")
	}
	return []session.Message{session.NewUserMessage("ADMITTED-CUSTOM-GUIDANCE")}, []prompt.InstructionManifest{{Kind: prompt.InstructionKindTurn0, Provenance: prompt.InstructionProvenanceProject, HasGuidance: true}}, nil
}

func TestHierarchyConfiguredNilRefreshRetainsGuidance(t *testing.T) {
	for _, mode := range []string{harnessModeCombine, harnessModeReplace} {
		t.Run(mode, func(t *testing.T) {
			kinds := harnessEmptyKinds()
			kinds.Instructions = permconfig.HarnessContextKind{Sources: []string{"custom", "lower"}, Mode: mode}
			cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"custom", "lower"}, Kinds: kinds})
			cfg.AllowAllTools, cfg.NoSoul, cfg.NoShell = true, true, true
			source := &failingRefreshInstructions{}
			lower := &policyCountWorkspace{Workspace: memfs.NewWorkspace("/lower")}
			harnessSeed(t, lower.Workspace, "AGENTS.md", "LOWER-GUIDANCE")
			cfg.HarnessInstructionSources = []HarnessSourceRegistration[prompt.InstructionAssembler]{
				{ID: "custom", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
					return source, nil, nil
				}},
				{ID: "lower", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
					return prompt.RootAssembler{Source: lower, SourceID: "lower", SourcePrefix: "."}, nil, nil
				}},
			}
			var turns []mockllm.Turn
			for _, dir := range []string{"one", "two", "three"} {
				if err := os.MkdirAll(filepath.Join(cfg.Workspace, dir), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfg.Workspace, dir, "file.txt"), []byte("draft"), 0600); err != nil {
					t.Fatal(err)
				}
				turns = append(turns, mockllm.ToolCallTurn(session.ToolCall{ID: session.ToolCallID(dir), Name: "Read", Args: json.RawMessage(fmt.Sprintf(`{"path":%q}`, dir+"/file.txt"))}))
			}
			turns = append(turns, mockllm.TextTurn("done"), mockllm.TextTurn("next run done"))
			var requests []port.LLMRequest
			var lowerReads []int
			cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
				requests = append(requests, r)
				lowerReads = append(lowerReads, lower.reads)
			})}, turns...)
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			id := harnessCreate(t, built, t.Context())
			events := harnessRun(t, built, t.Context(), id, "inspect")
			if len(requests) != 4 || source.calls != 4 {
				t.Fatalf("requests=%d assemblies=%d events=%v", len(requests), source.calls, events)
			}
			for i, request := range requests {
				text := harnessRequestText(request)
				if strings.Count(text, "ADMITTED-CUSTOM-GUIDANCE") != 1 || strings.Contains(text, "PRIVATE-SOURCE-FAILURE") {
					t.Fatalf("request %d lost/duplicated guidance or leaked error: %s", i, text)
				}
				wantWarnings := 0
				if i > 0 {
					wantWarnings = 1
				}
				if strings.Count(text, "additional guidance unavailable") != wantWarnings {
					t.Fatalf("request %d warning count differs from %d: %s", i, wantWarnings, text)
				}
				if lowerReads[i] != lowerReads[0] || strings.Contains(text, "LOWER-GUIDANCE") != (mode == harnessModeCombine) {
					t.Fatalf("request %d reread lower source or changed admitted guidance: %s", i, text)
				}
			}
			if mode == harnessModeReplace && lower.reads != 0 {
				t.Fatal("replacement failure consulted lower source")
			}
			warned := false
			for _, ev := range events {
				if ev.Type == session.EvHook && ev.Hook != nil && ev.Hook.Phase == "ProjectInstructions" {
					warned = warned || strings.Contains(ev.Text, "unavailable")
					if strings.Contains(ev.Text, "PRIVATE-SOURCE-FAILURE") {
						t.Fatalf("unsafe client warning: %+v", ev)
					}
				}
			}
			if !warned {
				t.Fatal("refresh failure did not warn the client")
			}
			harnessRun(t, built, t.Context(), id, "another task")
			if len(requests) != 5 || source.calls != 5 {
				t.Fatalf("next run: requests=%d assemblies=%d", len(requests), source.calls)
			}
			if strings.Contains(harnessRequestText(requests[4]), "ADMITTED-CUSTOM-GUIDANCE") {
				t.Fatal("failed source inherited another run's contributor cache")
			}
		})
	}
}

func TestHierarchyConfiguredFactoryPartialRefresh(t *testing.T) {
	for _, mode := range []string{harnessModeCombine, harnessModeReplace} {
		for _, faultFirst := range []bool{true, false} {
			if mode == harnessModeReplace && !faultFirst {
				continue // A successful higher replacement never consults the lower source.
			}
			t.Run(fmt.Sprintf("%s/faultFirst=%v", mode, faultFirst), func(t *testing.T) {
				fault := &policyFaultWorkspace{Workspace: memfs.NewWorkspace("/selected-fault")}
				other := &policyCountWorkspace{Workspace: memfs.NewWorkspace("/selected-other")}
				for _, item := range []struct {
					ws         *memfs.Workspace
					name, body string
				}{
					{fault.Workspace, "AGENTS.md", "FAULT-ROOT"},
					{fault.Workspace, "prior/AGENTS.md", "FAULT-PRIOR"},
					{fault.Workspace, "good/AGENTS.md", "FAULT-GOOD"},
					{other.Workspace, "AGENTS.md", "OTHER-ROOT"},
					{other.Workspace, "prior/AGENTS.md", "OTHER-PRIOR"},
					{other.Workspace, "good/AGENTS.md", "OTHER-GOOD"},
				} {
					harnessSeed(t, item.ws, item.name, item.body)
				}
				ids := []string{"fault", "other"}
				if !faultFirst {
					ids = []string{"other", "fault"}
				}
				kinds := harnessEmptyKinds()
				kinds.Instructions = permconfig.HarnessContextKind{Sources: ids, Mode: mode}
				cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: ids, Kinds: kinds})
				cfg.AllowAllTools, cfg.NoSoul, cfg.NoShell = true, true, true
				cfg.HarnessInstructionSources = []HarnessSourceRegistration[prompt.InstructionAssembler]{
					{ID: "fault", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
						return prompt.NewMultiAssembler(prompt.RootAssembler{Source: fault, SourceID: "fault", SourcePrefix: "."}, hcAssembler("FAULT-GLOBAL")), nil, nil
					}},
					{ID: "other", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
						return prompt.RootAssembler{Source: other, SourceID: "other", SourcePrefix: "."}, nil, nil
					}},
				}
				for _, dir := range []string{"prior", "good", "bad"} {
					if err := os.MkdirAll(filepath.Join(cfg.Workspace, dir), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(cfg.Workspace, dir, "file.txt"), []byte("draft"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(cfg.Workspace, "AGENTS.md"), []byte("UNAUTHORIZED-EXECUTION"), 0600); err != nil {
					t.Fatal(err)
				}
				var requests []port.LLMRequest
				var otherReads []int
				cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
					requests = append(requests, r)
					otherReads = append(otherReads, other.reads)
					if len(requests) == 2 {
						harnessSeed(t, fault.Workspace, "AGENTS.md", "CHANGED-ROOT")
						harnessSeed(t, fault.Workspace, "prior/AGENTS.md", "CHANGED-PRIOR")
					}
				})},
					mockllm.ToolCallTurn(session.ToolCall{ID: "prior", Name: "Read", Args: json.RawMessage(`{"path":"prior/file.txt"}`)}),
					mockllm.ToolCallTurn(
						session.ToolCall{ID: "good", Name: "Read", Args: json.RawMessage(`{"path":"good/file.txt"}`)},
						session.ToolCall{ID: "bad", Name: "Read", Args: json.RawMessage(`{"path":"bad/file.txt"}`)}),
					mockllm.ToolCallTurn(session.ToolCall{ID: "again", Name: "Read", Args: json.RawMessage(`{"path":"bad/file.txt"}`)}),
					mockllm.TextTurn("done"))
				built, err := buildIsolated(t, t.Context(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer built.Close()
				events := harnessRun(t, built, t.Context(), harnessCreate(t, built, t.Context()), "inspect")
				if len(requests) != 4 {
					t.Fatalf("requests=%d events=%v", len(requests), events)
				}
				for i, r := range requests {
					text := harnessRequestText(r)
					for _, marker := range []string{"FAULT-ROOT", "FAULT-GLOBAL"} {
						if !strings.Contains(text, marker) {
							t.Fatalf("request %d lost %s: %s", i, marker, text)
						}
					}
					if i > 0 && !strings.Contains(text, "FAULT-PRIOR") || i > 1 && (!strings.Contains(text, "FAULT-GOOD") || !strings.Contains(text, "guidance unavailable")) {
						t.Fatalf("request %d lost successful scopes or warning: %s", i, text)
					}
					if strings.Contains(text, "UNAUTHORIZED") || strings.Contains(text, "CHANGED-") || strings.Contains(text, "permission denied") || strings.Contains(text, "/selected-fault") {
						t.Fatalf("request %d leaked fallback, reread, or raw error: %s", i, text)
					}
					if mode == harnessModeReplace && strings.Contains(text, "OTHER-") {
						t.Fatalf("replacement consulted lower source: %s", text)
					}
					if mode == harnessModeCombine && (!strings.Contains(text, "OTHER-ROOT") || i > 0 && !strings.Contains(text, "OTHER-PRIOR")) {
						t.Fatalf("request %d lost already admitted lower guidance: %s", i, text)
					}
				}
				if fault.faultReads != 1 || fault.fallbackReads != 0 || faultFirst && otherReads[2] != otherReads[1] || mode == harnessModeReplace && other.reads != 0 {
					t.Fatalf("fault reads=%d fallback=%d other reads=%v", fault.faultReads, fault.fallbackReads, otherReads)
				}
				if strings.Contains(harnessRequestText(requests[2]), "OTHER-GOOD") != (!faultFirst && mode == harnessModeCombine) {
					t.Fatalf("refresh did not respect configured source order: %v", requests[2].Messages)
				}
				warned := false
				for _, ev := range events {
					if ev.Type == session.EvHook && ev.Hook != nil && ev.Hook.Phase == "ProjectInstructions" {
						warned = warned || strings.Contains(ev.Text, "unavailable")
						if strings.Contains(ev.Text, "permission denied") || strings.Contains(ev.Text, "/selected-fault") {
							t.Fatalf("unsafe client warning: %+v", ev)
						}
					}
				}
				if !warned {
					t.Fatal("partial refresh did not warn the client")
				}
			})
		}
	}
}

func TestHierarchyReplacementFaultKeepsPartialSourceWithoutLowerFallback(t *testing.T) {
	high := &policyFaultWorkspace{Workspace: memfs.NewWorkspace("/high")}
	low := &policyCountWorkspace{Workspace: memfs.NewWorkspace("/low")}
	for _, fixture := range []struct {
		ws         *memfs.Workspace
		name, body string
	}{
		{high.Workspace, "AGENTS.md", "ROOT-HIGH"},
		{high.Workspace, "good/AGENTS.md", "GOOD-HIGH"},
		{low.Workspace, "AGENTS.md", "LOW-POISON"},
	} {
		if err := fixture.ws.Write(t.Context(), fixture.name, []byte(fixture.body)); err != nil {
			t.Fatal(err)
		}
	}
	policy := policyInstructionAssembler{mode: harnessModeReplace, sources: []prompt.InstructionAssembler{
		prompt.RootAssembler{Source: high, SourceID: "high", SourcePrefix: "."},
		prompt.RootAssembler{Source: low, SourceID: "low", SourcePrefix: "."},
	}}
	state := &session.InstructionSnapshot{}
	messages, rows, err := policy.Assemble(t.Context(), []string{"good", "bad"}, state, 65536)
	if !errors.Is(err, os.ErrPermission) || len(messages) != len(rows) || len(rows) != 3 || !rows[0].HasGuidance || !rows[1].HasGuidance || rows[2].HasGuidance || !strings.Contains(fmt.Sprint(messages), "GOOD-HIGH") || strings.Contains(fmt.Sprint(messages), "LOW-POISON") || low.reads != 0 || high.fallbackReads != 0 {
		t.Fatalf("replacement fault: messages=%v rows=%v low reads=%d fallback reads=%d err=%v", messages, rows, low.reads, high.fallbackReads, err)
	}
	_, _, err = policy.Assemble(t.Context(), []string{"good", "bad"}, state, 65536)
	if err != nil || high.faultReads != 1 || low.reads != 0 {
		t.Fatalf("cached fault caused retry or lower fallback: faults=%d lower=%d err=%v", high.faultReads, low.reads, err)
	}
}

type policyFaultWorkspace struct {
	*memfs.Workspace
	faultReads, fallbackReads int
}

func (w *policyFaultWorkspace) Read(ctx context.Context, name string) ([]byte, error) {
	if name == "bad/AGENTS.md" {
		w.faultReads++
		return nil, os.ErrPermission
	}
	if name == "bad/CLAUDE.md" {
		w.fallbackReads++
	}
	return w.Workspace.Read(ctx, name)
}

type policyCountWorkspace struct {
	*memfs.Workspace
	reads int
}

func (w *policyCountWorkspace) Read(ctx context.Context, name string) ([]byte, error) {
	w.reads++
	return w.Workspace.Read(ctx, name)
}

func TestHierarchyIncompleteReplacementDisclosesSuppressedSource(t *testing.T) {
	high, low := memfs.NewWorkspace("/ws"), memfs.NewWorkspace("/ws")
	if err := high.Write(t.Context(), "AGENTS.md", []byte(strings.Repeat("H", 100))); err != nil {
		t.Fatal(err)
	}
	if err := low.Write(t.Context(), "AGENTS.md", []byte("LOWER-CONTRIBUTION")); err != nil {
		t.Fatal(err)
	}
	policy := policyInstructionAssembler{mode: harnessModeReplace, sources: []prompt.InstructionAssembler{
		prompt.RootAssembler{Source: high, SourceID: "high", SourcePrefix: "."},
		prompt.RootAssembler{Source: low, SourceID: "low", SourcePrefix: "."},
	}}
	messages, rows, err := policy.Assemble(t.Context(), []string{"."}, &session.InstructionSnapshot{}, 32)
	if err != nil || len(messages) != 2 || len(rows) != 2 || !rows[0].Partial || rows[1].HasGuidance || !strings.Contains(messages[1].Text, "lower-priority sources were not loaded") || strings.Contains(fmt.Sprint(messages), "LOWER-CONTRIBUTION") {
		t.Fatalf("incomplete replacement: messages=%v rows=%v err=%v", messages, rows, err)
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "test"}
	sess := session.New("replacement", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	var request port.LLMRequest
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { request = r })}, mockllm.TextTurn("done"))
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Instructions: policy, ProjectInstructionMaxBytes: 32})
	warnings := 0
	for ev := range engine.Run(t.Context(), sess, tool.MustEnvironment(ref, high, memledger.New(), nil), agent.RunRequest{Text: "go"}).Events() {
		if ev.Type == session.EvHook && ev.Text == "Project instructions: replacement guidance incomplete; lower-priority sources were not loaded." && ev.Hook != nil && ev.Hook.Decision == session.HookAdvisory {
			warnings++
		}
	}
	if warnings != 1 || !strings.Contains(fmt.Sprint(request.Messages), "lower-priority sources were not loaded") {
		t.Fatalf("replacement warnings=%d request=%v", warnings, request.Messages)
	}
	missing := memfs.NewWorkspace("/ws")
	policy.sources[0] = prompt.RootAssembler{Source: missing, SourceID: "missing", SourcePrefix: "."}
	messages, _, err = policy.Assemble(t.Context(), []string{"."}, &session.InstructionSnapshot{}, 128)
	if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Text, "LOWER-CONTRIBUTION") {
		t.Fatalf("empty higher source wrongly won replace: %v %v", messages, err)
	}
}

func TestHierarchyWholeSourceReplacement(t *testing.T) {
	a, b := memfs.NewWorkspace("/ws"), memfs.NewWorkspace("/ws")
	if err := a.Write(t.Context(), "website/AGENTS.md", []byte("WEBSITE-ONLY")); err != nil {
		t.Fatal(err)
	}
	if err := b.Write(t.Context(), "AGENTS.md", []byte("ROOT-LOWER")); err != nil {
		t.Fatal(err)
	}
	policy := policyInstructionAssembler{mode: harnessModeReplace, sources: []prompt.InstructionAssembler{
		fixedInstructionAssembler{inner: prompt.RootAssembler{Source: a, SourceID: "a", SourcePrefix: "."}, provenance: HarnessProvenancePolicy{Fixed: "project"}, projectAdmitted: true},
		fixedInstructionAssembler{inner: prompt.RootAssembler{Source: b, SourceID: "b", SourcePrefix: "."}, provenance: HarnessProvenancePolicy{Fixed: "project"}, projectAdmitted: true},
	}}
	mixed, rows, err := prompt.AssembleWithManifest(context.Background(), policy, []string{"website", "services"}, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(rows) != 1 || len(mixed) != 1 || rows[0].Provenance != prompt.InstructionProvenanceProject || !strings.Contains(mixed[0].Text, `scope: "website"`) || !strings.Contains(mixed[0].Text, "WEBSITE-ONLY") {
		t.Fatalf("mixed: %v %v %v", mixed, rows, err)
	}
	for _, m := range mixed {
		if strings.Contains(m.Text, "ROOT-LOWER") {
			t.Fatal("replace selected lower source for sibling")
		}
	}
	services, _, err := prompt.AssembleWithManifest(t.Context(), policy, []string{"services"}, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(services) != 1 || !strings.Contains(services[0].Text, "ROOT-LOWER") {
		t.Fatalf("services-only: %v %v", services, err)
	}
}

func TestHierarchyPointerCompositionKeepsLeaves(t *testing.T) {
	oldRoot, selectedRoot := memfs.NewWorkspace("/old"), memfs.NewWorkspace("/selected")
	for _, item := range []struct {
		ws         *memfs.Workspace
		path, text string
	}{
		{oldRoot, "AGENTS.md", "OLD-ROOT"}, {selectedRoot, "AGENTS.md", "NEW-ROOT"},
		{selectedRoot, "nested/AGENTS.md", "NEW-NESTED"},
	} {
		if err := item.ws.Write(t.Context(), item.path, []byte(item.text)); err != nil {
			t.Fatal(err)
		}
	}
	oldRules := frozenHarnessRules{rules: []prompt.Rule{{Name: "old", Body: "OLD-RULE"}}}
	newRules := frozenHarnessRules{rules: []prompt.Rule{{Name: "new", Body: "NEW-RULE"}}}
	base := &prompt.MultiAssembler{Assemblers: []prompt.InstructionAssembler{
		&prompt.RootAssembler{Source: oldRoot, SourceID: "old", SourcePrefix: "."}, hcAssembler("unrelated"), &prompt.RulesAssembler{Src: oldRules},
	}}
	replaced := replaceHarnessInstructions(base, Config{harnessInstructions: &prompt.RootAssembler{Source: selectedRoot, SourceID: "selected", SourcePrefix: "."}, harnessRules: newRules})
	messages, rows, err := prompt.AssembleWithManifest(t.Context(), replaced, []string{"nested"}, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(messages) != 4 || len(messages) != len(rows) {
		t.Fatalf("pointer composition: %v %v %v", messages, rows, err)
	}
	text := fmt.Sprint(messages)
	if !strings.Contains(text, "NEW-ROOT") || !strings.Contains(text, "NEW-NESTED") || !strings.Contains(text, "NEW-RULE") || !strings.Contains(text, "unrelated") || strings.Contains(text, "OLD-ROOT") || strings.Contains(text, "OLD-RULE") {
		t.Fatalf("replaced pointer leaves incorrectly: %s", text)
	}
	for i, want := range []string{prompt.InstructionProvenanceProject, prompt.InstructionProvenanceProject, prompt.InstructionProvenanceCustom, prompt.InstructionProvenanceRules} {
		if rows[i].Provenance != want {
			t.Fatalf("row %d provenance=%q want=%q", i, rows[i].Provenance, want)
		}
	}
}

func TestHierarchyOperatorCapReachesSharedSessionAndChildDeps(t *testing.T) {
	selected := memfs.NewWorkspace("/selected")
	if err := selected.Write(t.Context(), "AGENTS.md", []byte(strings.Repeat("X", 256))); err != nil {
		t.Fatal(err)
	}
	cfg := hcConfiguredFiles(t, selected)
	settings, err := os.ReadFile(cfg.PermissionConfigs[0])
	if err != nil {
		t.Fatal(err)
	}
	settings = []byte(strings.Replace(string(settings), "harness_context:\n", "harness_context:\n  project_instruction_max_bytes: 128\n", 1))
	if err := os.WriteFile(cfg.PermissionConfigs[0], settings, 0o600); err != nil {
		t.Fatal(err)
	}
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.TextTurn("done"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	harnessRun(t, built, t.Context(), harnessCreate(t, built, t.Context()), "task")
	if len(requests) != 1 || !strings.Contains(fmt.Sprint(requests[0].Messages), strings.Repeat("X", 100)) || strings.Contains(fmt.Sprint(requests[0].Messages), strings.Repeat("X", 129)) {
		t.Fatalf("operator cap did not bound the selected source: requests=%v", requests)
	}
	cfg.ProjectInstructionMaxBytes = 128
	deps := childEngineDepsForProvider(cfg, "task", cfg.MockProvider, testProviderModel("mock"), fixedDefaultWindow, tool.NewCatalog(), prompt.Config{}, nil)
	if deps.ProjectInstructionMaxBytes != 128 {
		t.Fatalf("provider child cap=%d", deps.ProjectInstructionMaxBytes)
	}
	plain := childEngineDeps(cfg, "task", cfg.MockProvider, testProviderModel("mock"), tool.NewCatalog(), fixedDefaultWindow, prompt.Config{}, nil)
	if plain.ProjectInstructionMaxBytes != 128 {
		t.Fatalf("default child cap=%d", plain.ProjectInstructionMaxBytes)
	}
}

func TestHierarchyBuildRejectsNegativeProjectInstructionCap(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), UserModelDir: t.TempDir(), UseMock: true, ProjectInstructionMaxBytes: -1}
	if built, err := buildIsolated(t, t.Context(), cfg); err == nil {
		built.Close()
		t.Fatal("negative project instruction cap accepted")
	}
}

var _ prompt.InstructionAssembler = prompt.RootAssembler{}
