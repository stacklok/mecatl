package agent

import (
	"strings"

	"github.com/stacklok/mecatl/engine/governance"
)

// UntrustedFence is the delimiter wrapping an untrusted block in a model-visible prompt.
//
// Deprecated: use governance.UntrustedFence.
const UntrustedFence = governance.UntrustedFence

// WriteUntrustedBlock writes body wrapped in an untrusted-content fence.
//
// Deprecated: use governance.WriteUntrustedBlock.
func WriteUntrustedBlock(b *strings.Builder, body string) {
	governance.WriteUntrustedBlock(b, body)
}

// FenceUntrusted returns body wrapped in an untrusted-content fence.
//
// Deprecated: use governance.FenceUntrusted.
func FenceUntrusted(body string) string {
	return governance.FenceUntrusted(body)
}

// NeutraliseFraming defangs model-visible harness framing in s.
//
// Deprecated: use governance.NeutraliseFraming.
func NeutraliseFraming(s string) string {
	return governance.NeutraliseFraming(s)
}

func neutraliseChildText(s string) string {
	return governance.NeutraliseDelegationResult(s)
}

// StripLoneCodeFence removes a single surrounding ```…``` fence (optionally
// language-tagged) from s, returning the inner text trimmed; if s is not a lone
// fenced block it is returned unchanged.
func StripLoneCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") || !strings.HasSuffix(s, "```") || len(s) < 6 {
		return s
	}
	inner := s[3 : len(s)-3]
	if nl := strings.IndexByte(inner, '\n'); nl >= 0 {
		first := strings.TrimSpace(inner[:nl])
		if first == "" || !strings.ContainsAny(first, " \t{}\"") {
			inner = inner[nl+1:]
		}
	}
	return strings.TrimSpace(inner)
}
