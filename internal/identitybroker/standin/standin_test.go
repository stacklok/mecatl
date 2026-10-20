package standin

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/conformance"
)

func TestStandinPassesContract(t *testing.T) {
	conformance.Run(t, func(*testing.T) identitybroker.Service { return New() })
}

func TestSessionIDsDoNotRepeatAcrossRestarts(t *testing.T) {
	// A fresh Broker stands in for a restarted process with no memory of
	// earlier IDs; sequential IDs would repeat here.
	seen := map[string]bool{}
	for range 3 {
		id := openSession(t, New()).ID
		if seen[id] {
			t.Fatalf("a restarted stand-in reissued broker session ID %q", id)
		}
		seen[id] = true
	}
}

func openSession(t *testing.T, b *Broker) identitybroker.Session {
	t.Helper()
	s, err := b.OpenSession(context.Background(), identitybroker.OpenSessionRequest{
		HostSessionID: "hostS42",
		Incarnation:   "inc_hostS42",
		Owner:         identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"},
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return s
}

func TestRegisterAgentReturnsTheFixedAgent(t *testing.T) {
	b := New()
	s := openSession(t, b)
	cases := map[string]identitybroker.DefinitionRef{
		"inc_main":     {Source: "system", Name: "main", Revision: "v1"},
		"inc_reviewer": {Name: "code-reviewer"}, // known only by name
	}
	for incarnation, definition := range cases {
		got, err := b.RegisterAgent(context.Background(), identitybroker.RegisterAgentRequest{
			SessionID:     s.ID,
			HostSessionID: s.HostSessionID,
			Owner:         s.Owner,
			Incarnation:   incarnation,
			Definition:    definition,
		})
		if err != nil {
			t.Fatalf("RegisterAgent(%s): %v", incarnation, err)
		}
		if got.Agent != fixedAgent || got.Credential != placeholderCredential {
			t.Fatalf("RegisterAgent(%s) = %+v, want the fixed agent and placeholder credential", incarnation, got)
		}
	}
}

func TestAuthorizeCallDeniesEveryCall(t *testing.T) {
	b := New()
	s := openSession(t, b)
	got, err := b.AuthorizeCall(context.Background(), identitybroker.AuthorizeCallRequest{
		SessionID:   s.ID,
		Credential:  placeholderCredential,
		OperationID: "op-1",
		Target:      "gateway",
		Tool:        "pull_request_read",
	})
	if !errors.Is(err, identitybroker.ReasonError(identitybroker.ReasonDenied, "")) {
		t.Fatalf("AuthorizeCall error = %v, want PERMISSION_DENIED denied", err)
	}
	if !got.GatewayToken.IsZero() {
		t.Fatal("a denied AuthorizeCall returned a gateway token")
	}
}
