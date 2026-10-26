package server

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

const internalBearer = "as1-internal-bearer-c41d"

func TestRequestAccessTokenEndsWithTheRequest(t *testing.T) {
	ctx, done := withRequestAccessToken(t.Context(), internalBearer)
	detached := context.WithoutCancel(ctx)
	for name, c := range map[string]context.Context{"request": ctx, "detached copy": detached} {
		if got := accessTokenFromRequest(c).Value(); got != internalBearer {
			t.Fatalf("%s token during the request = %q, want the bearer", name, got)
		}
	}
	done()
	for name, c := range map[string]context.Context{"request": ctx, "detached copy": detached} {
		if !accessTokenFromRequest(c).IsZero() {
			t.Fatalf("%s still yields the token after the request returned", name)
		}
	}
	if !accessTokenFromRequest(t.Context()).IsZero() {
		t.Fatal("a context with no request yields a token")
	}
}

func TestServerOwnedContextsHaveNoAccessToken(t *testing.T) {
	ctx, done := withRequestAccessToken(t.Context(), internalBearer)
	defer done()

	if plain := t.Context(); withoutRequestAccessToken(plain) != plain {
		t.Fatal("stripping a context with no token changed it")
	}
	if !accessTokenFromRequest(withoutRequestAccessToken(ctx)).IsZero() {
		t.Fatal("withoutRequestAccessToken kept the token")
	}

	detached, cancel := (&Service{}).detachedControlContext(ctx)
	defer cancel()
	if !accessTokenFromRequest(detached).IsZero() {
		t.Fatal("detachedControlContext kept the request's token")
	}

	st := &runState{}
	st.recordExactApprovalContext(ctx, "ask-1")
	if !accessTokenFromRequest(st.exactApprovalContexts["ask-1"]).IsZero() {
		t.Fatal("recordExactApprovalContext kept the request's token")
	}

	// The request itself still has its token.
	if accessTokenFromRequest(ctx).Value() != internalBearer {
		t.Fatal("stripping a derived context removed the request's token")
	}
}

func TestWithVerifiedCallerCarriesATokenOnlyWithAPrincipal(t *testing.T) {
	alice := &session.Principal{Issuer: "https://login.example.com", Subject: "alice", GrantType: session.GrantTypeUser}
	ctx, done := withVerifiedCaller(t.Context(), alice, internalBearer)
	if p := session.PrincipalFromContext(ctx); p == nil || p.Subject != alice.Subject || accessTokenFromRequest(ctx).Value() != internalBearer {
		t.Fatal("a verified caller lacks its principal or token")
	}
	done()
	if !accessTokenFromRequest(ctx).IsZero() {
		t.Fatal("done left the token")
	}

	ctx, done = withVerifiedCaller(t.Context(), nil, internalBearer)
	defer done()
	if !accessTokenFromRequest(ctx).IsZero() {
		t.Fatal("a request without a verified principal carries a token")
	}
}
