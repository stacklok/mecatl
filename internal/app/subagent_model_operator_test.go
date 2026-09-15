package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// These tests close issue #288: the operator-tier `models.subagent:` settings.yaml key,
// the def-less child-default model selector — the YAML twin of --subagent-model. The fold
// (foldOperatorSubagentModel) sets Config.SubagentModel from the operator YAML unless the
// CLI flag was set (CLI wins), then normalizeSubagentModel validates it fail-fast exactly
// like the flag.

// writeOperatorConfig writes an operator-tier settings.yaml and returns a resolver over it
// (ExplicitFiles = the CLI/operator tier, per the permconfig contract).
func writeOperatorConfig(t *testing.T, yaml string) *permconfig.Resolver {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	if res == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	return res
}

// TestFoldOperatorSubagentModelFromYAML: an operator-tier models.subagent selector folds
// onto Config.SubagentModel VERBATIM, and the def-less child chain resolves it onto the
// concrete id through the (operator-merged) alias map.
func TestFoldOperatorSubagentModelFromYAML(t *testing.T) {
	res := writeOperatorConfig(t, "models:\n  subagent: coder\n")

	// The alias map the child chain resolves `coder` through (in Build this is folded by
	// foldOperatorModelSlots; here we set it directly to isolate the subagent fold).
	cfg := Config{
		Model:        "parent-model",
		permResolver: res,
		ModelAliases: map[string]string{"coder": "gpt-4o-mini"},
	}
	cliKeys := captureCLIModelKeys(cfg) // no CLI --subagent-model set
	cfg = foldOperatorSubagentModel(cfg, cliKeys)

	if cfg.SubagentModel != "coder" {
		t.Fatalf("cfg.SubagentModel = %q, want the YAML selector kept verbatim (%q)", cfg.SubagentModel, "coder")
	}
	// The def-less child chain resolves the folded alias to the concrete id.
	model, _ := resolveDefaultChildModel(cfg, nil, providerMock, "parent-model")
	if model != "gpt-4o-mini" {
		t.Fatalf("resolveDefaultChildModel over the folded alias = %q, want the mapped concrete id %q", model, "gpt-4o-mini")
	}
}

// TestFoldOperatorSubagentModelCLIWins: a CLI --subagent-model (a non-empty SubagentModel
// at the capture point) beats the operator YAML — the fold is skipped.
func TestFoldOperatorSubagentModelCLIWins(t *testing.T) {
	res := writeOperatorConfig(t, "models:\n  subagent: coder\n")

	cfg := Config{
		Model:         "parent-model",
		permResolver:  res,
		SubagentModel: "cli-model-1.0", // CLI flag preset
	}
	cliKeys := captureCLIModelKeys(cfg) // subagentModelSet = true
	cfg = foldOperatorSubagentModel(cfg, cliKeys)

	if cfg.SubagentModel != "cli-model-1.0" {
		t.Fatalf("cfg.SubagentModel = %q, want the CLI value to WIN over the YAML (%q)", cfg.SubagentModel, "cli-model-1.0")
	}
}

// TestFoldOperatorSubagentModelAbsentNoOp: no operator models.subagent (or no models:
// block, or no resolver) leaves cfg byte-identical.
func TestFoldOperatorSubagentModelAbsentNoOp(t *testing.T) {
	// (a) No resolver.
	if got := foldOperatorSubagentModel(Config{Model: "m"}, cliModelKeys{}); got.SubagentModel != "" {
		t.Fatalf("with no resolver the fold must be a no-op; SubagentModel=%q", got.SubagentModel)
	}
	// (b) A resolver but a models: block WITHOUT subagent.
	res := writeOperatorConfig(t, "models:\n  default: some-model\n")
	cfg := Config{Model: "m", permResolver: res}
	if got := foldOperatorSubagentModel(cfg, captureCLIModelKeys(cfg)); got.SubagentModel != "" {
		t.Fatalf("a models: block with no subagent: must fold nothing; SubagentModel=%q", got.SubagentModel)
	}
}

// TestBuildFailsOnUnresolvableYAMLSubagentModel is the ADVERSARIAL production-wiring proof:
// an operator YAML models.subagent that does not resolve (a built-in inherit alias) FAILS
// Build fast — the SAME fail-fast path as the flag, DELIBERATELY (unlike fail-soft
// models.default) — and the error names models.subagent.
func TestBuildFailsOnUnresolvableYAMLSubagentModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte("models:\n  subagent: sonnet\n"), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace:         t.TempDir(),
		Model:             "mock",
		UseMock:           true,
		PermissionConfigs: []string{path},
	})
	if err == nil {
		built.Close()
		t.Fatal("Build(models.subagent=sonnet) succeeded, want a fail-fast startup error (a dead YAML selector must fail startup like the flag)")
	}
	if !strings.Contains(err.Error(), "models.subagent") {
		t.Errorf("Build error must name models.subagent, got: %v", err)
	}
	if !strings.Contains(err.Error(), "sonnet") {
		t.Errorf("Build error must name the given value %q, got: %v", "sonnet", err)
	}
}
