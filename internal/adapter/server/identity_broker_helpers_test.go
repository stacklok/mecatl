package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/conformance"
)

// helperFixture is a service with one root session, created by alice, whose
// main agent is registered.
type helperFixture struct {
	broker *recordingBroker
	svc    *server.Service
	root   *session.Session
	main   identitybroker.RegisterAgentRequest
	// mainAgent is the root's registered main agent.
	mainAgent identitybroker.AgentRef
}

func newHelperFixture(t *testing.T) helperFixture {
	t.Helper()
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New(mockllm.TextTurn("done")))
	root, err := svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	main := wantRegistered(t, broker, wantBrokerSession(t, broker, root), root)
	held, ok := svc.HeldIdentityAgentForTest(root.ID, root.ID)
	if !ok {
		t.Fatal("the root's main agent holds no credential")
	}
	return helperFixture{broker: broker, svc: svc, root: root, main: main, mainAgent: held}
}

// rootContext is a run context rooted in f's root session.
func (f helperFixture) rootContext(t *testing.T) context.Context {
	return port.WithRootSessionID(aliceContext(t), f.root.ID)
}

func helperRef() session.EnvironmentRef {
	return session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
}

func newSubagentSession(t *testing.T, id, parent session.SessionID) *session.Session {
	t.Helper()
	sess, err := session.NewSubagent(id, session.ModeDefault, helperRef(), session.Limits{}, time.Unix(1, 0), parent, parentIncarnation(parent), "call-1")
	if err != nil {
		t.Fatal(err)
	}
	labelAlice(t, sess)
	return sess
}

func labelAlice(t *testing.T, sess *session.Session) {
	t.Helper()
	if err := sess.RestoreLabels(brokerAlice, session.Authority{}); err != nil {
		t.Fatal(err)
	}
}

func lastRegistration(t *testing.T, broker *recordingBroker) identitybroker.RegisterAgentRequest {
	t.Helper()
	regs := broker.registerRequests()
	if len(regs) == 0 {
		t.Fatal("nothing registered")
	}
	return regs[len(regs)-1]
}

// TestRegisterHelperRegistersEachKindUnderItsParent pins what a helper
// registers with: its own host session and incarnation, the root's broker
// session and owner, its definition, and its creating agent and that agent's
// host session as parent.
func TestRegisterHelperRegistersEachKindUnderItsParent(t *testing.T) {
	branch := func(t *testing.T, id, parent session.SessionID) *session.Session {
		t.Helper()
		sess, err := session.NewParallelBranch(id, session.ModeDefault, helperRef(), session.Limits{}, time.Unix(1, 0), parent, parentIncarnation(parent), "call-1", 0)
		if err != nil {
			t.Fatal(err)
		}
		labelAlice(t, sess)
		return sess
	}
	member := func(t *testing.T, id, parent session.SessionID) *session.Session {
		t.Helper()
		sess, err := session.NewTeamMember(id, session.ModeDefault, helperRef(), session.Limits{}, time.Unix(1, 0), "team-1", "lead", parent, parentIncarnation(parent))
		if err != nil {
			t.Fatal(err)
		}
		labelAlice(t, sess)
		return sess
	}
	cases := []struct {
		name       string
		build      func(*testing.T, session.SessionID, session.SessionID) *session.Session
		parent     bool // the session records the root as its parent
		definition string
		want       identitybroker.DefinitionRef
	}{
		{name: "a default subagent runs system/explorer", build: newSubagentSession, parent: true, want: conformance.ExplorerDefinition},
		{name: "a named subagent runs its loaded global definition", build: newSubagentSession, parent: true, definition: "code-reviewer", want: conformance.ReviewerDefinition},
		{name: "a parallel branch runs system/branch", build: branch, parent: true, want: conformance.BranchDefinition},
		{name: "a tool-driven team member runs system/member", build: member, parent: true, want: conformance.MemberDefinition},
		{name: "a member of a team created over the API has the root's main agent as parent", build: member, want: conformance.MemberDefinition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHelperFixture(t)
			var parent session.SessionID
			if tc.parent {
				parent = f.root.ID
			}
			child := tc.build(t, "child-1", parent)
			if err := f.svc.RegisterHelper(f.rootContext(t), child, port.ChildSessionInfo{DefinitionName: tc.definition}); err != nil {
				t.Fatalf("RegisterHelper: %v", err)
			}
			got := lastRegistration(t, f.broker)
			want := identitybroker.RegisterAgentRequest{
				SessionID:           f.main.SessionID,
				HostSessionID:       "child-1",
				Owner:               f.main.Owner,
				Incarnation:         string(child.Incarnation()),
				Definition:          tc.want,
				Parent:              f.mainAgent,
				ParentHostSessionID: string(f.root.ID),
			}
			if got != want {
				t.Fatalf("registration = %+v, want %+v", got, want)
			}
			if _, ok := f.svc.HeldIdentityAgentForTest(f.root.ID, "child-1"); !ok {
				t.Fatal("the helper's credential is not held under its root")
			}
		})
	}
}

// TestRegisterHelperUnderAHelper pins that a helper created by another helper
// has that helper as parent and reports its host session.
func TestRegisterHelperUnderAHelper(t *testing.T) {
	f := newHelperFixture(t)
	ctx := f.rootContext(t)
	if err := f.svc.RegisterHelper(ctx, newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{}); err != nil {
		t.Fatalf("RegisterHelper(child): %v", err)
	}
	child, _ := f.svc.HeldIdentityAgentForTest(f.root.ID, "child-1")
	if err := f.svc.RegisterHelper(ctx, newSubagentSession(t, "grandchild-1", "child-1"), port.ChildSessionInfo{}); err != nil {
		t.Fatalf("RegisterHelper(grandchild): %v", err)
	}
	got := lastRegistration(t, f.broker)
	if got.Parent != child || got.ParentHostSessionID != "child-1" {
		t.Fatalf("grandchild registration = %+v, want parent %+v in child-1", got, child)
	}
}

// TestRegisterHelperSkipsHelpersOutsideARegisteredTree pins that a helper
// whose root holds no registered main agent runs unregistered, as its gateway
// calls then fail closed.
func TestRegisterHelperSkipsHelpersOutsideARegisteredTree(t *testing.T) {
	f := newHelperFixture(t)
	before := len(f.broker.registerRequests())
	for name, ctx := range map[string]context.Context{
		"no root":                             aliceContext(t),
		"a default-placement team":            port.WithRootSessionID(aliceContext(t), ""),
		"a root with no main agent":           port.WithRootSessionID(aliceContext(t), "unregistered-root"),
		"the root session itself (no helper)": port.WithRootSessionID(aliceContext(t), "child-1"),
	} {
		if err := f.svc.RegisterHelper(ctx, newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{}); err != nil {
			t.Fatalf("RegisterHelper with %s: %v", name, err)
		}
	}
	if n := len(f.broker.registerRequests()); n != before {
		t.Fatalf("helpers outside a registered tree registered %d times", n-before)
	}
	// Positive control: the same helper in the root's tree registers.
	if err := f.svc.RegisterHelper(f.rootContext(t), newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{}); err != nil {
		t.Fatalf("RegisterHelper in the tree: %v", err)
	}
	if n := len(f.broker.registerRequests()); n != before+1 {
		t.Fatalf("a helper in the tree registered %d times, want 1", n-before)
	}
}

// TestRegisterHelperRefusals pins the errors that stop a helper.
func TestRegisterHelperRefusals(t *testing.T) {
	t.Run("an unregistered parent", func(t *testing.T) {
		f := newHelperFixture(t)
		err := f.svc.RegisterHelper(f.rootContext(t), newSubagentSession(t, "child-1", "not-registered"), port.ChildSessionInfo{})
		if !errors.Is(err, server.ErrInternal) {
			t.Fatalf("RegisterHelper under an unregistered parent = %v, want ErrInternal", err)
		}
	})
	t.Run("a definition the broker did not load", func(t *testing.T) {
		f := newHelperFixture(t)
		err := f.svc.RegisterHelper(f.rootContext(t), newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{DefinitionName: "planner"})
		if !errors.Is(err, server.ErrInternal) {
			t.Fatalf("RegisterHelper of an unloaded definition = %v, want ErrInternal", err)
		}
		if got := lastRegistration(t, f.broker); got.Definition != (identitybroker.DefinitionRef{Name: "planner"}) {
			t.Fatalf("an unloaded definition was sent as %+v, want by name only", got.Definition)
		}
		if _, ok := f.svc.HeldIdentityAgentForTest(f.root.ID, "child-1"); ok {
			t.Fatal("a refused helper's credential is held")
		}
	})
	t.Run("another owner's helper", func(t *testing.T) {
		f := newHelperFixture(t)
		child, err := session.NewSubagent("child-1", session.ModeDefault, helperRef(), session.Limits{}, time.Unix(1, 0), f.root.ID, parentIncarnation(f.root.ID), "call-1")
		if err != nil {
			t.Fatal(err)
		}
		bob := &session.Principal{Issuer: brokerAlice.Issuer, Subject: "bob-17", GrantType: session.GrantTypeUser}
		if err := child.RestoreLabels(bob, session.Authority{}); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.RegisterHelper(f.rootContext(t), child, port.ChildSessionInfo{}); !errors.Is(err, server.ErrInternal) {
			t.Fatalf("RegisterHelper of another owner's helper = %v, want ErrInternal", err)
		}
	})
	t.Run("an unavailable broker is retryable", func(t *testing.T) {
		f := newHelperFixture(t)
		f.broker.set(func(b *recordingBroker) { b.registerUnavailable = true })
		err := f.svc.RegisterHelper(f.rootContext(t), newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{})
		if !errors.Is(err, server.ErrIdentityBrokerUnavailable) {
			t.Fatalf("RegisterHelper with the broker down = %v, want ErrIdentityBrokerUnavailable", err)
		}
	})
}

// parentIncarnation is the incarnation a child records for parent; none
// without one.
func parentIncarnation(parent session.SessionID) session.IncarnationID {
	if parent == "" {
		return ""
	}
	return session.NewIncarnationID()
}

// TestHelpersRegisterAgainInAReopenedBrokerSession pins that once a root's
// broker session reopens, its run entry registers the main agent and then the
// tree's helpers again, keeping their identities, before any of them makes a
// gateway call.
func TestHelpersRegisterAgainInAReopenedBrokerSession(t *testing.T) {
	f := newHelperFixture(t)
	if err := f.svc.RegisterHelper(f.rootContext(t), newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{}); err != nil {
		t.Fatalf("RegisterHelper: %v", err)
	}
	helper, _ := f.svc.HeldIdentityAgentForTest(f.root.ID, "child-1")
	if err := f.broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: f.main.SessionID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	if err := runToEnd(aliceContext(t), t, f.svc, f.root.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	reopened := wantBrokerSession(t, f.broker, f.root)
	if reopened.ID == f.main.SessionID {
		t.Fatal("the root's broker session did not reopen")
	}
	got := lastRegistration(t, f.broker)
	if got.HostSessionID != "child-1" || got.SessionID != reopened.ID || got.Parent != f.mainAgent {
		t.Fatalf("last registration = %+v, want the helper in %s under the main agent", got, reopened.ID)
	}
	if again, _ := f.svc.HeldIdentityAgentForTest(f.root.ID, "child-1"); again != helper {
		t.Fatalf("the helper moved as %+v, want %+v", again, helper)
	}
}
