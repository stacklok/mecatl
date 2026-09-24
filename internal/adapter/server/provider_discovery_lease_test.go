package server_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestProviderModelDiscovery_Scenario3_LeaseConflictBeforeDiscovery(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	clock := &fixedClock{t: time.Unix(100, 0)}
	leases := memlease.New(clock, time.Hour)
	newEngine := func() *agent.Engine {
		return agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("must not infer")),
			Catalog: tool.NewCatalog(),
			Store:   store,
		})
	}
	var holderAdmission, competitorAdmission atomic.Int32
	base := server.Config{
		Engine:               newEngine(),
		Store:                store,
		DefaultResolvedModel: server.ResolvedModel{ProviderID: "native", ModelID: "live-only"},
		SessionLease:         leases,
		LeaseTTL:             time.Hour,
		LeaseRenewInterval:   time.Hour,
	}
	holderCfg := base
	holderCfg.LeaseOwner = "holder"
	holderCfg.AwaitContextWindow = func(context.Context, string, string) error {
		holderAdmission.Add(1)
		return server.ErrContextWindowUnavailable
	}
	holder, err := newPlacementTestService(holderCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()

	competitorCfg := base
	competitorCfg.Engine = newEngine()
	competitorCfg.LeaseOwner = "competitor"
	competitorCfg.AwaitContextWindow = func(context.Context, string, string) error {
		competitorAdmission.Add(1)
		return nil
	}
	competitor, err := newPlacementTestService(competitorCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer competitor.Close()

	sess, err := holder.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := holder.StartRun(ctx, sess.ID, "holder rejects before execution")
	if run != nil || !errors.Is(err, server.ErrContextWindowUnavailable) {
		t.Fatalf("holder admission: run=%t err=%v", run != nil, err)
	}
	if holderAdmission.Load() != 1 {
		t.Fatalf("holder admission callbacks=%d, want 1", holderAdmission.Load())
	}

	run, err = competitor.StartRun(ctx, sess.ID, "competitor must stop at lease")
	if run != nil || !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("competitor admission: run=%t err=%v", run != nil, err)
	}
	if competitorAdmission.Load() != 0 {
		t.Fatalf("competitor reached discovery admission %d times", competitorAdmission.Load())
	}
	if _, err := leases.Acquire(ctx, sess.ID, "third-owner"); !errors.Is(err, port.ErrLeaseHeld) {
		t.Fatalf("holder released session-lifetime lease after metadata rejection: %v", err)
	}
	got, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Conversation.Messages) != 0 {
		t.Fatalf("rejected/competing prompts changed history: %d messages", len(got.Conversation.Messages))
	}
}
