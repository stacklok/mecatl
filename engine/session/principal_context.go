package session

import "context"

// principalKey is the unexported context key for the verified caller. An empty
// struct type (not a string) so no other package can collide with or forge it.
type principalKey struct{}

// WithPrincipal returns a context carrying the verified caller p.
//
// A nil p returns ctx UNCHANGED. A principal with unsafe owner-key framing is
// stored only as an invalid shadow, so it cannot leave an outer caller effective
// and PrincipalFromContext reports it as absent. Neither case can produce a
// present-but-empty fabricated principal, and a NUL in either authority-bearing
// component never reaches legacy owner-key framing. Absent identity is a nil
// *Principal, never a fabricated one (ADR 0204 decision 2).
//
// The principal is stored as a COPY, so a later mutation through the caller's
// pointer cannot change what the context reports.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, principalKey{}, p.Clone())
}

// PrincipalFromContext returns the verified caller carried by ctx, or nil when
// there is none. Callers MUST handle nil as "no verified identity" — this
// function never fabricates an anonymous principal.
//
// The returned principal is a COPY: a reader that mutates it cannot change what
// the context reports for every later reader (which would corrupt ownership and
// audit attribution downstream). This is the read half of WithPrincipal's
// copy-on-store promise.
func PrincipalFromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	if !principalIdentityHasSafeFraming(p) {
		return nil
	}
	return p.Clone()
}
