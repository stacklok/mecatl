package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// constClock is a port.Clock that always returns the same instant, so a child
// session minted under it has a deterministic CreatedAt (and zero-duration
// timings). It proves the engine reads the INJECTED clock, not the wall clock.
type constClock struct{ t time.Time }

func (c constClock) Now() time.Time { return c.t }

// TestSubagentChildCreatedAtFromInjectedClock is the end-to-end WIRING proof for
// issue #116. It is not enough that Engine.now() returns the injected clock (that
// is the internal TestEngineNow) — the child-session mint must actually CALL it.
// A subagent child is run under a fixed-clock engine, persisted, reloaded, and its
// CreatedAt asserted to equal the injected instant. This is the load-bearing
// reroute (buildChildSession, the persisted/restart-relevant child); before #116
// it was time.Now(), and a revert to time.Now() fails BOTH this test and
// engine/arch's TestNoWallClockInEngineCore.
func TestSubagentChildCreatedAtFromInjectedClock(t *testing.T) {
	want := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	store := memstore.New()
	childLLM := mockllm.NewWith(nil, mockllm.TextTurn("DONE"))
	childEngine := agent.NewEngine(agent.Deps{
		LLM:     childLLM,
		Catalog: catalogWith(t),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "child-model",
		Clock:   constClock{t: want},
	})
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	res := runOneSubagent(t, task, "p1", `{"prompt":"investigate"}`)
	if res.IsError {
		t.Fatalf("subagent run errored: %q", res.Content)
	}

	sess, err := store.Load(context.Background(), session.SessionID("subagent-p1"))
	if err != nil {
		t.Fatalf("child not persisted: %v", err)
	}
	if !sess.CreatedAt.Equal(want) {
		t.Errorf("child CreatedAt = %v, want the injected clock's %v — buildChildSession is not reading the injected port.Clock",
			sess.CreatedAt, want)
	}
}
