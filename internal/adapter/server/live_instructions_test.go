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
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestLiveInstructionsSurviveStoreReloadButNotServiceRestart(t *testing.T) {
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(ctx, "AGENTS.md", []byte("original guidance")); err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	var requests []string
	makeService := func() *server.Service {
		llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			for _, m := range req.Messages {
				requests = append(requests, m.Text)
			}
		})}, mockllm.TextTurn("ok"), mockllm.TextTurn("ok"), mockllm.TextTurn("ok"), mockllm.TextTurn("ok"), mockllm.TextTurn("ok"))
		eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model", Instructions: prompt.RootAssembler{Source: ws, SourceID: "ws@1", SourcePrefix: "."}, Compactor: serviceCompactCompactor{out: []session.Message{session.NewUserMessage("short")}, summary: "summary"}, TokenCounter: serviceCompactCounter{}})
		svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, PlacementProvider: testPlacementProvider{root: "/ws", workspaces: func(string) tool.Workspace { return ws }}, PlacementScope: "test", SharedEngineRoot: "/ws"})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	svc := makeService()
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{MaxTurns: 10})
	if err != nil {
		t.Fatal(err)
	}
	turn := func(s *server.Service) {
		t.Helper()
		run, err := s.StartRun(ctx, sess.ID, "continue")
		if err != nil {
			t.Fatal(err)
		}
		_ = finalText(t, run)
		s.Persist(ctx, sess.ID)
		s.FinishRun(sess.ID, run)
	}
	turn(svc)
	loaded, err := svc.LoadSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := loaded.InstructionSnapshot()
	if len(snapshot.Scopes) == 0 {
		t.Fatal("root guidance not captured")
	}
	snapshot.Scopes[0].Text = "aliased guidance"
	loaded.ReplaceInstructionSnapshot(snapshot)
	if err := ws.Write(ctx, "AGENTS.md", []byte("changed guidance")); err != nil {
		t.Fatal(err)
	}
	requests = nil
	turn(svc)
	if got := strings.Join(requests, "\n"); !strings.Contains(got, "original guidance") || strings.Contains(got, "changed guidance") || strings.Contains(got, "aliased guidance") {
		t.Fatalf("same live session request = %q", got)
	}
	compaction, err := svc.CompactSession(ctx, sess.ID, nil)
	if err != nil || !compaction.Changed {
		t.Fatalf("manual compaction = %+v, %v", compaction, err)
	}
	requests = nil
	turn(svc)
	if got := strings.Join(requests, "\n"); !strings.Contains(got, "original guidance") || strings.Contains(got, "changed guidance") {
		t.Fatalf("post-compaction request = %q", got)
	}
	forkID, err := forkSession(svc, ctx, sess.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	requests = nil
	forkRun, err := svc.StartRun(ctx, forkID, "fork")
	if err != nil {
		t.Fatal(err)
	}
	_ = finalText(t, forkRun)
	svc.Persist(ctx, forkID)
	svc.FinishRun(forkID, forkRun)
	if got := strings.Join(requests, "\n"); !strings.Contains(got, "original guidance") || strings.Contains(got, "changed guidance") || strings.Contains(got, "aliased guidance") {
		t.Fatalf("fork request = %q", got)
	}
	if err := store.Delete(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	replacement := session.New(sess.ID, session.ModeDefault, sess.EnvironmentRef, session.Limits{MaxTurns: 10}, sess.CreatedAt)
	if err := store.Save(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	requests = nil
	turn(svc)
	if got := strings.Join(requests, "\n"); !strings.Contains(got, "changed guidance") || strings.Contains(got, "original guidance") {
		t.Fatalf("new incarnation request = %q", got)
	}
	svc.Close()
	requests = nil
	next := makeService()
	defer next.Close()
	turn(next)
	if got := strings.Join(requests, "\n"); !strings.Contains(got, "changed guidance") || strings.Contains(got, "original guidance") {
		t.Fatalf("restarted session request = %q", got)
	}
}
