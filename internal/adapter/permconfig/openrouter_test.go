package permconfig

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

// The openrouter: block is OPERATOR-TIER ONLY (issue #480): steering requests to a
// downstream inference provider is a spend/compliance/capability decision the
// operator owns. These tests pin the operator capture, the project-tier WARN-ignore
// (the fail-closed core), and the strict parse. Mirrors posture_test.go /
// guardrails_test.go.

const operatorOpenRouterYAML = `
openrouter:
  models:
    "anthropic/claude-sonnet-4-6":
      order: ["anthropic", "google-vertex"]
      allow_fallbacks: false
    "openai/gpt-5":
      order: ["deepinfra/turbo"]
`

// TestOperatorOpenRouterFromCLIHonoured: an OPERATOR-TIER (CLI explicit) openrouter:
// block is read and returned by OperatorOpenRouter(), parsed faithfully.
func TestOperatorOpenRouterFromCLIHonoured(t *testing.T) {
	env := envWithExplicit("/etc/mecatl/openrouter.yaml", operatorOpenRouterYAML)
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/openrouter.yaml"}}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	sec := r.OperatorOpenRouter()
	if sec == nil {
		t.Fatal("operator-tier openrouter must be honoured from the CLI/explicit tier")
	}
	if len(sec.Models) != 2 {
		t.Fatalf("openrouter.models not parsed faithfully: %+v", sec.Models)
	}
	claude := sec.Models["anthropic/claude-sonnet-4-6"]
	if len(claude.Order) != 2 || claude.Order[0] != "anthropic" || claude.Order[1] != "google-vertex" {
		t.Errorf("claude order = %v, want [anthropic google-vertex]", claude.Order)
	}
	if claude.AllowFallbacks == nil || *claude.AllowFallbacks != false {
		t.Errorf("claude allow_fallbacks = %v, want explicit false", claude.AllowFallbacks)
	}
	gpt := sec.Models["openai/gpt-5"]
	if gpt.AllowFallbacks != nil {
		t.Errorf("gpt allow_fallbacks = %v, want nil (absent ⇒ OpenRouter default true)", gpt.AllowFallbacks)
	}
}

// TestProjectOpenRouterIgnoredWithWarn is the FAIL-CLOSED CORE: a PROJECT-TIER
// openrouter: block must NEVER become the operator routing config, and the resolver
// WARNs naming why (a project repo cannot pick the downstream provider).
func TestProjectOpenRouterIgnoredWithWarn(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, operatorOpenRouterYAML)

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)

	if got := r.OperatorOpenRouter(); got != nil {
		t.Fatalf("a PROJECT-tier openrouter must NOT become the operator config; got %+v", got)
	}
	log := buf.String()
	if !strings.Contains(log, "IGNORING a project-tier openrouter") {
		t.Fatalf("expected an ignore-WARN naming the project tier; got:\n%s", log)
	}
}

// TestOpenRouterStrictParseUnknownKey: an unknown key inside openrouter: (or inside a
// per-model entry) is a strict parse error — a typo must not silently drop the
// routing preferences.
func TestOpenRouterStrictParseUnknownKey(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"unknown top key", "openrouter:\n  moddels:\n    \"m\": {order: [\"anthropic\"]}\n"},
		{"unknown per-model key", "openrouter:\n  models:\n    \"m\": {orderr: [\"anthropic\"]}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := envWithExplicit("/etc/mecatl/or.yaml", tc.yaml)
			r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/or.yaml"}}, env)
			// The file fails to parse, so the resolver drops it: no config captured.
			if r != nil && r.OperatorOpenRouter() != nil {
				t.Fatalf("an unknown key inside openrouter: must be a strict parse error (config dropped); got %+v", r.OperatorOpenRouter())
			}
		})
	}
}
