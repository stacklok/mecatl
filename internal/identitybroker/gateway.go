package identitybroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	// AuthorizationDetails is the call entry AS-1 signs into the ID-JAG, a
	// JSON array of one entry of CallEntryType. The adapter omits the
	// parameter when it is empty.
	AuthorizationDetails json.RawMessage
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
// authorization_details: AS-2 takes the call entry only from the claim AS-1
// signed into the ID-JAG, and refuses the parameter.
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
	// AuthorizationDetails is the response's authorization_details member as
	// AS-2 sent it, nil when absent. The broker checks it against the call
	// entry it sent (step (8a)).
	AuthorizationDetails json.RawMessage
}

// Gateway is the MCP gateway the broker authorizes calls through, built from
// its gateway entry.
type Gateway struct {
	// Issuer and Resource are the AS-2 issuer and vMCP resource the gateway's
	// entry names. The AS-2 bound for the gateway must name both.
	Issuer, Resource string
	// Servers are the backend names the gateway routes. A call naming any
	// other server is denied.
	Servers []string
	// AS2 is the gateway's authorization server. The zero value leaves it
	// unbound until BindGatewayAS2.
	AS2 GatewayAS2
}

// GatewayKind is a gateway entry's kind.
type GatewayKind string

// The gateway kinds an entry may name.
const (
	// GatewayEmbedded is the gateway the process serving the broker's caller
	// runs: mecak8s's embedded vMCP, whose authorization server is AS-2.
	GatewayEmbedded GatewayKind = "embedded"
	// GatewayExternal is a gateway run elsewhere, with its own AS-2.
	GatewayExternal GatewayKind = "external"
)

// GatewayEntry is a gateway the operator declares in the broker's
// configuration. Its AS-2 issuer and resource are the target AS-1 issues the
// gateway's ID-JAGs for, and its servers are the ones check 6 lets calls
// reach through it.
type GatewayEntry struct {
	Kind GatewayKind
	// AS2Issuer is the gateway's authorization server's issuer, an ID-JAG's
	// audience.
	AS2Issuer string
	// TokenEndpoint is AS-2's token endpoint as AS-2 advertises it, never a
	// dial address.
	TokenEndpoint string
	// Resource is the gateway's vMCP resource, an ID-JAG's resource.
	Resource string
	// Servers are the backend names the gateway routes. An entry may route
	// none, which configures no gateway.
	Servers []string
}

// ValidateGatewayEntries checks entries as the broker accepts them: at most
// one entry, of kind GatewayEmbedded; its issuer, token endpoint, and
// resource non-empty printable text without spaces; and its servers letters,
// digits, and single underscores (isGatewayServerName), each listed once.
// The loader checks the URLs. Errors wrap ErrInvalidConfig.
//
// Shortcut: at most one gateway entry, of kind embedded; removed by #2812 (external gateways, and several gateways).
func ValidateGatewayEntries(entries []GatewayEntry) error {
	if len(entries) > 1 {
		return fmt.Errorf("%w: only one gateway entry can be configured", ErrInvalidConfig)
	}
	for i, e := range entries {
		switch e.Kind {
		case GatewayEmbedded:
		case GatewayExternal:
			return fmt.Errorf("%w: gateway entry %d: external gateways are not supported yet (#2812)", ErrInvalidConfig, i)
		default:
			return fmt.Errorf("%w: gateway entry %d: kind %q must be %q or %q", ErrInvalidConfig, i, e.Kind, GatewayEmbedded, GatewayExternal)
		}
		for _, f := range []struct{ name, value string }{{"AS-2 issuer", e.AS2Issuer}, {"token endpoint", e.TokenEndpoint}, {"resource", e.Resource}} {
			if !isToken(f.value) {
				return fmt.Errorf("%w: gateway entry %d: the %s must be non-empty printable text without spaces", ErrInvalidConfig, i, f.name)
			}
		}
		if err := validateServers(e.Servers); err != nil {
			return fmt.Errorf("%w: gateway entry %d: %v", ErrInvalidConfig, i, err)
		}
	}
	return nil
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
// WithGateway, a gateway without an issuer or resource, with no servers, with
// a server name that is not letters, digits, and single underscores or that
// appears twice, or with a partly set AS2 or one naming another issuer or
// resource,
// and a gateway without WithRecorder and WithIDJAGIssuer. New binds a non-zero
// AS2.
//
// Shortcut: one gateway; removed by #2812 (several gateways, each with its own AS-2).
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
// as2 is incomplete or names another issuer or resource than the gateway's.
//
// The returned unbind is idempotent and leaves a later binding alone. It
// returns once the calls using this binding have finished; calls after it
// starts find AS-2 unbound. It must not be called from as2's AuthServer or
// the broker's IDJAGIssuer, as it would wait for its own call.
//
// Shortcut: AS-2 bound late, leaving an UNAVAILABLE window; removed by #2559 (AS-2 reached over the network as a configured gateway entry).
func (b *Broker) BindGatewayAS2(as2 GatewayAS2) (unbind func(), err error) {
	if b.gateway == nil {
		return nil, fmt.Errorf("%w: no gateway is configured", ErrInvalidConfig)
	}
	if err := b.gateway.checkAS2(as2); err != nil {
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
		if err := g.checkAS2(g.AS2); err != nil {
			return err
		}
		b.as2.Store(&as2Binding{as2: g.AS2})
		g.AS2 = GatewayAS2{}
	}
	return nil
}

func (g *Gateway) validate() error {
	switch {
	case !isToken(g.Issuer):
		return errors.New("the gateway's AS-2 issuer must be non-empty printable text without spaces")
	case !isToken(g.Resource):
		return errors.New("the gateway's resource must be non-empty printable text without spaces")
	case len(g.Servers) == 0:
		return errors.New("the gateway routes no servers")
	}
	return validateServers(g.Servers)
}

// checkAS2 checks that as2 is complete and is the AS-2 of g's entry.
func (g *Gateway) checkAS2(as2 GatewayAS2) error {
	if err := as2.validate(); err != nil {
		return err
	}
	if as2.Issuer != g.Issuer || as2.Resource != g.Resource {
		return fmt.Errorf("the AS-2 at %q for %q is not the gateway's, at %q for %q", as2.Issuer, as2.Resource, g.Issuer, g.Resource)
	}
	return nil
}

// validateServers refuses a server name that isGatewayServerName refuses or
// that appears twice.
func validateServers(servers []string) error {
	seen := make(map[string]bool, len(servers))
	for _, s := range servers {
		if !isGatewayServerName(s) {
			return fmt.Errorf("gateway server %q must be letters, digits, and single underscores", s)
		}
		if seen[s] {
			return fmt.Errorf("gateway server %q is listed twice", s)
		}
		seen[s] = true
	}
	return nil
}

// isGatewayServerName reports whether s is a server name the gateway may
// route: letters, digits, and underscores, never two underscores together, as
// Mecatl's MCP configuration requires of server names. A call's tool is named
// "<server>.<tool>", so with no dot in a server name a call names exactly one
// server and tool. A definition excludes a tool as "mcp__<server>__<tool>",
// which one rule cannot keep unambiguous while the configuration allows a
// trailing underscore: "mcp__git___x" is tool "_x" on git and tool "x" on
// git_. The loader then excludes it from both, so an ambiguous exclusion only
// narrows.
func isGatewayServerName(s string) bool {
	if s == "" || strings.Contains(s, "__") {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// routes reports whether g routes server. A nil g routes nothing.
func (g *Gateway) routes(server string) bool {
	return g != nil && slices.Contains(g.Servers, server)
}
