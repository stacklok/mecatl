// Package conformance provides the contract tests every identity broker
// implementation, an identitybroker.Core and identitybroker.CredentialVerifier,
// passes.
//
// Importing testing from this non-test package is intentional: each
// implementation's tests call Run with a factory for fresh, empty instances.
// Most cases call the implementation the way Mecatl does, through
// identitybroker.NewInProcess, so the authentication step is always taken;
// cases about the calling workload call the Core directly. Cases are grouped by area, one
// file per area, so later work adds its cases in its own file.
package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// Implementation is an identity broker under test: the Core Mecatl's calls
// reach and the CredentialVerifier AS-1 calls.
type Implementation interface {
	identitybroker.Core
	identitybroker.CredentialVerifier
}

// Factory returns a fresh, empty implementation for one test, which reads
// the time from now. Each implementation it returns has its own signing key.
type Factory func(t *testing.T, now func() time.Time) Implementation

// coreFactory returns an implementation on the real clock.
type coreFactory func(t *testing.T) identitybroker.Core

// serviceFactory returns an implementation as Mecatl sees it.
type serviceFactory func(t *testing.T) identitybroker.Service

// Run executes the identity broker contract against factory.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	core := func(t *testing.T) identitybroker.Core { return factory(t, time.Now) }
	service := func(t *testing.T) identitybroker.Service { return asMecatl(core(t)) }
	t.Run("sessions", func(t *testing.T) { runSessions(t, service) })
	t.Run("closed sessions", func(t *testing.T) { runClosedSessions(t, service) })
	t.Run("agents", func(t *testing.T) { runAgents(t, service) })
	t.Run("credentials", func(t *testing.T) { runCredentials(t, factory) })
	t.Run("invalid requests", func(t *testing.T) { runInvalidRequests(t, service) })
	t.Run("workloads", func(t *testing.T) { runWorkloads(t, core) })
}

// asMecatl returns core behind the authentication boundary, as Mecatl calls
// it.
func asMecatl(core identitybroker.Core) identitybroker.Service {
	return identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: workloadA.Name}, core)
}

var (
	workloadA = identitybroker.Workload{Name: "system:serviceaccount:mecatl:mecak8s"}
	workloadB = identitybroker.Workload{Name: "system:serviceaccount:other:intruder"}
)

var (
	alice = identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	bob   = identitybroker.Owner{Issuer: "https://login.example.com", Subject: "bob-117"}
)

func openRequest(host string, owner identitybroker.Owner) identitybroker.OpenSessionRequest {
	return identitybroker.OpenSessionRequest{HostSessionID: host, Incarnation: incarnation(host), Owner: owner}
}

func findRequest(host string) identitybroker.FindSessionRequest {
	return identitybroker.FindSessionRequest{RootHostSessionID: host}
}

// incarnation returns a well-formed minted incarnation derived from label, so
// cases can name agents readably and the same label gives the same agent.
func incarnation(label string) string {
	sum := sha256.Sum256([]byte(label))
	enc := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
	return "inc_" + enc[:25] + "a"
}

// registerRequest registers s's main agent, whose incarnation is its host
// session's.
func registerRequest(s identitybroker.Session) identitybroker.RegisterAgentRequest {
	return identitybroker.RegisterAgentRequest{
		SessionID:     s.ID,
		HostSessionID: s.HostSessionID,
		Owner:         s.Owner,
		Incarnation:   s.Incarnation,
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

// sameError fails unless a and b are both errors with the same text, so a
// caller cannot tell the two situations apart.
func sameError(t *testing.T, call string, a, b error) {
	t.Helper()
	if a == nil || b == nil || a.Error() != b.Error() {
		t.Fatalf("%s: errors %v and %v differ, so the two situations can be told apart", call, a, b)
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
