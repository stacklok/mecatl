package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestAgentDefRootSettingsAliases(t *testing.T) {
	for _, router := range []bool{false, true} {
		for _, tc := range []struct {
			name, frontmatter, provider, model string
			selector                           server.ProviderSelector
			warn                               bool
		}{
			{name: "omitted", provider: providerOpenRouter, model: "main-model"},
			{name: "inherit", frontmatter: "model: inherit\n", provider: providerOpenRouter, model: "main-model"},
			{name: "Joe scalar", frontmatter: "model: joe\n", provider: providerOpenAI, model: "joe-model"},
			{name: "paired", frontmatter: "model: specialist\n", provider: providerAnthropic, model: "definition-model"},
			{name: "CLI contextual", frontmatter: "model: cli\n", provider: providerOpenRouter, model: "cli-model"},
			{name: "explicit pin", frontmatter: "provider: openai\nmodel: joe\n", provider: providerOpenAI, model: "joe-model"},
			{name: "conflict", frontmatter: "provider: openrouter\nmodel: joe\n", provider: providerOpenRouter, model: "main-model", warn: true},
			{name: "wire model opaque", frontmatter: "model: joe\n", selector: server.ProviderSelector{ModelID: "specialist"}, provider: providerOpenAI, model: "specialist"},
			{name: "wire model verbatim", frontmatter: "model: joe\n", selector: server.ProviderSelector{ModelID: "  vendor/opaque:model.v1  "}, provider: providerOpenAI, model: "  vendor/opaque:model.v1  "},
			{name: "wire pair", frontmatter: "model: joe\n", selector: server.ProviderSelector{ProviderID: providerAnthropic, ModelID: "opaque-model"}, provider: providerAnthropic, model: "opaque-model"},
			{name: "wire provider only", frontmatter: "model: joe\n", selector: server.ProviderSelector{ProviderID: providerOpenRouter}, provider: providerOpenRouter, model: builtinDefaultModel[providerOpenRouter]},
		} {
			t.Run(tc.name+"/router="+map[bool]string{false: "off", true: "on"}[router], func(t *testing.T) {
				settings := filepath.Join(t.TempDir(), "settings.yaml")
				body := `models:
  default_provider: openai
  default: main
  subagent: child
  aliases:
    main: {provider: openrouter, model: main-model}
    child: {provider: anthropic, model: child-model}
    specialist: {provider: anthropic, model: definition-model}
    joe: joe-model
`
				if router {
					body += "  router:\n    categories:\n      - name: ordinary\n        description: ordinary work\n        model: child\n"
				}
				if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				writeAgentDefFrontmatterFile(t, dir, "joe", "tools: [Read]\n"+tc.frontmatter, "DEFINITION-SCOPE-MARKER")
				diag := &recordingDiag{}
				var requests []port.LLMRequest
				var used []string
				built, err := buildIsolated(t, t.Context(), Config{
					Workspace: t.TempDir(), NoSoul: true, AgentsDirs: []string{dir}, PermissionConfigs: []string{settings},
					Diagnostics: diag, ContextWindowOverride: 128000, ModelAliases: map[string]string{"cli": "cli-model"},
					envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "test-key", "OPENROUTER_API_KEY": "test-key", "ANTHROPIC_API_KEY": "test-key"}),
					liveModelHTTPClient: offlineHTTPClient(),
					providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
						return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
							used = append(used, id)
							requests = append(requests, req)
						})}, mockllm.TextTurn("reply-from-"+id))
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer built.Close()
				sess, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, tc.selector, server.ProfileDefault, server.WithAgentDefinitionName("joe"))
				if err != nil {
					t.Fatal(err)
				}
				run, err := built.Service.StartRun(t.Context(), sess.ID, "hello")
				if err != nil {
					t.Fatal(err)
				}
				if got := drainRun(run); got != "reply-from-"+tc.provider {
					t.Fatalf("reply=%q", got)
				}
				built.Service.FinishRun(sess.ID, run)
				if len(requests) != 1 || used[0] != tc.provider || requests[0].Model != tc.model {
					t.Fatalf("providers=%v request count=%d; want %s/%s, no classifier", used, len(requests), tc.provider, tc.model)
				}
				if !strings.Contains(requests[0].System.Render(), "DEFINITION-SCOPE-MARKER") || requestHasTool(requests[0], "Subagent") || requestHasTool(requests[0], "Write") {
					t.Fatal("definition scope lost")
				}
				if tc.warn && !diag.has("conflicts with its provider") {
					t.Fatal("missing conflict warning")
				}
			})
		}
	}
}

func TestAgentDefRootPrincipalBinding(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte("harness_context:\n  enabled_sources: [tenant]\n  kinds:\n    instructions: {sources: [], mode: combine}\n    commands: {sources: [], mode: combine}\n    rules: {sources: [], mode: combine}\n    skills: {sources: [], mode: combine}\n    agent_defs: {sources: [tenant], mode: combine}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var binds, closes atomic.Int32
	var requests []port.LLMRequest
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace: t.TempDir(), NoSoul: true, OwnershipEnforced: true, PermissionConfigs: []string{settings},
		MockProvider: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })}, mockllm.TextTurn("alice"), mockllm.TextTurn("bob")),
		HarnessAgentDefSources: []HarnessSourceRegistration[tool.AgentDefSource]{{ID: "tenant", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(_ context.Context, scope HarnessSourceScope) (tool.AgentDefSource, func() error, error) {
			binds.Add(1)
			if scope.Principal == nil {
				t.Fatal("principal source bound without owner")
			}
			owner := scope.Principal.Subject
			return &resolvedAgentSource{defs: []tool.AgentDef{{Name: "agent-" + owner, Body: "ROOT-OWNER-" + owner, Tools: []string{"Read"}}}}, func() error { closes.Add(1); return nil }, nil
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if binds.Load() != 0 {
		t.Fatal("bound at startup")
	}
	var sessions []session.SessionID
	for i, owner := range []string{"alice", "bob"} {
		ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: owner, GrantType: session.GrantTypeUser})
		_, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithAgentDefinitionName("agent-"+map[string]string{"alice": "bob", "bob": "alice"}[owner]))
		if !errors.Is(err, server.ErrInvalidArgument) {
			t.Fatalf("foreign def=%v", err)
		}
		if closes.Load() != int32(i+1) {
			t.Fatalf("failed build lease not released: %d", closes.Load())
		}
		sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithAgentDefinitionName("agent-"+owner))
		if err != nil {
			t.Fatal(err)
		}
		run, err := built.Service.StartRun(ctx, sess.ID, "hello")
		if err != nil {
			t.Fatal(err)
		}
		if got := drainRun(run); got != owner {
			t.Fatalf("reply=%q", got)
		}
		built.Service.FinishRun(sess.ID, run)
		if !strings.Contains(requests[i].System.Render(), "ROOT-OWNER-"+owner) || strings.Contains(requests[i].System.Render(), "ROOT-OWNER-"+map[string]string{"alice": "bob", "bob": "alice"}[owner]) {
			t.Fatal("owner scope lost")
		}
		sessions = append(sessions, sess.ID)
	}
	for _, id := range sessions {
		built.Service.CloseSession(id)
	}
	if binds.Load() != 4 || closes.Load() != 4 {
		t.Fatalf("binds=%d closes=%d", binds.Load(), closes.Load())
	}
}

func TestAgentDefRootPlacementPostureAndPolicy(t *testing.T) {
	fakeRulesEnv(t, t.TempDir(), t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, posture := range []string{"local", "no-fs", "remote"} {
		t.Run(posture, func(t *testing.T) {
			project := t.TempDir()
			initTestRepo(t, project)
			writeProjectFile(t, filepath.Join(project, "AGENTS.md"), "LOCAL-INSTRUCTION-SENTINEL")
			mkdirProjectSettings(t, project, "permissions:\n  deny:\n    - 'Shell(echo allowed)'\n")
			settings := filepath.Join(t.TempDir(), "operator.yaml")
			writeProjectFile(t, settings, "permissions:\n  deny:\n    - 'Shell(echo denied)'\n  ask:\n    - 'Shell(echo asked)'\n")
			dir := t.TempDir()
			writeScopedAgentDefFile(t, dir, "root", []string{"Read", "Shell"}, "ROOT-POSTURE-SENTINEL")
			var requests []port.LLMRequest
			turns := []mockllm.Turn{mockllm.TextTurn("done")}
			if posture == "remote" {
				turns = []mockllm.Turn{mockllm.ToolCallTurn(
					session.NewToolCall("denied", "Shell", json.RawMessage(`{"command":"echo denied"}`)),
					session.NewToolCall("asked", "Shell", json.RawMessage(`{"command":"echo asked"}`)),
					session.NewToolCall("allowed", "Shell", json.RawMessage(`{"command":"echo allowed"}`)),
				), mockllm.TextTurn("done")}
			}
			llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })}, turns...)
			cfg := Config{Workspace: project, Shell: "/bin/sh", TrustProject: true, NoSoul: true, Posture: PostureAuto, PermissionConfigs: []string{settings}, PermissionsConventional: true, AgentsDirs: []string{dir}, MockProvider: llm}
			profile := server.ProfileDefault
			if posture == "no-fs" {
				profile = server.ProfileNoFS
			}
			if posture == "remote" {
				ws := memfs.NewWorkspace(project)
				if _, err := ws.CreateFile(t.Context(), "AGENTS.md", []byte("REMOTE-INSTRUCTION-SENTINEL")); err != nil {
					t.Fatal(err)
				}
				runner := memfs.NewCommandRunner()
				runner.SetResult(&tool.CommandResult{Stdout: "REMOTE-EXECUTED"}, nil)
				env := tool.MustEnvironment(session.EnvironmentRef{Kind: "kubernetes", ID: "remote", Revision: "v1"}, ws, memledger.New(), remotePermissionRunner{runner, project})
				cfg.RemoteExecution, cfg.PlacementScope, cfg.PlacementProvider = true, "remote", remoteFactoryPlacement{env: env}
			}
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser})
			sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, profile, server.WithAgentDefinitionName("root"))
			if err != nil {
				t.Fatal(err)
			}
			run, err := built.Service.StartRun(ctx, sess.ID, "check posture")
			if err != nil {
				t.Fatal(err)
			}
			asks := 0
			results := map[session.ToolCallID]session.ToolResult{}
			for ev := range run.Events() {
				if ev.Ask != nil {
					asks++
					if ev.Ask.Call != "asked" || ev.Ask.AskProvenance != governance.AskProvenanceConfigured {
						t.Errorf("unexpected ask: %+v", ev.Ask)
					}
					if _, err := built.Service.ApproveRun(ctx, sess.ID, ev.Ask.AskID, session.VerdictDeny, ""); err != nil {
						t.Fatal(err)
					}
				}
				if ev.ToolResult != nil {
					results[ev.ToolResult.CallID] = *ev.ToolResult
				}
			}
			built.Service.FinishRun(sess.ID, run)
			if len(requests) == 0 {
				t.Fatal("no provider request")
			}
			text := requests[0].System.Render()
			if !strings.Contains(text, "ROOT-POSTURE-SENTINEL") {
				t.Fatal("definition lost")
			}
			if posture == "local" {
				for _, marker := range []string{project, "/bin/sh", "initial commit"} {
					if !strings.Contains(text, marker) {
						t.Errorf("local positive control missing %q", marker)
					}
				}
			} else {
				for _, marker := range []string{project, "/bin/sh", "initial commit", "<git-status>"} {
					if strings.Contains(text, marker) {
						t.Errorf("host prompt leak: %q", marker)
					}
				}
				if posture == "remote" {
					for _, msg := range requests[0].Messages {
						if strings.Contains(msg.Text, "LOCAL-INSTRUCTION-SENTINEL") || strings.Contains(msg.Text, "REMOTE-INSTRUCTION-SENTINEL") {
							t.Fatal("project instructions crossed remote placement posture")
						}
					}
				}
			}
			if posture == "no-fs" && (!strings.Contains(text, noFSPostureNote) || requestHasTool(requests[0], "Read") || requestHasTool(requests[0], "Shell")) {
				t.Fatal("no-fs posture/catalog mismatch")
			}
			if posture == "remote" {
				if !strings.Contains(text, remoteExecutionPostureNote) || !strings.Contains(text, "cwd: /workspace") {
					t.Fatal("missing remote posture")
				}
				if asks != 1 {
					t.Fatalf("configured asks=%d", asks)
				}
				for _, id := range []session.ToolCallID{"denied", "asked", "allowed"} {
					result, ok := results[id]
					blocked := id != "allowed"
					if !ok || result.IsError != blocked || strings.Contains(result.Content, "REMOTE-EXECUTED") == blocked {
						t.Errorf("result %s=%+v", id, result)
					}
				}
			}
		})
	}
}

func TestAgentDefRootRuntimePublication(t *testing.T) {
	oldManager := connectMainManager(t, "svc", newEntryRuntimeServer(t, "old", nil, nil))
	newManager := connectMainManager(t, "svc", newEntryRuntimeServer(t, "new", nil, nil))
	runtimes := newMCPRuntimeSet(nil)
	defer runtimes.close()
	old := &mcpReconcileCandidate{manager: oldManager, generation: 7, tools: toolMetadata(oldManager.Tools())}
	next := &mcpReconcileCandidate{manager: newManager, generation: 8, tools: toolMetadata(newManager.Tools())}
	if !runtimes.publish(nil, old) {
		t.Fatal("publish old")
	}
	var requests []port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })},
		mockllm.ToolCallTurn(session.NewToolCall("old", entryRuntimeTool, []byte(`{"text":"work"}`))), mockllm.TextTurn("done"),
		mockllm.ToolCallTurn(session.NewToolCall("new", entryRuntimeTool, []byte(`{"text":"work"}`))), mockllm.TextTurn("done"))
	cfg := isolateConfig(t, Config{Workspace: t.TempDir(), Model: "test-model"})
	var err error
	cfg.authorityEvaluator, _, err = selectAuthorityEvaluator("local", "")
	if err != nil {
		t.Fatal(err)
	}
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	store := memstore.New()
	defs := agents.NewRegistry([]agents.AgentDef{{Name: "root", Tools: []string{"Read"}, MCPServers: []agents.AgentMCPServer{{Name: "svc"}}}})
	factory := agentDefSessionEngineFactory(cfg, reg, provider, store, testAgentDefRootPolicy(cfg), hookexec.New(nil), runtimes, nil, catalogAssets{agentReg: defs, globalMgr: oldManager, mcpRuntimes: runtimes}, nil)
	var revisions []uint64
	svc, err := newTestServerService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Model: cfg.Model}), Store: store,
		OperationPin: runtimes.pin, OperationRevision: mcpOperationRevision,
		AgentDefSessionEngine: func(ctx context.Context, sel server.ProviderSelector, profile server.SessionProfile, mode session.PermissionMode, limits session.Limits, name string) (server.SessionEngineResult, error) {
			res, err := factory(ctx, sel, profile, mode, limits, name)
			if err == nil {
				revisions = append(revisions, res.RuntimeRevision)
			}
			return res, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	sess, err := svc.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithAgentDefinitionName("root"))
	if err != nil {
		t.Fatal(err)
	}
	for i, version := range []string{"old", "new"} {
		run, err := svc.StartRun(t.Context(), sess.ID, "use the service")
		if err != nil {
			t.Fatal(err)
		}
		var result string
		for ev := range run.Events() {
			if ev.Ask != nil {
				run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
			}
			if ev.ToolResult != nil {
				result = ev.ToolResult.Content
			}
		}
		if !strings.Contains(result, version+":work") {
			t.Fatalf("%s call result=%q", version, result)
		}
		if len(revisions) != i+1 || revisions[i] != uint64(7+i) {
			t.Fatalf("build revisions=%v", revisions)
		}
		for _, spec := range requests[i*2].Tools {
			if spec.Name == entryRuntimeTool && !strings.Contains(spec.Description, version) {
				t.Fatalf("stale tool spec=%+v", spec)
			}
		}
		if i == 0 {
			if !runtimes.publish(old, next) {
				t.Fatal("publish replacement")
			}
			if old.isClosed() {
				t.Fatal("operation lease released before FinishRun")
			}
		}
		svc.FinishRun(sess.ID, run)
		if i == 0 && !old.isClosed() {
			t.Fatal("retired runtime lease leaked")
		}
	}
	if len(runtimes.pins) != 0 {
		t.Fatal("runtime pins leaked")
	}
	_, err = factory(t.Context(), server.ProviderSelector{}, server.ProfileDefault, session.ModeDefault, session.Limits{}, "missing")
	if !errors.Is(err, server.ErrInvalidArgument) || len(runtimes.pins) != 0 {
		t.Fatal("failed factory leaked runtime lease")
	}
}

func TestAgentDefRootEffectiveProviderOptions(t *testing.T) {
	for _, tc := range []struct {
		name, requested, effective string
		reasoning                  bool
	}{
		{name: "incapable", requested: " MAX "},
		{name: "reasoning", requested: " MAX ", effective: "high", reasoning: true},
		{name: "capabilities only", requested: "low", effective: "low", reasoning: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{Image: true, Audio: true})}, mockllm.TextTurn("WRONG shared provider"))
			reg := regForTest(provider, providerOpenAI, "main-model")
			reg.meta = newMetadataFixture()
			reg.meta.setMetadataFixture(map[string][]modelEntry{providerOpenAI: {{ID: "definition-model", Reasoning: tc.reasoning, InputModalities: []string{"text", "image"}}}})
			var effort string
			var caps port.ProviderCapabilities
			var request port.LLMRequest
			entry := reg.entries[providerOpenAI]
			entry.defaultCaps = port.ProviderCapabilities{Image: true, Audio: true}
			entry.remint = func(e string, c port.ProviderCapabilities) port.LLMProvider {
				effort, caps = e, c
				return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { request = req })}, mockllm.TextTurn("reminted"))
			}
			reg.entries[providerOpenAI] = entry
			defs := agents.NewRegistry([]agents.AgentDef{{Name: "root", Model: "definition-model", Tools: []string{"Read"}}})
			cfg := isolateConfig(t, Config{Model: "main-model", ReasoningEffort: "low"})
			factory := agentDefSessionEngineFactory(cfg, reg, provider, memstore.New(), testAgentDefRootPolicy(cfg), hookexec.New(nil), nil, nil, catalogAssets{agentReg: defs}, nil)
			res, err := factory(t.Context(), server.ProviderSelector{ReasoningEffort: tc.requested}, server.ProfileDefault, session.ModeDefault, session.Limits{}, "root")
			if err != nil {
				t.Fatal(err)
			}
			defer res.Close()
			if effort != tc.effective || res.ReasoningEffort != tc.effective || caps != (port.ProviderCapabilities{Image: true}) || res.Capabilities != caps {
				t.Fatalf("effort=%q result=%+v caps=%+v", effort, res, caps)
			}
			sess := session.New("root", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
			if got := drainRun(res.Engine.Run(t.Context(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "hello"})); got != "reminted" || request.Model != "definition-model" {
				t.Fatalf("reply=%q request model=%q", got, request.Model)
			}
		})
	}
}
