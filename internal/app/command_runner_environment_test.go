package app

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

func commandRunnerEnv(t *testing.T, runner interface {
	Run(context.Context, string) (tool.CommandResult, error)
}) string {
	t.Helper()
	result, err := runner.Run(context.Background(), "env")
	if err != nil {
		t.Fatalf("run env: %v", err)
	}
	return result.Stdout
}

func TestCommandRunnerEnvironment_Scenario2_DefaultScrub(t *testing.T) {
	t.Setenv("GH_TOKEN", "default-secret")
	t.Setenv("PATH", "/bin")
	cfg := teamCfg(t)
	for name, runner := range map[string]tool.CommandRunner{
		"main":                buildCommandRunner(cfg),
		"alternate placement": buildCommandRunnerForRoot(cfg, t.TempDir()),
	} {
		env := commandRunnerEnv(t, runner)
		if strings.Contains(env, "default-secret") || !strings.Contains(env, "PATH=/bin") {
			t.Errorf("%s environment did not preserve default scrub: %s", name, env)
		}
	}
}

func TestCommandRunnerEnvironment_Scenario2_MainGrant(t *testing.T) {
	t.Setenv("GH_TOKEN", "operator-granted-secret")
	cfg := teamCfg(t)
	cfg.commandEnvironmentInherit = []string{"GH_TOKEN"}
	for name, runner := range map[string]tool.CommandRunner{
		"main":                buildCommandRunner(cfg),
		"alternate placement": buildCommandRunnerForRoot(cfg, t.TempDir()),
	} {
		env := commandRunnerEnv(t, runner)
		if !strings.Contains(env, "GH_TOKEN=operator-granted-secret") {
			t.Errorf("%s runner omitted deliberate grant: %s", name, env)
		}
	}

	customRoot := t.TempDir()
	customRunner, err := osfs.NewCommandRunnerShell(customRoot, "/bin/sh", osfs.WithCommandEnvList([]string{"GH_TOKEN=custom-provider-value"}))
	if err != nil {
		t.Fatal(err)
	}
	provider := appTestPlacementProvider{root: customRoot, runner: customRunner}
	binding, err := provider.Bind(context.Background(), server.PlacementBindRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if env := commandRunnerEnv(t, binding.Environment.CommandRunner()); !strings.Contains(env, "GH_TOKEN=custom-provider-value") || strings.Contains(env, "operator-granted-secret") {
		t.Fatalf("custom placement environment was rewritten: %s", env)
	}
}

func TestCommandRunnerEnvironment_Scenario2_NonOverridableAndAbsent(t *testing.T) {
	t.Setenv("GH_TOKEN", "allowed")
	t.Setenv("OPENAI_API_KEY", "harness-secret")
	t.Setenv("DYNAMIC_TOKEN", "dynamic-secret")
	t.Setenv("OTHER_TOKEN", "unconfigured-secret")
	cfg := teamCfg(t)
	cfg.commandEnvironmentInherit = []string{"GH_TOKEN", "OPENAI_API_KEY", "DYNAMIC_TOKEN", "ABSENT_TOKEN"}
	cfg.commandEnvironmentReserved = map[string]struct{}{"OPENAI_API_KEY": {}, "DYNAMIC_TOKEN": {}}
	env := commandRunnerEnv(t, buildCommandRunner(cfg))
	if !strings.Contains(env, "GH_TOKEN=allowed") {
		t.Fatalf("allowed grant absent: %s", env)
	}
	for _, forbidden := range []string{"harness-secret", "dynamic-secret", "unconfigured-secret"} {
		if strings.Contains(env, forbidden) {
			t.Errorf("runner leaked %q: %s", forbidden, env)
		}
	}

	const absent = "DEFINITELY_ABSENT_COMMAND_RUNNER_TOKEN"
	const dynamic = "MCP_DYNAMIC_TOKEN"
	t.Setenv(dynamic, "dynamic-mcp-secret")
	path := t.TempDir() + "/settings.yaml"
	if err := os.WriteFile(path, []byte("command_runner:\n  environment:\n    inherit: ["+absent+", "+dynamic+"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	cfg = teamCfg(t)
	cfg.Diagnostics = slogdiag.New(&logs, false, port.LevelDebug)
	cfg.MCPServers = []mcp.ServerConfig{{Name: "dynamic"}}
	cfg.permResolver = permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	cfg, err := foldOperatorCommandRunnerEnvironment(cfg)
	if err != nil {
		t.Fatalf("fold command runner environment: %v", err)
	}
	if inherited := commandRunnerEnv(t, buildCommandRunner(cfg)); strings.Contains(inherited, "dynamic-mcp-secret") {
		t.Fatalf("configured MCP credential reference reached main runner: %s", inherited)
	}
	if text := logs.String(); !strings.Contains(text, absent) || strings.Contains(text, "dynamic-secret") {
		t.Fatalf("absent diagnostic must contain only the configured name: %s", text)
	}
}

func TestCommandRunnerEnvironment_Scenario3_HardenedChildrenStayScrubbed(t *testing.T) {
	t.Setenv("GH_TOKEN", "main-only-secret")
	cfg := teamCfg(t)
	cfg.commandEnvironmentInherit = []string{"GH_TOKEN"}
	for name, runner := range map[string]tool.CommandRunner{
		"read-only":          buildSandboxedCommandRunner(cfg),
		"team/parallel copy": buildForceCopyRunner(cfg),
	} {
		env := commandRunnerEnv(t, runner)
		if strings.Contains(env, "main-only-secret") || !strings.Contains(env, "GIT_CONFIG_NOSYSTEM=1") {
			t.Errorf("%s child runner lost hardening: %s", name, env)
		}
	}
}

func TestCommandRunnerEnvironment_Scenario3_InternalGitStaysScrubbed(t *testing.T) {
	t.Setenv("GH_TOKEN", "main-only-secret")
	env := strings.Join(internalGitEnvironment(), "\n")
	if strings.Contains(env, "main-only-secret") || !strings.Contains(env, "GIT_CONFIG_NOSYSTEM=1") {
		t.Fatalf("internal Git environment lost hardening: %s", env)
	}
}

func TestCommandRunnerEnvironment_Scenario3_DirectWriteParity(t *testing.T) {
	t.Setenv("GH_TOKEN", "main-only-secret")
	cfg := teamCfg(t)
	cfg.commandEnvironmentInherit = []string{"GH_TOKEN"}
	if env := commandRunnerEnv(t, directWriteCommandRunner(cfg)); !strings.Contains(env, "main-only-secret") {
		t.Fatalf("direct-write runner omitted the main grant: %s", env)
	}
}
