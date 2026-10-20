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
	for _, incarnation := range []string{"inc_main", "inc_reviewer"} {
		got, err := b.RegisterAgent(context.Background(), identitybroker.RegisterAgentRequest{
			SessionID:     s.ID,
			HostSessionID: s.HostSessionID,
			Owner:         s.Owner,
			Incarnation:   incarnation,
			Definition:    identitybroker.DefinitionRef{Source: "system", Name: "main", Revision: "v1"},
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
