package session_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestInvariant_no_fabricated_principal pins the AGENTS.md invariant
// "no fabricated principal" (ADR 0204 decision 2): the context seam round-trips
// a verified principal, and an ABSENT principal reads back as nil — never a
// fabricated anonymous value. A nil stored through WithPrincipal must read back
// as absent too, so a caller cannot launder "no identity" into a non-nil empty
// principal.
func TestInvariant_no_fabricated_principal(t *testing.T) {
	t.Parallel()

	t.Run("round trip", func(t *testing.T) {
		t.Parallel()
		want := &session.Principal{
			Issuer:    "https://idp.example",
			Subject:   "user-1",
			GrantType: session.GrantTypeUser,
			Name:      "Ada",
		}
		got := session.PrincipalFromContext(session.WithPrincipal(context.Background(), want))
		if got == nil {
			t.Fatal("PrincipalFromContext returned nil for a stored principal")
		}
		if *got != *want {
			t.Fatalf("round trip mismatch: got %+v, want %+v", *got, *want)
		}
	})

	t.Run("absent is nil", func(t *testing.T) {
		t.Parallel()
		if got := session.PrincipalFromContext(context.Background()); got != nil {
			t.Fatalf("bare context: got %+v, want nil (no fabricated principal)", *got)
		}
	})

	t.Run("nil stored reads back absent", func(t *testing.T) {
		t.Parallel()
		ctx := session.WithPrincipal(context.Background(), nil)
		if got := session.PrincipalFromContext(ctx); got != nil {
			t.Fatalf("WithPrincipal(nil): got %+v, want nil (no fabricated principal)", *got)
		}
	})

	t.Run("nil stored does not erase an outer principal into a fabricated one", func(t *testing.T) {
		t.Parallel()
		outer := session.WithPrincipal(context.Background(), &session.Principal{
			Issuer: "https://idp.example", Subject: "user-1", GrantType: session.GrantTypeUser,
		})
		if got := session.PrincipalFromContext(session.WithPrincipal(outer, nil)); got != nil && got.Subject == "" {
			t.Fatalf("nil shadow produced an empty fabricated principal: %+v", *got)
		}
	})

	t.Run("identity is the issuer/subject pair", func(t *testing.T) {
		t.Parallel()
		a := session.PrincipalFromContext(session.WithPrincipal(context.Background(),
			&session.Principal{Issuer: "https://a.example", Subject: "same", GrantType: session.GrantTypeUser}))
		b := session.PrincipalFromContext(session.WithPrincipal(context.Background(),
			&session.Principal{Issuer: "https://b.example", Subject: "same", GrantType: session.GrantTypeUser}))
		if *a == *b {
			t.Fatal("principals from different issuers compared equal on subject alone")
		}
	})

	t.Run("stored principal is copied, not aliased", func(t *testing.T) {
		t.Parallel()
		p := &session.Principal{Issuer: "https://idp.example", Subject: "user-1", GrantType: session.GrantTypeUser}
		ctx := session.WithPrincipal(context.Background(), p)
		p.Subject = "attacker"
		if got := session.PrincipalFromContext(ctx); got.Subject != "user-1" {
			t.Fatalf("context principal mutated through the caller's pointer: %+v", *got)
		}
	})

	t.Run("read hands back a copy, not the stored pointer", func(t *testing.T) {
		t.Parallel()
		ctx := session.WithPrincipal(context.Background(),
			&session.Principal{Issuer: "https://idp.example", Subject: "user-1", GrantType: session.GrantTypeUser})
		first := session.PrincipalFromContext(ctx)
		first.Subject = "attacker"
		if got := session.PrincipalFromContext(ctx); got.Subject != "user-1" {
			t.Fatalf("a reader mutated what the context reports for every later reader: %+v", *got)
		}
	})
}
