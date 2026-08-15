package session

import (
	"errors"
	"strings"
)

// GrantType names how a Principal was authenticated. It is a closed enum of
// exactly three values (ADR 0204 decision 1); the zero value is deliberately
// NOT a member, so an unset grant type is never mistaken for a valid one.
type GrantType string

const (
	// GrantTypeUser is an interactive end user (an OIDC authorization-code flow).
	GrantTypeUser GrantType = "user"
	// GrantTypeClientCredentials is a machine caller (an OAuth2
	// client-credentials flow), e.g. a schedule firing under its captured owner.
	GrantTypeClientCredentials GrantType = "client_credentials"
	// GrantTypeSystem is an internal harness goroutine (childgc, the memory/dream
	// consolidators, the scheduler tick/fire/delivery/reconcile). It is explicit
	// so an internal caller is never an ABSENT principal.
	GrantTypeSystem GrantType = "system"
)

// Valid reports whether g is one of the three defined grant types.
func (g GrantType) Valid() bool {
	switch g {
	case GrantTypeUser, GrantTypeClientCredentials, GrantTypeSystem:
		return true
	default:
		return false
	}
}

// Principal is the verified caller a session or schedule is attributed to
// (ADR 0204 decision 1). Identity is the (Issuer, Subject) PAIR, never Subject
// alone — two IdPs or realms collide on `sub`.
//
// It is a pure value object: comparable, stdlib-only, and deliberately narrow.
// It carries NO scopes, NO authority, NO credentials, and NO claims map — those
// belong to the enforcement tracks, not to attribution. It is modeled on
// ToolHive's PrincipalInfo; ToolHive is not imported.
//
// Absent identity is a nil *Principal, NEVER a fabricated anonymous one (the
// ToolHive anonymous-middleware anti-pattern ADR 0204 rejects).
type Principal struct {
	// Issuer is the IdP that minted the token (the canonical `iss` claim).
	Issuer string
	// Subject is the caller id within that issuer (the `sub` claim).
	Subject string
	// GrantType is how the caller authenticated.
	GrantType GrantType
	// Name is an optional human-readable display label. It is never part of
	// identity — display only.
	Name string
}

// Clone returns a copy of p, or nil when p is nil. It is the ONE place the
// "copy a *Principal across a boundary, nil stays nil" rule lives — every site
// that hands a principal out of, or into, a structure it does not own routes
// through it instead of hand-rolling the nil check and the deref.
//
// Principal is all-strings today, so a shallow copy IS a deep copy. The method
// exists so that the day it gains a slice or map field, every site stays correct
// together rather than silently becoming an aliasing bug.
func (p *Principal) Clone() *Principal {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

// PrincipalFromClaims projects an already-verified token claim set into the
// narrow caller identity used by the engine. It does not verify claims or
// credentials; callers must do that before calling it. Missing, empty, or
// non-string iss/sub claims yield nil. Claim strings are preserved byte-exactly.
func PrincipalFromClaims(claims map[string]any) *Principal {
	issuer, issuerOK := claims["iss"].(string)
	subject, subjectOK := claims["sub"].(string)
	if !issuerOK || issuer == "" || !subjectOK || subject == "" {
		return nil
	}
	name, _ := claims["name"].(string)
	return &Principal{
		Issuer:    issuer,
		Subject:   subject,
		GrantType: GrantTypeFromClaims(claims),
		Name:      name,
	}
}

// GrantTypeFromClaims derives the conservative attribution grant from an
// already-verified token claim set. The first non-empty explicit claim wins
// (`grant_type` before `gty`): a recognised user or client grant is decisive.
// An unknown explicit value is not itself classification evidence, so the
// equal-client-identity fallback may still identify a machine. With no such
// fallback, unknown and malformed signals default to user. It never returns
// system.
func GrantTypeFromClaims(claims map[string]any) GrantType {
	switch normalizeGrantClaim(claimString(claims, "grant_type"), claimString(claims, "gty")) {
	case "client_credentials":
		return GrantTypeClientCredentials
	case "authorization_code", "implicit", "password", "refresh_token", "device_code":
		return GrantTypeUser
	}
	if sub := claimString(claims, "sub"); sub != "" {
		for _, key := range []string{"azp", "client_id", "cid"} {
			if claimString(claims, key) == sub {
				return GrantTypeClientCredentials
			}
		}
	}
	return GrantTypeUser
}

func normalizeGrantClaim(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		value := strings.ToLower(strings.TrimSpace(candidate))
		if i := strings.LastIndex(value, ":"); i >= 0 {
			value = value[i+1:]
		}
		return strings.ReplaceAll(value, "-", "_")
	}
	return ""
}

func claimString(claims map[string]any, key string) string {
	value, _ := claims[key].(string)
	return value
}

// SameIdentity reports whether p and other carry the same verified owner
// identity. Ownership is the exact (Issuer, Subject) pair: metadata such as
// GrantType and Name is deliberately excluded, and issuer spelling is not
// normalized. An absent principal never identifies an owner.
func (p *Principal) SameIdentity(other *Principal) bool {
	return p != nil && other != nil && p.Issuer == other.Issuer && p.Subject == other.Subject
}

// Authority is Track C's placeholder label on the Session aggregate. It is
// INERT in the caller-identity plan: nothing reads or writes it beyond the
// snapshot round-trip. It ships now so the contended engine/api/*.txt
// regeneration and CHANGELOG note are paid once (ADR 0204 consequences).
// The zero value ("") means "unset".
type Authority string

// ErrOwnerAlreadySet is returned by RestoreLabels when the session already
// carries a DIFFERENT owner. The owner is write-once (ADR 0204 decision 4).
var ErrOwnerAlreadySet = errors.New("session: owner already set")

// RestoreLabels stamps the write-once identity labels (Owner, Authority) on the
// aggregate. It is the restore seam sessnap uses — Session is an aggregate, so
// an adapter must not poke the exported fields.
//
// Write-once: a nil owner leaves the label unset (the ownerless / no-auth path,
// and it does NOT burn the slot); re-stamping the SAME owner value is
// idempotent; stamping a DIFFERENT owner over a set one returns
// ErrOwnerAlreadySet rather than silently re-owning the session. The owner is
// stored as a COPY, so the caller cannot mutate a stamped session's owner
// through its own pointer. A zero Authority leaves that label untouched (the
// same additive posture); it is otherwise inert in this plan.
func (s *Session) RestoreLabels(owner *Principal, authority Authority) error {
	if owner != nil {
		if s.Owner != nil && *s.Owner != *owner {
			return ErrOwnerAlreadySet
		}
		s.Owner = owner.Clone()
	}
	if authority != "" {
		s.Authority = authority
	}
	return nil
}
