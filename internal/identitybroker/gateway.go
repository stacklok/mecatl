package identitybroker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// TokenTypeJWT is the RFC 8693 JWT token type, the type of the agent SVID a
// per-call ID-JAG exchange sends as its actor token.
const TokenTypeJWT = "urn:ietf:params:oauth:token-type:jwt" // #nosec G101 -- token type URN, not a credential.

// IDJAGIssuer is AS-1's token endpoint as a call uses it, design step (7):
// the RFC 8693 exchange of the session's refresh token, with the agent's
// SVID as the actor token, for an ID-JAG addressed to AS-2. Its errors follow
// AuthServer's: a refusal is a *ServerRefusal; a request with no response
// wraps ErrNoResponse; a misconfigured connection wraps
// ErrTransportConfiguration; a success response it cannot decode, or whose
// issued_token_type is not an ID-JAG's, wraps both ErrMalformedResponse and
// ErrTransportConfiguration. The core separately checks the token's header
// typ. Errors never carry a token.
type IDJAGIssuer interface {
	IssueIDJAG(ctx context.Context, req IDJAGRequest) (IDJAGResult, error)
}

// IDJAGRequest is a per-call ID-JAG exchange request.
type IDJAGRequest struct {
	// SubjectToken is the session's refresh token.
	SubjectToken Token
	// ActorToken is the acting agent's SVID.
	ActorToken Token
	// Audience is AS-2's issuer.
	Audience string
	// Resource is the vMCP resource the gateway token is for.
	Resource string
	// OperationID is the call's operation, sent as mecatl_operation.
	OperationID string
}

// IDJAGResult is AS-1's answer to an ID-JAG exchange.
type IDJAGResult struct {
	IDJAG Token
}

// GatewayAuthServer is AS-2's token endpoint as a call uses it, design step
// (8): the RFC 7523 JWT-bearer redemption of an ID-JAG for a gateway token.
// The token endpoint and HTTP client live in the adapter, as the core holds
// no network client. Its errors follow IDJAGIssuer's.
type GatewayAuthServer interface {
	Redeem(ctx context.Context, req Redemption) (GatewayTokenResult, error)
}

// Redemption is an ID-JAG redemption request. It carries no
// authorization_details yet (#2727).
type Redemption struct {
	// Assertion is the ID-JAG.
	Assertion Token
	// Resource is the vMCP resource.
	Resource string
}

// GatewayTokenResult is AS-2's answer to a redemption.
type GatewayTokenResult struct {
	AccessToken Token
	// ExpiresIn is AS-2's expires_in, zero when it sent none.
	ExpiresIn time.Duration
}

// Gateway is the MCP gateway the broker authorizes calls through.
type Gateway struct {
	// Servers are the backend names the gateway routes. A call naming any
	// other server is denied.
	Servers []string
	// AS2 is the gateway's authorization server. The zero value leaves it
	// unbound until BindGatewayAS2.
	AS2 GatewayAS2
}

// GatewayAS2 is the gateway's authorization server, AS-2, as the broker uses
// it: the audience of the ID-JAGs it requests, the vMCP resource, and the
// port that redeems ID-JAGs there.
type GatewayAS2 struct {
	// Issuer is AS-2's issuer, an ID-JAG's audience.
	Issuer string
	// Resource is the vMCP resource, an ID-JAG's resource.
	Resource string
	// AuthServer redeems ID-JAGs at AS-2's token endpoint.
	AuthServer GatewayAuthServer
}

func (a GatewayAS2) isZero() bool {
	return a.Issuer == "" && a.Resource == "" && a.AuthServer == nil
}

func (a GatewayAS2) validate() error {
	switch {
	case !isToken(a.Issuer):
		return errors.New("the gateway's AS-2 issuer must be non-empty printable text without spaces")
	case !isToken(a.Resource):
		return errors.New("the gateway's AS-2 resource must be non-empty printable text without spaces")
	case a.AuthServer == nil:
		return errors.New("the gateway's AS-2 has no authorization server")
	}
	return nil
}

// WithGateway has AuthorizeCall authorize calls through g. Without it every
// call that reaches the server check is denied. New refuses a second
// WithGateway, a gateway with no servers, with a server name that is not
// printable text without spaces or that appears twice, or with a partly set
// AS2, and a gateway without WithRecorder and WithIDJAGIssuer. New binds a
// non-zero AS2.
//
// Shortcut: one gateway, the embedded one; removed by #2538 (operator-declared gateway entries, each with its own AS-2).
func WithGateway(g Gateway) Option {
	return func(b *Broker) {
		// Each broker gets its own copy: New clears the copy's AS2 once bound.
		gc := g
		gc.Servers = slices.Clone(g.Servers)
		b.gateways++
		b.gateway = &gc
	}
}

// WithIDJAGIssuer has AuthorizeCall obtain each call's ID-JAG at as1
// (design step (7)). A gateway requires it.
func WithIDJAGIssuer(as1 IDJAGIssuer) Option {
	return func(b *Broker) { b.idjag = as1 }
}

// BindGatewayAS2 binds the configured gateway's AS-2, which AuthorizeCall
// needs to redeem ID-JAGs; until it is bound, a call that passes the checks
// before a grant fails with CodeUnavailable. It returns an error wrapping
// ErrInvalidConfig when no gateway is configured, AS-2 is already bound, or
// as2 is incomplete.
//
// The returned unbind is idempotent and leaves a later binding alone. It
// returns once the calls using this binding have finished; calls after it
// starts find AS-2 unbound. It must not be called from as2's AuthServer or
// the broker's IDJAGIssuer, as it would wait for its own call.
//
// Shortcut: AS-2 bound late, leaving an UNAVAILABLE window; removed by #2560 (AS-2 reached over the network as a configured gateway entry).
func (b *Broker) BindGatewayAS2(as2 GatewayAS2) (unbind func(), err error) {
	if b.gateway == nil {
		return nil, fmt.Errorf("%w: no gateway is configured", ErrInvalidConfig)
	}
	if err := as2.validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	bound := &as2Binding{as2: as2}
	if !b.as2.CompareAndSwap(nil, bound) {
		return nil, fmt.Errorf("%w: the gateway's AS-2 is already bound", ErrInvalidConfig)
	}
	return func() { bound.unbind(&b.as2) }, nil
}

// as2Binding is a bound AS-2. Each call using it holds mu for reading, so
// unbinding, which takes mu for writing, waits for them.
type as2Binding struct {
	as2     GatewayAS2
	mu      sync.RWMutex
	unbound bool
	once    sync.Once
}

// acquireAS2 returns the bound AS-2 and a release the caller must call when
// done with it, or false when AS-2 is not bound.
func (b *Broker) acquireAS2() (GatewayAS2, func(), bool) {
	bound := b.as2.Load()
	if bound == nil {
		return GatewayAS2{}, nil, false
	}
	bound.mu.RLock()
	if bound.unbound {
		bound.mu.RUnlock()
		return GatewayAS2{}, nil, false
	}
	return bound.as2, bound.mu.RUnlock, true
}

// unbind removes a from slot, if it is still there, and waits for the calls
// using it to finish. Concurrent and repeated calls all wait.
func (a *as2Binding) unbind(slot *atomic.Pointer[as2Binding]) {
	a.once.Do(func() {
		slot.CompareAndSwap(a, nil)
		a.mu.Lock()
		a.unbound = true
		a.mu.Unlock()
	})
}

// configureGateway validates the broker's gateway and the collaborators it
// requires, and binds its AS2 when set.
func (b *Broker) configureGateway() error {
	g := b.gateway
	if g == nil {
		return nil
	}
	if b.gateways > 1 {
		return errors.New("only one gateway can be configured")
	}
	if err := g.validate(); err != nil {
		return err
	}
	if b.recorder == nil || b.idjag == nil {
		return errors.New("a gateway requires a recorder and an ID-JAG issuer")
	}
	if !g.AS2.isZero() {
		if err := g.AS2.validate(); err != nil {
			return err
		}
		b.as2.Store(&as2Binding{as2: g.AS2})
		g.AS2 = GatewayAS2{}
	}
	return nil
}

func (g *Gateway) validate() error {
	if len(g.Servers) == 0 {
		return errors.New("the gateway routes no servers")
	}
	seen := make(map[string]bool, len(g.Servers))
	for _, s := range g.Servers {
		if !isToken(s) {
			return fmt.Errorf("gateway server %q must be non-empty printable text without spaces", s)
		}
		if seen[s] {
			return fmt.Errorf("gateway server %q is listed twice", s)
		}
		seen[s] = true
	}
	return nil
}

// routes reports whether g routes server. A nil g routes nothing.
func (g *Gateway) routes(server string) bool {
	return g != nil && slices.Contains(g.Servers, server)
}
