// Package conformance provides the contract tests every
// identitybroker.Service implementation passes.
//
// Importing testing from this non-test package is intentional: each
// implementation's tests call Run with a factory for fresh, empty instances.
// Cases are grouped by area, one file per area, so later work adds its cases
// in its own file.
package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// Factory returns a fresh, empty implementation for one test.
type Factory func(t *testing.T) identitybroker.Service

// Run executes the identitybroker.Service contract against factory.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("sessions", func(t *testing.T) { runSessions(t, factory) })
	t.Run("closed sessions", func(t *testing.T) { runClosedSessions(t, factory) })
	t.Run("invalid requests", func(t *testing.T) { runInvalidRequests(t, factory) })
}

var (
	alice = identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	bob   = identitybroker.Owner{Issuer: "https://login.example.com", Subject: "bob-117"}
)

func openRequest(host string, owner identitybroker.Owner) identitybroker.OpenSessionRequest {
	return identitybroker.OpenSessionRequest{HostSessionID: host, Incarnation: "inc_" + host, Owner: owner}
}

func findRequest(host string) identitybroker.FindSessionRequest {
	return identitybroker.FindSessionRequest{RootHostSessionID: host}
}

func registerRequest(s identitybroker.Session) identitybroker.RegisterAgentRequest {
	return identitybroker.RegisterAgentRequest{
		SessionID:     s.ID,
		HostSessionID: s.HostSessionID,
		Owner:         s.Owner,
		Incarnation:   "inc_main",
		Definition:    identitybroker.DefinitionRef{Source: "system", Name: "main", Revision: "v1"},
	}
}

func authorizeRequest(sessionID string) identitybroker.AuthorizeCallRequest {
	return identitybroker.AuthorizeCallRequest{
		SessionID:   sessionID,
		Credential:  identitybroker.NewToken("credential"),
		OperationID: "op-1",
		Target:      "gateway",
		Tool:        "pull_request_read",
	}
}

func mustOpen(t *testing.T, svc identitybroker.Service, req identitybroker.OpenSessionRequest) identitybroker.Session {
	t.Helper()
	s, err := svc.OpenSession(context.Background(), req)
	if err != nil {
		t.Fatalf("OpenSession(%s): %v", req.HostSessionID, err)
	}
	return s
}

func mustClose(t *testing.T, svc identitybroker.Service, id string) {
	t.Helper()
	err := svc.CloseSession(context.Background(), identitybroker.CloseSessionRequest{SessionID: id, Reason: identitybroker.ReasonSessionClosed})
	if err != nil {
		t.Fatalf("CloseSession(%s): %v", id, err)
	}
}

// wantError fails unless err is an *identitybroker.Error with code and reason.
func wantError(t *testing.T, call string, err error, code identitybroker.Code, reason identitybroker.Reason) {
	t.Helper()
	var got *identitybroker.Error
	if !errors.As(err, &got) {
		t.Fatalf("%s: error = %v, want *identitybroker.Error %s %s", call, err, code, reason)
	}
	if got.Code != code || got.Reason != reason {
		t.Fatalf("%s: error = %s %q, want %s %q", call, got.Code, got.Reason, code, reason)
	}
}
