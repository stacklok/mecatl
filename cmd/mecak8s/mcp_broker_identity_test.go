package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func writeBrokerToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-only-key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(signed), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBrokerWorkloadIdentityReadsProjectedTokenClaims(t *testing.T) {
	path := writeBrokerToken(t, jwt.MapClaims{"iss": "https://kubernetes.default.svc", "sub": "system:serviceaccount:agents:mecak8s"})
	got, err := brokerWorkloadIdentity(path)
	if err != nil || got == nil || got.Issuer != "https://kubernetes.default.svc" || got.Subject != "system:serviceaccount:agents:mecak8s" {
		t.Fatalf("identity = %+v, %v", got, err)
	}
}

func TestBrokerWorkloadIdentityDisabledOrFailsClosed(t *testing.T) {
	if got, err := brokerWorkloadIdentity(""); got != nil || err != nil {
		t.Fatalf("empty path = %+v, %v; want nil, nil", got, err)
	}
	if _, err := brokerWorkloadIdentity(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing token accepted")
	}
	garbage := filepath.Join(t.TempDir(), "garbage")
	if err := os.WriteFile(garbage, []byte("not-a-jwt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := brokerWorkloadIdentity(garbage); err == nil {
		t.Fatal("malformed token accepted")
	}
	if _, err := brokerWorkloadIdentity(writeBrokerToken(t, jwt.MapClaims{"iss": "https://issuer.example"})); err == nil {
		t.Fatal("token without subject accepted")
	}
}
