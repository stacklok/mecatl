package server

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// corsAllowedMethods is the method set advertised on a preflight.
//
// It is the fixed set the HTTP API actually serves (see NewHTTPHandler's route
// table) rather than an echo of the requested method: advertising a method we do
// not route would let a browser send a request that can only 405.
var corsAllowedMethods = []string{
	http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions,
}

// corsMaxAgeSeconds caps how long a browser may cache a preflight result.
//
// Ten minutes. Long enough that a chatty client is not re-preflighting every
// request, short enough that removing an origin from the allowlist takes effect
// without operators wondering why a revoked origin still works.
const corsMaxAgeSeconds = "600"

// CORSPolicy is an EXACT-ORIGIN cross-origin policy for the HTTP API.
//
// Exact means exact: an origin matches only if it is byte-equal (after
// normalisation) to a configured one. There is no wildcard, no suffix match, and
// no subdomain match. Suffix matching is the classic CORS bug — an allowlist of
// "example.com" matched by suffix also admits "evil-example.com" and
// "example.com.attacker.net" — and mecatl's HTTP API can start agent runs, so
// the cost of getting it wrong is not information disclosure but arbitrary
// action taken with the victim's credentials.
//
// A nil *CORSPolicy is the DEFAULT and is a no-op: with no --cors-origins the
// middleware is not installed at all and responses are byte-identical to before
// this type existed.
//
// This is the LOCAL-DEVELOPMENT path. The production browser path remains a
// same-origin BFF that injects bearer credentials server-side and enforces its
// own Origin/CSRF policy. See ADR 0244.
type CORSPolicy struct {
	// origins is the exact-match allowlist. A map because the decision is exact
	// equality; there is deliberately no pattern, prefix, or suffix structure to
	// walk.
	origins map[string]struct{}
}

// NewCORSPolicy validates and builds a policy from configured origins.
//
// Validation is strict and happens at STARTUP, where the operator is present and
// the message is actionable. A malformed entry is refused rather than silently
// ignored: an ignored entry looks identical to a working one until a browser
// quietly fails, and the operator's mental model would be wrong in the unsafe
// direction.
func NewCORSPolicy(origins []string) (*CORSPolicy, error) {
	if len(origins) == 0 {
		return nil, nil
	}
	set := make(map[string]struct{}, len(origins))
	for _, raw := range origins {
		o := strings.TrimSpace(raw)
		if o == "" {
			continue
		}
		if err := validateCORSOrigin(o); err != nil {
			return nil, err
		}
		set[o] = struct{}{}
	}
	if len(set) == 0 {
		return nil, nil
	}
	return &CORSPolicy{origins: set}, nil
}

// validateCORSOrigin rejects anything that is not a bare, exact web origin.
func validateCORSOrigin(o string) error {
	// "*" is refused EXPLICITLY rather than falling out of the parse, because it
	// is the thing an operator is most likely to try and the refusal should say
	// why. A wildcard is incompatible with credentials by specification, and this
	// policy always sends credentials for an allowed origin.
	if o == "*" {
		return fmt.Errorf("--cors-origins: %q is not allowed; this policy sends credentials, and wildcard-with-credentials is forbidden — list each origin exactly", o)
	}
	// "null" is the origin browsers send for sandboxed iframes, data: URLs, and
	// some file:// contexts. It is not owned by anyone, so allowing it grants
	// access to any page that can arrange to be opaque.
	if strings.EqualFold(o, "null") {
		return fmt.Errorf("--cors-origins: %q is not a real origin (browsers send it for sandboxed/opaque contexts, which anyone can arrange)", o)
	}
	u, err := url.Parse(o)
	if err != nil {
		return fmt.Errorf("--cors-origins: %q is not a valid origin: %w", o, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("--cors-origins: %q must use http or https (got scheme %q)", o, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("--cors-origins: %q has no host", o)
	}
	// A "*" anywhere in the host means the operator wanted a pattern. It PARSES
	// as a perfectly valid literal hostname, so without this check it would be
	// accepted and then never match any real Origin header — a silently dead
	// allowlist entry that looks identical to a working one, which is exactly the
	// failure this validation exists to prevent. Refuse it and say why.
	if strings.Contains(u.Host, "*") {
		return fmt.Errorf("--cors-origins: %q contains a wildcard; matching is exact, so list each origin in full (a wildcard host would be taken literally and never match)", o)
	}
	// An origin is scheme + host + port and nothing else. A trailing slash, path,
	// query, or fragment means the operator wrote a URL, not an origin — and it
	// would never match the Origin header a browser sends, so it must be a loud
	// error rather than a silently dead entry.
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("--cors-origins: %q must be a bare origin (scheme://host[:port]) with no path, query, fragment, or userinfo", o)
	}
	return nil
}

// allows reports whether origin is exactly allowed.
func (p *CORSPolicy) allows(origin string) bool {
	if p == nil || origin == "" {
		return false
	}
	_, ok := p.origins[origin]
	return ok
}

// Middleware applies the policy to next.
//
// IT MUST WRAP OUTSIDE THE AUTH MIDDLEWARE. A browser preflight is an
// unauthenticated OPTIONS request — the CORS specification forbids sending
// credentials on it — so a policy installed INSIDE auth would 401 every
// preflight and cross-origin access would never work at all. Wrapping outside is
// safe because a preflight is answered with headers only: it never reaches a
// handler, never touches a session, and never returns data.
func (p *CORSPolicy) Middleware(next http.Handler) http.Handler {
	if p == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Vary: Origin goes on EVERY response this middleware sees, including
		// ones with no Origin and ones from a refused origin. The response body
		// and headers depend on the Origin header, so a cache that does not vary
		// on it could serve an allowed origin's CORS headers to a different
		// origin. Setting it unconditionally is the only version of this that is
		// correct for shared caches.
		w.Header().Add("Vary", "Origin")

		allowed := p.allows(origin)
		if allowed {
			// Echo the EXACT origin, never "*". With credentials enabled a
			// wildcard is rejected by the browser anyway, but echoing is also
			// what makes Vary: Origin meaningful.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		if isCORSPreflight(r) {
			// A preflight is answered HERE and never forwarded. Forwarding it
			// would route an OPTIONS at a mux with no OPTIONS routes (405) and,
			// worse, would run handler logic for a request the browser sent only
			// to ask permission.
			if allowed {
				w.Header().Set("Access-Control-Allow-Methods", strings.Join(corsAllowedMethods, ", "))
				// Echo the requested headers rather than publishing a fixed list.
				// This is safe precisely BECAUSE the origin already passed the
				// exact-match allowlist: we are telling a trusted origin which of
				// its own headers it may send. A fixed list would silently break
				// any client that needs a header we did not predict.
				if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
					w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
					w.Header().Add("Vary", "Access-Control-Request-Headers")
				}
				w.Header().Set("Access-Control-Max-Age", corsMaxAgeSeconds)
			}
			// 204 either way. A refused preflight carries NO CORS headers, which
			// is what makes the browser block the real request; answering 403
			// instead would leak whether an origin is on the allowlist to any
			// page that cares to probe.
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isCORSPreflight reports whether r is a CORS preflight.
//
// Both conditions are required: OPTIONS alone is not a preflight (it is a
// legitimate, if unrouted, HTTP method), and the Access-Control-Request-Method
// header is what actually distinguishes the browser's permission question from
// an ordinary request.
func isCORSPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

// AllowedOrigins returns the configured origins, sorted. It exists for startup
// logging and tests; the policy decision never walks this slice.
func (p *CORSPolicy) AllowedOrigins() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.origins))
	for o := range p.origins {
		out = append(out, o)
	}
	slices.Sort(out)
	return out
}
