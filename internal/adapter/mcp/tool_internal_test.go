package mcp

import (
	"encoding/json"
	"testing"
)

// TestIsTopLevelTypeMismatch is the direct table test for the pure helpers
// behind validateStructuredContent's top-level type-mismatch suppression
// (tool.go ~line 499 / 533). These were previously exercised only indirectly via
// TestMapContentStructuredContentArrayAgainstObjectSchema; this pins every
// branch at the unit level: single-type schema, multi-type Types schema,
// integer/number subsumption (both directions), null/boolean/primitive
// mismatches, the empty-schema (no declared type) pass-through, and the
// json.RawMessage unmarshal arm in jsonInstanceType.
//
// "wantMismatch true" means the validator's top-level type error is a shape
// mismatch and the warning is SUPPRESSED; false means the failure is field-level
// (or there is no top-level type to mismatch against) and the warning SURFACES.
func TestIsTopLevelTypeMismatch(t *testing.T) {
	tests := []struct {
		name          string
		declared      string
		declaredTypes []string
		instance      any
		wantMismatch  bool
	}{
		// 1. array vs object schema → suppressed (the regression at unit level).
		{"array vs object", "object", nil, []any{map[string]any{"id": float64(1)}}, true},
		// 2. primitive (number) vs object schema → suppressed.
		{"number primitive vs object", "object", nil, float64(42.5), true},
		// 3. null vs object schema → suppressed.
		{"null vs object", "object", nil, nil, true},
		// 4. boolean vs object schema → suppressed.
		{"boolean vs object", "object", nil, true, true},
		// 5. object vs object schema (field violation) → NOT suppressed: the
		// instance's top-level type matches the declared type, so the failure is
		// field-level and must surface as a warning.
		{"object vs object (field violation)", "object", nil, map[string]any{"answer": float64(42)}, false},
		// 6. array vs multi-type ["object","array"] → NOT suppressed: the array
		// matches one of the declared types.
		{"array vs multi-type object|array", "", []string{"object", "array"}, []any{float64(1), float64(2)}, false},
		// 7. integer instance (integral float64) vs number schema → NOT
		// suppressed: "number" subsumes "integer".
		{"integral float64 vs number", "number", nil, float64(42), false},
		// 8. non-integral number vs integer schema → SUPPRESSED: the instance
		// type is "number" which does NOT subsume into "integer" (only the
		// reverse), so this is a top-level type mismatch.
		{"non-integral number vs integer", "integer", nil, float64(42.5), true},
		// 9. empty schema (no declared type) → NOT suppressed: with no top-level
		// type to mismatch against, isTopLevelTypeMismatch returns false and any
		// original validation error surfaces.
		{"no declared type", "", nil, float64(42), false},
		// 10. json.RawMessage instance of an array vs object schema →
		// suppressed: exercises the json.RawMessage unmarshal arm in
		// jsonInstanceType (the SDK may pass StructuredContent through raw).
		{"raw message array vs object", "object", nil, json.RawMessage("[1,2]"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isTopLevelTypeMismatch(tc.declared, tc.declaredTypes, tc.instance)
			if got != tc.wantMismatch {
				t.Errorf("isTopLevelTypeMismatch(%q, %v, %T) = %v, want %v",
					tc.declared, tc.declaredTypes, tc.instance, got, tc.wantMismatch)
			}
		})
	}
}

// TestJSONInstanceType pins jsonInstanceType's type-classification arms,
// including the json.RawMessage unmarshal path and the integer/number split for
// float64. These arms are reachable through isTopLevelTypeMismatch but are
// asserted here directly so a regression in the probe is localized.
func TestJSONInstanceType(t *testing.T) {
	tests := []struct {
		name     string
		instance any
		want     string
	}{
		{"nil", nil, jsonTypeNull},
		{"bool", true, jsonTypeBoolean},
		{"string", "hi", jsonTypeString},
		{"object", map[string]any{"a": float64(1)}, jsonTypeObject},
		{"array", []any{float64(1)}, jsonTypeArray},
		{"integral float64", float64(42), jsonTypeInteger},
		{"non-integral float64", float64(42.5), jsonTypeNumber},
		{"int", int(7), jsonTypeInteger},
		{"int32", int32(7), jsonTypeInteger},
		{"int64", int64(7), jsonTypeInteger},
		{"raw message array", json.RawMessage("[1,2]"), jsonTypeArray},
		{"raw message object", json.RawMessage(`{"a":1}`), jsonTypeObject},
		{"raw message number", json.RawMessage("42.5"), jsonTypeNumber},
		{"raw message null", json.RawMessage("null"), jsonTypeNull},
		{"raw message malformed", json.RawMessage("{not json"), ""},
		{"unknown type", struct{}{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := jsonInstanceType(tc.instance)
			if got != tc.want {
				t.Errorf("jsonInstanceType(%T) = %q, want %q", tc.instance, got, tc.want)
			}
		})
	}
}
