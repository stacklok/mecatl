package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/rulesfs"
	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/adapter/sourceconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type harnessFileInstructions struct {
	source tool.Workspace
	file   string
}

func (s harnessFileInstructions) Assemble(ctx context.Context) ([]session.Message, error) {
	data, err := s.source.Read(ctx, s.file)
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return nil, nil
	}
	return []session.Message{session.NewUserMessage(body)}, nil
}

func harnessHybridConfig(t *testing.T, includeRepository bool, instructionMode string) (Config, string) {
	t.Helper()
	kinds := harnessEmptyKinds()
	enabled := []string{"deployment", "organization"}
	kinds.Instructions = permconfig.HarnessContextKind{Sources: []string{"deployment", "organization"}, Mode: instructionMode}
	kinds.Rules = permconfig.HarnessContextKind{Sources: []string{"organization"}, Mode: instructionMode}
	kinds.Skills = permconfig.HarnessContextKind{Sources: []string{"organization"}, Mode: "combine"}
	if includeRepository {
		enabled = append(enabled, "repository")
		kinds.Instructions.Sources = append(kinds.Instructions.Sources, "repository")
		kinds.Rules.Sources = append(kinds.Rules.Sources, "repository")
		kinds.Skills.Sources = append(kinds.Skills.Sources, "repository")
		kinds.Skills.Overrides = []permconfig.HarnessContextOverride{{Name: "review", Winner: "repository", Replaces: []string{"organization"}}}
	}
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: enabled, Kinds: kinds})
	cfg.AllowAllTools = true
	deployment := t.TempDir()
	if err := os.WriteFile(filepath.Join(deployment, "policy.md"), []byte("DEPLOYMENT-INSTRUCTIONS"), 0o600); err != nil {
		t.Fatal(err)
	}
	deployWS, err := osfs.NewWorkspace(deployment)
	if err != nil {
		t.Fatal(err)
	}
	repo := cfg.Workspace
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("REPOSITORY-INSTRUCTIONS"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "data.txt"), []byte("REPOSITORY-DATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	repoWS, err := osfs.NewWorkspace(repo)
	if err != nil {
		t.Fatal(err)
	}
	rulesDir := filepath.Join(repo, ".mecatl/rules")
	if err := os.MkdirAll(rulesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "repo.md"), []byte("REPOSITORY-RULE"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(repo, ".mecatl/skills")
	writeSkill(t, skillDir, "review", "repository review", "REPOSITORY-SKILL")
	cfg.HarnessInstructionSources = []HarnessSourceRegistration[prompt.InstructionAssembler]{
		{ID: "deployment", Provenance: HarnessProvenancePolicy{Fixed: "user"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			return harnessFileInstructions{source: deployWS, file: "policy.md"}, nil, nil
		}},
		{ID: "organization", Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			return hcAssembler("ORGANIZATION-INSTRUCTIONS"), nil, nil
		}},
		{ID: "repository", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			return prompt.RootAssembler{Source: repoWS}, nil, nil
		}},
	}
	cfg.HarnessRulesSources = []HarnessSourceRegistration[prompt.RulesSource]{
		{ID: "organization", Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (prompt.RulesSource, func() error, error) {
			return frozenHarnessRules{rules: []prompt.Rule{{Name: "organization", Body: "ORGANIZATION-RULE", Origin: prompt.RuleOriginUser}}}, nil, nil
		}},
		{ID: "repository", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(ctx context.Context, _ HarnessSourceScope) (prompt.RulesSource, func() error, error) {
			source, _, err := rulesfs.NewFSSource(ctx, rulesfs.DirSource{Dir: rulesDir, Tier: prompt.RuleOriginProject})
			return source, nil, err
		}},
	}
	cfg.HarnessSkillSources = []HarnessSourceRegistration[tool.SkillSource]{
		{ID: "organization", Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (tool.SkillSource, func() error, error) {
			return markedHarnessSkills{SkillSource: sourceconformance.NewFixtureSource(), marker: "ORGANIZATION-SKILL"}, nil, nil
		}},
		{ID: "repository", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(ctx context.Context, _ HarnessSourceScope) (tool.SkillSource, func() error, error) {
			source, _, err := skillfs.NewFSSource(ctx, skillfs.DirSource{Dir: skillDir, Tier: tool.SkillOriginProject})
			return source, nil, err
		}},
	}
	return cfg, repo
}
func harnessRequestText(request port.LLMRequest) string {
	var out strings.Builder
	for _, message := range request.Messages {
		out.WriteString(message.Text)
		out.WriteByte('\n')
	}
	return out.String()
}

func TestADR_0357_HarnessContext_Scenario5_HelpdeskDeploymentAndServiceSources(t *testing.T) {
	var contexts [][]session.Message
	for _, transport := range []string{"file", "service"} {
		t.Run(transport, func(t *testing.T) {
			cfg, _ := harnessHybridConfig(t, false, "combine")
			cfg.Workspace = ""
			cfg.NoShell = true
			if transport == "service" {
				cfg.HarnessInstructionSources[0].Bind = func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
					return hcAssembler("DEPLOYMENT-INSTRUCTIONS"), nil, nil
				}
			}
			var requests []port.LLMRequest
			cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.ToolCallTurn(session.ToolCall{ID: "skill", Name: "Skill", Args: json.RawMessage(`{"name":"review"}`)}), mockllm.TextTurn("done"))
			b, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			sess, err := b.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS)
			if err != nil {
				t.Fatal(err)
			}
			events := harnessRun(t, b, t.Context(), sess.ID, "helpdesk task")
			var activated bool
			for _, ev := range events {
				if ev.ToolResult != nil && ev.ToolResult.CallID == "skill" {
					activated = !ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "ORGANIZATION-SKILL")
				}
			}
			if !activated {
				t.Fatal("service skill source did not reach real Skill tool")
			}
			if len(requests) != 2 {
				t.Fatalf("requests=%d", len(requests))
			}
			text := harnessRequestText(requests[0])
			for _, marker := range []string{"DEPLOYMENT-INSTRUCTIONS", "ORGANIZATION-INSTRUCTIONS", "ORGANIZATION-RULE"} {
				if !strings.Contains(text, marker) {
					t.Fatalf("missing %s", marker)
				}
			}
			if strings.Index(text, "DEPLOYMENT-INSTRUCTIONS") > strings.Index(text, "ORGANIZATION-INSTRUCTIONS") {
				t.Fatal("transport changed instruction order")
			}
			for _, spec := range requests[0].Tools {
				if spec.Name == "Shell" || spec.Name == "Read" {
					t.Fatal("helpdesk gained execution filesystem")
				}
			}
			contexts = append(contexts, requests[0].Messages)
		})
	}
	if !reflect.DeepEqual(contexts[0], contexts[1]) {
		t.Fatal("equivalent file/service data produced different admitted context")
	}
}

func TestADR_0357_HarnessContext_Scenario5_HybridCodingContext(t *testing.T) {
	cfg, _ := harnessHybridConfig(t, true, "combine")
	cfg.Shell = "/bin/sh"
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })},
		mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"data.txt"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "shell", Name: "Shell", Args: json.RawMessage(`{"command":"cat data.txt"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "skill", Name: "Skill", Args: json.RawMessage(`{"name":"review"}`)}), mockllm.TextTurn("done"))
	b, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := harnessCreate(t, b, t.Context())
	events := harnessRun(t, b, t.Context(), id, "coding task")
	results := map[session.ToolCallID]session.ToolResult{}
	for _, ev := range events {
		if ev.ToolResult != nil {
			results[ev.ToolResult.CallID] = *ev.ToolResult
		}
	}
	for _, call := range []session.ToolCallID{"read", "shell"} {
		result, ok := results[call]
		if !ok || result.IsError || !strings.Contains(result.Content, "REPOSITORY-DATA") {
			t.Fatalf("%s did not use coherent repository execution: %+v", call, result)
		}
	}
	skill, ok := results["skill"]
	if !ok || skill.IsError || !strings.Contains(skill.Content, "REPOSITORY-SKILL") || strings.Contains(skill.Content, "references/checklist") {
		t.Fatalf("skill override merged/leaked organization bundle: %+v", skill)
	}
	if len(requests) != 4 {
		t.Fatalf("requests=%d", len(requests))
	}
	text := harnessRequestText(requests[0])
	for _, marker := range []string{"DEPLOYMENT-INSTRUCTIONS", "ORGANIZATION-INSTRUCTIONS", "REPOSITORY-INSTRUCTIONS", "ORGANIZATION-RULE", "REPOSITORY-RULE"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("hybrid context missing %s", marker)
		}
	}
}

func TestADR_0357_HarnessContext_Scenario5_CombineExcludeAndDisableRepositoryContext(t *testing.T) {
	for _, tc := range []struct {
		name, mode                 string
		repository                 bool
		wantRepo, wantOrganization bool
	}{
		{"combine", "combine", true, true, true},
		{"replace", "replace", true, false, false},
		{"disable repository", "combine", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := harnessHybridConfig(t, tc.repository, tc.mode)
			var requests []port.LLMRequest
			cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"data.txt"}`)}), mockllm.TextTurn("done"))
			b, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			id := harnessCreate(t, b, t.Context())
			events := harnessRun(t, b, t.Context(), id, "task")
			var read bool
			for _, ev := range events {
				if ev.ToolResult != nil && ev.ToolResult.CallID == "read" {
					read = !ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "REPOSITORY-DATA")
				}
			}
			if !read {
				t.Fatal("context policy disabled repository execution")
			}
			if len(requests) != 2 {
				t.Fatalf("requests=%d", len(requests))
			}
			text := harnessRequestText(requests[0])
			if strings.Contains(text, "REPOSITORY-INSTRUCTIONS") != tc.wantRepo || strings.Contains(text, "ORGANIZATION-INSTRUCTIONS") != tc.wantOrganization {
				t.Fatalf("incorrect combined/replaced instructions: %s", text)
			}
			if strings.Contains(text, "REPOSITORY-RULE") != tc.wantRepo {
				t.Fatal("rules did not follow their own combine/replace policy")
			}
			if !strings.Contains(text, "DEPLOYMENT-INSTRUCTIONS") || !strings.Contains(text, "ORGANIZATION-RULE") {
				t.Fatal("disabling/replacing repository erased independent context")
			}
		})
	}
	t.Run("instruction and rule exclusions", func(t *testing.T) {
		cfg, _ := harnessHybridConfig(t, true, "combine")
		data, err := os.ReadFile(cfg.PermissionConfigs[0])
		if err != nil {
			t.Fatal(err)
		}
		var policy permconfig.Config
		if err := yaml.Unmarshal(data, &policy); err != nil {
			t.Fatal(err)
		}
		policy.HarnessContext.Kinds.Instructions.Exclude = []permconfig.HarnessContextExclude{{Source: "repository"}}
		policy.HarnessContext.Kinds.Rules.Exclude = []permconfig.HarnessContextExclude{{Source: "repository", Name: "repo"}}
		data, err = yaml.Marshal(map[string]any{"harness_context": policy.HarnessContext})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfg.PermissionConfigs[0], data, 0o600); err != nil {
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
		text := harnessRequestText(requests[0])
		if strings.Contains(text, "REPOSITORY-INSTRUCTIONS") || strings.Contains(text, "REPOSITORY-RULE") {
			t.Fatal("excluded source contributed")
		}
		if !strings.Contains(text, "DEPLOYMENT-INSTRUCTIONS") || !strings.Contains(text, "ORGANIZATION-RULE") {
			t.Fatal("exclusion erased independent source")
		}
	})
	t.Run("post-exclusion replace", TestHarnessReplaceUsesPostExclusionNonemptiness)
}
