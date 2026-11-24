package server_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

var runOwnerBob = &session.Principal{Issuer: brokerAlice.Issuer, Subject: "bob-117", GrantType: session.GrantTypeUser}

// runProbeTool is a Write tool that asks for approval and sends the main
// agent's run its call's context carries, the run a gateway call made there
// would name.
type runProbeTool struct{ runs chan agentcredential.Run }

func (*runProbeTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Write", Description: "Write: test tool", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*runProbeTool) ReadOnly() bool { return false }
func (p *runProbeTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	run, _ := agentcredential.RunFromContext(ctx)
	p.runs <- run
	return session.NewToolResult(in.ID, "wrote"), nil
}

var _ tool.Tool = (*runProbeTool)(nil)

// newRunProbeService builds a service whose model plays turns. engineSaves is
// as for newAskingService.
func newRunProbeService(t *testing.T, store port.SessionStore, probe *runProbeTool, ownershipEnforced, engineSaves bool, turns ...mockllm.Turn) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	cat.MustRegister(probe)
	var engineStore port.SessionStore
	if engineSaves {
		engineStore = store
	}
	llm := mockllm.New(turns...)
	svc, err := newPlacementTestService(server.Config{
		Engine:            agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(nil, permstore.New()), Model: "test-model", Store: engineStore}),
		Store:             store,
		OwnershipEnforced: ownershipEnforced,
		Now:               func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// parkRun starts a run in ctx that parks at its Write ask, saves the awaiting
// session, and ends the run as a dying process would. It returns the ask.
// writeTurn makes the Write call that asks.
func writeTurn() mockllm.Turn {
	return mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"a.go"}`)))
}

func parkRun(ctx context.Context, t *testing.T, svc *server.Service, id session.SessionID) string {
	t.Helper()
	run, err := svc.StartRun(ctx, id, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var askID string
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && askID == "" {
			askID = ev.Ask.AskID
			svc.Persist(ctx, id)
			run.Cancel()
		}
	}
	svc.FinishRun(id, run)
	if askID == "" {
		t.Fatal("the run never asked")
	}
	return askID
}

func probedRun(t *testing.T, probe *runProbeTool) agentcredential.Run {
	t.Helper()
	select {
	case run := <-probe.runs:
		return run
	case <-time.After(10 * time.Second):
		t.Fatal("the Write call never ran")
		return agentcredential.Run{}
	}
}

func brokerOwner(p *session.Principal) identitybroker.Owner {
	return identitybroker.Owner{Issuer: p.Issuer, Subject: p.Subject}
}

// TestAResumedRunKeepsTheOwnerItStartedWith proves an interactive run's owner
// is the caller that started it, recorded with the run and kept across a
// restart: each resume path continues the run under that owner, both when
// the session's owner is unchanged (today's single-owner sessions, where the
// owner is the one the session's owner gave before) and when the session's
// owner value has changed since the run started.
func TestAResumedRunKeepsTheOwnerItStartedWith(t *testing.T) {
	resumes := map[string]func(ctx context.Context, svc *server.Service, id session.SessionID, runID, askID string) error{
		"approve": func(ctx context.Context, svc *server.Service, id session.SessionID, runID, askID string) error {
			run, err := svc.ApproveRun(ctx, id, askID, session.VerdictAllowOnce, runID)
			drain(run)
			return err
		},
		"resolve run ask": func(ctx context.Context, svc *server.Service, id session.SessionID, runID, askID string) error {
			_, err := svc.ResolveRunAsk(ctx, id, runID, askID, session.VerdictAllowOnce)
			return err
		},
	}
	for name, resume := range resumes {
		for _, resumer := range []*session.Principal{brokerAlice, runOwnerBob} {
			t.Run(name+"/resumed by "+resumer.Subject, func(t *testing.T) {
				dir := t.TempDir()
				store, err := jsonlstore.New(dir)
				if err != nil {
					t.Fatal(err)
				}
				aliceCtx := session.WithPrincipal(t.Context(), brokerAlice)
				parking := newRunProbeService(t, store, &runProbeTool{runs: make(chan agentcredential.Run, 1)}, true, false, writeTurn())
				sess, err := parking.CreateSession(aliceCtx, session.ModeDefault, session.Limits{})
				if err != nil {
					t.Fatal(err)
				}
				askID := parkRun(aliceCtx, t, parking, sess.ID)

				parked, err := store.Load(t.Context(), sess.ID)
				if err != nil {
					t.Fatal(err)
				}
				runID := parked.RunID()
				if got := parked.RunOwner(); runID == "" || got == nil || *got != *brokerAlice {
					t.Fatalf("parked run %q owner = %+v, want %+v", runID, got, brokerAlice)
				}
				if *resumer != *brokerAlice {
					// The session's owner value changes after the run started;
					// ownership then admits only the new owner's requests.
					parked.Owner = resumer
					if err := store.Save(t.Context(), parked); err != nil {
						t.Fatal(err)
					}
				}

				// A restart: a new service over the same directory.
				reopened, err := jsonlstore.New(dir)
				if err != nil {
					t.Fatal(err)
				}
				probe := &runProbeTool{runs: make(chan agentcredential.Run, 1)}
				resuming := newRunProbeService(t, reopened, probe, true, true, mockllm.TextTurn("done"))
				if err := resume(session.WithPrincipal(t.Context(), resumer), resuming, sess.ID, runID, askID); err != nil {
					t.Fatalf("resume: %v", err)
				}
				want := agentcredential.Run{ID: runID, Owner: brokerOwner(brokerAlice)}
				if got := probedRun(t, probe); got != want {
					t.Fatalf("resumed run = %+v, want %+v", got, want)
				}
			})
		}
	}
}

// TestANewRunIsOwnedByItsCaller proves a new interactive run's owner comes
// from the request that starts it, not from the session's owner. Ownership
// enforcement makes the two equal, so the session here is unenforced; a
// request with no verified caller starts a run with no owner, whose gateway
// calls fail closed rather than borrow the session owner's.
func TestANewRunIsOwnedByItsCaller(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caller *session.Principal
		want   *session.Principal
	}{
		{name: "the session's owner", caller: brokerAlice, want: brokerAlice},
		{name: "another caller", caller: runOwnerBob, want: runOwnerBob},
		{name: "no verified caller", caller: nil, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := jsonlstore.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			parking := newRunProbeService(t, store, &runProbeTool{runs: make(chan agentcredential.Run, 1)}, false, false, writeTurn())
			sess, err := parking.CreateSession(session.WithPrincipal(t.Context(), brokerAlice), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if sess.Owner == nil || *sess.Owner != *brokerAlice {
				t.Fatalf("session owner = %+v, want %+v", sess.Owner, brokerAlice)
			}
			ctx := session.WithPrincipal(t.Context(), tc.caller)
			askID := parkRun(ctx, t, parking, sess.ID)
			parked, err := store.Load(t.Context(), sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := parked.RunOwner(); (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("run owner = %+v, want %+v", got, tc.want)
			}
			reopened, err := jsonlstore.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			probe := &runProbeTool{runs: make(chan agentcredential.Run, 1)}
			resuming := newRunProbeService(t, reopened, probe, false, true, mockllm.TextTurn("done"))
			run, err := resuming.ApproveRun(ctx, sess.ID, askID, session.VerdictAllowOnce, parked.RunID())
			if err != nil {
				t.Fatal(err)
			}
			drain(run)
			got := probedRun(t, probe)
			if tc.want == nil {
				if !got.IsZero() {
					t.Fatalf("run = %+v, want none", got)
				}
				return
			}
			if want := (agentcredential.Run{ID: parked.RunID(), Owner: brokerOwner(tc.want)}); got != want {
				t.Fatalf("run = %+v, want %+v", got, want)
			}
		})
	}
}
