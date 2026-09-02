package server_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// newLimitsService builds a minimal Service with the given DefaultLimits.
func newLimitsService(t *testing.T, def session.Limits) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:        engine,
		Store:         memstore.New(),
		Workspaces:    func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultLimits: def,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// Finding 3: a session created with empty (all-zero) limits must inherit the
// injected DefaultLimits so it is bounded rather than running unbounded.
func TestCreateSessionAppliesDefaultLimits(t *testing.T) {
	def := session.Limits{MaxTurns: 50, MaxToolCalls: 200, MaxConsecutiveFailures: 5}
	svc := newLimitsService(t, def)

	sess, err := svc.CreateSession(context.Background(), "", session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Limits != def {
		t.Fatalf("default limits not applied: got %+v, want %+v", sess.Limits, def)
	}
}

// A fully-set explicit Limits must be kept verbatim — no field is zero, so nothing
// is defaulted.
func TestCreateSessionKeepsFullyExplicitLimits(t *testing.T) {
	def := session.Limits{MaxTurns: 50, MaxToolCalls: 200, MaxConsecutiveFailures: 5}
	svc := newLimitsService(t, def)

	explicit := session.Limits{MaxTurns: 3, MaxToolCalls: 9, MaxConsecutiveFailures: 2}
	sess, err := svc.CreateSession(context.Background(), "", explicit)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Limits != explicit {
		t.Fatalf("fully-set explicit limits overridden: got %+v, want %+v", sess.Limits, explicit)
	}
}

// A PARTIALLY-set Limits keeps its non-zero caps and inherits the rest per-field
// from DefaultLimits. This is the trap the --max-turns flag must not fall into:
// pinning ONLY MaxTurns must NOT silently disable the tool-call / failure caps (a
// zero field means "unset", not "unlimited", once a default is supplied).
func TestCreateSessionPerFieldDefaultsPreserveOtherCaps(t *testing.T) {
	def := session.Limits{MaxTurns: 50, MaxToolCalls: 200, MaxConsecutiveFailures: 5}
	svc := newLimitsService(t, def)

	sess, err := svc.CreateSession(context.Background(), "", session.Limits{MaxTurns: 3})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	want := session.Limits{MaxTurns: 3, MaxToolCalls: 200, MaxConsecutiveFailures: 5}
	if sess.Limits != want {
		t.Fatalf("per-field defaulting wrong: got %+v, want %+v (MaxTurns pinned, the rest inherited — the other caps must NOT become 0/unlimited)", sess.Limits, want)
	}
}
