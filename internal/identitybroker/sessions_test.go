package identitybroker_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/conformance"
)

var (
	testWorkload = identitybroker.Workload{Name: "system:serviceaccount:mecatl:mecak8s"}
	testOwner    = identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
)

// newBroker returns a broker with its own signing key and opts.
func newBroker(t *testing.T, opts ...identitybroker.Option) *identitybroker.Broker {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("test key: %v", err)
	}
	b, err := identitybroker.New(identitybroker.Config{
		TrustDomain:        "agents.example.com",
		Audience:           "https://broker.example.com",
		CredentialLifetime: 5 * time.Minute,
	}, nil, key, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

func openSession(t *testing.T, b *identitybroker.Broker) identitybroker.Session {
	t.Helper()
	s, err := b.OpenSession(context.Background(), testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: "hostS42",
		Incarnation:   "inc_abcdefghijklmnopqrstuvwxya",
		Owner:         testOwner,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return s
}

func TestBrokerPassesContract(t *testing.T) {
	conformance.Run(t, func(t *testing.T, now func() time.Time) conformance.Implementation {
		b := newBroker(t)
		identitybroker.SetClock(b, now)
		return b
	})
}

func TestBrokerSessionIDsDoNotRepeatAcrossRestarts(t *testing.T) {
	// A fresh Broker stands in for a restarted process with no memory of
	// earlier IDs; sequential IDs would repeat here.
	seen := map[string]bool{}
	for range 3 {
		id := openSession(t, newBroker(t)).ID
		if seen[id] {
			t.Fatalf("a restarted broker reissued broker session ID %q", id)
		}
		seen[id] = true
	}
}

func TestBrokerAgentIDIsRandomPerProcess(t *testing.T) {
	// A fresh Broker stands in for another process: a constant ag_ identifier
	// would repeat here.
	agentID := func() string {
		b := newBroker(t)
		s := openSession(t, b)
		reg, err := b.RegisterAgent(context.Background(), testWorkload, identitybroker.RegisterAgentRequest{
			SessionID: s.ID, HostSessionID: s.HostSessionID, Owner: s.Owner, Incarnation: s.Incarnation,
			Definition: identitybroker.DefinitionRef{Name: "main"},
		})
		if err != nil {
			t.Fatalf("RegisterAgent: %v", err)
		}
		got, err := b.VerifyCredential(context.Background(), identitybroker.VerifyCredentialRequest{SessionID: s.ID, Credential: reg.Credential})
		if err != nil {
			t.Fatalf("VerifyCredential: %v", err)
		}
		return got.AgentID
	}
	if a, b := agentID(), agentID(); a == b {
		t.Fatalf("two brokers share the ag_ identifier %q", a)
	}
}

func TestBrokerAuthorizeCallDeniesEveryCall(t *testing.T) {
	b := newBroker(t)
	s := openSession(t, b)
	got, err := b.AuthorizeCall(context.Background(), testWorkload, identitybroker.AuthorizeCallRequest{
		SessionID:   s.ID,
		Credential:  identitybroker.NewToken("credential"),
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
