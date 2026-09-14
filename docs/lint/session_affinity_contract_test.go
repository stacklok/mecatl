package lint

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// collapseWhitespace normalizes runs of whitespace (including the line breaks
// that Prettier's proseWrap introduces) to a single space, so a needle that
// happens to straddle a reflowed line break still matches.
var whitespaceRun = regexp.MustCompile(`\s+`)

func collapseWhitespace(s string) string {
	return whitespaceRun.ReplaceAllString(s, " ")
}

func TestADR_0294_DocumentationContract(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	requirements := map[string][]string{
		"docs/architecture/deployment-and-hardening.md": {
			"X-Mecatl-Session-ID",
			"routing hint grants no authority",
			"authoritative run context",
			"already-started storage call may still complete",
			"modeled tests do not prove Gateway",
		},
		"docs/design/IMPLEMENTATION-NOTES.md": {
			"SessionMutationCapability",
			"provider ID comes from the run context",
			"local invalidation is not backend fencing",
			"durable `PendingAsk`",
		},
		"user-docs/building/deployment/mecak8s.md": {
			"Existing clients may omit it",
			"Duplicate, malformed",
			"Closing a live running or awaiting session",
			"During shutdown",
			"client retries",
			"authenticated admission",
			"request and header-size bounds",
			"client, IP, and principal rate limits",
			"blocking prerequisite",
			"separate infrastructure PR",
		},
	}

	for name, needles := range requirements {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			normalized := collapseWhitespace(string(body))
			for _, needle := range needles {
				if !strings.Contains(normalized, collapseWhitespace(needle)) {
					t.Errorf("%s does not document %q", name, needle)
				}
			}
		})
	}
}
