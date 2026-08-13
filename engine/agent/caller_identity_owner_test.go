package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// ownerAlice and ownerBob are the two principals the child-inheritance test
// uses. They differ in BOTH halves of the identity pair, so an assertion that
// compares only `sub` still distinguishes them.
var (
	ownerAlice = &session.Principal{Issuer: "https://idp.example/alice-realm", Subject: "alice", GrantType: session.GrantTypeUser, Name: "Alice"}
	ownerBob   = &session.Principal{Issuer: "https://idp.example/bob-realm", Subject: "bob", GrantType: session.GrantTypeUser, Name: "Bob"}
)

// TestCallerIdentity_Scenario3_ForkInheritsSourceOwner pins the engine half of
// AC3.3: a `subagent-`/`parallel-`/`team-` child session inherits the PARENT
// session's owner — the source's, not the calling goroutine's. The context this
// run is driven under carries a DIFFERENT principal (Bob), so a child that
// picked its owner off the ambient context instead of the parent aggregate
// fails here: that is exactly the laundering path the AC forbids. An OWNERLESS
// parent yields an OWNERLESS child — never fabricated, never rejected (the
// no-auth path must keep working).
//
// All THREE delegation families are driven, because all three mint their child
// session through the same parentCaps.inheritOwner seam: Subagent, a Parallel
// branch, and a Team member. Asserting only one would leave the other two's
// attribution unproven.
//
// The service half (a forked session inheriting the source session's owner) is
// pinned by the same-named test in internal/adapter/server.
func TestCallerIdentity_Scenario3_ForkInheritsSourceOwner(t *testing.T) {
	for _, path := range []struct {
		name string
		// childID is the persisted child session id the delegation path mints.
		childID session.SessionID
		// register wires the delegation tool onto the parent catalog, persisting
		// its children into store.
		register func(t *testing.T, cat *tool.Catalog, store port.SessionStore)
		// call is the parent model's tool call that starts the delegation.
		call session.ToolCall
	}{
		{
			name:    "subagent",
			childID: "subagent-p1",
			register: func(t *testing.T, cat *tool.Catalog, store port.SessionStore) {
				t.Helper()
				cat.MustRegister(NewSubagentTool(childEngineForOwnerTest(t), WithSubagentStore(store)))
			},
			call: session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"investigate"}`)),
		},
		{
			name:    "parallel branch",
			childID: "parallel-p1-0",
			register: func(t *testing.T, cat *tool.Catalog, store port.SessionStore) {
				t.Helper()
				cat.MustRegister(NewParallelTool(childEngineForOwnerTest(t), ownerTestForker{},
					WithParallelStore(store), WithParallelConcurrency(1)))
			},
			call: session.NewToolCall("p1", "Parallel", json.RawMessage(`{"tasks":["one"]}`)),
		},
		{
			name:    "team member",
			childID: MemberSessionID("p1", "worker"),
			register: func(t *testing.T, cat *tool.Catalog, store port.SessionStore) {
				t.Helper()
				cat.MustRegister(NewTeamTool(ownerTestMemberFactory(t),
					WithTeamToolStore(store), WithTeamToolReadOnlyForker(ownerTestForker{})))
			},
			call: session.NewToolCall("p1", "Team",
				json.RawMessage(`{"goal":"fix it","members":[{"name":"lead","role":"lead"},{"name":"worker","role":"work"}]}`)),
		},
	} {
		for _, tc := range []struct {
			name        string
			parentOwner *session.Principal
		}{
			{"owned parent", ownerAlice},
			{"ownerless parent", nil},
		} {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				store := memstore.New()
				allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
				cat := tool.NewCatalog()
				path.register(t, cat, store)

				parentLLM := mockllm.New(
					mockllm.ToolCallTurn(path.call),
					mockllm.TextTurn("parent done"),
				)
				e := NewEngine(Deps{LLM: parentLLM, Catalog: cat, Policy: allow, Model: "parent-model"})

				sess := session.New("owner-parent", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
				if err := sess.RestoreLabels(tc.parentOwner, ""); err != nil {
					t.Fatalf("RestoreLabels: %v", err)
				}

				// The run is driven under a context carrying BOB — a different
				// principal from the parent session's owner.
				ctx := session.WithPrincipal(context.Background(), ownerBob)
				r := e.Run(ctx, sess, memEnv("/ws"), RunRequest{Text: "go"})
				drainRunEvents(t, r)

				child, err := store.Load(context.Background(), path.childID)
				if err != nil || child == nil {
					t.Fatalf("child %q must be persisted: %v", path.childID, err)
				}
				switch {
				case tc.parentOwner == nil:
					if child.Owner != nil {
						t.Fatalf("ownerless parent produced child owned by %+v, want nil (never fabricated, never the caller's)", *child.Owner)
					}
				case child.Owner == nil:
					t.Fatalf("child owner = nil, want the PARENT's owner %+v", *tc.parentOwner)
				case *child.Owner != *tc.parentOwner:
					t.Fatalf("child owner = %+v, want the PARENT's owner %+v (a child must not be attributed to the calling goroutine)", *child.Owner, *tc.parentOwner)
				}
			})
		}
	}
}

// childEngineForOwnerTest is the one-turn child engine every delegation path in
// the AC3.3 test delegates to. Its content is irrelevant — what is under test is
// the OWNER stamped on the child session, not what the child says.
func childEngineForOwnerTest(t *testing.T) *Engine {
	t.Helper()
	return NewEngine(Deps{
		LLM:     mockllm.New(mockllm.TextTurn("child done")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "child-model",
	})
}

// ownerTestForker is the minimal tool.EnvironmentForker the Parallel and Team
// paths require: each fork is a fresh in-memory environment, no git, no cleanup.
type ownerTestForker struct{}

func (ownerTestForker) Fork(_ context.Context, _ tool.Environment, label string) (tool.Environment, func() error, string, error) {
	return memEnv("/fork-" + label), func() error { return nil }, "", nil
}

// ownerTestMemberFactory builds each team member's engine over its own scripted
// one-turn provider plus the team coordination tools, mirroring the real
// composition factory's shape.
func ownerTestMemberFactory(t *testing.T) TeamMemberEngineFactory {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	return func(tm *team.Team, spec MemberSpec, _ string) MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return MemberBuild{Engine: NewEngine(Deps{
			LLM:     mockllm.New(mockllm.TextTurn(spec.Name + " done")),
			Catalog: cat,
			Policy:  allow,
			Model:   "member-model",
		})}
	}
}

// drainRunEvents drains a run to completion with a deadline, so a wedge fails
// the test instead of hanging it.
func drainRunEvents(t *testing.T, r *Run) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case _, ok := <-r.Events():
			if !ok {
				return
			}
		case <-deadline:
			r.Cancel()
			t.Fatal("run did not terminate (possible wedge)")
		}
	}
}
