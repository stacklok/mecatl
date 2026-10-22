package conformance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

func mustRegister(t *testing.T, svc identitybroker.Service, req identitybroker.RegisterAgentRequest) identitybroker.Registration {
	t.Helper()
	return mustRegisterAt(t, svc, req, time.Now)
}

// mustRegisterAt is mustRegister for an implementation reading the time from
// now.
func mustRegisterAt(t *testing.T, svc identitybroker.Service, req identitybroker.RegisterAgentRequest, now func() time.Time) identitybroker.Registration {
	t.Helper()
	reg, err := svc.RegisterAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("RegisterAgent(%s): %v", req.Incarnation, err)
	}
	if !strings.HasPrefix(reg.Agent.Subject, "spiffe://") || !strings.HasSuffix(reg.Agent.Subject, "/inst/"+req.Incarnation) {
		t.Fatalf("RegisterAgent returned agent %+v, want a SPIFFE ID ending in /inst/%s", reg.Agent, req.Incarnation)
	}
	if reg.Credential.IsZero() {
		t.Fatal("RegisterAgent returned no credential")
	}
	if !reg.ExpiresAt.After(now()) {
		t.Fatalf("RegisterAgent returned a credential expiring at %v, want a future time", reg.ExpiresAt)
	}
	return reg
}

func runAgents(t *testing.T, factory serviceFactory) {
	ctx := context.Background()

	t.Run("a main agent registers and gets a credential", func(t *testing.T) {
		svc := factory(t)
		mustRegister(t, svc, registerRequest(mustOpen(t, svc, openRequest("hostS42", alice))))
	})

	t.Run("a definition named only by name registers", func(t *testing.T) {
		svc := factory(t)
		req := registerRequest(mustOpen(t, svc, openRequest("hostS42", alice)))
		req.Definition = identitybroker.DefinitionRef{Name: "main"}
		mustRegister(t, svc, req)
	})

	t.Run("a legacy incarnation registers", func(t *testing.T) {
		svc := factory(t)
		req := registerRequest(mustOpen(t, svc, openRequest("hostS42", alice)))
		req.Incarnation = "legacy_" + strings.Repeat("0f", 32)
		mustRegister(t, svc, req)
	})

	t.Run("registering again returns the same agent with a credential", func(t *testing.T) {
		svc := factory(t)
		req := registerRequest(mustOpen(t, svc, openRequest("hostS42", alice)))
		first := mustRegister(t, svc, req)
		again := mustRegister(t, svc, req)
		if again.Agent != first.Agent {
			t.Fatalf("registering again returned agent %+v, want %+v", again.Agent, first.Agent)
		}
		if again.ExpiresAt.Before(first.ExpiresAt) {
			t.Fatalf("registering again returned a credential expiring at %v, before the first's %v", again.ExpiresAt, first.ExpiresAt)
		}
	})

	t.Run("agents with different incarnations are different agents", func(t *testing.T) {
		svc := factory(t)
		a := registerRequest(mustOpen(t, svc, openRequest("hostS42", alice)))
		b := registerRequest(mustOpen(t, svc, openRequest("hostS43", alice)))
		b.Incarnation = incarnation("other")
		if ra, rb := mustRegister(t, svc, a), mustRegister(t, svc, b); ra.Agent == rb.Agent {
			t.Fatalf("two incarnations share agent %+v", ra.Agent)
		}
	})

	t.Run("a reopened session registers the agent again with the same identity", func(t *testing.T) {
		svc := factory(t)
		old := mustOpen(t, svc, openRequest("hostS42", alice))
		first := mustRegister(t, svc, registerRequest(old))
		mustClose(t, svc, old.ID)
		reopened := mustOpen(t, svc, openRequest("hostS42", alice))
		if again := mustRegister(t, svc, registerRequest(reopened)); again.Agent != first.Agent {
			t.Fatalf("after reopening, the agent is %+v, want %+v", again.Agent, first.Agent)
		}
	})

	t.Run("a main agent outside the root host session is refused", func(t *testing.T) {
		svc := factory(t)
		req := registerRequest(mustOpen(t, svc, openRequest("hostS42", alice)))
		req.HostSessionID = "hostS99"
		_, err := svc.RegisterAgent(ctx, req)
		wantError(t, "RegisterAgent of a main agent in another host session", err, identitybroker.CodePermissionDenied, "")
	})

	t.Run("a child registers under a parent registered to the session", func(t *testing.T) {
		svc := factory(t)
		s := mustOpen(t, svc, openRequest("hostS42", alice))
		parent := mustRegister(t, svc, registerRequest(s))
		child := registerRequest(s)
		child.HostSessionID = "hostS42-child"
		child.Incarnation = incarnation("child")
		child.Parent = parent.Agent
		if got := mustRegister(t, svc, child); got.Agent == parent.Agent {
			t.Fatalf("the child shares its parent's agent %+v", got.Agent)
		}
	})

	t.Run("a parent not registered to the session is refused", func(t *testing.T) {
		svc := factory(t)
		other := mustOpen(t, svc, openRequest("hostS43", alice))
		elsewhere := registerRequest(other)
		elsewhere.Incarnation = incarnation("elsewhere")
		parentElsewhere := mustRegister(t, svc, elsewhere)
		s := mustOpen(t, svc, openRequest("hostS42", alice))
		for name, parent := range map[string]identitybroker.AgentRef{
			"unregistered":           {Subject: strings.Replace(parentElsewhere.Agent.Subject, incarnation("elsewhere"), incarnation("unknown"), 1)},
			"in another session":     parentElsewhere.Agent,
			"not a registered agent": {Subject: "spiffe://agents.example.com/other"},
		} {
			child := registerRequest(s)
			child.HostSessionID = "hostS42-child"
			child.Incarnation = incarnation("child")
			child.Parent = parent
			_, err := svc.RegisterAgent(ctx, child)
			wantError(t, "RegisterAgent under a parent "+name, err, identitybroker.CodePermissionDenied, "")
		}
	})

	t.Run("a registered agent cannot be taken over by another owner or host session", func(t *testing.T) {
		svc := factory(t)
		first := mustRegister(t, svc, registerRequest(mustOpen(t, svc, openRequest("hostS42", alice))))
		for name, s := range map[string]identitybroker.Session{
			"another owner's session":        mustOpen(t, svc, openRequest("hostS43", bob)),
			"the owner's other host session": mustOpen(t, svc, openRequest("hostS44", alice)),
		} {
			req := registerRequest(s)
			req.Incarnation = incarnation("main") // the first agent's incarnation
			_, err := svc.RegisterAgent(ctx, req)
			wantError(t, "RegisterAgent of an existing agent from "+name, err, identitybroker.CodeAlreadyExists, "")
		}
		// The agent is unchanged: registering it again where it is succeeds.
		if again := mustRegister(t, svc, registerRequest(mustOpen(t, svc, openRequest("hostS42", alice)))); again.Agent != first.Agent {
			t.Fatalf("after refused takeovers, the agent is %+v, want %+v", again.Agent, first.Agent)
		}
	})

	t.Run("another owner reopening the host session cannot take over its agent", func(t *testing.T) {
		svc := factory(t)
		alices := mustOpen(t, svc, openRequest("hostS42", alice))
		mustRegister(t, svc, registerRequest(alices))
		mustClose(t, svc, alices.ID)
		// Only the owner differs from the earlier registration.
		_, err := svc.RegisterAgent(ctx, registerRequest(mustOpen(t, svc, openRequest("hostS42", bob))))
		wantError(t, "RegisterAgent of an existing agent by another owner", err, identitybroker.CodeAlreadyExists, "")
	})

	t.Run("a registered child cannot change its parent", func(t *testing.T) {
		svc := factory(t)
		s := mustOpen(t, svc, openRequest("hostS42", alice))
		parent := mustRegister(t, svc, registerRequest(s))
		childReq := registerRequest(s)
		childReq.HostSessionID = "hostS42-child"
		childReq.Incarnation = incarnation("child")
		childReq.Parent = parent.Agent
		child := mustRegister(t, svc, childReq)
		sibling := childReq
		sibling.Incarnation = incarnation("sibling")
		siblingReg := mustRegister(t, svc, sibling)
		for name, newParent := range map[string]identitybroker.AgentRef{
			"itself":    child.Agent,
			"a sibling": siblingReg.Agent,
		} {
			req := childReq
			req.Parent = newParent
			_, err := svc.RegisterAgent(ctx, req)
			wantError(t, "RegisterAgent of a child with "+name+" as parent", err, identitybroker.CodeAlreadyExists, "")
		}
	})
}
