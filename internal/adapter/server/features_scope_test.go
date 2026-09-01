package server

import (
	"reflect"
	"slices"
	"testing"
)

// TestFeatureScopeFieldsAllGateSomething closes the review finding that
// permittedBy's `default: return true` makes the failure mode
// ADVERTISE-EVERYWHERE.
//
// Fail-open is the right default for BUILD facts — a feature with no listener
// dependency genuinely is permitted everywhere, and inverting the default would
// force every build fact to carry a redundant arm. The gap is narrower than the
// default: a new FeatureScope FIELD whose identifier has no matching `case` arm is
// advertised on a deployment that will refuse it, which is precisely the
// dishonesty features.go says the one-place filter exists to prevent. The existing
// registry test pins sorted order and uniqueness, not scope coverage, so nothing
// caught the omission.
//
// This reflects over FeatureScope so the guard cannot go stale: for each bool
// field, setting it false must remove at least one identifier that an all-true
// scope advertises. Adding a field without wiring its arm fails here rather than
// shipping a false advertisement.
func TestFeatureScopeFieldsAllGateSomething(t *testing.T) {
	scopeType := reflect.TypeOf(FeatureScope{})
	if scopeType.NumField() == 0 {
		t.Skip("FeatureScope has no fields yet; nothing to gate")
	}

	// The permissive baseline: every gate open.
	allOpen := reflect.New(scopeType).Elem()
	for i := range scopeType.NumField() {
		f := allOpen.Field(i)
		if f.Kind() != reflect.Bool {
			t.Fatalf("FeatureScope.%s is %s, not bool: this test only understands bool gates, so extend it deliberately rather than letting a new kind go unchecked",
				scopeType.Field(i).Name, f.Kind())
		}
		f.SetBool(true)
	}
	baseline := serverFeatures(allOpen.Interface().(FeatureScope))
	if len(baseline) == 0 {
		t.Fatal("an all-permitting scope advertises nothing; the projection is broken")
	}

	for i := range scopeType.NumField() {
		name := scopeType.Field(i).Name
		t.Run(name, func(t *testing.T) {
			closed := reflect.New(scopeType).Elem()
			for j := range scopeType.NumField() {
				closed.Field(j).SetBool(j != i)
			}
			got := serverFeatures(closed.Interface().(FeatureScope))

			var removed []string
			for _, f := range baseline {
				if !slices.Contains(got, f) {
					removed = append(removed, f)
				}
			}
			if len(removed) == 0 {
				t.Fatalf("setting FeatureScope.%s false removed no feature identifier: it gates nothing, so a deployment that refuses it still advertises it. Add the matching case arm in permittedBy.", name)
			}
			// It must gate its OWN feature, not merely perturb the set.
			for _, f := range got {
				if !slices.Contains(baseline, f) {
					t.Fatalf("closing FeatureScope.%s ADDED %q: a scope field must only ever remove", name, f)
				}
			}
		})
	}
}

// TestAllFeaturesAreAdvertisableAndSorted keeps the registry honest in the other
// direction: every identifier must be reachable under some scope (an entry that no
// scope can advertise is dead), and the list stays sorted so diffs read cleanly.
func TestAllFeaturesAreAdvertisableAndSorted(t *testing.T) {
	if !slices.IsSorted(allFeatures) {
		t.Errorf("allFeatures is not sorted: %v", allFeatures)
	}
	scopeType := reflect.TypeOf(FeatureScope{})
	allOpen := reflect.New(scopeType).Elem()
	for i := range scopeType.NumField() {
		allOpen.Field(i).SetBool(true)
	}
	advertisable := serverFeatures(allOpen.Interface().(FeatureScope))
	for _, f := range allFeatures {
		if !slices.Contains(advertisable, f) {
			t.Errorf("feature %q is never advertised even with every scope gate open: it is unreachable", f)
		}
	}
}
