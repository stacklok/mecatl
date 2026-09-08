package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestADR_0291_PlacementReauditInventoriesEphemeralSelectorKey(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "adr", "0027-cloud-native.md"))
	if err != nil {
		t.Fatalf("read ADR 0027: %v", err)
	}
	doc := string(data)

	list1 := sectionBetween(t, doc, "## List 1: resource inventory", "## List 2: rehydrate-fidelity ledger")
	list2 := sectionBetween(t, doc, "## List 2: rehydrate-fidelity ledger", "## List 3: decisions")

	for name, section := range map[string]string{"List 1": list1, "List 2": list2} {
		for _, required := range []string{
			"selector HMAC key",
			"app.Build",
			"reset-by-design",
			"not persisted",
			"relist",
			"no selector registry",
		} {
			if !strings.Contains(section, required) {
				t.Errorf("%s does not inventory %q", name, required)
			}
		}
	}
}

func sectionBetween(t *testing.T, doc, start, end string) string {
	t.Helper()
	startAt := strings.Index(doc, start)
	if startAt < 0 {
		t.Fatalf("missing section %q", start)
	}
	endAt := strings.Index(doc[startAt+len(start):], end)
	if endAt < 0 {
		t.Fatalf("missing section %q after %q", end, start)
	}
	return doc[startAt : startAt+len(start)+endAt]
}
