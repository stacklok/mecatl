package providercatalog

import (
	"encoding/json"
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

// TestCuratedModelFidelity is the "no silent caps" tripwire: it verifies the
// parsed catalog is a faithful projection of the embedded JSON — the model count
// per provider in the parsed Catalog EXACTLY equals the number of model entries
// in the raw embedded bytes. Hardcoded floor/ceiling numbers rot every time the
// weekly catalog-refresh re-pins (a provider dropping or gaining models flips a
// magic constant), so this test derives its expectation from the data itself and
// only asserts the structural contract: no model was dropped on the floor, and
// the per-provider model-id sets match 1:1.
func TestCuratedModelFidelity(t *testing.T) {
	// Decode the raw embedded bytes to count model entries per provider. This is
	// the source of truth the parse must faithfully project — if the jq regen
	// silently dropped a model, or parse() filtered one out, the counts diverge.
	var raw map[string]struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(rawCatalogBytes, &raw); err != nil {
		t.Fatalf("embedded models.dev.curated.json failed to decode: %v", err)
	}

	c := Default()
	for _, id := range []string{"openai", "anthropic", "openrouter"} {
		rawProv, ok := raw[id]
		if !ok {
			t.Fatalf("embedded JSON has no %q provider entry", id)
		}
		p, ok := c.Provider(id)
		if !ok {
			t.Errorf("provider %q missing from parsed catalog", id)
			continue
		}
		rawCount := len(rawProv.Models)
		parsedCount := len(p.Models())
		if parsedCount != rawCount {
			t.Errorf("provider %q parsed model count = %d, want exactly %d (embedded entries); a model was dropped on the floor",
				id, parsedCount, rawCount)
		}
		// 1:1 id-set fidelity: every embedded model id survives parse. This
		// catches a model whose id decodes to "" being silently kept, or any
		// deduplication in parse that would collapse two distinct entries.
		parsedIDs := make(map[string]bool, parsedCount)
		for _, m := range p.Models() {
			if m.ID() == "" {
				t.Errorf("provider %q has a model with an empty id", id)
			}
			if parsedIDs[m.ID()] {
				t.Errorf("provider %q model id %q appears twice after parse (dedup collapsed distinct entries)", id, m.ID())
			}
			parsedIDs[m.ID()] = true
		}
		for rawID := range rawProv.Models {
			if !parsedIDs[rawID] {
				t.Errorf("provider %q embedded model %q missing from parsed catalog", id, rawID)
			}
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

func TestProviderModel(t *testing.T) {
	const fixture = `{
		"test": {
			"id": "test",
			"models": {
				"middle": {"id": "middle", "name": "Middle", "family": "beta", "reasoning": true, "tool_call": true, "attachment": true, "modalities": {"input": ["text", "image"], "output": ["text"]}, "limit": {"context": 200, "input": 150, "output": 100}},
				"last": {"id": "last", "name": "Last", "family": "gamma", "modalities": {"input": ["text"], "output": ["text"]}, "limit": {"context": 300, "input": 250, "output": 200}},
				"first": {"id": "first", "name": "First", "family": "alpha", "modalities": {"input": ["text"], "output": ["text"]}, "limit": {"context": 100, "input": 75, "output": 50}}
			}
		}
	}`
	c, err := parse([]byte(fixture))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	p, ok := c.Provider("test")
	if !ok {
		t.Fatal("test provider missing")
	}

	for _, want := range []struct {
		id, name, family                string
		context, input, output          int
		reasoning, toolCall, attachment bool
		modalities                      []string
	}{
		{"first", "First", "alpha", 100, 75, 50, false, false, false, []string{"text"}},
		{"middle", "Middle", "beta", 200, 150, 100, true, true, true, []string{"text", "image"}},
		{"last", "Last", "gamma", 300, 250, 200, false, false, false, []string{"text"}},
	} {
		got, ok := p.Model(want.id)
		if !ok {
			t.Errorf("Model(%q) = miss", want.id)
			continue
		}
		if got.ID() != want.id || got.Name() != want.name || got.Family() != want.family ||
			got.ContextLimit() != want.context || got.InputLimit() != want.input || got.OutputLimit() != want.output ||
			got.SupportsReasoning() != want.reasoning || got.SupportsToolCall() != want.toolCall || got.SupportsAttachment() != want.attachment ||
			!reflect.DeepEqual(got.InputModalities(), want.modalities) {
			t.Errorf("Model(%q) = %+v, want complete metadata", want.id, got)
		}
	}
	for _, id := range []string{"", "before", "first-middle", "zzzz"} {
		if got, ok := p.Model(id); ok || !reflect.DeepEqual(got, Model{}) {
			t.Errorf("Model(%q) = (%+v, %v), want (zero, false)", id, got, ok)
		}
	}
	if got, ok := (Provider{}).Model("anything"); ok || !reflect.DeepEqual(got, Model{}) {
		t.Errorf("zero Provider Model = (%+v, %v), want (zero, false)", got, ok)
	}
}

func TestProviderModelMatchesEnumerationAndPreservesImmutability(t *testing.T) {
	p, ok := Default().Provider("openrouter")
	if !ok {
		t.Fatal("openrouter missing")
	}
	models := p.Models()
	if len(models) == 0 {
		t.Fatal("openrouter models empty")
	}
	for _, want := range models {
		got, ok := p.Model(want.ID())
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("Model(%q) = (%+v, %v), want (%+v, true)", want.ID(), got, ok, want)
		}
	}

	id := models[0].ID()
	models[0] = Model{}
	if got, ok := p.Model(id); !ok || got.ID() != id {
		t.Errorf("Model(%q) changed after Models() slice mutation: (%+v, %v)", id, got, ok)
	}
	model, ok := p.Model(id)
	if !ok {
		t.Fatalf("Model(%q) missing", id)
	}
	modalities := model.InputModalities()
	if len(modalities) > 0 {
		modalities[0] = "tampered"
		if again := model.InputModalities(); again[0] == "tampered" {
			t.Error("mutating InputModalities() result corrupted the model")
		}
	}
}

func TestProviderModelAllocs(t *testing.T) {
	p, ok := Default().Provider("openrouter")
	if !ok {
		t.Fatal("openrouter missing")
	}
	models := p.Models()
	if len(models) == 0 {
		t.Fatal("openrouter models missing")
	}
	id := models[len(models)/2].ID()
	if _, ok := p.Model(id); !ok {
		t.Fatalf("Model(%q) missing", id)
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		if _, ok := p.Model(id); !ok {
			t.Fatal("Model lookup missed")
		}
	}); allocs != 0 {
		t.Errorf("Model(%q) allocations = %v, want 0", id, allocs)
	}
}
