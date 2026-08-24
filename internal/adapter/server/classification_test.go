package server

import (
	"strings"
	"testing"
)

// TestInvariant_owned_access_is_classified pins ADR 0212 decision 2 (AC5.1):
// the classification guard resolves EVERY current designated
// application-facade (*Service), in-memory-registry/event-relay (also
// *Service), cache/index (memory.CallerStore), and shared-system
// (syscaller.Root) boundary to exactly one valid table entry. Model-facing
// tools are guarded at their real composition registration sites in internal/app.
func TestInvariant_owned_access_is_classified(t *testing.T) {
	if errs := ClassifyAllBoundaries(); len(errs) > 0 {
		for _, err := range errs {
			t.Error(err)
		}
	}
}

// TestCallerSeparation_Scenario5_UnclassifiedAccessFailsGuard is the AC5.2
// fixture: it drives the SAME classifyNames function the production guard
// uses (not a copy) over a deliberately incomplete fixture table, and asserts
// the guard reports the exact unclassified boundary by name. This is the
// negative proof that a new owned access path cannot ship unclassified — the
// planted violation goes red here.
func TestCallerSeparation_Scenario5_UnclassifiedAccessFailsGuard(t *testing.T) {
	fixtureTable := map[string]ClassificationEntry{
		"GetWidget": {KindCallerOwned, "authorizes via ownsResource before returning the widget"},
		// "DeleteWidget" is a new owned access path that ships WITHOUT a table
		// entry — the guard must catch it.
	}
	fixtureBoundaries := []string{"GetWidget", "DeleteWidget"}

	report := classifyNames("fixture", fixtureTable, fixtureBoundaries)
	if len(report.Unclassified) != 1 || report.Unclassified[0] != "DeleteWidget" {
		t.Fatalf("Unclassified = %v, want exactly [DeleteWidget]", report.Unclassified)
	}
	errs := report.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `"DeleteWidget"`) {
		t.Fatalf("Errors() = %v, want one error naming DeleteWidget", errs)
	}

	// Sanity: a fully classified fixture is quiet, so the failure above is
	// genuinely caused by the missing entry, not some other defect.
	completeReport := classifyNames("fixture", fixtureTable, []string{"GetWidget"})
	if len(completeReport.Errors()) != 0 {
		t.Fatalf("fully classified fixture reported errors: %v", completeReport.Errors())
	}
}

// TestCallerSeparation_Scenario5_ExemptionsAreExplicitAndNarrow is the AC5.3
// proof: a shared-infrastructure/exempt entry must state a concrete,
// reviewable rationale and must not read as a blanket bypass. It plants both
// violations, watches the guard go red for each, and confirms a properly
// narrow rationale (the shape every real table entry uses) passes.
func TestCallerSeparation_Scenario5_ExemptionsAreExplicitAndNarrow(t *testing.T) {
	cases := []struct {
		name    string
		entry   ClassificationEntry
		wantErr bool
	}{
		{
			name:    "unknown classification kind",
			entry:   ClassificationEntry{Kind: AccessKind(99), Rationale: "authorizeSession"},
			wantErr: true,
		},
		{
			name:    "missing rationale",
			entry:   ClassificationEntry{Kind: KindSharedInfrastructure, Rationale: ""},
			wantErr: true,
		},
		{
			name:    "rationale too short to review",
			entry:   ClassificationEntry{Kind: KindExempt, Rationale: "internal"},
			wantErr: true,
		},
		{
			name:    "blanket bypass phrasing",
			entry:   ClassificationEntry{Kind: KindSharedInfrastructure, Rationale: "always allow — this is trusted infrastructure code"},
			wantErr: true,
		},
		{
			name:    "narrow reviewable rationale passes",
			entry:   ClassificationEntry{Kind: KindSharedInfrastructure, Rationale: "process-wide model catalog read, identical for every caller by design"},
			wantErr: false,
		},
		{
			name:    "caller-owned entries are not held to the length floor",
			entry:   ClassificationEntry{Kind: KindCallerOwned, Rationale: "authorizeSession"},
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.entry.validate()
			if tc.wantErr && err == nil {
				t.Fatalf("validate() = nil, want an error for rationale %q", tc.entry.Rationale)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validate() = %v, want nil for rationale %q", err, tc.entry.Rationale)
			}
		})
	}

	// The real table's own shared-infrastructure/exempt entries must never
	// devolve into a caller-owned bypass: none of them may claim
	// KindCallerOwned while also reading like a rubber stamp. This exercises
	// the PRODUCTION tables (not just the cases above) so a future edit that
	// weakens a real entry's rationale fails here.
	for _, table := range []map[string]ClassificationEntry{
		serviceAccessTable, callerStoreAccessTable,
	} {
		for name, entry := range table {
			if err := entry.validate(); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
	for root, entry := range systemAccessTable {
		if err := entry.validate(); err != nil {
			t.Errorf("%s: %v", root, err)
		}
		if entry.Kind == KindCallerOwned {
			t.Errorf("%s: a system-principal root must never be classified caller-owned (ADR 0212 decision 5: no universal bypass)", root)
		}
	}
}
