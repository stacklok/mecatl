package executionenv

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestGrantRoundTripAndExactBindings(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	claims := GrantClaims{KeyID: "k1", Issuer: "execution-provider", Audience: "mecatl-execution", Client: "spiffe://cluster/ns/mecak8s", OwnerHash: "owner", BindingID: "binding", Environment: EnvironmentRef{ID: "env", Revision: "rev"}, Epoch: 7, Operations: []Operation{OpFileRead}, NotBefore: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), Nonce: "n1"}
	token, err := SignGrant(priv, claims)
	if err != nil {
		t.Fatal(err)
	}
	v := GrantVerifier{Keys: map[string]ed25519.PublicKey{"k1": pub}, Issuer: claims.Issuer, Audience: claims.Audience, MaxLifetime: 5 * time.Minute, Now: func() time.Time { return now }}
	if _, err := v.Verify(token, GrantExpectation{Client: claims.Client, OwnerHash: "owner", BindingID: claims.BindingID, Environment: claims.Environment, Epoch: 7, Operation: OpFileRead}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	for name, mutate := range map[string]func(*GrantExpectation){
		"client":    func(e *GrantExpectation) { e.Client = "spiffe://other" },
		"owner":     func(e *GrantExpectation) { e.OwnerHash = "other" },
		"binding":   func(e *GrantExpectation) { e.BindingID = "other" },
		"revision":  func(e *GrantExpectation) { e.Environment.Revision = "other" },
		"epoch":     func(e *GrantExpectation) { e.Epoch++ },
		"operation": func(e *GrantExpectation) { e.Operation = OpFileReplace },
	} {
		t.Run(name, func(t *testing.T) {
			e := GrantExpectation{Client: claims.Client, OwnerHash: claims.OwnerHash, BindingID: claims.BindingID, Environment: claims.Environment, Epoch: claims.Epoch, Operation: OpFileRead}
			mutate(&e)
			if _, err := v.Verify(token, e); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestGrantRejectsExpiryRevocationAndUnknownFields(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_800_000_000, 0).UTC()
	c := GrantClaims{KeyID: "k1", Issuer: "i", Audience: "a", Client: "spiffe://c", OwnerHash: "o", BindingID: "b", Environment: EnvironmentRef{ID: "e", Revision: "r"}, Epoch: 1, Operations: []Operation{OpAttach}, NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(-time.Second), Nonce: "n"}
	tok, _ := SignGrant(priv, c)
	v := GrantVerifier{Keys: map[string]ed25519.PublicKey{"k1": pub}, Issuer: "i", Audience: "a", Now: func() time.Time { return now }}
	if _, err := v.Verify(tok, GrantExpectation{Client: c.Client, OwnerHash: c.OwnerHash, BindingID: c.BindingID, Environment: c.Environment, Epoch: 1, Operation: OpAttach}); err == nil {
		t.Fatal("expired grant accepted")
	}
	c.ExpiresAt = now.Add(time.Minute)
	tok, _ = SignGrant(priv, c)
	v.RevokedNonces = map[string]struct{}{"n": {}}
	if _, err := v.Verify(tok, GrantExpectation{Client: c.Client, OwnerHash: c.OwnerHash, BindingID: c.BindingID, Environment: c.Environment, Epoch: 1, Operation: OpAttach}); err == nil {
		t.Fatal("revoked nonce accepted")
	}
}

func TestDecodeStrictRejectsUnknownTrailingAndOversize(t *testing.T) {
	var req ValidateProfileRequest
	for _, body := range []string{`{"profile":"p","extra":true}`, `{"profile":"p"}{}`, `{"profile":"` + string(make([]byte, MaxJSONBody)) + `"}`} {
		if err := DecodeStrict([]byte(body), &req); err == nil {
			t.Fatalf("accepted invalid body")
		}
	}
}
