package app

import (
	"context"
	"encoding/json"
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
