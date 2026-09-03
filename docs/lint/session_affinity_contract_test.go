package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestADR_0291_DocumentationContract(t *testing.T) {
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
		"docs/usage/configuration.md": {
			"missing header remains compatible",
			"invalid session affinity metadata",
			"CloseSession",
			"GracefulDrain",
			"client retry",
		},
		"docs/design/IMPLEMENTATION-NOTES.md": {
			"SessionMutationCapability",
			"provider ID comes from the run context",
			"local invalidation is not backend fencing",
			"durable `PendingAsk`",
		},
		"user-docs/building/deployment/mecak8s.md": {
			"authenticated admission",
			"request and header-size bounds",
			"client, IP, and principal rate limits",
			"blocking prerequisite",
			"separate infrastructure PR",
		},
	}

	for name, needles := range requirements {
		name, needles := name, needles
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			for _, needle := range needles {
				if !strings.Contains(string(body), needle) {
					t.Errorf("%s does not document %q", name, needle)
				}
			}
		})
	}
}
