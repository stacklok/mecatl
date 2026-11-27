// Package as2trust holds the ID-JAG issuer AS-2 trusts in tests; see
// mcpbroker.AS2Trust.
package as2trust

import (
	"slices"
	"time"
)

// Trust is an ID-JAG issuer AS-2 trusts for the listed subjects only, with
// its keys fetched from JWKSURL, trusting CAFile. AllowPrivateAddresses lets
// that fetch reach a private or loopback address; without it, ToolHive
// refuses a JWKSURL naming one.
type Trust struct {
	Issuer, JWKSURL, CAFile string
	AllowPrivateAddresses   bool
	Subjects                []string
	MaxAssertionAge         time.Duration
	// ExpiredTokenGrace, when positive, replaces the vMCP's grace after a
	// gateway token's exp, so a test can outlive it; it lives here, with the
	// rest of the test-only trust, so production cannot set it.
	ExpiredTokenGrace time.Duration
}

// DefaultMaxAssertionAge is the oldest ID-JAG AS-2 accepts when a Trust
// leaves MaxAssertionAge zero.
const DefaultMaxAssertionAge = 10 * time.Minute

// WithDefaults returns t with its own copy of Subjects and, when
// MaxAssertionAge is zero or negative, DefaultMaxAssertionAge.
func (t Trust) WithDefaults() Trust {
	if t.MaxAssertionAge <= 0 {
		t.MaxAssertionAge = DefaultMaxAssertionAge
	}
	t.Subjects = slices.Clone(t.Subjects)
	return t
}
