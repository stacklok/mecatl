package session

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/governance"
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
// credentials; callers must do that before calling it. Missing, empty,
// non-string, or NUL-bearing iss/sub claims yield nil. Claim strings are
// otherwise preserved byte-exactly.
func PrincipalFromClaims(claims map[string]any) *Principal {
	issuer, issuerOK := claims["iss"].(string)
	subject, subjectOK := claims["sub"].(string)
	if !issuerOK || !subjectOK {
		return nil
	}
	name, _ := claims["name"].(string)
	principal := &Principal{
		Issuer:    issuer,
		Subject:   subject,
		GrantType: GrantTypeFromClaims(claims),
		Name:      name,
	}
	if issuer == "" || subject == "" || !principalIdentityHasSafeFraming(principal) {
		return nil
	}
	return principal
}

// principalIdentityHasSafeFraming is the single delimiter-safety rule for
// authority-bearing identity components. NUL is reserved as the legacy
// persisted-key separator, so admitting it would make distinct
// (Issuer, Subject) pairs alias. Empty components retain their existing
// non-claims behavior; authenticated claims reject them separately above.
func principalIdentityHasSafeFraming(p *Principal) bool {
	return p != nil && !strings.ContainsRune(p.Issuer, '\x00') && !strings.ContainsRune(p.Subject, '\x00')
}

// IdentityWellFramed reports whether p's authority-bearing components are free
// of the reserved owner-key separator. It is the EXPORTED form of the single
// delimiter-safety rule, so a consumer outside this package — notably the
// request edge's re-validation of an already-verified principal — enforces the
// same rule the owner-key derivations depend on instead of hand-rolling it.
//
// A nil p is not well framed: absent identity is a nil *Principal, and callers
// that admit ownerless operation test for nil themselves rather than routing it
// through here.
func (p *Principal) IdentityWellFramed() bool {
	return principalIdentityHasSafeFraming(p)
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

// invalidPrincipalScope is the domain-separated digest every identity with
// unsafe owner-key framing collapses to. It is hashed from a NUL-free constant,
// and an admissible pair always hashes a string containing exactly one NUL, so
// no valid principal can ever produce this digest.
var invalidPrincipalScope = sha256.Sum256([]byte("mecatl:invalid-principal-scope"))

// PrincipalScopeHash returns the SHA-256 digest of p's (Issuer, Subject)
// identity pair, joined by a NUL separator. NUL is reserved from both
// components, making the legacy framing unambiguous for every admissible
// principal while preserving its byte-for-byte storage contract. The reservation
// is enforced HERE rather than only at the construction seams
// (PrincipalFromClaims, WithPrincipal, RestoreLabels), because a Principal built
// directly from a struct literal — notably internal/adapter/grpcdriver's
// wire-supplied owner, which ADR-0213/#452 still leaves unverified — reaches this
// function without passing any of them. An identity whose framing is unsafe
// returns invalidPrincipalScope, so it cannot alias a valid owner's scope.
//
// The raw hash core has several owner-scope-keying call sites that build on top
// of it with their own nil-handling, prefix, and truncation conventions (which
// are load-bearing for their on-disk/wire formats and must NOT be changed here).
// A nil p hashes the empty string; callers that need a distinct nil
// representation apply that before calling this.
func PrincipalScopeHash(p *Principal) [32]byte {
	if p == nil {
		return sha256.Sum256(nil)
	}
	if !principalIdentityHasSafeFraming(p) {
		return invalidPrincipalScope
	}
	return sha256.Sum256([]byte(p.Issuer + "\x00" + p.Subject))
}

// Authority is the durable, plain authority payload carried by a bound session.
// CapabilitySet is the one governance-domain representation; provenance and
// definition identity are safe labels, not caller claims or runtime handles.
type Authority struct {
	CapabilitySet      governance.CapabilitySet `json:"capability_set"`
	Provenance         string                   `json:"provenance"`
	DefinitionIdentity string                   `json:"definition_identity,omitempty"`
}

// Clone returns an independent copy of a.
func (a Authority) Clone() Authority {
	a.CapabilitySet.Tools = append([]string(nil), a.CapabilitySet.Tools...)
	return a
}

// validAuthorityLabel rejects path-shaped and control-bearing provenance labels.
func validAuthorityLabel(label string) bool {
	if label == "" || len(label) > 256 {
		return false
	}
	for _, r := range label {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == ':' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// Valid reports whether a has safe provenance labels. CapabilitySet is already
// typed, so malformed serialized capability data fails during snapshot decoding
// before this method can bind the aggregate.
func (a Authority) Valid() bool {
	return validAuthorityLabel(a.Provenance) && (a.DefinitionIdentity == "" || validAuthorityLabel(a.DefinitionIdentity))
}

var (
	// ErrOwnerAlreadySet is returned by RestoreLabels when the session already
	// carries a DIFFERENT owner. The owner is write-once (ADR 0204 decision 4).
	ErrOwnerAlreadySet = errors.New("session: owner already set")

	errInvalidPrincipalIdentity = errors.New("session: invalid principal identity")
)

// RestoreLabels stamps the write-once owner label and, when present, restores a
// bound authority payload before the first runnable state.
func (s *Session) RestoreLabels(owner *Principal, authority Authority) error {
	if owner != nil {
		if !principalIdentityHasSafeFraming(owner) {
			return errInvalidPrincipalIdentity
		}
		if s.Owner != nil && *s.Owner != *owner {
			return ErrOwnerAlreadySet
		}
		s.Owner = owner.Clone()
	}
	if authority.Provenance != "" {
		return s.BindAuthority(authority)
	}
	return nil
}

// BindAuthority attaches a derived authority payload before the session becomes
// runnable. It is write-once and copies its capability set so callers cannot mutate it.
func (s *Session) BindAuthority(authority Authority) error {
	if s.State != StateIdle {
		return fmt.Errorf("%w: BindAuthority from %q", ErrIllegalTransition, s.State)
	}
	if s.authorityBound {
		return errors.New("session: authority already bound")
	}
	if !authority.Valid() {
		return errors.New("session: invalid authority payload")
	}
	s.Authority = authority.Clone()
	s.authorityBound = true
	return nil
}

// BoundAuthority returns the copied durable authority payload and whether this
// session was explicitly bound. An absent payload is a documented pre-feature
// legacy session, never an empty bound set.
func (s *Session) BoundAuthority() (Authority, bool) {
	if !s.authorityBound {
		return Authority{}, false
	}
	return s.Authority.Clone(), true
}
