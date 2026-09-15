package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// writeOperatorSettingsFile writes an operator-tier settings.yaml carrying the given
// body to a temp file and returns its path, for use as a Config.PermissionConfigs
// entry (the explicit CLI tier — an OPERATOR tier, so its keys are honoured). Mirrors
// posture_build_test.go's writeOperatorPostureFile.
func writeOperatorSettingsFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "operator.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write operator settings file: %v", err)
	}
	return path
}

// buildWithToolhiveAndKey builds the REAL app.Build offline with an OPENROUTER key
// (so openrouter is a keyed, key-driven provider — the ladder's default absent an
// override) AND a ToolHive LLM gateway entry (registered + probed via the models
// fixture), returning the build error (the caller closes a successful build). The
// operator settings.yaml is supplied via PermissionConfigs (the operator explicit
// tier). Without a default_provider override the ladder prefers openrouter (keyed);
// the YAML override is what routes zero-selector sessions to toolhive.
func buildWithToolhiveAndKey(t *testing.T, operatorSettingsPath string, diag port.Diagnostics) (*Built, error) {
	t.Helper()
	return buildIsolated(t, context.Background(), Config{
		Workspace: t.TempDir(),
		NoSoul:    true,
		// An openrouter key makes openrouter a keyed, key-driven provider — the
		// ladder's default when no override is set. toolhive is registered below.
		envDetector: fakeEnv(map[string]string{
			"OPENROUTER_API_KEY": "sk-x",
		}),
		// ToolHive LLM gateway auto-detect (issue #262): registered + probed via
		// the offline models fixture, so "toolhive" is an AVAILABLE provider.
		ToolhiveLLM:         true,
		toolhiveConfigPath:  writeToolhiveConfig(t, "https://upstream.example/gw"),
		liveModelHTTPClient: toolhiveModelsClient(t, toolhiveFixtureJSON),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("ok"))
		},
		PermissionConfigs: []string{operatorSettingsPath},
		Diagnostics:       diag,
	})
}

// TestBuildOperatorYAMLDefaultProviderSeam is the HEADLINE Wave 2b seam guard: it
// drives the REAL app.Build (offline) and proves the operator-tier settings.yaml
// `models.default_provider: toolhive` key — with an OPENROUTER key present — flows
// all the way to the composed default provider, OVERRIDING the ladder (which would
// otherwise prefer the keyed openrouter). The ladder is UNCHANGED; this is an
// explicit operator override feeding cfg.DefaultProvider.
func TestBuildOperatorYAMLDefaultProviderSeam(t *testing.T) {
	ctx := context.Background()
	settings := writeOperatorSettingsFile(t, "models:\n  default_provider: toolhive\n")
	built, err := buildWithToolhiveAndKey(t, settings, port.NopDiagnostics{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	// A zero-selector session inherits the deployment default: with the YAML
	// override ACTIVE, the resolved provider is toolhive (NOT openrouter, the
	// keyed ladder winner absent the override).
	sess, err := svc.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession(zero-selector): %v", err)
	}
	got := svc.ResolvedModel(sess.ID)
	if got.ProviderID != providerToolhive {
		t.Fatalf("operator-YAML models.default_provider: toolhive must override the keyed ladder default; ResolvedModel.ProviderID = %q, want %q", got.ProviderID, providerToolhive)
	}
}

// TestBuildOperatorYAMLDefaultProviderAbsentKeepsLadderDefault pins the no-op: with
// NO models.default_provider key (and the same openrouter-keyed + toolhive setup),
// the ladder's preferred default (openrouter, the keyed provider) wins — the fold is
// byte-identical when the key is absent, and toolhive is NOT silently elevated.
func TestBuildOperatorYAMLDefaultProviderAbsentKeepsLadderDefault(t *testing.T) {
	ctx := context.Background()
	// An operator settings.yaml with a models: block but NO default_provider key.
	settings := writeOperatorSettingsFile(t, "models:\n  default: sonnet\n")
	built, err := buildWithToolhiveAndKey(t, settings, port.NopDiagnostics{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	sess, err := svc.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession(zero-selector): %v", err)
	}
	got := svc.ResolvedModel(sess.ID)
	if got.ProviderID != providerOpenRouter {
		t.Fatalf("with no models.default_provider the keyed ladder default must win; ResolvedModel.ProviderID = %q, want %q (openrouter, the keyed provider)", got.ProviderID, providerOpenRouter)
	}
}

// TestBuildOperatorYAMLDefaultProviderCLIWins pins the CLI-wins precedence end-to-end:
// an explicit --default-provider (DefaultProviderFlagSet) OUT-RANKS the YAML value, so
// the YAML toolhive override is NOT applied and the CLI-named provider wins instead.
func TestBuildOperatorYAMLDefaultProviderCLIWins(t *testing.T) {
	ctx := context.Background()
	settings := writeOperatorSettingsFile(t, "models:\n  default_provider: toolhive\n")
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace: t.TempDir(),
		NoSoul:    true,
		envDetector: fakeEnv(map[string]string{
			"OPENROUTER_API_KEY": "sk-x",
		}),
		ToolhiveLLM:         true,
		toolhiveConfigPath:  writeToolhiveConfig(t, "https://upstream.example/gw"),
		liveModelHTTPClient: toolhiveModelsClient(t, toolhiveFixtureJSON),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("ok"))
		},
		PermissionConfigs: []string{settings},
		// CLI wins: an explicit --default-provider=openrouter OUT-RANKS the YAML
		// toolhive value.
		DefaultProvider:        "openrouter",
		DefaultProviderFlagSet: true,
		Diagnostics:            port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	sess, err := svc.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession(zero-selector): %v", err)
	}
	got := svc.ResolvedModel(sess.ID)
	if got.ProviderID != providerOpenRouter {
		t.Fatalf("CLI --default-provider must out-rank the YAML value; ResolvedModel.ProviderID = %q, want %q (openrouter, the CLI value)", got.ProviderID, providerOpenRouter)
	}
}

// TestBuildOperatorYAMLDefaultProviderUnknownFailsFast pins the fail-fast gate: an
// operator-YAML models.default_provider naming an UNKNOWN/unavailable provider is a
// startup error (validateDefaultModel), surfacing the value — never a silent no-op.
func TestBuildOperatorYAMLDefaultProviderUnknownFailsFast(t *testing.T) {
	settings := writeOperatorSettingsFile(t, "models:\n  default_provider: no-such-provider\n")
	_, err := buildWithToolhiveAndKey(t, settings, port.NopDiagnostics{})
	if err == nil {
		t.Fatal("Build with operator-YAML models.default_provider: no-such-provider succeeded, want a fail-fast startup error")
	}
	if !strings.Contains(err.Error(), "no-such-provider") {
		t.Errorf("error must name the configured provider value; got: %v", err)
	}
}
