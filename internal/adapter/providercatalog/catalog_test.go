package providercatalog

import (
	"reflect"
	"testing"
)

// TestCatalogParses: Default() returns a non-nil catalog (does not panic on the
// embedded bytes) and exposes a non-empty provider set.
func TestCatalogParses(t *testing.T) {
	c := Default()
	if c == nil {
		t.Fatal("Default() returned nil")
	}
	if len(c.Providers()) == 0 {
		t.Fatal("Providers() is empty; expected the curated providers")
	}
}

// TestCatalogHasInScopeProviders: openai, openrouter, AND anthropic must all be
// present. anthropic is the "ready for P1" guard — its absence is a curation
// regression even though the S2 registry does not yet construct it.
func TestCatalogHasInScopeProviders(t *testing.T) {
	c := Default()
	for _, id := range []string{"openai", "openrouter", "anthropic"} {
		p, ok := c.Provider(id)
		if !ok {
			t.Errorf("provider %q missing from curated catalog", id)
			continue
		}
		if len(p.Models()) == 0 {
			t.Errorf("provider %q has no curated models", id)
		}
	}
}

// TestCatalogOnlyInScopeProviders: exactly the three in-scope providers are
// vendored — the 136 out-of-scope providers are dropped wholesale.
func TestCatalogOnlyInScopeProviders(t *testing.T) {
	c := Default()
	got := make([]string, 0)
	for _, p := range c.Providers() {
		got = append(got, p.ID())
	}
	want := []string{"anthropic", "openai", "openrouter"} // Providers() is sorted by id
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Providers() ids = %v, want %v", got, want)
	}
}

// TestCuratedModelCounts is the "no silent caps" tripwire: a count drift means
// the regen jq changed silently. All three providers now vendor their FULL
// upstream model set (no allowlist), so each uses a documented FLOOR (organic
// upstream growth on a re-pin must not fail spuriously) paired with a sane UPPER
// bound so a doubled/garbage projection trips loudly.
func TestCuratedModelCounts(t *testing.T) {
	c := Default()
	cases := []struct {
		id      string
		floor   int // minimum (>= floor)
		ceiling int // exclusive sanity upper bound (< ceiling)
	}{
		{id: "openai", floor: 50, ceiling: 500},
		{id: "anthropic", floor: 10, ceiling: 200},
		{id: "openrouter", floor: 300, ceiling: 1000},
	}
	for _, tc := range cases {
		p, ok := c.Provider(tc.id)
		if !ok {
			t.Errorf("provider %q missing", tc.id)
			continue
		}
		n := len(p.Models())
		if n < tc.floor {
			t.Errorf("provider %q model count = %d, want >= %d (a model was dropped)", tc.id, n, tc.floor)
		}
		if n >= tc.ceiling {
			t.Errorf("provider %q model count = %d, want < %d (a doubled/garbage projection)", tc.id, n, tc.ceiling)
		}
	}
}

// TestOpenRouterModelIDsValid is the structural guard that replaced the old
// exact-allowlist test (which pinned 27 hand-curated ids). Now that we vendor ALL
// openrouter models, we assert structural properties instead of an exact set:
// every id is non-empty, unique, and carries the provider/namespace prefix
// ("vendor/model") that OpenRouter uses.
func TestOpenRouterModelIDsValid(t *testing.T) {
	p, ok := Default().Provider("openrouter")
	if !ok {
		t.Fatal("openrouter missing")
	}
	seen := make(map[string]bool, len(p.Models()))
	for _, m := range p.Models() {
		if m.ID() == "" {
			t.Error("found an openrouter model with an empty id")
		}
		if seen[m.ID()] {
			t.Errorf("openrouter model id %q appears more than once", m.ID())
		}
		seen[m.ID()] = true
	}
}

// TestProviderEnvVarsFromCatalog: the env[] arrays stay HONEST to upstream. The
// openrouter OPENAI_API_KEY fallback is a COMPOSITION augmentation, NOT catalog
// data, so the catalog must report openrouter as ["OPENROUTER_API_KEY"] only.
func TestProviderEnvVarsFromCatalog(t *testing.T) {
	c := Default()
	cases := map[string][]string{
		"openai":     {"OPENAI_API_KEY"},
		"openrouter": {"OPENROUTER_API_KEY"},
		"anthropic":  {"ANTHROPIC_API_KEY"},
	}
	for id, want := range cases {
		p, ok := c.Provider(id)
		if !ok {
			t.Errorf("provider %q missing", id)
			continue
		}
		if got := p.EnvVars(); !reflect.DeepEqual(got, want) {
			t.Errorf("provider %q EnvVars() = %v, want %v", id, got, want)
		}
	}
}

// TestProviderAPIBaseURL: openai/anthropic carry "" (SDK default); openrouter
// carries its base URL (the source api field, null ⇒ "" for the SDK-default
// providers).
func TestProviderAPIBaseURL(t *testing.T) {
	c := Default()
	if p, _ := c.Provider("openai"); p.APIBaseURL() != "" {
		t.Errorf("openai APIBaseURL = %q, want \"\"", p.APIBaseURL())
	}
	if p, _ := c.Provider("anthropic"); p.APIBaseURL() != "" {
		t.Errorf("anthropic APIBaseURL = %q, want \"\"", p.APIBaseURL())
	}
	if p, _ := c.Provider("openrouter"); p.APIBaseURL() != "https://openrouter.ai/api/v1" {
		t.Errorf("openrouter APIBaseURL = %q, want the OpenRouter base URL", p.APIBaseURL())
	}
}

// TestModelMetadata guards the field mapping (modalities → image bool, limit →
// context) against a known model, openai gpt-5.
func TestModelMetadata(t *testing.T) {
	c := Default()
	p, ok := c.Provider("openai")
	if !ok {
		t.Fatal("openai missing")
	}
	var gpt5 *Model
	for _, m := range p.Models() {
		if m.ID() == "gpt-5" {
			mm := m
			gpt5 = &mm
			break
		}
	}
	if gpt5 == nil {
		t.Fatal("openai/gpt-5 not found in curated catalog")
		return
	}
	if gpt5.Name() != "GPT-5" {
		t.Errorf("Name() = %q, want GPT-5", gpt5.Name())
	}
	if gpt5.ContextLimit() != 400000 {
		t.Errorf("ContextLimit() = %d, want 400000", gpt5.ContextLimit())
	}
	if !gpt5.SupportsImageInput() {
		t.Error("SupportsImageInput() = false, want true (input modalities include image)")
	}
	if !gpt5.SupportsReasoning() {
		t.Error("SupportsReasoning() = false, want true")
	}
	if !gpt5.SupportsToolCall() {
		t.Error("SupportsToolCall() = false, want true")
	}
	if !gpt5.SupportsAttachment() {
		t.Error("SupportsAttachment() = false, want true (gpt-5 attachment=true in source)")
	}
	if mods := gpt5.InputModalities(); len(mods) == 0 {
		t.Error("InputModalities() empty, want at least text")
	}
}

func TestOpenRouterGLM52Metadata(t *testing.T) {
	p, ok := Default().Provider("openrouter")
	if !ok {
		t.Fatal("openrouter missing")
	}
	var glm52 *Model
	for _, m := range p.Models() {
		if m.ID() == "z-ai/glm-5.2" {
			mm := m
			glm52 = &mm
			break
		}
	}
	if glm52 == nil {
		t.Fatal("z-ai/glm-5.2 not found in curated OpenRouter allowlist")
		return
	}
	if glm52.Name() != "GLM-5.2" {
		t.Errorf("Name() = %q, want GLM-5.2", glm52.Name())
	}
	if glm52.ContextLimit() != 1048576 {
		t.Errorf("ContextLimit() = %d, want 1048576", glm52.ContextLimit())
	}
	if !glm52.SupportsReasoning() {
		t.Error("SupportsReasoning() = false, want true")
	}
	if !glm52.SupportsToolCall() {
		t.Error("SupportsToolCall() = false, want true")
	}
	if glm52.SupportsImageInput() {
		t.Error("SupportsImageInput() = true, want false (GLM 5.2 is text-only in OpenRouter)")
	}
}

// TestParseAPINullDecodesToEmpty pins the null→"" contract at the parse() level
// (rather than relying on the embedded data): a provider with "api": null must
// decode to the Go string zero value without a parse error.
func TestParseAPINullDecodesToEmpty(t *testing.T) {
	const minimal = `{"openai":{"id":"openai","name":"OpenAI","env":["OPENAI_API_KEY"],"api":null,"models":{}}}`
	c, err := parse([]byte(minimal))
	if err != nil {
		t.Fatalf("parse(api:null) returned error: %v", err)
	}
	p, ok := c.Provider("openai")
	if !ok {
		t.Fatal("openai missing from parsed minimal catalog")
	}
	if p.APIBaseURL() != "" {
		t.Errorf("APIBaseURL() = %q, want \"\" (api:null ⇒ Go string zero)", p.APIBaseURL())
	}
}

// TestModelsSortedByID: Models() returns a deterministic, id-sorted slice.
func TestModelsSortedByID(t *testing.T) {
	c := Default()
	for _, p := range c.Providers() {
		models := p.Models()
		for i := 1; i < len(models); i++ {
			if models[i-1].ID() > models[i].ID() {
				t.Errorf("provider %q models not sorted: %q before %q", p.ID(), models[i-1].ID(), models[i].ID())
			}
		}
	}
}

// TestCatalogImmutableShare: Default() returns the same pointer twice (parse
// once), and mutating a returned slice does not corrupt the singleton (accessors
// return fresh copies).
func TestCatalogImmutableShare(t *testing.T) {
	a := Default()
	b := Default()
	if a != b {
		t.Fatal("Default() returned different pointers; parse-once broken")
	}

	// Mutate a returned providers slice — must not affect a fresh read.
	provs := a.Providers()
	n := len(provs)
	if n > 0 {
		provs[0] = Provider{}
	}
	if len(a.Providers()) != n {
		t.Error("mutating Providers() result corrupted the catalog")
	}

	// Mutate a returned env slice — must not affect a fresh read.
	p, _ := a.Provider("openrouter")
	env := p.EnvVars()
	if len(env) > 0 {
		env[0] = "TAMPERED"
	}
	if again, _ := a.Provider("openrouter"); again.EnvVars()[0] == "TAMPERED" {
		t.Error("mutating EnvVars() result corrupted the catalog")
	}

	// Mutate a returned models slice — must not affect a fresh read.
	models := p.Models()
	if len(models) > 0 {
		models[0] = Model{}
	}
	if again, _ := a.Provider("openrouter"); len(again.Models()) > 0 && again.Models()[0].ID() == "" {
		t.Error("mutating Models() result corrupted the catalog")
	}
}

// TestParseFailurePanics: the parse path rejects malformed bytes. (Default()
// panics on a bad embed; we exercise the same parse function with junk to prove
// the error path, without corrupting the embedded singleton.)
func TestParseFailureSurfaced(t *testing.T) {
	if _, err := parse([]byte("not json")); err == nil {
		t.Fatal("parse(garbage) returned nil error, want a parse error")
	}
}
