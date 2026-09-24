package app

import (
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestHarnessReplaceUsesPostExclusionNonemptiness(t *testing.T) {
	cfg := permconfig.HarnessContextKind{Sources: []string{"a", "b"}, Mode: "replace", Exclude: []permconfig.HarnessContextExclude{{Source: "a", Name: "x"}}}
	ids := map[HarnessSourceID]struct{}{"a": {}, "b": {}}
	policy, err := compileHarnessKind("commands", cfg, ids, ids, true)
	if err != nil {
		t.Fatal(err)
	}
	binding := &resolvedCommandBinding{policy: policy, sources: []boundCommandSource{{id: "a", binding: &hcCommands{values: map[string]string{"x": "excluded"}}}, {id: "b", binding: &hcCommands{values: map[string]string{"x": "visible", "other": "second"}}}}}
	listed, err := binding.List(t.Context())
	if err != nil || len(listed) != 2 {
		t.Fatalf("post-exclusion replace list=%v,%v", listed, err)
	}
	out, ok, err := binding.Expand(t.Context(), "/x")
	if err != nil || !ok || out != "visible" {
		t.Fatalf("replace expand=%q,%v,%v", out, ok, err)
	}
}
