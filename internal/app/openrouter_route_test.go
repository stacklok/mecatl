package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/provider/openai"
)

// foldOpenRouter writes an operator-tier openrouter: YAML file and returns a Config
// whose permResolver carries it (the fold's input seam).
func foldOpenRouter(t *testing.T, yaml string) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openrouter.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	if res == nil {
		t.Fatal("resolver should be non-nil")
	}
	return Config{permResolver: res}
}

// TestFoldOperatorOpenRouter pins the operator-tier fold (issue #480): a valid
// openrouter: block resolves into cfg.openRouterRoutes, keyed by model id, with
// allow_fallbacks preserved as a pointer (absent ⇒ nil ⇒ OpenRouter default true).
func TestFoldOperatorOpenRouter(t *testing.T) {
	cfg := foldOpenRouter(t, `
openrouter:
  models:
    "anthropic/claude-sonnet-4-6":
      order: ["Anthropic", "Google-Vertex"]
      allow_fallbacks: false
    "openai/gpt-5":
      order: ["deepinfra/turbo"]
`)
	got := foldOperatorOpenRouter(cfg)
	if len(got.openRouterRoutes) != 2 {
		t.Fatalf("openRouterRoutes = %v, want 2 models", got.openRouterRoutes)
	}
	claude := got.openRouterRoutes["anthropic/claude-sonnet-4-6"]
	// Slugs are normalised to lowercase-kebab.
	if len(claude.Order) != 2 || claude.Order[0] != "anthropic" || claude.Order[1] != "google-vertex" {
		t.Errorf("claude order = %v, want [anthropic google-vertex] (normalised)", claude.Order)
	}
	if claude.AllowFallbacks == nil || *claude.AllowFallbacks != false {
		t.Errorf("claude allow_fallbacks = %v, want explicit false", claude.AllowFallbacks)
	}
	gpt := got.openRouterRoutes["openai/gpt-5"]
	if gpt.AllowFallbacks != nil {
		t.Errorf("gpt allow_fallbacks = %v, want nil (absent)", gpt.AllowFallbacks)
	}
}

// TestFoldOperatorOpenRouterNoResolver: a nil / absent block is a no-op.
func TestFoldOperatorOpenRouterNoResolver(t *testing.T) {
	if got := foldOperatorOpenRouter(Config{}); got.openRouterRoutes != nil {
		t.Errorf("nil resolver must be a no-op; got %v", got.openRouterRoutes)
	}
	// An operator file with NO openrouter: block.
	cfg := foldOpenRouter(t, "posture: auto\n")
	if got := foldOperatorOpenRouter(cfg); got.openRouterRoutes != nil {
		t.Errorf("absent openrouter: block must be a no-op; got %v", got.openRouterRoutes)
	}
}

// TestFoldOperatorOpenRouterResolvesAlias pins the alias-keyed route fix (issue #480
// review): a route keyed by an OPERATOR alias must land under the RESOLVED concrete
// model id (the value req.Model carries), not the alias string; an unknown bare token
// (no "/" and none of the id-shape chars) is dropped fail-soft.
func TestFoldOperatorOpenRouterResolvesAlias(t *testing.T) {
	cfg := foldOpenRouter(t, `
models:
  aliases:
    fast-claude: anthropic/claude-sonnet-4-6
openrouter:
  models:
    "fast-claude":
      order: ["anthropic"]
    "sonnetinvented":
      order: ["anthropic"]
`)
	// Match Build's load-bearing order: operator aliases must fold before OpenRouter
	// route keys are resolved, so aliases declared in this same file are available.
	cfg = foldOperatorModelSlots(cfg)
	got := foldOperatorOpenRouter(cfg)
	if _, ok := got.openRouterRoutes["anthropic/claude-sonnet-4-6"]; !ok {
		t.Errorf("alias-keyed route must land under the resolved model id; got %v", got.openRouterRoutes)
	}
	// lookupModelAlias: "sonnetinvented" has no "/-.:" or space and matches no alias ⇒
	// known=false ⇒ dropped fail-soft (it can never be a concrete model id).
	if _, ok := got.openRouterRoutes["sonnetinvented"]; ok {
		t.Errorf("an unknown bare-token key must be dropped fail-soft; got %v", got.openRouterRoutes)
	}
}

// TestFoldOperatorOpenRouterDropsResolvedKeyConflicts pins deterministic handling
// when an alias and concrete key name the same model. A compliance route must not
// depend on Go map iteration order, so every conflicting route is dropped.
func TestFoldOperatorOpenRouterDropsResolvedKeyConflicts(t *testing.T) {
	cfg := foldOpenRouter(t, `
models:
  aliases:
    fast-claude: anthropic/claude-sonnet-4-6
openrouter:
  models:
    fast-claude:
      order: ["anthropic"]
      allow_fallbacks: false
    anthropic/claude-sonnet-4-6:
      order: ["google-vertex"]
`)
	cfg = foldOperatorModelSlots(cfg)
	got := foldOperatorOpenRouter(cfg)
	if _, ok := got.openRouterRoutes["anthropic/claude-sonnet-4-6"]; ok {
		t.Fatalf("conflicting resolved routes must be dropped, got %v", got.openRouterRoutes)
	}
}

// TestFoldOperatorOpenRouterDropsInvalid pins the fail-soft drop discipline: an
// empty order, an empty model id, or an invalid slug is WARN-dropped while the
// valid entries survive.
func TestFoldOperatorOpenRouterDropsInvalid(t *testing.T) {
	cfg := foldOpenRouter(t, `
openrouter:
  models:
    "good/model":
      order: ["anthropic"]
    "empty-order/model":
      order: []
    "bad-slug/model":
      order: ["Anthropic_Bad"]
    "":
      order: ["anthropic"]
`)
	got := foldOperatorOpenRouter(cfg)
	if len(got.openRouterRoutes) != 1 {
		t.Fatalf("invalid entries must be dropped, valid kept: got %v", got.openRouterRoutes)
	}
	if _, ok := got.openRouterRoutes["good/model"]; !ok {
		t.Errorf("the valid entry must survive; got %v", got.openRouterRoutes)
	}
}

// TestOpenRouterRouteFor pins the per-model resolver the registry entry's
// WithOpenRouterProviderPreferences closure calls: a configured model converts to
// openai.OpenRouterProviderPreferences, an unconfigured model returns nil (no body key).
func TestOpenRouterRouteFor(t *testing.T) {
	allow := false
	cfg := Config{openRouterRoutes: map[string]openai.OpenRouterProviderPreferences{
		"anthropic/claude-sonnet-4-6": {Order: []string{"anthropic"}, AllowFallbacks: &allow},
	}}
	prefs := cfg.openRouterRouteFor("anthropic/claude-sonnet-4-6")
	if prefs == nil {
		t.Fatal("a configured model must resolve to ProviderPreferences")
	}
	if len(prefs.Order) != 1 || prefs.Order[0] != "anthropic" {
		t.Errorf("prefs.Order = %v, want [anthropic]", prefs.Order)
	}
	if prefs.AllowFallbacks == nil || *prefs.AllowFallbacks != false {
		t.Errorf("prefs.AllowFallbacks = %v, want explicit false", prefs.AllowFallbacks)
	}
	if got := cfg.openRouterRouteFor("openai/gpt-5"); got != nil {
		t.Errorf("an unconfigured model must resolve to nil (no body key); got %+v", got)
	}
	// nil-map Config is nil-safe.
	var zero Config
	if got := zero.openRouterRouteFor("m"); got != nil {
		t.Errorf("nil openRouterRoutes must be nil-safe; got %+v", got)
	}
}
