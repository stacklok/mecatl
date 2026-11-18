package as1server

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRevokeGrant revokes a session-open grant in AS-1's storage: its next
// ID-JAG exchange is refused as revoked, a second revocation succeeds, and a
// token naming no grant is ErrUnknownGrant without the token or its
// signature in the error.
func TestRevokeGrant(t *testing.T) {
	x := startIDJAG(t)
	if err := x.f.server.UseCredentialVerifier(allowing(time.Now().Add(time.Hour))); err != nil {
		t.Fatalf("UseCredentialVerifier: %v", err)
	}
	if err := x.f.server.RevokeGrant(t.Context(), x.grant.Value()); err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}
	_, err := x.issue(t, actorToken(t))
	wantIDJAGRefusal(t, err, 400, "invalid_request", "revoked")
	if err := x.f.server.RevokeGrant(t.Context(), x.grant.Value()); err != nil {
		t.Fatalf("RevokeGrant of a revoked grant: %v", err)
	}

	for name, token := range map[string]string{
		"empty":              "",
		"no dot":             "not-a-refresh-token",
		"two dots":           "a.b.c",
		"no key":             ".signature",
		"unknown":            "ory_rt_unknown.signature",
		"a public sign-in's": x.f.signIn(t, testAudience).RefreshToken,
		"trailing .":         x.grant.Value() + ".",
	} {
		err := x.f.server.RevokeGrant(t.Context(), token)
		if !errors.Is(err, ErrUnknownGrant) {
			t.Errorf("%s: RevokeGrant = %v, want ErrUnknownGrant", name, err)
			continue
		}
		_, signature, _ := strings.Cut(token, ".")
		for _, secret := range []string{token, signature} {
			if secret != "" && strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the error discloses the refresh token or its signature", name)
			}
		}
	}
}
