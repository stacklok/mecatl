package conformance

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// runWorkloads calls the Core directly, so the cases can choose the workload.
func runWorkloads(t *testing.T, factory coreFactory) {
	ctx := context.Background()

	open := func(t *testing.T, core identitybroker.Core, workload identitybroker.Workload, host string) identitybroker.Session {
		t.Helper()
		s, err := core.OpenSession(ctx, workload, openRequest(host, alice))
		if err != nil {
			t.Fatalf("OpenSession(%s) as %s: %v", host, workload.Name, err)
		}
		return s
	}

	// Another workload includes the same name with a different Verified.
	others := map[string]identitybroker.Workload{
		"another name":            workloadB,
		"same name, now verified": {Name: workloadA.Name, Verified: true},
	}

	for name, other := range others {
		t.Run("a session refuses another workload: "+name, func(t *testing.T) {
			core := factory(t)
			s := open(t, core, workloadA, "hostS42")
			_, err := core.OpenSession(ctx, other, openRequest("hostS42", alice))
			wantError(t, "OpenSession by another workload", err, identitybroker.CodePermissionDenied, "")
			if _, ok, err := core.FindSession(ctx, other, findRequest("hostS42")); err != nil || ok {
				t.Fatalf("FindSession by another workload = %v, %v; want false, nil", ok, err)
			}
			_, err = core.RegisterAgent(ctx, other, registerRequest(s))
			wantError(t, "RegisterAgent by another workload", err, identitybroker.CodePermissionDenied, "")
			_, err = core.AuthorizeCall(ctx, other, authorizeRequest(s.ID))
			wantError(t, "AuthorizeCall by another workload", err, identitybroker.CodePermissionDenied, "")
			err = core.CloseSession(ctx, other, identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed})
			wantError(t, "CloseSession by another workload", err, identitybroker.CodePermissionDenied, "")
			if got, ok, err := core.FindSession(ctx, workloadA, findRequest("hostS42")); err != nil || !ok || got != s {
				t.Fatalf("after refusals, FindSession by its workload = %+v, %v, %v; want %+v, true, nil", got, ok, err, s)
			}
		})
	}

	t.Run("another workload's session looks like an unknown one", func(t *testing.T) {
		core := factory(t)
		s := open(t, core, workloadA, "hostS42")
		unknown := s
		unknown.ID = s.ID + "-unknown"
		_, theirs := core.RegisterAgent(ctx, workloadB, registerRequest(s))
		_, missing := core.RegisterAgent(ctx, workloadB, registerRequest(unknown))
		sameError(t, "RegisterAgent", theirs, missing)
		_, theirs = core.AuthorizeCall(ctx, workloadB, authorizeRequest(s.ID))
		_, missing = core.AuthorizeCall(ctx, workloadB, authorizeRequest(unknown.ID))
		sameError(t, "AuthorizeCall", theirs, missing)
		theirs = core.CloseSession(ctx, workloadB, identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed})
		missing = core.CloseSession(ctx, workloadB, identitybroker.CloseSessionRequest{SessionID: unknown.ID, Reason: identitybroker.ReasonSessionClosed})
		sameError(t, "CloseSession", theirs, missing)
	})

	t.Run("another workload is refused before a closed session's reason", func(t *testing.T) {
		core := factory(t)
		s := open(t, core, workloadA, "hostS42")
		if err := core.CloseSession(ctx, workloadA, identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
			t.Fatalf("CloseSession: %v", err)
		}
		_, err := core.RegisterAgent(ctx, workloadB, registerRequest(s))
		wantError(t, "RegisterAgent by another workload in a closed session", err, identitybroker.CodePermissionDenied, "")
	})

	t.Run("workloads open their own sessions", func(t *testing.T) {
		core := factory(t)
		a := open(t, core, workloadA, "hostS42")
		b := open(t, core, workloadB, "hostS43")
		if a.ID == b.ID {
			t.Fatalf("two workloads share broker session %q", a.ID)
		}
	})

	t.Run("an unnamed workload is refused before the request is checked", func(t *testing.T) {
		core := factory(t)
		s := open(t, core, workloadA, "hostS42")
		for name, workload := range map[string]identitybroker.Workload{
			"no name":     {},
			"spaced name": {Name: "system:serviceaccount:mecatl: mecak8s"},
		} {
			_, err := core.OpenSession(ctx, workload, identitybroker.OpenSessionRequest{})
			wantError(t, "OpenSession with "+name, err, identitybroker.CodeUnauthenticated, "")
			_, _, err = core.FindSession(ctx, workload, findRequest("hostS42"))
			wantError(t, "FindSession with "+name, err, identitybroker.CodeUnauthenticated, "")
			err = core.CloseSession(ctx, workload, identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed})
			wantError(t, "CloseSession with "+name, err, identitybroker.CodeUnauthenticated, "")
			_, err = core.RegisterAgent(ctx, workload, registerRequest(s))
			wantError(t, "RegisterAgent with "+name, err, identitybroker.CodeUnauthenticated, "")
			_, err = core.AuthorizeCall(ctx, workload, authorizeRequest(s.ID))
			wantError(t, "AuthorizeCall with "+name, err, identitybroker.CodeUnauthenticated, "")
		}
	})
}
