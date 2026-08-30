package agent

import (
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestRunBoundsInventoryIsComplete is the issue-#90 structural drift guard: the
// set of run-bound default consts in engine/agent (+ the var-declared session.Limits
// used as one-shot/checker/child defaults) must match the inventory below EXACTLY.
// Add a knob → add a row to the table in docs/design/IMPLEMENTATION-NOTES.md AND a
// row here; change a value → update both; remove one → remove both. This mirrors the
// posture of TestPerSessionCatalogMatchesSharedCatalog
// (internal/app/catalog_drift_test.go) and the DAG layering test
// (engine/arch/layering_test.go): exact-set equality, loud diff on drift. The doc
// table uses `path` (`Symbol`) citations, so docs/lint (CheckCitations) is the
// human-readable second guard — this test is the strong, compile-time-checked one.
//
// The consts live in engine/agent because the layering rule forbids engine/ from
// importing internal/ or os; the composition-root defaults
// (internal/app/runbounds_drift_test.go) have their OWN guard, which additionally
// AST-scans for un-inventoried default* consts (engine/ cannot, since go/build/os
// are banned there). There is no engine/session default-const inventory —
// session.Limits is a domain struct whose defaults are supplied by internal/app
// (deploymentMaxTurns etc.) — so nothing is pinned here for the domain tier.
//
// Every inventoried name is referenced by IDENTIFIER in the switch below, so a
// typo'd or renamed const is a COMPILE ERROR, not just a test failure — that is the
// closed-set guarantee for the names this package owns.
func TestRunBoundsInventoryIsComplete(t *testing.T) {
	t.Parallel()

	// Each row pins a run-bound const/var to its known value. A value change makes
	// the matching assertion fail until the inventory (and the doc table) is updated
	// in lockstep — that is the drift signal.
	type bound struct {
		name string // the const/var identifier, for the failure message
		val  any    // the pinned value
	}
	want := []bound{
		{"defaultNoProgressNudges", 2},
		{"defaultCompactionRatio", 0.8},
		{"compactionTargetRatio", 0.6},
		{"defaultMaxConcurrentChildren", 8},
		{"defaultStructuredOutputRetries", 2},
		{"MinSubagentRunTokens", 25_000},
		{"defaultMaxBranches", 16},
		{"defaultParallelConcurrency", 8},
		{"defaultMaxRounds", 48},
		{"defaultTeamConcurrency", 8},
		{"defaultMemberTurnBudget", 200},
		{"DefaultAskReviewMaxDenies", 3},
		{"defaultModelRouterMaxMisses", 3},
		{"DefaultPreservedForkCap", 8},
	}

	for _, b := range want {
		var got any
		switch b.name {
		case "defaultNoProgressNudges":
			got = defaultNoProgressNudges
		case "defaultCompactionRatio":
			got = defaultCompactionRatio
		case "compactionTargetRatio":
			got = compactionTargetRatio
		case "defaultMaxConcurrentChildren":
			got = defaultMaxConcurrentChildren
		case "defaultStructuredOutputRetries":
			got = defaultStructuredOutputRetries
		case "MinSubagentRunTokens":
			got = MinSubagentRunTokens
		case "defaultMaxBranches":
			got = defaultMaxBranches
		case "defaultParallelConcurrency":
			got = defaultParallelConcurrency
		case "defaultMaxRounds":
			got = defaultMaxRounds
		case "defaultTeamConcurrency":
			got = defaultTeamConcurrency
		case "defaultMemberTurnBudget":
			got = defaultMemberTurnBudget
		case "DefaultAskReviewMaxDenies":
			got = DefaultAskReviewMaxDenies
		case "defaultModelRouterMaxMisses":
			got = defaultModelRouterMaxMisses
		case "DefaultPreservedForkCap":
			got = DefaultPreservedForkCap
		default:
			t.Errorf("inventory references unknown const %q — update the switch", b.name)
			continue
		}
		if got != b.val {
			t.Errorf("run-bound const %q = %v, want %v — update the doc table in docs/design/IMPLEMENTATION-NOTES.md AND this inventory",
				b.name, got, b.val)
		}
	}

	// The var-declared session.Limits used as one-shot/checker/child defaults are
	// pinned by field rather than by identity, so a field drift is caught even
	// though the var itself is not a const.
	type limitsRow struct {
		name   string
		limits session.Limits
	}
	for _, r := range []limitsRow{
		{"defaultChildLimits", defaultChildLimits},
		{"askReviewLimits", askReviewLimits},
		{"guardrailCheckLimits", guardrailCheckLimits},
	} {
		switch r.name {
		case "defaultChildLimits":
			wantChild := session.Limits{MaxTurns: 500, MaxToolCalls: 2000, MaxConsecutiveFailures: 5}
			if r.limits != wantChild {
				t.Errorf("defaultChildLimits = %+v, want %+v — update the doc table AND this inventory", r.limits, wantChild)
			}
		case "askReviewLimits", "guardrailCheckLimits":
			wantOneShot := session.Limits{MaxTurns: 1, MaxToolCalls: 1, MaxConsecutiveFailures: 1}
			if r.limits != wantOneShot {
				t.Errorf("%s = %+v, want %+v (one-shot) — update the doc table AND this inventory", r.name, r.limits, wantOneShot)
			}
		}
	}
}
