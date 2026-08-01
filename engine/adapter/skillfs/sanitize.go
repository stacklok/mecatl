package skillfs

import (
	"regexp"
	"strings"
)

// injectionMarkers is the deny-list of instruction-injection / role-override
// patterns scanned for in a candidate skill's description AND body. A drafted
// skill is UNTRUSTED model output that, if ever promoted, re-enters context as
// instruction-like text; the scan rejects the classic prompt-injection openers
// so a model cannot launder "ignore previous instructions"-style steering into
// the (eventually trusted) skill layer. Matching is case-insensitive and
// whitespace-tolerant.
//
// These are deliberately conservative: they target known override phrasings, not
// arbitrary content. Promotion (the operator gate) re-runs this scan as defense
// in depth.
var injectionMarkers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?(the\s+)?(previous|prior|above|preceding)\s+instructions`),
	regexp.MustCompile(`(?i)disregard\s+(all\s+)?(the\s+)?(previous|prior|above|your|preceding)`),
	regexp.MustCompile(`(?i)forget\s+(all\s+)?(the\s+)?(previous|prior|above|your)\s+instructions`),
	regexp.MustCompile(`(?i)override\s+(your|the)\s+(system|previous|prior)\s+(prompt|instructions)`),
	regexp.MustCompile(`(?i)(^|\n)\s*system\s*:`),
	regexp.MustCompile(`(?i)(^|\n)\s*assistant\s*:`),
	regexp.MustCompile(`(?i)you\s+are\s+now\s+(a|an|the)\b`),
	regexp.MustCompile(`(?i)\bnew\s+(system\s+)?(prompt|instructions)\s*:`),
}

// ScanForInjection scans s for any disallowed instruction-injection / role-
// override marker. It returns the first matched marker text and true on a hit, or
// ("", false) when s is clean. It is exported so both the Drafter (write-time
// gate) and the promote CLI (defense-in-depth at the gate) can reuse the same
// scan, and so it is independently unit-testable.
func ScanForInjection(s string) (marker string, found bool) {
	for _, re := range injectionMarkers {
		if loc := re.FindStringIndex(s); loc != nil {
			return strings.TrimSpace(s[loc[0]:loc[1]]), true
		}
	}
	return "", false
}
