package app

import (
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestJSONLDurabilityPostureWarning exercises both capability projections directly.
// The sibling construction test pins the real buildStore wiring; forcing weak host
// capabilities still stays here because jsonlstore's syscall hooks are adapter-internal.
func TestJSONLDurabilityPostureWarning(t *testing.T) {
	tests := []struct {
		name       string
		capability jsonlstore.SnapshotDurabilityCapability
		wantLevel  port.Level
	}{
		{
			name:      "weak capabilities warn once",
			wantLevel: port.LevelWarn,
		},
		{
			name: "all capabilities report without warning",
			capability: jsonlstore.SnapshotDurabilityCapability{
				AtomicReplace: true,
				FileSync:      true,
				DirectorySync: true,
			},
			wantLevel: port.LevelInfo,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diag := &captureDiag{}
			logJSONLDurabilityPosture(diag, tc.capability)

			if len(diag.entries) != 1 {
				t.Fatalf("diagnostic entries = %d, want 1", len(diag.entries))
			}
			entry := diag.entries[0]
			if entry.level != tc.wantLevel {
				t.Fatalf("diagnostic level = %v, want %v", entry.level, tc.wantLevel)
			}
			if entry.msg != "jsonl store durability posture" {
				t.Fatalf("diagnostic message = %q", entry.msg)
			}
			if got := diag.warnCount("jsonl store durability posture"); got != boolToInt(tc.wantLevel == port.LevelWarn) {
				t.Fatalf("warning count = %d", got)
			}
		})
	}
}

func TestJSONLDurabilityPostureIsWiredThroughStoreConstruction(t *testing.T) {
	diag := &captureDiag{}
	_, _, closeStore, err := buildStore(Config{StoreDir: t.TempDir(), Diagnostics: diag})
	if err != nil {
		t.Fatalf("buildStore: %v", err)
	}
	defer closeStore()

	var posture []diagEntry
	for _, entry := range diag.entries {
		if entry.msg == "jsonl store durability posture" {
			posture = append(posture, entry)
		}
	}
	if len(posture) != 1 {
		t.Fatalf("durability posture entries = %d, want 1", len(posture))
	}
	args := posture[0].args
	for _, key := range []string{"atomic_replace", "file_sync", "directory_sync", "consequences"} {
		found := false
		for i := 0; i+1 < len(args); i += 2 {
			if args[i] == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("durability posture missing %q: %v", key, args)
		}
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
