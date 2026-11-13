// Package authserverlimit bounds the anonymous routes of the authorization
// servers Mecatl serves: AS-1 in mecak8s and the MCP broker's embedded
// authorization server (AS-2).
//
// /oauth/authorize stores a pending authorization before anyone signs in, so
// request size and rate bound the memory an unauthenticated caller can make
// an authorization server hold. Mecatl does not trust forwarded headers, so
// behind an ingress every user has the same peer address: the rates are
// global, with the browser sign-in routes and the token endpoint in separate
// buckets so a flood of one cannot starve the other.
package authserverlimit

import (
	"net/http"

	"golang.org/x/time/rate"
)

// Bounds on an authorization server's anonymous routes.
const (
	MaxQueryBytes    = 8 << 10
	MaxBodyBytes     = 64 << 10
	signInRatePerSec = 50
	SignInBurst      = 200
	tokenRatePerSec  = 50
	TokenBurst       = 200
)

// NewSignInLimiter is a bucket for one authorization server's browser
// sign-in, discovery, and metadata routes.
func NewSignInLimiter() *rate.Limiter { return rate.NewLimiter(signInRatePerSec, SignInBurst) }

// NewTokenLimiter is a bucket for one authorization server's token endpoint.
func NewTokenLimiter() *rate.Limiter { return rate.NewLimiter(tokenRatePerSec, TokenBurst) }

// Limit refuses a request limiter has no room for with 429, then applies Caps.
func Limit(next http.Handler, limiter *rate.Limiter) http.Handler {
	capped := Caps(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		capped.ServeHTTP(w, r)
	})
}

// Caps refuses an oversized query with 414 and caps the request body.
func Caps(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.RawQuery) > MaxQueryBytes {
			http.Error(w, "query too long", http.StatusRequestURITooLong)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}
