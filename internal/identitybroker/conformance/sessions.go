package conformance

import (
	"context"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

func runSessions(t *testing.T, factory serviceFactory) {
	ctx := context.Background()

	t.Run("open returns the session it opened", func(t *testing.T) {
		svc := factory(t)
		req := openRequest("hostS42", alice)
		s := mustOpen(t, svc, req)
		if s.ID == "" {
			t.Fatal("OpenSession returned an empty broker session ID")
		}
		want := identitybroker.Session{ID: s.ID, HostSessionID: req.HostSessionID, Incarnation: req.Incarnation, Owner: req.Owner}
		if s != want {
			t.Fatalf("OpenSession = %+v, want %+v", s, want)
		}
	})

	t.Run("repeated open returns the open session", func(t *testing.T) {
		svc := factory(t)
		first := mustOpen(t, svc, openRequest("hostS42", alice))
		again := mustOpen(t, svc, openRequest("hostS42", alice))
		if again != first {
			t.Fatalf("repeated OpenSession = %+v, want %+v", again, first)
		}
	})

	t.Run("concurrent opens return one session", func(t *testing.T) {
		svc := factory(t)
		const opens = 8
		var wg sync.WaitGroup
		got := make([]identitybroker.Session, opens)
		errs := make([]error, opens)
		for i := range opens {
			wg.Go(func() { got[i], errs[i] = svc.OpenSession(ctx, openRequest("hostS42", alice)) })
		}
		wg.Wait()
		for i := range opens {
			if errs[i] != nil {
				t.Fatalf("concurrent OpenSession %d: %v", i, errs[i])
			}
			if got[i] != got[0] {
				t.Fatalf("concurrent opens returned %+v and %+v, want one session", got[0], got[i])
			}
		}
	})

	t.Run("open for another owner is refused", func(t *testing.T) {
		svc := factory(t)
		first := mustOpen(t, svc, openRequest("hostS42", alice))
		_, err := svc.OpenSession(ctx, openRequest("hostS42", bob))
		wantError(t, "OpenSession by another owner", err, identitybroker.CodePermissionDenied, identitybroker.ReasonSessionOwned)
		if again := mustOpen(t, svc, openRequest("hostS42", alice)); again != first {
			t.Fatalf("after a refused open, OpenSession = %+v, want %+v", again, first)
		}
	})

	t.Run("open for another incarnation is refused", func(t *testing.T) {
		svc := factory(t)
		mustOpen(t, svc, openRequest("hostS42", alice))
		req := openRequest("hostS42", alice)
		req.Incarnation = "inc_other"
		_, err := svc.OpenSession(ctx, req)
		wantError(t, "OpenSession for another incarnation", err, identitybroker.CodeAlreadyExists, "")
	})

	t.Run("host sessions get distinct broker sessions", func(t *testing.T) {
		svc := factory(t)
		a := mustOpen(t, svc, openRequest("hostS42", alice))
		b := mustOpen(t, svc, openRequest("hostS43", alice))
		if a.ID == b.ID {
			t.Fatalf("two host sessions share broker session %q", a.ID)
		}
	})

	t.Run("find returns the open session for its root host session", func(t *testing.T) {
		svc := factory(t)
		s := mustOpen(t, svc, openRequest("hostS42", alice))
		got, ok, err := svc.FindSession(ctx, findRequest("hostS42"))
		if err != nil || !ok || got != s {
			t.Fatalf("FindSession = %+v, %v, %v; want %+v, true, nil", got, ok, err, s)
		}
		if _, ok, err := svc.FindSession(ctx, findRequest("hostS99")); err != nil || ok {
			t.Fatalf("FindSession(unknown) = %v, %v; want false, nil", ok, err)
		}
	})

	t.Run("unknown broker sessions are refused", func(t *testing.T) {
		svc := factory(t)
		s := mustOpen(t, svc, openRequest("hostS42", alice))
		unknown := s
		unknown.ID = s.ID + "-unknown"
		_, err := svc.RegisterAgent(ctx, registerRequest(unknown))
		wantError(t, "RegisterAgent in an unknown session", err, identitybroker.CodePermissionDenied, "")
		_, err = svc.AuthorizeCall(ctx, authorizeRequest(unknown.ID))
		wantError(t, "AuthorizeCall in an unknown session", err, identitybroker.CodePermissionDenied, "")
		err = svc.CloseSession(ctx, identitybroker.CloseSessionRequest{SessionID: unknown.ID, Reason: identitybroker.ReasonSessionClosed})
		wantError(t, "CloseSession of an unknown session", err, identitybroker.CodePermissionDenied, "")
	})

	t.Run("registration under another owner is refused", func(t *testing.T) {
		svc := factory(t)
		s := mustOpen(t, svc, openRequest("hostS42", alice))
		req := registerRequest(s)
		req.Owner = bob
		_, err := svc.RegisterAgent(ctx, req)
		wantError(t, "RegisterAgent under another owner", err, identitybroker.CodePermissionDenied, "")
	})
}

func runClosedSessions(t *testing.T, factory serviceFactory) {
	ctx := context.Background()

	t.Run("a closed session refuses registration and calls with its reason", func(t *testing.T) {
		svc := factory(t)
		s := mustOpen(t, svc, openRequest("hostS42", alice))
		mustClose(t, svc, s.ID)
		_, err := svc.RegisterAgent(ctx, registerRequest(s))
		wantError(t, "RegisterAgent in a closed session", err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
		_, err = svc.AuthorizeCall(ctx, authorizeRequest(s.ID))
		wantError(t, "AuthorizeCall in a closed session", err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
	})

	t.Run("a registration naming its definition only by name passes validation", func(t *testing.T) {
		// Reaching the closed-session refusal shows validation accepted it.
		svc := factory(t)
		s := mustOpen(t, svc, openRequest("hostS42", alice))
		mustClose(t, svc, s.ID)
		req := registerRequest(s)
		req.Definition = identitybroker.DefinitionRef{Name: "code-reviewer"}
		_, err := svc.RegisterAgent(ctx, req)
		wantError(t, "RegisterAgent naming only a definition name", err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
	})

	t.Run("closing again succeeds", func(t *testing.T) {
		svc := factory(t)
		s := mustOpen(t, svc, openRequest("hostS42", alice))
		mustClose(t, svc, s.ID)
		mustClose(t, svc, s.ID)
	})

	t.Run("a closed session is not found", func(t *testing.T) {
		svc := factory(t)
		s := mustOpen(t, svc, openRequest("hostS42", alice))
		mustClose(t, svc, s.ID)
		if _, ok, err := svc.FindSession(ctx, findRequest("hostS42")); err != nil || ok {
			t.Fatalf("FindSession after close = %v, %v; want false, nil", ok, err)
		}
	})

	t.Run("reopening opens a new session and the closed one stays closed", func(t *testing.T) {
		svc := factory(t)
		old := mustOpen(t, svc, openRequest("hostS42", alice))
		mustClose(t, svc, old.ID)
		reopened := mustOpen(t, svc, openRequest("hostS42", alice))
		if reopened.ID == old.ID {
			t.Fatalf("reopened broker session reuses closed ID %q", old.ID)
		}
		_, err := svc.RegisterAgent(ctx, registerRequest(old))
		wantError(t, "RegisterAgent in the closed session after reopening", err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
	})
}

func runInvalidRequests(t *testing.T, factory serviceFactory) {
	ctx := context.Background()
	svc := factory(t)
	s := mustOpen(t, svc, openRequest("hostS42", alice))

	opens := map[string]func(*identitybroker.OpenSessionRequest){
		"no host session": func(r *identitybroker.OpenSessionRequest) { r.HostSessionID = "" },
		"no incarnation":  func(r *identitybroker.OpenSessionRequest) { r.Incarnation = "" },
		"no owner":        func(r *identitybroker.OpenSessionRequest) { r.Owner = identitybroker.Owner{} },
		"spaced host":     func(r *identitybroker.OpenSessionRequest) { r.HostSessionID = "host S42" },
	}
	for name, mutate := range opens {
		req := openRequest("hostS43", alice)
		mutate(&req)
		_, err := svc.OpenSession(ctx, req)
		wantError(t, "OpenSession with "+name, err, identitybroker.CodeInvalidArgument, "")
	}

	for name, host := range map[string]string{"no host session": "", "spaced host": "host S42", "control character": "host\x00S42"} {
		_, _, err := svc.FindSession(ctx, findRequest(host))
		wantError(t, "FindSession with "+name, err, identitybroker.CodeInvalidArgument, "")
	}

	closes := map[string]identitybroker.CloseSessionRequest{
		"no session":     {Reason: identitybroker.ReasonSessionClosed},
		"no reason":      {SessionID: s.ID},
		"unknown reason": {SessionID: s.ID, Reason: identitybroker.ReasonDenied},
	}
	for name, req := range closes {
		wantError(t, "CloseSession with "+name, svc.CloseSession(ctx, req), identitybroker.CodeInvalidArgument, "")
	}

	registers := map[string]func(*identitybroker.RegisterAgentRequest){
		"no session":      func(r *identitybroker.RegisterAgentRequest) { r.SessionID = "" },
		"no host session": func(r *identitybroker.RegisterAgentRequest) { r.HostSessionID = "" },
		"no owner":        func(r *identitybroker.RegisterAgentRequest) { r.Owner = identitybroker.Owner{} },
		"no incarnation":  func(r *identitybroker.RegisterAgentRequest) { r.Incarnation = "" },
		"no definition":   func(r *identitybroker.RegisterAgentRequest) { r.Definition = identitybroker.DefinitionRef{} },
		"bad definition":  func(r *identitybroker.RegisterAgentRequest) { r.Definition.Name = "../main" },
		"bad source":      func(r *identitybroker.RegisterAgentRequest) { r.Definition.Source = "../system" },
		"bad revision":    func(r *identitybroker.RegisterAgentRequest) { r.Definition.Revision = "v 1" },
		"bad parent":      func(r *identitybroker.RegisterAgentRequest) { r.Parent.Subject = "spiffe://x/ y" },
	}
	for name, mutate := range registers {
		req := registerRequest(s)
		mutate(&req)
		_, err := svc.RegisterAgent(ctx, req)
		wantError(t, "RegisterAgent with "+name, err, identitybroker.CodeInvalidArgument, "")
	}

	authorizes := map[string]func(*identitybroker.AuthorizeCallRequest){
		"no session":    func(r *identitybroker.AuthorizeCallRequest) { r.SessionID = "" },
		"no credential": func(r *identitybroker.AuthorizeCallRequest) { r.Credential = identitybroker.Token{} },
		"no operation":  func(r *identitybroker.AuthorizeCallRequest) { r.OperationID = "" },
		"no target":     func(r *identitybroker.AuthorizeCallRequest) { r.Target = "" },
		"no tool":       func(r *identitybroker.AuthorizeCallRequest) { r.Tool = "" },
	}
	for name, mutate := range authorizes {
		req := authorizeRequest(s.ID)
		mutate(&req)
		_, err := svc.AuthorizeCall(ctx, req)
		wantError(t, "AuthorizeCall with "+name, err, identitybroker.CodeInvalidArgument, "")
	}
}
