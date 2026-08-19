package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestStorageHealthPanelIsCapabilityDrivenAndContentFree(t *testing.T) {
	health := client.StorageHealth{
		Available: true, CurrentBytesAvailable: true, CurrentBytes: 2048,
		SessionCount: 3, FileCount: 5, V1Count: 1, V2Count: 2,
		MainCount: 1, ChildCount: 1, ScheduledCount: 1,
		Policy: client.RetentionPolicy{MainMaxAge: 24 * time.Hour, MainMaxCount: 4, SweepCadence: time.Hour},
	}
	st := newSessionsPanelState()
	st.loading = false
	st.tab = tabStorageHealth
	st.health = &health
	rendered := stripANSIstr(renderSessionsPanel(testTheme(), st, client.Capabilities{StorageHealth: true}, helpKeys{}, 100, 30))
	for _, want := range []string{"Maintenance", "Current: 2 KB", "Sessions: 3", "Status only"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("panel missing %q:\n%s", want, rendered)
		}
	}
	for _, forbidden := range []string{"session-id", "/secret", "owner@example", "transcript body"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("panel leaked %q:\n%s", forbidden, rendered)
		}
	}
	without := stripANSIstr(renderSessionsPanel(testTheme(), st, client.Capabilities{}, helpKeys{}, 100, 30))
	if strings.Contains(without, "Maintenance") {
		t.Fatalf("capability-disabled panel advertised maintenance:\n%s", without)
	}
}
