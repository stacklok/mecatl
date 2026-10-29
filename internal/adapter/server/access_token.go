package server

import (
	"context"
	"sync"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// The user's AS-1 access token reaches the identity broker once, when a user
// request opens a broker session. The authenticator attaches the bearer that
// passed authentication to the request context, and the broker session open
// reads it from there; nothing else may read it, and nothing re-reads it from
// request metadata or headers. It is a secret: it is never logged, stored,
// persisted, put in errors or diagnostics, or given to the engine or the model.

// requestAccessToken holds the verified bearer of one request. The
// authenticator empties it when the request returns, so a context copied from
// the request, such as one detached with context.WithoutCancel or retained by a
// run, no longer yields the token once the request is over.
type requestAccessToken struct {
	mu    sync.Mutex
	token identitybroker.Token
}

type requestAccessTokenKey struct{}

// withRequestAccessToken attaches the access token that authenticated the
// request. The caller must call the returned function when the request returns.
func withRequestAccessToken(ctx context.Context, token identitybroker.Token) (context.Context, func()) {
	held := &requestAccessToken{token: token}
	return context.WithValue(ctx, requestAccessTokenKey{}, held), func() {
		held.mu.Lock()
		held.token = identitybroker.Token{}
		held.mu.Unlock()
	}
}

// withoutRequestAccessToken hides the request's access token from work the
// server owns after accepting the request, such as a plan continuation, which
// acts without a user request and so has no token.
func withoutRequestAccessToken(ctx context.Context) context.Context {
	if ctx.Value(requestAccessTokenKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, requestAccessTokenKey{}, (*requestAccessToken)(nil))
}

// accessTokenFromRequest returns the access token of the request ctx belongs
// to, or the zero Token when there is none: no verified caller, a path with no
// user request, a request that has returned, or a token already taken.
func accessTokenFromRequest(ctx context.Context) identitybroker.Token {
	held, _ := ctx.Value(requestAccessTokenKey{}).(*requestAccessToken)
	if held == nil {
		return identitybroker.Token{}
	}
	held.mu.Lock()
	defer held.mu.Unlock()
	return held.token
}

// takeRequestAccessToken returns the request's access token and empties the
// holder. The identity broker converts an access token once, so the open that
// presents it takes it, and every later open in the same request presents
// nothing and so cannot be refused for the token after the request changed
// something.
func takeRequestAccessToken(ctx context.Context) identitybroker.Token {
	held, _ := ctx.Value(requestAccessTokenKey{}).(*requestAccessToken)
	if held == nil {
		return identitybroker.Token{}
	}
	held.mu.Lock()
	defer held.mu.Unlock()
	token := held.token
	held.token = identitybroker.Token{}
	return token
}
