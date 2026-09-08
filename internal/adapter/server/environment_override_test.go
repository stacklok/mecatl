package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func newServerTestSubagent(engine *agent.Engine, opts ...agent.SubagentOption) tool.Tool {
	opts = append(opts, agent.WithSubagentReadLedgerFactory(func() tool.ReadLedger { return memledger.New() }))
	return agent.NewSubagentTool(engine, opts...)
}

func newServerTestTeamTool(factory agent.TeamMemberEngineFactory, opts ...agent.TeamOption) tool.Tool {
	opts = append(opts, agent.WithTeamToolReadLedgerFactory(func() tool.ReadLedger { return memledger.New() }))
	return agent.NewTeamTool(factory, opts...)
}

// TestSetSessionEnvironmentOverrideIsUsedVerbatim pins issue #462 phase-2
// finding #2: a per-session Environment override registered via
// SetSessionEnvironment is the COMPLETE environment a run executes against — the
// Service does NOT guess a ref or runner from the override's presence. An
// ACP-style override (a real-filesystem workspace rooted at a non-empty path,
// shell-less) must carry a LOCAL ref whose ID is the workspace root, NOT a
// guessed nofs ref.
func TestSetSessionEnvironmentOverrideIsUsedVerbatim(t *testing.T) {
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: shared,
		Store:  memstore.New(),

		DefaultLimits: session.Limits{MaxTurns: 5},
		Now:           func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Register a complete shell-less Environment with an ACCURATE local ref — the
	// shape the ACP adapter installs (real FS workspace, no shell). The Service
	// must use this verbatim: ref Kind local, ID the workspace root, no runner.
	ws := memfs.NewWorkspace(sess.EnvironmentRef.ID)
	wantRef := sess.EnvironmentRef
	override := tool.MustEnvironment(wantRef, ws, memledger.New(), nil)
	svc.SetSessionEnvironment(sess.ID, override)

	run, err := svc.StartRun(context.Background(), sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainServerRun(run); got != "ok" {
		t.Fatalf("run reply = %q, want ok", got)
	}
}
