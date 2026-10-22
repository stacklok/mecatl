package identitybroker_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
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

func newBroker(t *testing.T) *identitybroker.Broker {
	t.Helper()
	scalar := sha256.Sum256([]byte("sessions test key"))
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), scalar[:])
	if err != nil {
		t.Fatalf("test key: %v", err)
	}
	b, err := identitybroker.New(identitybroker.Config{
		TrustDomain:        "agents.example.com",
		Audience:           "https://broker.example.com",
		CredentialLifetime: 5 * time.Minute,
	}, nil, key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

func openSession(t *testing.T, b *identitybroker.Broker) identitybroker.Session {
	t.Helper()
	s, err := b.OpenSession(context.Background(), testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: "hostS42",
		Incarnation:   "inc_hostS42",
		Owner:         testOwner,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return s
}

func TestBrokerPassesContract(t *testing.T) {
	conformance.Run(t, func(t *testing.T) identitybroker.Core { return newBroker(t) })
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
