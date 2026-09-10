// Package llmendpoint validates host-internal native LLM endpoint identities
// and confines gateway request URLs before credential retrieval.
package llmendpoint

import (
	"errors"
	"net/url"
	"sort"
)

// OIDC identifies the native endpoint's exact authorization-server contract.
type OIDC struct {
	Issuer           string
	ClientID         string
	ResourceAudience string
	Scopes           []string
}

// Trust keeps issuer and gateway trust policies separate.
type Trust struct {
	Policy   string
	CABundle string
}

// ValidIssuer accepts an exact HTTPS issuer identifier without rewriting it.
func ValidIssuer(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && raw != "" && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Opaque == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

// NormalizeScopes validates RFC 6749 scope-token bytes, deduplicates them, and
// returns a lexicographically sorted owned slice.
func NormalizeScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, errors.New("at least one OAuth scope is required")
	}
	set := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		if scope == "" || !validScope(scope) {
			return nil, errors.New("invalid OAuth scope")
		}
		set[scope] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for scope := range set {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out, nil
}

func validScope(scope string) bool {
	for i := 0; i < len(scope); i++ {
		b := scope[i]
		if b != 0x21 && (b < 0x23 || b > 0x5b) && (b < 0x5d || b > 0x7e) {
			return false
		}
	}
	return true
}

// ValidateTrust enforces the closed public/private-ca policy vocabulary.
func ValidateTrust(trust Trust) error {
	switch trust.Policy {
	case "public":
		if trust.CABundle != "" {
			return errors.New("public trust forbids a CA bundle")
		}
	case "private-ca":
		if trust.CABundle == "" {
			return errors.New("private-ca trust requires a CA bundle")
		}
	default:
		return errors.New("unsupported trust policy")
	}
	return nil
}
