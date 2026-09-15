package client

import (
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestDraftAwareSessionInventory_Scenario3_FeatureGatedSummaryMapping(t *testing.T) {
	rows := listSessionsFromProto([]*mecatlv1.SessionSummary{
		{SessionId: "draft", ActivityState: "draft"},
		{SessionId: "active", ActivityState: "active"},
		{SessionId: "absent"},
		{SessionId: "malformed", ActivityState: " draft"},
		{SessionId: "future", ActivityState: "paused"},
	})
	want := []SessionActivityState{SessionActivityDraft, SessionActivityActive, SessionActivityUnknown, SessionActivityUnknown, SessionActivityUnknown}
	for i, row := range rows {
		if row.UsageState != want[i] {
			t.Errorf("%s activity = %q, want %q", row.ID, row.UsageState, want[i])
		}
	}
}
