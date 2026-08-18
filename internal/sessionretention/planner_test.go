package sessionretention

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestInvariant_retention_requires_durable_taxonomy(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	owner := &session.Principal{Issuer: "issuer", Subject: "alice"}
	rows := []port.SessionDiscoveryMeta{
		{ID: "eligible-old", ModifiedAt: now.Add(-48 * time.Hour), State: session.StateCompleted, Kind: session.SessionKindMain, Owner: owner},
		{ID: "eligible-new", ModifiedAt: now.Add(-time.Hour), State: session.StateCompleted, Kind: session.SessionKindMain, Owner: owner},
		{ID: "unknown", ModifiedAt: now.Add(-72 * time.Hour), State: session.StateCompleted, Kind: session.SessionKindUnknown, Owner: owner},
		{ID: "invalid", ModifiedAt: now.Add(-72 * time.Hour), State: session.StateCompleted, Kind: "invented", Owner: owner},
		{ID: "running", ModifiedAt: now.Add(-72 * time.Hour), State: session.StateRunning, Kind: session.SessionKindMain, Owner: owner},
		{ID: "awaiting", ModifiedAt: now.Add(-72 * time.Hour), State: session.StateAwaiting, Kind: session.SessionKindMain, Owner: owner},
		{ID: "live", ModifiedAt: now.Add(-72 * time.Hour), State: session.StateCompleted, Kind: session.SessionKindMain, Owner: owner},
		{ID: "leased", ModifiedAt: now.Add(-72 * time.Hour), State: session.StateCompleted, Kind: session.SessionKindMain, Owner: owner},
	}

	plan := Plan(rows, Policy{MainMaxCount: 1}, Scope{Owner: owner}, RuntimeProtection{
		Live: map[session.SessionID]bool{"live": true}, Leased: map[session.SessionID]bool{"leased": true},
	}, now)
	if len(plan.Eligible) != 1 || plan.Eligible[0].ID != "eligible-old" {
		t.Fatalf("eligible = %+v, want only eligible-old; protected rows must not consume cap slots", plan.Eligible)
	}
	if got := plan.Protected.Total; got != 6 {
		t.Fatalf("protected total = %d, want 6", got)
	}
	for _, id := range []session.SessionID{"unknown", "invalid", "running", "awaiting", "live", "leased"} {
		if plan.ContainsEligible(id) {
			t.Fatalf("protected %q became eligible", id)
		}
	}
}
