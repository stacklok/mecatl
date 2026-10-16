package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// parkAwaitingInMode drives a default session to a durable pending Write ask
// on a loose deployment, then rewrites the stored mode to model a snapshot
// parked under a deployment that admitted stored.
func parkAwaitingInMode(t *testing.T, store port.SessionStore, stored session.PermissionMode) (session.SessionID, string) {
	t.Helper()
	ctx := context.Background()
	var ran atomic.Int64
	loose := newAskingService(t, store, permstore.New(), &ran,
		mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"a.go"}`)))), false)
	sess, err := loose.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	askID := driveServiceToAwaiting(t, loose, sess.ID)
	parked, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	parked.Mode = stored
	if err := store.Save(ctx, parked); err != nil {
		t.Fatal(err)
	}
	return sess.ID, askID
}

// recoveryService is a restarted deployment over store with the given ceiling
// and checker; lease, when non-nil, is shared with a peer.
func recoveryService(t *testing.T, store port.SessionStore, llm *mockllm.Provider, ran *atomic.Int64, ceiling session.PermissionMode, checker bool, lease port.SessionLease) *server.Service {
	t.Helper()
	return recoveryServiceAs(t, store, llm, ran, ceiling, checker, lease, "recovering")
}

// recoveryServiceAs is recoveryService with an explicit lease owner, so two
// replicas can share one lease backend.
func recoveryServiceAs(t *testing.T, store port.SessionStore, llm *mockllm.Provider, ran *atomic.Int64, ceiling session.PermissionMode, checker bool, lease port.SessionLease, owner string) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	cat.MustRegister(&writeAskTool{ran: ran})
	cfg := server.Config{
		Engine:                     agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(nil, permstore.New()), Model: "test-model", Store: store}),
		Store:                      store,
		Now:                        func() time.Time { return time.Unix(0, 0) },
		MaxAllowedMode:             ceiling,
		EnforcingCheckerConfigured: checker,
	}
	if lease != nil {
		cfg.SessionLease, cfg.LeaseOwner, cfg.LeaseTTL, cfg.LeaseRenewInterval = lease, owner, time.Minute, time.Hour
	}
	svc, err := newPlacementTestService(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// TestSetModeDowngradeRecoversRefusedAwaitingSession pins option B's recovery:
// a stored session awaiting an approval in a mode the restarted server refuses
// is unrecoverable by approval, and a SetMode to an admitted lower mode cancels
// the pending turn without any provider or tool work and applies the mode in
// one save; the next StartRun then runs in the new mode.
func TestSetModeDowngradeRecoversRefusedAwaitingSession(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	id, askID := parkAwaitingInMode(t, store, session.ModeYolo)

	var ran atomic.Int64
	llm := mockllm.New(mockllm.TextTurn("continued"))
	svc := recoveryService(t, store, llm, &ran, session.ModeAccept, true, nil)

	_, err := svc.ApproveRun(ctx, id, askID, session.VerdictAllowOnce, "")
	if !errors.Is(err, server.ErrFailedPrecondition) || !strings.Contains(err.Error(), "SetMode to an admitted lower mode") {
		t.Fatalf("ApproveRun err = %v, want FailedPrecondition naming the SetMode downgrade", err)
	}

	got, err := svc.SetMode(ctx, id, session.ModeDefault)
	if err != nil {
		t.Fatalf("SetMode(default) on refused awaiting session: %v", err)
	}
	if got.Mode != session.ModeDefault || got.State != session.StateCancelled {
		t.Fatalf("SetMode returned mode %q state %q, want default/cancelled", got.Mode, got.State)
	}
	if llm.Calls() != 0 || ran.Load() != 0 {
		t.Fatalf("recovery ran work: provider calls %d, tool executions %d", llm.Calls(), ran.Load())
	}
	kept, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := kept.PendingAsk(); pending || kept.Mode != session.ModeDefault || kept.State != session.StateCancelled {
		t.Fatalf("stored after recovery: mode %q state %q pending %t", kept.Mode, kept.State, pending)
	}
	if _, err := svc.ApproveRun(ctx, id, askID, session.VerdictAllowOnce, ""); err == nil {
		t.Fatal("the cancelled ask still resumed")
	}

	run, err := svc.StartRun(ctx, id, "continue")
	if err != nil {
		t.Fatalf("StartRun after recovery: %v", err)
	}
	drainRun(t, run)
	svc.FinishRun(id, run)
	if llm.Calls() != 1 || ran.Load() != 0 {
		t.Fatalf("next run: provider calls %d, tool executions %d; want 1, 0", llm.Calls(), ran.Load())
	}
	after, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode != session.ModeDefault || after.State != session.StateCompleted {
		t.Fatalf("after next run: mode %q state %q, want default/completed", after.Mode, after.State)
	}
}

// TestSetModeOnAwaitingSessionStillRefusedOutsideRecovery pins that only the
// narrow recovery widens SetMode: every other awaiting SetMode is refused with
// the pending ask intact and no work run.
func TestSetModeOnAwaitingSessionStillRefusedOutsideRecovery(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		stored  session.PermissionMode
		ceiling session.PermissionMode
		checker bool
		target  session.PermissionMode
		peer    bool
		want    error
	}{
		{"stored mode still admitted", session.ModeAccept, session.ModeAccept, true, session.ModeDefault, false, server.ErrInvalidArgument},
		{"target above ceiling", session.ModeYolo, session.ModeAccept, true, session.ModeAuto, false, server.ErrInvalidArgument},
		{"target not lower", session.ModeAuto, session.ModeYolo, false, session.ModeYolo, false, server.ErrInvalidArgument},
		{"peer holds the lease", session.ModeYolo, session.ModeAccept, true, session.ModeDefault, true, server.ErrSessionLeasedElsewhere},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			id, askID := parkAwaitingInMode(t, store, tc.stored)
			var lease port.SessionLease
			if tc.peer {
				shared := memlease.New(wallclock.Clock{}, time.Minute)
				if _, err := shared.Acquire(ctx, id, "peer"); err != nil {
					t.Fatalf("peer Acquire: %v", err)
				}
				lease = shared
			}
			var ran atomic.Int64
			llm := mockllm.New(mockllm.TextTurn("must not run"))
			svc := recoveryService(t, store, llm, &ran, tc.ceiling, tc.checker, lease)

			if _, err := svc.SetMode(ctx, id, tc.target); !errors.Is(err, tc.want) {
				t.Fatalf("SetMode(%s) err = %v, want %v", tc.target, err, tc.want)
			}
			if llm.Calls() != 0 || ran.Load() != 0 {
				t.Fatalf("refusal ran work: provider calls %d, tool executions %d", llm.Calls(), ran.Load())
			}
			kept, err := store.Load(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if pending, ok := kept.PendingAsk(); kept.State != session.StateAwaiting || !ok || pending.AskID != askID || kept.Mode != tc.stored {
				t.Fatalf("refusal changed the session: state %q mode %q pending %t", kept.State, kept.Mode, ok)
			}
		})
	}
}
