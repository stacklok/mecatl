// Package as2trust holds the ID-JAG issuer AS-2 trusts in tests; see
// mcpbroker.AS2Trust.
package as2trust

import "time"

// Trust is an ID-JAG issuer AS-2 trusts for the listed subjects only, with
// its keys fetched from JWKSURL over a private address, trusting CAFile.
type Trust struct {
	Issuer, JWKSURL, CAFile string
	Subjects                []string
	MaxAssertionAge         time.Duration
}
