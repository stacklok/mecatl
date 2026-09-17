package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type remoteFactoryPlacement struct {
	env   tool.Environment
	binds *int
}

func (remoteFactoryPlacement) ValidatePlacement(context.Context) error { return nil }
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

func TestRemoteDeploymentNoFSUsesLocalAttenuationWithoutProviderCall(t *testing.T) {
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

func TestRemoteExecutionRealFactoryCarriesPostureAndAttenuatedCatalog(t *testing.T) {
	ws := memfs.NewWorkspace("/workspace")
	if _, err := ws.CreateFile(context.Background(), "main.go", []byte("package main\n")); err != nil {
		t.Fatal(err)
	}
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env-1", Revision: "rev-1"}
	env := tool.MustEnvironment(ref, ws, memledger.New(), nil)
	var mu sync.Mutex
	var captured port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { mu.Lock(); captured = req; mu.Unlock() })},
		mockllm.ToolCallTurn(session.ToolCall{ID: "unknown", Name: "NoSuchTool", Args: json.RawMessage(`{}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"main.go"}`)}),
		mockllm.TextTurn(""),
		mockllm.TextTurn("done"),
	)
	localProject := t.TempDir()
	if err := os.WriteFile(filepath.Join(localProject, "AGENTS.md"), []byte("LOCAL_PROJECT_MARKER_DO_NOT_LOAD\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	built, err := buildIsolated(t, context.Background(), Config{MockProvider: provider, PlacementProvider: remoteFactoryPlacement{env: env}, PlacementScope: "remote", RemoteExecution: true, Workspace: localProject, TrustProject: true, NoSoul: true, SchedulerEnabled: false})
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
	run, err := built.Service.StartRun(ctx, sess.ID, "read the remote fixture")
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
	if strings.Contains(captured.System.Render(), "LOCAL_PROJECT_MARKER_DO_NOT_LOAD") {
		t.Fatal("remote session ingested host-local project instructions")
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
