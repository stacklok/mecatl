package server_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestFactorySessionLiveInstructionsAcrossRebuild(t *testing.T) {
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(ctx, "AGENTS.md", []byte("initial factory guidance")); err != nil {
		t.Fatal(err)
	}
	var requests []string
	build := func() *agent.Engine {
		llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			for _, m := range req.Messages {
				requests = append(requests, m.Text)
			}
		})}, mockllm.TextTurn("ok"), mockllm.TextTurn("ok"), mockllm.TextTurn("ok"))
		return agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model", Instructions: prompt.RootAssembler{Source: ws, SourceID: "ws@1", SourcePrefix: "."}})
	}
	factory := func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
		return server.SessionEngineResult{Engine: build(), Close: func() error { return nil }}, nil
	}
	svc, err := newPlacementTestService(server.Config{Engine: build(), Store: memstore.New(), PlacementProvider: testPlacementProvider{root: "/ws", workspaces: func(string) tool.Workspace { return ws }}, PlacementScope: "test", SharedEngineRoot: "/ws", SessionEngine: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{MaxTurns: 10}, server.ProviderSelector{ProviderID: "test", ModelID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	runTurn := func() {
		t.Helper()
		run, err := svc.StartRun(ctx, sess.ID, "continue")
		if err != nil {
			t.Fatal(err)
		}
		_ = finalText(t, run)
		svc.Persist(ctx, sess.ID)
		svc.FinishRun(sess.ID, run)
	}
	runTurn()
	if err := ws.Write(ctx, "AGENTS.md", []byte("edited factory guidance")); err != nil {
		t.Fatal(err)
	}
	// A different mode forces the per-session engine to be rebuilt before this turn.
	if _, err := svc.SetMode(ctx, sess.ID, session.ModePlan); err != nil {
		t.Fatal(err)
	}
	requests = nil
	runTurn()
	if got := strings.Join(requests, "\n"); !strings.Contains(got, "initial factory guidance") || strings.Contains(got, "edited factory guidance") {
		t.Fatalf("rebuilt factory request = %q", got)
	}
}
