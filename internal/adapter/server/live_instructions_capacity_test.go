package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestFailedUnguidedAdmissionReleasesReservation(t *testing.T) {
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	fail := true
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: memstore.New(), MaxSessionEngines: 1, PlacementProvider: testPlacementProvider{root: "/ws", workspaces: func(string) tool.Workspace { return ws }}, PlacementScope: "test", SharedEngineRoot: "/ws", AwaitContextWindow: func(context.Context, string, string) error {
		if fail {
			return errors.New("unavailable")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	first, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartRun(ctx, first.ID, "first"); err == nil {
		t.Fatal("window failure was not reported")
	}
	fail = false
	second, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRun(ctx, second.ID, "second")
	if err != nil {
		t.Fatalf("failed unguided admission retained capacity: %v", err)
	}
	_ = finalText(t, run)
	svc.Persist(ctx, second.ID)
	svc.FinishRun(second.ID, run)
}

func TestUnguidedRunsReleaseLiveInstructionReservations(t *testing.T) {
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	var requests []string
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		for _, m := range req.Messages {
			requests = append(requests, m.Text)
		}
	})}, mockllm.TextTurn("one"), mockllm.TextTurn("two"), mockllm.TextTurn("three"), mockllm.TextTurn("four"))
	eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test", Instructions: prompt.RootAssembler{Source: ws, SourceID: "ws@1", SourcePrefix: "."}})
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: memstore.New(), MaxSessionEngines: 1, PlacementProvider: testPlacementProvider{root: "/ws", workspaces: func(string) tool.Workspace { return ws }}, PlacementScope: "test", SharedEngineRoot: "/ws"})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	first, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	turn := func(id session.SessionID) {
		t.Helper()
		run, err := svc.StartRun(ctx, id, "continue")
		if err != nil {
			t.Fatalf("run blocked by empty reservation: %v", err)
		}
		_ = finalText(t, run)
		svc.Persist(ctx, id)
		if id == first.ID {
			if _, err := svc.StartRun(ctx, second.ID, "concurrent"); !errors.Is(err, server.ErrTooManySessionEngines) {
				t.Fatalf("unsettled reservation capacity = %v", err)
			}
		}
		svc.FinishRun(id, run)
	}
	turn(first.ID)
	turn(second.ID)
	if err := ws.Write(ctx, "AGENTS.md", []byte("newly discovered guidance")); err != nil {
		t.Fatal(err)
	}
	requests = nil
	turn(first.ID)
	if got := strings.Join(requests, "\n"); !strings.Contains(got, "newly discovered guidance") {
		t.Fatalf("later prompt did not discover guidance: %q", got)
	}
	if err := ws.Write(ctx, "AGENTS.md", []byte("replacement guidance")); err != nil {
		t.Fatal(err)
	}
	requests = nil
	turn(first.ID)
	if got := strings.Join(requests, "\n"); !strings.Contains(got, "newly discovered guidance") || strings.Contains(got, "replacement guidance") {
		t.Fatalf("loaded guidance changed: %q", got)
	}
	if _, err := svc.StartRun(ctx, second.ID, "capacity"); !errors.Is(err, server.ErrTooManySessionEngines) {
		t.Fatalf("retained guidance capacity = %v", err)
	}
}

func TestUnguidedParkedApprovalRetainsLiveInstructionReservation(t *testing.T) {
	for _, resume := range []string{"live", "reloaded"} {
		t.Run(resume, func(t *testing.T) {
			ctx := context.Background()
			ws := memfs.NewWorkspace("/ws")
			var requests []string
			llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
				for _, m := range req.Messages {
					requests = append(requests, m.Text)
				}
			})}, mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"a.go"}`))), mockllm.TextTurn("approved"), mockllm.TextTurn("next"))
			cat := tool.NewCatalog()
			var executed atomic.Int64
			cat.MustRegister(&writeAskTool{ran: &executed})
			eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(nil, permstore.New()), Model: "test", Instructions: prompt.RootAssembler{Source: ws, SourceID: "ws@1", SourcePrefix: "."}})
			svc, err := newPlacementTestService(server.Config{Engine: eng, Store: memstore.New(), MaxSessionEngines: 1, PlacementProvider: testPlacementProvider{root: "/ws", workspaces: func(string) tool.Workspace { return ws }}, PlacementScope: "test", SharedEngineRoot: "/ws"})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			first, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			second, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			var live *agent.Run
			var askID string
			if resume == "reloaded" {
				askID = driveServiceToAwaiting(t, svc, first.ID)
			} else {
				live, err = svc.StartRun(ctx, first.ID, "go")
				if err != nil {
					t.Fatal(err)
				}
				for ev := range live.Events() {
					if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
						askID = ev.Ask.AskID
						svc.Persist(ctx, first.ID)
						break
					}
				}
				if askID == "" {
					t.Fatal("run did not ask for approval")
				}
			}
			if _, err := svc.StartRun(ctx, second.ID, "while parked"); !errors.Is(err, server.ErrTooManySessionEngines) {
				t.Fatalf("parked reservation capacity = %v", err)
			}
			if err := ws.Write(ctx, "AGENTS.md", []byte("created while parked")); err != nil {
				t.Fatal(err)
			}
			requests = nil
			run, err := svc.ApproveRun(ctx, first.ID, askID, session.VerdictAllowOnce, "")
			if err != nil {
				t.Fatal(err)
			}
			if live != nil {
				if run != nil {
					t.Fatal("live approval unexpectedly started another run")
				}
				run = live
			}
			_ = finalText(t, run)
			svc.Persist(ctx, first.ID)
			svc.FinishRun(first.ID, run)
			if got := strings.Join(requests, "\n"); strings.Contains(got, "created while parked") {
				t.Fatalf("parked absence was refreshed: %q", got)
			}
			next, err := svc.StartRun(ctx, second.ID, "after approval")
			if err != nil {
				t.Fatalf("settled unguided approval retained capacity: %v", err)
			}
			_ = finalText(t, next)
			svc.Persist(ctx, second.ID)
			svc.FinishRun(second.ID, next)
		})
	}
}

func TestIdleLoadsDoNotConsumeLiveInstructionCapacity(t *testing.T) {
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(ctx, "AGENTS.md", []byte("guided")); err != nil {
		t.Fatal(err)
	}
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test", Instructions: prompt.RootAssembler{Source: ws, SourceID: "ws@1", SourcePrefix: "."}})
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: memstore.New(), MaxSessionEngines: 1, PlacementProvider: testPlacementProvider{root: "/ws", workspaces: func(string) tool.Workspace { return ws }}, PlacementScope: "test", SharedEngineRoot: "/ws"})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for range 3 {
		idle, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.LoadSession(ctx, idle.ID); err != nil {
			t.Fatalf("idle load: %v", err)
		}
	}
	guided, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRun(ctx, guided.ID, "guide")
	if err != nil {
		t.Fatalf("idle loads exhausted guidance capacity: %v", err)
	}
	_ = finalText(t, run)
	svc.Persist(ctx, guided.ID)
	svc.FinishRun(guided.ID, run)
}

func TestLiveInstructionsRetainedAcrossParkedApproval(t *testing.T) {
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(ctx, "AGENTS.md", []byte("parked guidance")); err != nil {
		t.Fatal(err)
	}
	var requests []string
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		for _, m := range req.Messages {
			requests = append(requests, m.Text)
		}
	})}, mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"a.go"}`))), mockllm.TextTurn("approved"))
	cat := tool.NewCatalog()
	var executed atomic.Int64
	cat.MustRegister(&writeAskTool{ran: &executed})
	store := memstore.New()
	eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(nil, permstore.New()), Model: "test", Instructions: prompt.RootAssembler{Source: ws, SourceID: "ws@1", SourcePrefix: "."}})
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, PlacementProvider: testPlacementProvider{root: "/ws", workspaces: func(string) tool.Workspace { return ws }}, PlacementScope: "test", SharedEngineRoot: "/ws"})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	askID := driveServiceToAwaiting(t, svc, sess.ID)
	if err := ws.Write(ctx, "AGENTS.md", []byte("changed while parked")); err != nil {
		t.Fatal(err)
	}
	requests = nil
	run, err := svc.ApproveRun(ctx, sess.ID, askID, session.VerdictAllowOnce, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = finalText(t, run)
	svc.Persist(ctx, sess.ID)
	svc.FinishRun(sess.ID, run)
	if executed.Load() != 1 {
		t.Fatalf("write executions = %d", executed.Load())
	}
	if got := strings.Join(requests, "\n"); !strings.Contains(got, "parked guidance") || strings.Contains(got, "changed while parked") {
		t.Fatalf("resumed provider request = %q", got)
	}
}

func TestLiveInstructionCapacityAndCloseRefusal(t *testing.T) {
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(ctx, "AGENTS.md", []byte("guidance")); err != nil {
		t.Fatal(err)
	}
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("one"), mockllm.TextTurn("two")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test", Instructions: prompt.RootAssembler{Source: ws, SourceID: "ws@1", SourcePrefix: "."}})
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: memstore.New(), MaxSessionEngines: 1, PlacementProvider: testPlacementProvider{root: "/ws", workspaces: func(string) tool.Workspace { return ws }}, PlacementScope: "test", SharedEngineRoot: "/ws"})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	a, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRun(ctx, a.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	_ = finalText(t, run)
	svc.Persist(ctx, a.ID)
	if err := svc.EndSession(ctx, a.ID); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("active close = %v", err)
	}
	if _, err := svc.StartRun(ctx, b.ID, "second"); !errors.Is(err, server.ErrTooManySessionEngines) {
		t.Fatalf("capacity before close = %v", err)
	}
	svc.FinishRun(a.ID, run)
	if err := svc.EndSession(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	next, err := svc.StartRun(ctx, b.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	_ = finalText(t, next)
	svc.Persist(ctx, b.ID)
	svc.FinishRun(b.ID, next)
}
