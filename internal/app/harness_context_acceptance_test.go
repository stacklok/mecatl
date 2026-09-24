package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type hcAssembler string

func (a hcAssembler) Assemble(context.Context) ([]session.Message, error) {
	return []session.Message{session.NewUserMessage(string(a))}, nil
}

type hcCommands struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *hcCommands) List(context.Context) ([]prompt.Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]prompt.Command, 0, len(s.values))
	for name, body := range s.values {
		out = append(out, prompt.Command{Name: name, Description: body})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (s *hcCommands) Expand(_ context.Context, input string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fields := strings.Fields(input)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return input, false, nil
	}
	body, ok := s.values[strings.TrimPrefix(fields[0], "/")]
	if !ok {
		return input, false, nil
	}
	return body, true, nil
}

func hcConfiguredFiles(t *testing.T, source tool.Workspace) Config {
	t.Helper()
	file := filepath.Join(t.TempDir(), "settings.yaml")
	body := `harness_context:
  enabled_sources: [source]
  kinds:
    instructions: {sources: [source], mode: combine}
    commands: {sources: [source], mode: combine}
    rules: {sources: [], mode: combine}
    skills: {sources: [], mode: combine}
    agent_defs: {sources: [], mode: combine}
`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{Workspace: t.TempDir(), UseMock: true, Headless: true, TrustProject: true, UserModelDir: t.TempDir(), PermissionConfigs: []string{file},
		HarnessInstructionSources: []HarnessSourceRegistration[prompt.InstructionAssembler]{{ID: "source", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			return prompt.RootAssembler{Source: source}, nil, nil
		}}},
		HarnessCommandSources: []HarnessSourceRegistration[server.CommandSourceBinding]{{ID: "source", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			return prompt.NewDirCommandExpander(source), nil, nil
		}}}}
}

func TestADR_0354_HarnessContext_Scenario1_SourceIndependentOfExecution(t *testing.T) {
	source := memfs.NewWorkspace("/not-a-host-path")
	if _, err := source.CreateFile(t.Context(), "AGENTS.md", []byte("SELECTED-CONTEXT")); err != nil {
		t.Fatal(err)
	}
	var contexts [][]session.Message
	for _, executionMarker := range []string{"EXECUTION-ONE", "EXECUTION-TWO"} {
		cfg := hcConfiguredFiles(t, source)
		if err := os.WriteFile(filepath.Join(cfg.Workspace, "AGENTS.md"), []byte(executionMarker), 0o600); err != nil {
			t.Fatal(err)
		}
		var requests []port.LLMRequest
		cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.TextTurn("done"))
		built, err := buildIsolated(t, t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(built.Close)
		sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := built.Service.StartRun(t.Context(), sess.ID, "task")
		if err != nil {
			t.Fatal(err)
		}
		for range run.Events() {
		}
		built.Service.FinishRun(sess.ID, run)
		if len(requests) != 1 {
			t.Fatalf("requests=%d", len(requests))
		}
		var selected bool
		for _, m := range requests[0].Messages {
			selected = selected || strings.Contains(m.Text, "SELECTED-CONTEXT")
			if strings.Contains(m.Text, executionMarker) {
				t.Fatal("execution instructions entered context")
			}
		}
		if !selected {
			t.Fatal("selected source did not reach real engine request")
		}
		contexts = append(contexts, requests[0].Messages)
	}
	if !reflect.DeepEqual(contexts[0], contexts[1]) {
		t.Fatal("execution placement changed admitted messages")
	}
}

func TestADR_0354_HarnessContext_Scenario1_NoFSKeepsConfiguredSources(t *testing.T) {
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "AGENTS.md"), []byte("FILE-BACKED-LOGICAL-SOURCE"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := osfs.NewWorkspace(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := hcConfiguredFiles(t, source)
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })},
		mockllm.ToolCallTurn(session.ToolCall{ID: "forbidden-read", Name: "Read", Args: json.RawMessage(`{"path":"AGENTS.md"}`)}), mockllm.TextTurn("done"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS)
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(t.Context(), sess.ID, "task")
	if err != nil {
		t.Fatal(err)
	}
	var rejected bool
	for ev := range run.Events() {
		if ev.ToolResult != nil && ev.ToolResult.CallID == "forbidden-read" {
			rejected = ev.ToolResult.IsError
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if !rejected {
		t.Fatal("uncooperative mock's Read was not rejected")
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	for _, req := range requests {
		var selected bool
		for _, message := range req.Messages {
			if strings.Contains(message.Text, "FILE-BACKED-LOGICAL-SOURCE") {
				selected = true
			}
		}
		if !selected {
			t.Fatal("no-fs suppressed the independent file-backed source")
		}
		for _, spec := range req.Tools {
			switch spec.Name {
			case "Read", "ListDir", "Edit", "Write", "Copy", "Move", "Remove", "Grep", "Glob", "Shell", "Parallel", "SkillDraft":
				t.Fatalf("no-fs advertised %s", spec.Name)
			}
		}
	}
}

func TestHarnessOverrideKeepsNonReplacedBlocker(t *testing.T) {
	policy := harnessKindPolicy{sources: []HarnessSourceID{"a", "b", "c"}, overrides: map[string]harnessOverride{"x": {winner: "c", replaces: map[HarnessSourceID]struct{}{"a": {}}}}}
	for _, tc := range []struct {
		name    string
		present map[HarnessSourceID]bool
		want    HarnessSourceID
	}{
		{"blocker", map[HarnessSourceID]bool{"a": true, "b": true, "c": true}, "b"},
		{"no blocker", map[HarnessSourceID]bool{"a": true, "c": true}, "c"},
		{"absent winner", map[HarnessSourceID]bool{"a": true, "b": true}, "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := chooseHarnessWinner("x", tc.present, policy)
			if !ok || got != tc.want {
				t.Fatalf("winner=%q,%v want %q", got, ok, tc.want)
			}
		})
	}
}
