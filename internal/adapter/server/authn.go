package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// PrincipalValidator verifies a bearer credential and returns the caller it
// vouches for (ADR 0204 decision 3). Validation itself — JWT parse, signature,
// alg, issuer/audience/exp/nbf, JWKS fetch and rotation — is DELEGATED to the
// implementation (toolhive-core/authn); mecatl hand-rolls none of it. This
// narrow interface is the seam: the edge only decides what to do with the
// verdict.
//
// A nil returned principal with a nil error is treated as a rejection: the edge
// never fabricates an anonymous caller (absent identity is a nil principal, and
// that only happens when NO validator is wired at all).
type PrincipalValidator interface {
	Validate(ctx context.Context, bearer string) (*session.Principal, error)
}

var (
	// ErrInvalidToken is the 401-class verdict: the credential is malformed,
	// unsigned, from the wrong issuer/audience, expired, not yet valid, or
	// otherwise not vouched for. A validator should wrap it.
	ErrInvalidToken = errors.New("invalid bearer token")
	// ErrIdentityUnavailable is the TRANSIENT 503-class verdict: the identity
	// provider (its JWKS endpoint / discovery document) could not be reached, so
	// the token's validity is UNKNOWN. It is deliberately distinct from
	// ErrInvalidToken — an IdP outage must not be reported as an authn failure.
	ErrIdentityUnavailable = errors.New("identity provider unavailable")
	// errRejectedRateLimit means the direct peer exhausted its separate
	// pre-validation budget for rejected bearer credentials.
	errRejectedRateLimit = errors.New("rejected bearer rate limit exceeded")
)

// AuthenticationRejectionCategorizer supplies a safe, closed diagnostic category
// for an authentication failure. Its value is never sent to clients.
type AuthenticationRejectionCategorizer interface {
	AuthenticationRejectionCategory() string
}

const (
	authCategoryMissingBearer          = "missing_bearer"
	authCategoryDuplicateAuthorization = "duplicate_authorization"
	authCategoryWrongAudience          = "wrong_audience"
	authCategoryWrongIssuer            = "wrong_issuer"
	authCategoryMalformed              = "malformed"
	authCategorySignature              = "signature"
	authCategoryUnknownKID             = "unknown_kid"
	authCategoryExpired                = "expired"
	authCategoryNotYetValid            = "not_yet_valid"
	authCategoryJWKSUnavailable        = "jwks_unavailable"
	authCategoryJWKSStale              = "jwks_stale"
	authCategoryInvalidToken           = "invalid_token"
)

// SecurityConfig configures the reusable authentication and rate-limiting
// interceptors/middleware shared by the gRPC and HTTP surfaces. The zero value
// is a valid, fully permissive (dev) configuration: no token is required and no
// rate limit is enforced.
//
// Both knobs compose with the loopback-default + off-loopback warning in the
// composition root (cmd/mecated): an empty AuthToken on a non-loopback bind is a
// loud-but-not-fatal misconfiguration the operator is warned about.
type SecurityConfig struct {
	// AuthToken, when non-empty, requires every RPC/request to present
	// Authorization: Bearer <AuthToken> (gRPC: the "authorization" metadata
	// header). An empty token disables authentication (dev mode).
	AuthToken string
	// RateLimit is the sustained per-client request rate in requests/second. A
	// value <= 0 disables rate limiting entirely. Limiting is applied both
	// per-client (keyed by token or peer IP) and globally.
	RateLimit float64
	// RateBurst is the token-bucket burst size. It defaults to a small multiple
	// of RateLimit when left zero (see newLimiterSet).
	RateBurst int
	// Validator, when non-nil, turns caller identity ON: every request must
	// present a bearer the validator vouches for, and the verified principal is
	// stashed on the handler context (session.WithPrincipal). Nil (the default)
	// leaves the path byte-identical to a mecatl without identity: no
	// validation, no principal, no new failure mode.
	Validator PrincipalValidator
	// ResourceMetadataURL is the validated, operator-configured RFC 9728
	// metadata endpoint for this service's configured protected-resource base.
	// Middleware deliberately advertises that one base for every protected API
	// route; it never derives a resource from the untrusted Host or request path.
	// Empty retains the legacy bare Bearer challenge.
	ResourceMetadataURL string
	// Diagnostics receives sanitized authentication-rejection records. Nil leaves
	// diagnostics disabled; records never include credentials or validator errors.
	Diagnostics port.Diagnostics
}

// authEnabled reports whether a STATIC shared bearer token is configured. It
// says nothing about caller identity — see identityConfigured.
func (c SecurityConfig) authEnabled() bool { return c.AuthToken != "" }

// identityConfigured reports whether caller identity is on, i.e. whether a
// verifier is wired. It is deliberately INDEPENDENT of authEnabled(): a
// shared-token deployment has one credential and zero subjects, and an OIDC
// deployment may have subjects and no static token (ADR 0204 decision 2).
// Neither predicate may gate the other's behaviour.
func (c SecurityConfig) identityConfigured() bool { return c.Validator != nil }

// rateEnabled reports whether rate limiting is configured.
func (c SecurityConfig) rateEnabled() bool { return c.RateLimit > 0 }

// authHeader is the canonical bearer-token header on both surfaces.
const authHeader = "authorization"

// bearerPrefix is the RFC 6750 scheme prefix on the Authorization value.
const bearerPrefix = "Bearer "

// constantTimeTokenMatch compares got against want in constant time so a
// malformed/short token cannot be distinguished from a wrong one by timing.
func constantTimeTokenMatch(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// bearerFromAuthValue extracts the token from an "Authorization: Bearer <tok>"
// value, returning the token and whether the Bearer scheme was present.
func bearerFromAuthValue(v string) (string, bool) {
	if len(v) < len(bearerPrefix) || !strings.EqualFold(v[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	return v[len(bearerPrefix):], true
}

// --- rate limiting ----------------------------------------------------------

// limiterSet holds a global limiter plus a bounded, idle-evicting map of
// per-client limiters. It is safe for concurrent use.
type limiterSet struct {
	r     rate.Limit
	burst int

	global *rate.Limiter

	mu      sync.Mutex
	clients map[string]*clientLimiter
	// overflow is used only by the rejected-token limiter after clients reaches
	// maxTrackedClients and no idle entry can be evicted. New peers then share a
	// bucket rather than growing the map without bound.
	overflow *clientLimiter
	// idleTTL evicts a client limiter that has not been seen for this long, so
	// memory stays bounded under churning client identities (e.g. peer IPs).
	idleTTL time.Duration
	now     func() time.Time
}

// clientLimiter is a per-client token bucket plus its last-seen stamp.
type clientLimiter struct {
	lim  *rate.Limiter
	seen time.Time
	// validationGate serializes rejected-bearer validation for one direct peer.
	// It is a context-aware gate: a waiting request may leave when its caller
	// cancels, without waiting for an in-flight validator call to finish.
	validationGate chan struct{}
}

// maxTrackedClients caps the per-client map; once exceeded a sweep evicts idle
// entries. It bounds memory even if every request carries a fresh identity.
const maxTrackedClients = 4096

// newLimiterSet builds a limiterSet for the given sustained rate and burst. A
// non-positive burst defaults to max(rps, 1) rounded up so a single client can
// still issue a small burst.
func newLimiterSet(rps float64, burst int) *limiterSet {
	if burst <= 0 {
		burst = int(rps)
		if burst < 1 {
			burst = 1
		}
	}
	return &limiterSet{
		r:       rate.Limit(rps),
		burst:   burst,
		global:  rate.NewLimiter(rate.Limit(rps), burst),
		clients: make(map[string]*clientLimiter),
		idleTTL: 10 * time.Minute,
		now:     time.Now,
	}
}

func newRejectedLimiterSet(rps float64, burst int) *limiterSet {
	s := newLimiterSet(rps, burst)
	s.overflow = newClientLimiter(s.r, s.burst)
	return s
}

func newClientLimiter(r rate.Limit, burst int) *clientLimiter {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &clientLimiter{lim: rate.NewLimiter(r, burst), validationGate: gate}
}

// allow reports whether a request from client key may proceed: it must satisfy
// both the per-client and the global limiter.
func (s *limiterSet) allow(key string) bool {
	cl := s.clientLimiter(key)
	// Check the per-client bucket first, then the global one. Both must permit.
	if !cl.Allow() {
		return false
	}
	return s.global.Allow()
}

// allowValidation runs validate only while key's rejected-token bucket has
// budget. Successful validation and operational errors are not charged; an
// invalid/admissibility rejection consumes one token. A peer is intentionally
// serialized to protect the validator, but waiting requests can leave promptly
// when ctx is canceled or reaches its deadline.
func (s *limiterSet) allowValidation(ctx context.Context, key string, validate func() (*session.Principal, error), charge func(error) bool) (*session.Principal, bool, error) {
	cl := s.clientLimiterEntry(key)
	select {
	case <-cl.validationGate:
		defer func() { cl.validationGate <- struct{}{} }()
	case <-ctx.Done():
		return nil, true, ctx.Err()
	}

	now := s.now()
	if cl.lim.TokensAt(now) < 1 {
		return nil, false, nil
	}
	p, err := validate()
	if charge(err) {
		cl.lim.AllowN(s.now(), 1)
	}
	return p, true, err
}

// clientLimiter returns (creating if needed) the limiter for key, refreshing its
// last-seen stamp and opportunistically evicting idle entries.
func (s *limiterSet) clientLimiter(key string) *rate.Limiter {
	return s.clientLimiterEntry(key).lim
}

func (s *limiterSet) clientLimiterEntry(key string) *clientLimiter {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[key]; ok {
		c.seen = now
		return c
	}
	if len(s.clients) >= maxTrackedClients {
		s.evictIdleLocked(now)
		if len(s.clients) >= maxTrackedClients && s.overflow != nil {
			s.overflow.seen = now
			return s.overflow
		}
	}
	c := newClientLimiter(s.r, s.burst)
	c.seen = now
	s.clients[key] = c
	return c
}

// evictIdleLocked removes clients not seen within idleTTL. The caller holds mu.
func (s *limiterSet) evictIdleLocked(now time.Time) {
	for k, c := range s.clients {
		if now.Sub(c.seen) > s.idleTTL {
			delete(s.clients, k)
		}
	}
}

// --- client identity --------------------------------------------------------

// clientKeyFromToken keys rate limiting by the presented token when auth is on
// (so each credential gets its own bucket), falling back to a fixed key.
func clientKeyFromToken(token string) string {
	if token == "" {
		return "anon"
	}
	return "tok:" + token
}

// clientKeyFromPrincipal keys rate limiting by the VERIFIED (iss, sub) pair, so
// two tokens for the same subject share one bucket — rotating a credential is
// not a limit bypass. Identity is the pair, never sub alone (two issuers collide
// on sub), and the raw token never appears in the key.
func clientKeyFromPrincipal(p *session.Principal) string {
	return "sub:" + p.Issuer + "\x00" + p.Subject
}

// clientKeyFromAddr keys rate limiting by the peer IP (host portion), so a
// single host shares one bucket regardless of source port.
func clientKeyFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return "ip:unknown"
	}
	return "ip:" + host
}

// --- gRPC interceptors ------------------------------------------------------

// Authenticator bundles the configured auth + rate-limit policy and exposes the
// gRPC interceptors and HTTP middleware that enforce it. Construct it once and
// share it across both surfaces.
type Authenticator struct {
	cfg      SecurityConfig
	limiters *limiterSet
	// rejectedLimiters protects the validator from repeated rejected bearers.
	// It is separate from limiters so valid requests consume only their verified
	// principal's post-validation bucket.
	rejectedLimiters *limiterSet
	// closeOnce guards the optional validator teardown so a defer plus an
	// explicit shutdown call cannot double-close.
	closeOnce sync.Once
}

// Close releases the edge's own long-lived resources at shutdown. Today that is
// exactly one thing: the configured validator's teardown, when it has one.
//
// Teardown is an OPTIONAL CAPABILITY, type-asserted (the port.HookApprovalLearner
// idiom), never a method on PrincipalValidator — widening that single-method
// interface would break every fake and every test that scripts the seam. A
// validator without a Close needs none.
//
// It exists because the real validator (toolhive-core/authn) owns a BACKGROUND
// JWKS refresh that its Close() stops, and cancelling the root context does NOT
// call Close(). Both server mains defer Authenticator.Close.
//
// Safe to call more than once and on the identity-OFF zero value.
func (a *Authenticator) Close() {
	a.closeOnce.Do(func() {
		if c, ok := a.cfg.Validator.(io.Closer); ok {
			_ = c.Close()
		}
	})
}

// NewAuthenticator builds an Authenticator from cfg. When rate limiting is
// disabled the limiter set is nil and the rate-limit checks are skipped.
func NewAuthenticator(cfg SecurityConfig) *Authenticator {
	a := &Authenticator{cfg: cfg}
	if cfg.rateEnabled() {
		a.limiters = newLimiterSet(cfg.RateLimit, cfg.RateBurst)
		if cfg.identityConfigured() {
			a.rejectedLimiters = newRejectedLimiterSet(cfg.RateLimit, cfg.RateBurst)
		}
	}
	return a
}

// tokenFromMetadata extracts the bearer token from incoming gRPC metadata. gRPC
// permits repeated metadata keys, but authorization is singular: duplicates are
// rejected rather than choosing an attacker-controlled first or last value.
func tokenFromMetadata(ctx context.Context) (token string, present, duplicate bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false, false
	}
	vals := md.Get(authHeader)
	if len(vals) != 1 {
		return "", false, len(vals) > 1
	}
	token, present = bearerFromAuthValue(vals[0])
	return token, present, false
}

// identify runs the caller-identity step: when a verifier is wired the bearer is
// validated and the verified principal returned; when it is not, it returns
// (nil, nil) — absent identity, never a fabricated caller. A missing bearer,
// a rejected token, a nil-principal verdict and an UNUSABLE principal (see
// admissiblePrincipal) are all ErrInvalidToken; an unreachable IdP surfaces as
// ErrIdentityUnavailable. With OIDC rate limiting enabled, a presented bearer
// first passes the direct-peer rejected-token budget; invalid/admissibility
// rejections consume that separate budget, while successful validation and
// operational validator errors do not.
func (a *Authenticator) identify(ctx context.Context, bearer string, present bool, peerKey string) (*session.Principal, error) {
	if !a.cfg.identityConfigured() {
		return nil, nil
	}
	if !present {
		return nil, ErrInvalidToken
	}
	validate := func() (*session.Principal, error) {
		p, err := a.cfg.Validator.Validate(ctx, bearer)
		if err != nil {
			return nil, err
		}
		if !admissiblePrincipal(p) {
			return nil, ErrInvalidToken
		}
		return p, nil
	}
	if a.rejectedLimiters == nil {
		return validate()
	}
	p, allowed, err := a.rejectedLimiters.allowValidation(ctx, peerKey, validate, func(err error) bool {
		return errors.Is(err, ErrInvalidToken)
	})
	if !allowed {
		return nil, errRejectedRateLimit
	}
	return p, err
}

// admissiblePrincipal reports whether a validator's verdict is a principal the
// edge may admit. A non-nil principal is not automatically trustworthy — the
// validator is an injected dependency, so the edge enforces the shape it
// promises rather than trusting it:
//
//   - Identity is the (Issuer, Subject) PAIR; a principal missing either half
//     cannot be attributed to anyone. A wholly empty one is exactly the
//     fabricated-anonymous caller ADR 0204 decision 2 rejects, arriving through
//     the front door.
//   - GrantType must be inside the closed enum: an out-of-enum value would flow
//     to every downstream consumer as an unrecognised, unhandled case.
//   - Identity components must be free of the reserved owner-key separator
//     (NUL). Owner scope keys are derived as hash(issuer + NUL + subject), so a
//     NUL inside either component makes distinct principals collide on one
//     namespace: ("a", "b\x00c") and ("a\x00b", "c") produce the same key.
//     PrincipalFromClaims already refuses these, but that is the SHIPPED
//     validator's guarantee, not the edge's — cfg.Validator is an injection
//     seam, so the edge re-states the rule rather than inheriting it.
//   - The INTERNAL namespace is off limits to an external caller. A token
//     presenting mecatl:internal as its issuer, or the system grant, is
//     byte-identical to a syscaller-stamped harness goroutine at every consumer
//     — including the isolation track (#368), which will read it to decide.
//     Only internal/syscaller mints those, and it never goes through this edge.
func admissiblePrincipal(p *session.Principal) bool {
	switch {
	case p == nil, p.Issuer == "", p.Subject == "":
		return false
	case !p.GrantType.Valid(), p.GrantType == session.GrantTypeSystem:
		return false
	case p.Issuer == syscaller.Issuer:
		return false
	case !p.IdentityWellFramed():
		return false
	}
	return true
}

// identityStatus maps an identify error onto a gRPC status: context cancellation
// and deadlines retain their native statuses, rejected-token throttling is
// ResourceExhausted, a transient IdP outage is Unavailable (503-class), and
// every other rejection is Unauthenticated.
func identityStatus(err error) error {
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, context.Canceled.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, context.DeadlineExceeded.Error())
	}
	if errors.Is(err, errRejectedRateLimit) {
		return status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
	if errors.Is(err, ErrIdentityUnavailable) {
		return status.Error(codes.Unavailable, "identity provider unavailable")
	}
	return status.Error(codes.Unauthenticated, "missing or invalid bearer token")
}

func rejectionCategory(err error) string {
	var categorized AuthenticationRejectionCategorizer
	if errors.As(err, &categorized) {
		switch category := categorized.AuthenticationRejectionCategory(); category {
		case authCategoryWrongAudience, authCategoryWrongIssuer, authCategoryMalformed,
			authCategorySignature, authCategoryUnknownKID, authCategoryExpired,
			authCategoryNotYetValid, authCategoryJWKSUnavailable, authCategoryJWKSStale:
			return category
		}
	}
	return authCategoryInvalidToken
}

func (a *Authenticator) logRejection(ctx context.Context, category, transport, statusText string) {
	if a.cfg.Diagnostics == nil {
		return
	}
	a.cfg.Diagnostics.Log(ctx, port.LevelWarn, "authentication rejected", "category", category, "transport", transport, "status", statusText)
}

// authGRPC verifies the static bearer token (when configured) and the caller
// identity (when a verifier is wired), returning the token presented (empty when
// static auth is off) and the verified principal (nil when identity is off).
func (a *Authenticator) authGRPC(ctx context.Context) (string, *session.Principal, error) {
	bearer, present, duplicate := tokenFromMetadata(ctx)
	if duplicate {
		a.logRejection(ctx, authCategoryDuplicateAuthorization, "grpc", codes.Unauthenticated.String())
		return "", nil, status.Error(codes.Unauthenticated, "duplicate authorization metadata")
	}
	staticTok := ""
	if a.cfg.authEnabled() {
		if !present || !constantTimeTokenMatch(bearer, a.cfg.AuthToken) {
			category := authCategoryInvalidToken
			if !present {
				category = authCategoryMissingBearer
			}
			a.logRejection(ctx, category, "grpc", codes.Unauthenticated.String())
			return "", nil, status.Error(codes.Unauthenticated, "missing or invalid bearer token")
		}
		staticTok = bearer
	}
	peerKey := clientKeyFromAddr("")
	if pr, ok := peer.FromContext(ctx); ok && pr.Addr != nil {
		peerKey = clientKeyFromAddr(pr.Addr.String())
	}
	p, err := a.identify(ctx, bearer, present, peerKey)
	if err != nil {
		identityErr := identityStatus(err)
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, errRejectedRateLimit) {
			category := rejectionCategory(err)
			if !present {
				category = authCategoryMissingBearer
			}
			if errors.Is(err, ErrIdentityUnavailable) && category == authCategoryInvalidToken {
				category = authCategoryJWKSUnavailable
			}
			a.logRejection(ctx, category, "grpc", status.Code(identityErr).String())
		}
		return "", nil, identityErr
	}
	return staticTok, p, nil
}

// rateGRPC enforces the rate limit for a gRPC call keyed by the verified
// (iss, sub) when identity is on, else by token (when static auth is on) or by
// peer IP. It returns codes.ResourceExhausted when over the limit.
func (a *Authenticator) rateGRPC(ctx context.Context, token string, p *session.Principal) error {
	if a.limiters == nil {
		return nil
	}
	var key string
	switch {
	case p != nil:
		key = clientKeyFromPrincipal(p)
	case a.cfg.authEnabled():
		key = clientKeyFromToken(token)
	default:
		key = clientKeyFromToken(token)
		if pr, ok := peer.FromContext(ctx); ok && pr.Addr != nil {
			key = clientKeyFromAddr(pr.Addr.String())
		}
	}
	if !a.limiters.allow(key) {
		return status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
	return nil
}

// UnaryInterceptor returns a grpc.UnaryServerInterceptor enforcing auth then
// rate limiting before the handler runs. The verified principal (if any) rides
// the handler context.
func (a *Authenticator) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		tok, p, err := a.authGRPC(ctx)
		if err != nil {
			return nil, err
		}
		if err := a.rateGRPC(ctx, tok, p); err != nil {
			return nil, err
		}
		return handler(session.WithPrincipal(ctx, p), req)
	}
}

// StreamInterceptor returns a grpc.StreamServerInterceptor enforcing auth then
// rate limiting before the stream handler runs. The rate check is applied once,
// at stream establishment.
func (a *Authenticator) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()
		tok, p, err := a.authGRPC(ctx)
		if err != nil {
			return err
		}
		if err := a.rateGRPC(ctx, tok, p); err != nil {
			return err
		}
		if p == nil {
			return handler(srv, ss)
		}
		return handler(srv, principalStream{ServerStream: ss, ctx: session.WithPrincipal(ctx, p)})
	}
}

// principalStream overrides a ServerStream's Context so the stream handler sees
// the verified principal. It is only used when identity is on, so the no-auth
// path hands the handler the ORIGINAL stream, unchanged.
type principalStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s principalStream) Context() context.Context { return s.ctx }

// --- HTTP middleware --------------------------------------------------------

// Middleware wraps next with bearer-auth and rate-limit enforcement. The health
// endpoints (/healthz, /readyz) MUST be mounted outside this middleware so they
// remain reachable without credentials and are never rate limited.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer, present := bearerFromAuthValue(r.Header.Get("Authorization"))
		token := ""
		if a.cfg.authEnabled() {
			if !present || !constantTimeTokenMatch(bearer, a.cfg.AuthToken) {
				category := authCategoryInvalidToken
				if !present {
					category = authCategoryMissingBearer
				}
				a.logRejection(r.Context(), category, "http", "401")
				w.Header().Set("WWW-Authenticate", protectedResourceChallenge(a.cfg.ResourceMetadataURL))
				writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}
			token = bearer
		}
		principal, err := a.identify(r.Context(), bearer, present, clientKeyFromAddr(r.RemoteAddr))
		if err != nil {
			if errors.Is(err, context.Canceled) {
				// HTTP has no standard cancellation status. Use 408 without an auth
				// challenge: this is the caller ending its request, not bad credentials.
				writeError(w, http.StatusRequestTimeout, "request canceled")
				return
			}
			if errors.Is(err, context.DeadlineExceeded) {
				writeError(w, http.StatusRequestTimeout, "request deadline exceeded")
				return
			}
			if errors.Is(err, errRejectedRateLimit) {
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			if errors.Is(err, ErrIdentityUnavailable) {
				// A transient IdP outage: the token's validity is UNKNOWN, so
				// this is a 503 with no auth challenge — never a 401.
				category := rejectionCategory(err)
				if category == authCategoryInvalidToken {
					category = authCategoryJWKSUnavailable
				}
				a.logRejection(r.Context(), category, "http", "503")
				writeError(w, http.StatusServiceUnavailable, "identity provider unavailable")
				return
			}
			category := rejectionCategory(err)
			if !present {
				category = authCategoryMissingBearer
			}
			a.logRejection(r.Context(), category, "http", "401")
			w.Header().Set("WWW-Authenticate", protectedResourceChallenge(a.cfg.ResourceMetadataURL))
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		if a.limiters != nil {
			var key string
			switch {
			case principal != nil:
				key = clientKeyFromPrincipal(principal)
			case a.cfg.authEnabled():
				key = clientKeyFromToken(token)
			default:
				key = clientKeyFromAddr(r.RemoteAddr)
			}
			if !a.limiters.allow(key) {
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}
		if principal != nil {
			r = r.WithContext(session.WithPrincipal(r.Context(), principal))
		}
		next.ServeHTTP(w, r)
	})
}
