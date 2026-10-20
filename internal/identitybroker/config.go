package identitybroker

import (
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxCredentialLifetime bounds Config.CredentialLifetime. Agent credentials are
// short-lived bearer tokens; the bound limits how long a stolen one stays usable
// and rejects unit mistakes such as hours entered for minutes. The value is an
// interim choice: the agent identity design does not set one.
const MaxCredentialLifetime = time.Hour

// maxTrustDomainBytes is the SPIFFE-ID trust domain length limit.
const maxTrustDomainBytes = 255

// Config is the operator-supplied identity configuration. The caller resolves
// it from whatever configuration source it uses; New validates it. Config holds
// only immutable values, so New's copy is independent of the caller's.
type Config struct {
	// TrustDomain is the SPIFFE trust domain name for agent identities, such as
	// "agents.example.com", without the spiffe:// scheme.
	TrustDomain string
	// Audience is the single audience every agent credential names: the
	// broker's, such as "https://broker.example.com". JWT-SVID recommends one
	// audience per token, and the broker's verifiers reject more than one.
	Audience string
	// CredentialLifetime is the validity period of an agent credential. It must
	// be positive and at most MaxCredentialLifetime.
	CredentialLifetime time.Duration
}

func (c Config) validate() error {
	if err := validateTrustDomain(c.TrustDomain); err != nil {
		return err
	}
	if !isToken(c.Audience) {
		return fmt.Errorf("audience %q must be non-empty printable text without spaces", c.Audience)
	}
	if c.CredentialLifetime <= 0 || c.CredentialLifetime > MaxCredentialLifetime {
		return fmt.Errorf("credential lifetime %s must be positive and at most %s", c.CredentialLifetime, MaxCredentialLifetime)
	}
	return nil
}

// validateTrustDomain applies the SPIFFE-ID trust domain grammar: lowercase
// letters, digits, '.', '-' and '_', at most 255 bytes.
func validateTrustDomain(td string) error {
	if td == "" {
		return errors.New("trust domain is required")
	}
	if len(td) > maxTrustDomainBytes {
		return fmt.Errorf("trust domain exceeds %d bytes", maxTrustDomainBytes)
	}
	for _, r := range td {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' && r != '_' {
			return fmt.Errorf("trust domain %q may contain only lowercase letters, digits, '.', '-' and '_'", td)
		}
	}
	return nil
}

// isToken reports whether s is a non-empty, valid UTF-8 string of printable,
// non-space characters. Revisions, the audience, profiles, and metadata keys are
// opaque to the broker but must be unambiguous when compared or recorded, so
// whitespace, control characters, and invisible format characters such as
// zero-width spaces and direction overrides are rejected.
func isToken(s string) bool {
	if s == "" || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == ' ' || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
