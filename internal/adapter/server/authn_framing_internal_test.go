package server

import (
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestAdmissiblePrincipalRefusesReservedOwnerKeySeparator pins the request
// edge's re-validation of the owner-key framing rule.
//
// Owner scope keys are derived as hash(issuer + NUL + subject), so a NUL inside
// either component makes distinct principals collide on one namespace:
// ("a","b\x00c") and ("a\x00b","c") produce the same key. PrincipalFromClaims
// already refuses these, but that is the SHIPPED validator's guarantee, not the
// edge's — cfg.Validator is an injection seam, so a deployment supplying its own
// validator could hand the edge a colliding pair. The edge therefore re-states
// the rule rather than inheriting it.
func TestAdmissiblePrincipalRefusesReservedOwnerKeySeparator(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    *session.Principal
		want bool
	}{
		{
			name: "NUL in subject",
			p:    &session.Principal{Issuer: "https://idp.example", Subject: "b\x00c", GrantType: session.GrantTypeUser},
		},
		{
			name: "NUL in issuer",
			p:    &session.Principal{Issuer: "https://idp.example\x00b", Subject: "c", GrantType: session.GrantTypeUser},
		},
		{
			// The pair above collide on one owner key; this is the well-framed
			// shape they collide onto, and it must still be admitted so the rule
			// is not a blanket deny.
			name: "well framed",
			p:    &session.Principal{Issuer: "https://idp.example", Subject: "bc", GrantType: session.GrantTypeUser},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := admissiblePrincipal(tc.p); got != tc.want {
				t.Fatalf("admissiblePrincipal(%+v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

// TestAdmissibleFramingMatchesTheOwnerKeyDerivation is the reason the rule
// exists: the two refused identities hash to the SAME owner scope key, so
// admitting either would put two callers in one namespace.
func TestAdmissibleFramingMatchesTheOwnerKeyDerivation(t *testing.T) {
	a := &session.Principal{Issuer: "x", Subject: "y\x00z"}
	b := &session.Principal{Issuer: "x\x00y", Subject: "z"}
	if session.PrincipalScopeHash(a) != session.PrincipalScopeHash(b) {
		t.Skip("quarantine sentinel already separates these; the edge rule is then defence in depth")
	}
	if admissiblePrincipal(a) || admissiblePrincipal(b) {
		t.Fatal("the edge admitted an identity pair that collides on one owner scope key")
	}
}
