package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestBuiltEngineAdvertisesCurrentSessionWithoutEmbeddingID(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []port.LLMRequest
	)
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
	})}, mockllm.TextTurn("done"))
	cfg := Config{
		Workspace: t.TempDir(), NoSoul: true,
		envDetector: fakeEnv(map[string]string{"OPENAI_API_KEY": "test"}), liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider { return provider },
	}
	built, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	const id session.SessionID = "concrete-session-id-must-not-enter-system-prompt"
	sess, err := built.Service.CreateSessionWithProfile(context.Background(), cfg.Workspace, session.ModeDefault,
		session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id))
	if err != nil {
		t.Fatalf("CreateSessionWithProfile: %v", err)
	}
	run, err := built.Service.StartRun(context.Background(), sess.ID, "what is this session?")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	drainRun(run)

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	var found bool
	for _, spec := range requests[0].Tools {
		if spec.Name != agent.CurrentSessionToolName {
			continue
		}
		found = true
		for _, want := range []string{"exact session ID", "mecatui debug", "opaque reference", "no authority"} {
			if !strings.Contains(spec.Description, want) {
				t.Errorf("CurrentSession description %q does not contain %q", spec.Description, want)
			}
		}
	}
	if !found {
		t.Fatal("built engine did not advertise CurrentSession")
	}
	if strings.Contains(requests[0].System.StablePrefix, string(id)) || strings.Contains(requests[0].System.VolatileSuffix, string(id)) {
		t.Fatalf("concrete session ID leaked into system prompt: %+v", requests[0].System)
	}
}

func TestWritableManagedSpecialistCanUseCurrentSession(t *testing.T) {
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser})
	workspace := t.TempDir()
	agentsDir := t.TempDir()
	definition := "---\nname: writer\ndescription: writable specialist\ntools: [Read]\n---\nUse CurrentSession when asked.\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "writer.md"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write agent definition: %v", err)
	}

	var (
		mu       sync.Mutex
		requests []port.LLMRequest
	)
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
	})},
		mockllm.ToolCallTurn(session.NewToolCall("delegate", "Subagent", []byte(`{"prompt":"identify yourself","agent":"writer","mode":"read-write"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("current", agent.CurrentSessionToolName, []byte(`{}`))),
		mockllm.TextTurn("specialist complete"),
		mockllm.TextTurn("parent complete"),
	)
	built, err := Build(ctx, Config{
		Workspace: workspace, StoreDir: filepath.Join(t.TempDir(), "sessions"), AgentsDirs: []string{agentsDir},
		MockProvider: provider, NoSoul: true, AllowAllTools: true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	parent, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, parent.ID, "delegate")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "parent complete" {
		t.Fatalf("terminal text = %q, want parent complete", got)
	}

	childID := session.SessionID("subagent-" + string(parent.ID) + "-delegate")
	child, err := built.Service.GetSession(ctx, childID)
	if err != nil {
		t.Fatalf("GetSession(%q): %v", childID, err)
	}
	var executed bool
	for _, message := range child.Conversation.Messages {
		if message.Role != session.RoleTool || message.ToolResult == nil || message.ToolResult.CallID != "current" {
			continue
		}
		executed = true
		if message.ToolResult.IsError || message.ToolResult.Content != string(childID) {
			t.Fatalf("CurrentSession result = %+v, want successful child ID %q", message.ToolResult, childID)
		}
	}
	if !executed {
		t.Fatal("writable specialist did not execute CurrentSession")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, req := range requests {
		if !strings.Contains(req.System.StablePrefix, "Agent definition (writer)") {
			continue
		}
		for _, spec := range req.Tools {
			if spec.Name == agent.CurrentSessionToolName {
				return
			}
		}
		t.Fatal("writable managed specialist request did not expose CurrentSession")
	}
	t.Fatal("writable managed specialist request was not observed")
}

func TestAgentDefinitionDisallowedCurrentSessionStaysDenied(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), Model: "m"}
	def := agents.AgentDef{Name: "restricted", Tools: []string{"Read"}, DisallowedTools: []string{agent.CurrentSessionToolName}}
	base := baseSubagentTools(cfg)

	for _, allowMutating := range []bool{false, true} {
		eng, closeFn, names, _, _ := buildAgentDefEngine(context.Background(), cfg, def, "task:restricted", "test",
			mockllm.New(mockllm.TextTurn("done")), cfg.Model, nil, base, allowMutating, false, nil, hookexec.New(nil), nil, nil)
		if closeFn != nil {
			defer func() { _ = closeFn() }()
		}
		if eng.HasTool(agent.CurrentSessionToolName) {
			t.Fatalf("allowMutating=%t: disallowed CurrentSession remained in specialist catalog", allowMutating)
		}
		for _, name := range names {
			if name == agent.CurrentSessionToolName {
				t.Fatalf("allowMutating=%t: disallowed CurrentSession remained in authority names %v", allowMutating, names)
			}
		}
	}

	factory := memberFactoryForTest(cfg, mockllm.New(mockllm.TextTurn("done")), hookexec.New(nil), regOf(def), nil, nil, false, nil)
	member := factory(team.New("t"), agent.MemberSpec{Name: "restricted", AgentType: def.Name}, "")
	if member.Engine.HasTool(agent.CurrentSessionToolName) {
		t.Fatal("disallowed CurrentSession remained in managed team-member catalog")
	}
}
