package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type remoteFactoryPlacement struct {
	env         tool.Environment
	binds       *int
	validateErr error
}

func (p remoteFactoryPlacement) ValidatePlacement(context.Context) error { return p.validateErr }
func (p remoteFactoryPlacement) Bind(_ context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if p.binds != nil {
		*p.binds++
	}
	if req.BindingID == "" || req.Principal == nil {
		return server.PlacementBinding{}, server.ErrInvalidPlacementSelection
	}
	return server.PlacementBinding{Environment: p.env, Ref: p.env.Ref(), Metadata: server.PlacementMetadata{Label: "Remote Kubernetes workspace"}}, nil
}
func (p remoteFactoryPlacement) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if req.BindingID == "" || req.Ref != p.env.Ref() {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	return server.PlacementBinding{Environment: p.env, Ref: p.env.Ref()}, nil
}

func TestRemoteExecutionPreflightNeverAllocates(t *testing.T) {
	fakeRulesEnv(t, t.TempDir(), t.TempDir())
	for _, validation := range []error{nil, server.ErrInvalidPlacementBinding, server.ErrPlacementUnavailable} {
		binds := 0
		built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true, RemoteExecution: true, PlacementScope: "remote", PlacementProvider: remoteFactoryPlacement{binds: &binds, validateErr: validation}})
		if built != nil {
			built.Close()
		}
		if validation == nil && err != nil || validation != nil && !errors.Is(err, validation) {
			t.Fatalf("validation=%v startup error=%v", validation, err)
		}
		if binds != 0 {
			t.Fatalf("preflight allocated %d environments", binds)
		}
	}
}

func TestRemoteDeploymentNoFSUsesLocalAttenuationWithoutProviderCall(t *testing.T) {
	fakeRulesEnv(t, t.TempDir(), t.TempDir())
	binds := 0
	remoteWS := memfs.NewWorkspace("/workspace")
	remoteRef := session.EnvironmentRef{Kind: "kubernetes", ID: "env-1", Revision: "rev-1"}
	remoteEnv := tool.MustEnvironment(remoteRef, remoteWS, memledger.New(), nil)
	var captured port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { captured = req })},
		mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"secret"}`)}),
		mockllm.TextTurn("done"),
	)
	built, err := buildIsolated(t, context.Background(), Config{MockProvider: provider, PlacementProvider: remoteFactoryPlacement{env: remoteEnv, binds: &binds}, PlacementScope: "remote", RemoteExecution: true, NoSoul: true, SchedulerEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser})
	sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeAccept, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS)
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "no files")
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	if binds != 0 {
		t.Fatalf("remote provider Bind calls = %d, want zero", binds)
	}
	if !strings.Contains(captured.System.StablePrefix, noFSPostureNote) || strings.Contains(captured.System.StablePrefix, remoteExecutionPostureNote) {
		t.Fatalf("no-fs posture was replaced by remote posture: %q", captured.System.StablePrefix)
	}
	for _, spec := range captured.Tools {
		if spec.Name == "Read" || spec.Name == "Shell" {
			t.Fatalf("no-fs catalog exposed %q", spec.Name)
		}
	}
}

type remotePermissionRunner struct {
	*memfs.CommandRunner
	root string
}

func (r remotePermissionRunner) BoundWorkspaceRoot() string { return r.root }

func TestRemoteExecutionPreservesOperatorPermissionsUnderAuto(t *testing.T) {
	for _, source := range []string{"global", "explicit"} {
		t.Run(source, func(t *testing.T) {
			xdg := t.TempDir()
			fakeRulesEnv(t, xdg, t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			operatorPath := filepath.Join(xdg, "mecatl", "settings.yaml")
			if source == "explicit" {
				operatorPath = filepath.Join(t.TempDir(), "operator.yaml")
			}
			writeProjectFile(t, operatorPath, "permissions:\n  deny:\n    - 'Shell(echo denied)'\n  ask:\n    - 'Shell(echo asked)'\n")
			project := t.TempDir()
			projectRules := "permissions:\n  deny:\n    - 'Shell(echo allowed)'\n  ask:\n    - 'Shell(echo project-ask)'\n  allow:\n    - 'Shell(echo denied)'\n    - 'Shell(echo asked)'\n"
			mkdirProjectSettings(t, project, projectRules)
			ws := memfs.NewWorkspace(project)
			if _, err := ws.CreateFile(t.Context(), ".mecatl/settings.yaml", []byte(projectRules)); err != nil {
				t.Fatal(err)
			}
			runner := memfs.NewCommandRunner()
			runner.SetResult(&tool.CommandResult{Stdout: "EXECUTED"}, nil)
			ref := session.EnvironmentRef{Kind: "kubernetes", ID: "permissions", Revision: "v1"}
			env := tool.MustEnvironment(ref, ws, memledger.New(), remotePermissionRunner{runner, project})
			llm := mockllm.New(
				mockllm.ToolCallTurn(
					session.NewToolCall("denied", "Shell", json.RawMessage(`{"command":"echo denied"}`)),
					session.NewToolCall("asked", "Shell", json.RawMessage(`{"command":"echo asked"}`)),
					session.NewToolCall("allowed", "Shell", json.RawMessage(`{"command":"echo allowed"}`)),
					session.NewToolCall("project-ask", "Shell", json.RawMessage(`{"command":"echo project-ask"}`)),
				), mockllm.TextTurn("done"))
			cfg := Config{MockProvider: llm, Workspace: project, Posture: PostureAuto, PermissionsConventional: true, NoSoul: true, RemoteExecution: true, PlacementScope: "remote", PlacementProvider: remoteFactoryPlacement{env: env}}
			if source == "explicit" {
				cfg.PermissionConfigs = []string{operatorPath}
			}
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser})
			sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			run, err := built.Service.StartRun(ctx, sess.ID, "check permissions")
			if err != nil {
				t.Fatal(err)
			}
			asks := 0
			results := map[session.ToolCallID]session.ToolResult{}
			for ev := range run.Events() {
				if ev.Ask != nil {
					asks++
					if ev.Ask.Call != "asked" || ev.Ask.AskProvenance != governance.AskProvenanceConfigured {
						t.Errorf("unexpected permission ask: %+v", ev.Ask)
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
			if asks != 1 {
				t.Errorf("configured asks = %d, want 1", asks)
			}
			for _, id := range []session.ToolCallID{"denied", "asked", "allowed", "project-ask"} {
				result, ok := results[id]
				blocked := id == "denied" || id == "asked"
				if !ok || result.IsError != blocked || strings.Contains(result.Content, "EXECUTED") == blocked {
					t.Errorf("%s result = %+v, present=%t, want blocked=%t", id, result, ok, blocked)
				}
			}
		})
	}
}

func TestRemoteExecutionRealFactoryCarriesPostureAndAttenuatedCatalog(t *testing.T) {
	xdg := t.TempDir()
	fakeRulesEnv(t, xdg, t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeUserRule(t, xdg, "operator-rule", "OPERATOR_RULE_MARKER\n")
	writeSkill(t, filepath.Join(xdg, "mecatl/skills"), "operator-skill", "Operator skill", "OPERATOR_SKILL_BODY")
	localProject := t.TempDir()
	initTestRepo(t, localProject)
	localSnapshotSeen := false
	localLLM := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		localSnapshotSeen = strings.Contains(req.System.Render(), "initial commit") && strings.Contains(req.System.Render(), "<git-status>")
	})}, mockllm.TextTurn("local done"))
	local, err := buildIsolated(t, t.Context(), Config{MockProvider: localLLM, Workspace: localProject, TrustProject: true, NoSoul: true, Shell: "/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	localSession, err := local.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	localRun, err := local.Service.StartRun(t.Context(), localSession.ID, "inspect local")
	if err != nil {
		t.Fatal(err)
	}
	for range localRun.Events() {
	}
	local.Service.FinishRun(localSession.ID, localRun)
	if !localSnapshotSeen {
		t.Fatal("local factory omitted the test repository's Git snapshot")
	}
	ws := memfs.NewWorkspace(localProject)
	if _, err := ws.CreateFile(context.Background(), "AGENTS.md", []byte("REMOTE_PROJECT_MARKER_DO_NOT_LOAD")); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.CreateFile(context.Background(), "main.go", []byte("package main\n")); err != nil {
		t.Fatal(err)
	}
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env-1", Revision: "rev-1"}
	env := tool.MustEnvironment(ref, ws, memledger.New(), nil)
	var mu sync.Mutex
	var captured port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { mu.Lock(); captured = req; mu.Unlock() })},
		mockllm.ToolCallTurn(
			session.ToolCall{ID: "Subagent", Name: "Subagent", Args: json.RawMessage(`{}`)},
			session.ToolCall{ID: "Parallel", Name: "Parallel", Args: json.RawMessage(`{}`)},
			session.ToolCall{ID: "Team", Name: "Team", Args: json.RawMessage(`{}`)},
			session.ToolCall{ID: "SkillDraft", Name: "SkillDraft", Args: json.RawMessage(`{}`)},
			session.ToolCall{ID: "Schedule", Name: "Schedule", Args: json.RawMessage(`{}`)},
			session.ToolCall{ID: "project-skill", Name: "Skill", Args: json.RawMessage(`{"name":"project-only"}`)},
			session.ToolCall{ID: "operator-skill", Name: "Skill", Args: json.RawMessage(`{"name":"operator-skill"}`)},
		),
		mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"main.go"}`)}),
		mockllm.TextTurn(""),
		mockllm.TextTurn("done"),
	)
	if err := os.WriteFile(filepath.Join(localProject, "AGENTS.md"), []byte("LOCAL_PROJECT_MARKER_DO_NOT_LOAD\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeProjectRule(t, localProject, "project-rule", "LOCAL_RULE_MARKER_DO_NOT_LOAD\n")
	writeSkill(t, filepath.Join(localProject, ".mecatl/skills"), "project-only", "LOCAL_SKILL_MARKER_DO_NOT_LOAD", "LOCAL_SKILL_BODY_DO_NOT_LOAD")
	commandDir := filepath.Join(localProject, ".claude", "commands")
	if err := os.MkdirAll(commandDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandDir, "project-only.md"), []byte("LOCAL_COMMAND_MARKER_DO_NOT_LOAD"), 0o600); err != nil {
		t.Fatal(err)
	}
	built, err := buildIsolated(t, context.Background(), Config{MockProvider: provider, PlacementProvider: remoteFactoryPlacement{env: env}, PlacementScope: "remote", RemoteExecution: true, Workspace: localProject, TrustProject: true, AllowAllTools: true, EnableCommands: true, SkillsConventional: true, NoSoul: true, SchedulerEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	principal := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	ctx := session.WithPrincipal(context.Background(), principal)
	sess, err := built.Service.CreateSession(ctx, session.ModeAccept, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if commands, err := built.Service.ListCommandsForSession(ctx, sess.ID); err != nil || len(commands) != 0 {
		t.Fatalf("remote command discovery fell back locally: %v, %v", commands, err)
	}
	if _, err := built.Service.ListWorktreesForSession(ctx, sess.ID); !errors.Is(err, server.ErrPlacementUnavailable) {
		t.Fatalf("remote worktree discovery did not reject unsupported placement: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "/project-only")
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(captured.System.StablePrefix, remoteExecutionPostureNote) {
		t.Fatal("real per-session factory omitted remote execution posture")
	}
	if strings.Contains(captured.System.Render(), "<git-status>") {
		t.Fatal("remote factory ingested the local Git snapshot")
	}
	requestJSON, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"LOCAL_PROJECT_MARKER", "REMOTE_PROJECT_MARKER", "LOCAL_RULE_MARKER", "LOCAL_SKILL_MARKER", "LOCAL_SKILL_BODY", "LOCAL_COMMAND_MARKER", "initial commit"} {
		if strings.Contains(string(requestJSON), marker) {
			t.Errorf("remote request ingested forbidden project source %s", marker)
		}
	}
	if !strings.Contains(string(requestJSON), "OPERATOR_RULE_MARKER") || !strings.Contains(string(requestJSON), "OPERATOR_SKILL_BODY") {
		t.Fatal("remote request dropped operator-global rules or skills")
	}
	failed := map[string]bool{}
	for _, message := range captured.Messages {
		if result := message.ToolResult; result != nil {
			failed[string(result.CallID)] = result.IsError
		}
	}
	for _, id := range []string{"Subagent", "Parallel", "Team", "SkillDraft", "Schedule", "project-skill"} {
		if !failed[id] {
			t.Errorf("unsupported invocation %s did not return a model-visible error", id)
		}
	}
	names := map[string]bool{}
	for _, spec := range captured.Tools {
		names[spec.Name] = true
	}
	for _, name := range []string{"Read", "Write", "WebFetch"} {
		if !names[name] {
			t.Errorf("remote catalog omitted compatible tool %q", name)
		}
	}
	for _, name := range []string{"Subagent", "SubagentStatus", "InspectSubagent", "Parallel", "Team", "InspectMember", "SkillDraft", "Schedule"} {
		if names[name] {
			t.Errorf("remote catalog advertised unsupported tool %q", name)
		}
	}
}
