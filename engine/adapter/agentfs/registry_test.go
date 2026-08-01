package agentfs

import "testing"

func TestRegistryGetListLen(t *testing.T) {
	r := NewRegistry([]AgentDef{
		{Name: "beta", Description: "B"},
		{Name: "alpha", Description: "A"},
	})
	if r.Len() != 2 {
		t.Fatalf("Len = %d, want 2", r.Len())
	}
	if d, ok := r.Get("alpha"); !ok || d.Description != "A" {
		t.Fatalf("Get(alpha) = %+v ok=%v", d, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatalf("Get(missing) should be false")
	}
	list := r.List()
	if len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "beta" {
		t.Fatalf("List not sorted by name: %+v", list)
	}
}

func TestRegistryDuplicateNameKeepsFirst(t *testing.T) {
	r := NewRegistry([]AgentDef{
		{Name: "x", Description: "first"},
		{Name: "x", Description: "second"},
	})
	if r.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (dup collapsed)", r.Len())
	}
	if d, _ := r.Get("x"); d.Description != "first" {
		t.Fatalf("want keep-first, got %q", d.Description)
	}
}

func TestResolveRegistryNilSource(t *testing.T) {
	r, skips, err := ResolveRegistry(t.Context(), nil)
	if err != nil || len(skips) != 0 || r.Len() != 0 {
		t.Fatalf("nil source must yield empty registry, got len=%d skips=%v err=%v", r.Len(), skips, err)
	}
}

func TestResolveRegistryFromSource(t *testing.T) {
	src := staticSource{discovered: []Discovered{{Def: AgentDef{Name: "a", Description: "A"}}}}
	r, _, err := ResolveRegistry(t.Context(), src)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := r.Get("a"); !ok {
		t.Fatalf("expected def 'a' in registry")
	}
}

// TestRegistryDetail pins the NON-PORT detail channel: NewRegistryDiscovered
// retains each def's locator for diagnostics; the one-arg NewRegistry carries
// none ("" for every name).
func TestRegistryDetail(t *testing.T) {
	r := NewRegistryDiscovered([]Discovered{
		{Def: AgentDef{Name: "rev", Description: "R"}, Detail: "explicit: /a/rev.md"},
		{Def: AgentDef{Name: "bare", Description: "B"}},
	})
	if got := r.Detail("rev"); got != "explicit: /a/rev.md" {
		t.Fatalf("Detail(rev) = %q, want the discovered locator", got)
	}
	if got := r.Detail("bare"); got != "" {
		t.Fatalf("Detail(bare) = %q, want \"\"", got)
	}
	if got := r.Detail("ghost"); got != "" {
		t.Fatalf("Detail(ghost) = %q, want \"\"", got)
	}
	if got := NewRegistry([]AgentDef{{Name: "x", Description: "X"}}).Detail("x"); got != "" {
		t.Fatalf("one-arg NewRegistry Detail = %q, want \"\" (no detail channel)", got)
	}
}
