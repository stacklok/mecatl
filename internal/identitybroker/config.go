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

// MaxClockSkew bounds Config.ClockSkew. A skew extends every expiry the broker
// checks, an agent credential's and a stored converted access token's, and
// ends every session that much early, so the bound rejects unit mistakes
// such as minutes entered for seconds. It is 60s because AS-1's actor
// validator returns the credential's real exp, and ToolHive answers
// actor_token_expired only for an exp under 60s in the past: a larger skew lets
// the broker accept a credential AS-1 then answers with a server error, so the
// agent never renews it.
const MaxClockSkew = 60 * time.Second

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
	// ClockSkew is how far the broker's clock may differ from another
	// party's, from zero to MaxClockSkew. A verifier accepts an agent
	// credential until its exp plus the skew; the broker counts a session's
	// hard expiry as reached one skew early, so it never presents a grant AS-1
	// may already have expired; and it remembers a converted access token
	// until its exp plus the skew, which must cover the skew AS-1 allows on
	// an access token's exp (none: AS-1 refuses a token once its exp passes).
	ClockSkew time.Duration
}

// Validate checks c as New does, so a configuration loader can refuse a bad
// value before the broker is built. Errors name the field.
func (c Config) Validate() error {
	if err := ValidateTrustDomain(c.TrustDomain); err != nil {
		return err
	}
	if !isToken(c.Audience) {
		return fmt.Errorf("audience %q must be non-empty printable text without spaces", c.Audience)
	}
	if c.CredentialLifetime <= 0 || c.CredentialLifetime > MaxCredentialLifetime {
		return fmt.Errorf("credential lifetime %s must be positive and at most %s", c.CredentialLifetime, MaxCredentialLifetime)
	}
	if c.ClockSkew < 0 || c.ClockSkew > MaxClockSkew {
		return fmt.Errorf("clock skew %s must be between 0s and %s", c.ClockSkew, MaxClockSkew)
	}
	return nil
}

// Bounds of Limits.
const (
	// MinLimit is the least value of every Limits setting.
	MinLimit = time.Second
	// MaxIDJAGMaxLifetime bounds Limits.IDJAGMaxLifetime: ToolHive's largest
	// id_jag_max_lifetime.
	MaxIDJAGMaxLifetime = 720 * time.Hour
	// MaxIDJAGLifetime bounds Limits.IDJAGLifetime: ToolHive's largest ID-JAG
	// lifetime.
	MaxIDJAGLifetime = 5 * time.Minute
	// MaxAS1RequestTimeout bounds Limits.AS1RequestTimeout, and so how long
	// session open holds a host session's open lock.
	MaxAS1RequestTimeout = 30 * time.Second
	// MaxAS2RequestTimeout bounds Limits.AS2RequestTimeout, and so how long a
	// gateway call waits for AS-2.
	MaxAS2RequestTimeout = time.Minute
	// skewsPerIDJAGMaxLifetime is how many clock skews the ID-JAG grant
	// maximum must hold at least: the broker ends each session one skew
	// before its hard expiry, so a skew near the maximum would leave sessions
	// little or no time.
	skewsPerIDJAGMaxLifetime = 10
)

// Limits are the broker's operator-set lifetimes and timeouts that its host
// applies around the core: AS-1's ID-JAG settings, the client-assertion
// signer's lifetime, and the request timeouts of the broker's calls to AS-1
// and AS-2. The configuration loader resolves their defaults; Validate checks
// them. Config holds the lifetimes and skew New applies itself.
type Limits struct {
	// IDJAGMaxLifetime is a hosted AS-1's maximum lifetime for the grants
	// that issue ID-JAGs (ToolHive's id_jag_max_lifetime on the
	// BrokerClientID delegate client), and so every session's hard expiry:
	// whole seconds from 1s to MaxIDJAGMaxLifetime, and at least ten clock
	// skews. It is required with a hosted AS-1 and refused without one.
	IDJAGMaxLifetime time.Duration
	// IDJAGLifetime is the lifetime of each ID-JAG a hosted AS-1 issues for a
	// gateway entry, which also bounds the gateway token's, as AS-2 caps it at
	// the ID-JAG's remaining validity: whole seconds from 1s to
	// MaxIDJAGLifetime. It is required with a hosted AS-1 and refused without
	// one.
	IDJAGLifetime time.Duration
	// ClientAssertionLifetime is the lifetime of each client assertion the
	// broker signs (NewClientAssertionSigner): whole seconds from 1s to
	// MaxClientAssertionLifetime.
	ClientAssertionLifetime time.Duration
	// AS1RequestTimeout bounds one request the broker makes at AS-1, from 1s
	// to MaxAS1RequestTimeout. Session open holds the host session's open
	// lock while it makes at most two, the exchange and one resend, so this
	// bounds how long that lock is held. Once AS-1 re-checks the user at the
	// upstream IdP during an exchange (#2568), it must exceed that re-check's
	// lease wait plus the IdP call.
	AS1RequestTimeout time.Duration
	// AS2RequestTimeout bounds one request the broker makes at a gateway's
	// AS-2, from 1s to MaxAS2RequestTimeout.
	AS2RequestTimeout time.Duration
}

// Validate checks l for a broker with config beside host, so a configuration
// loader can refuse a bad value before anything is built. Errors wrap
// ErrInvalidConfig and name the setting.
func (l Limits) Validate(config Config, host Host) error {
	type bound struct {
		name     string
		value    time.Duration
		max      time.Duration
		whole    bool
		required bool
	}
	bounds := []bound{
		{"client assertion lifetime", l.ClientAssertionLifetime, MaxClientAssertionLifetime, true, true},
		{"AS-1 request timeout", l.AS1RequestTimeout, MaxAS1RequestTimeout, false, true},
		{"AS-2 request timeout", l.AS2RequestTimeout, MaxAS2RequestTimeout, false, true},
		{"ID-JAG grant maximum lifetime", l.IDJAGMaxLifetime, MaxIDJAGMaxLifetime, true, host.AS1},
		{"ID-JAG lifetime", l.IDJAGLifetime, MaxIDJAGLifetime, true, host.AS1},
	}
	for _, b := range bounds {
		switch {
		case !b.required && b.value != 0:
			return fmt.Errorf("%w: the %s requires a hosted AS-1, which this process does not run", ErrInvalidConfig, b.name)
		case !b.required:
		case b.value < MinLimit || b.value > b.max:
			return fmt.Errorf("%w: the %s %s must be between %s and %s", ErrInvalidConfig, b.name, b.value, MinLimit, b.max)
		case b.whole && b.value%time.Second != 0:
			return fmt.Errorf("%w: the %s %s must be whole seconds", ErrInvalidConfig, b.name, b.value)
		}
	}
	if host.AS1 && config.ClockSkew*skewsPerIDJAGMaxLifetime > l.IDJAGMaxLifetime {
		return fmt.Errorf("%w: the clock skew %s must be at most a tenth of the ID-JAG grant maximum lifetime %s: the broker ends every session one skew before its hard expiry",
			ErrInvalidConfig, config.ClockSkew, l.IDJAGMaxLifetime)
	}
	return nil
}

// ValidateTrustDomain applies the SPIFFE-ID trust domain grammar to td, a
// trust domain name without the spiffe:// scheme: lowercase letters, digits,
// '.', '-' and '_', at most 255 bytes.
func ValidateTrustDomain(td string) error {
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
