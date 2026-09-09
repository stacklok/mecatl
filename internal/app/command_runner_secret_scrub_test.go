package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// TestMainCommandRunnerScrubsSecrets is the security oracle for "Finding B": the
// MAIN-session Shell runner — the one posture `auto`/`yolo` exposes to the model —
// MUST NOT let a child shell read the harness's provider/auth credentials out of
// the process environment, while the toolchain vars (PATH/HOME) MUST survive so
// builds still work.
//
// MUTATION-TEST DISCIPLINE: revert the envscrub.Scrub call in buildCommandRunner
// (drop the option so r.env stays nil → os.Environ() passthrough) and this test
// FAILS — it is not vacuous.
func TestMainCommandRunnerScrubsSecrets(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-MUST-NOT-LEAK")
	t.Setenv("OPENAI_API_KEY", "sk-oa-MUST-NOT-LEAK")
	t.Setenv("ANTHROPIC_API_KEY", "sk-an-MUST-NOT-LEAK")
	t.Setenv("MECATL_AUTH_TOKEN", "tok-MUST-NOT-LEAK")
	t.Setenv("GH_TOKEN", "ghp-MUST-NOT-LEAK")

	cfg := teamCfg(t)
	runner := buildCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil main command runner with Shell set")
	}

	res, err := runner.Run(context.Background(), "env")
	if err != nil {
		t.Fatalf("runner.Run(env): %v", err)
	}
	env := res.Stdout

	for _, secret := range []string{
		"OPENROUTER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
		"MECATL_AUTH_TOKEN", "GH_TOKEN",
	} {
		if strings.Contains(env, secret+"=") || strings.Contains(env, "MUST-NOT-LEAK") {
			t.Errorf("main command runner leaked secret %q to the child shell env:\n%s", secret, env)
		}
	}

	// Toolchain vars MUST survive (scrubbing PATH/HOME would break go build / git).
	for _, keep := range []string{"PATH=", "HOME="} {
		if !strings.Contains(env, keep) {
			t.Errorf("main command runner dropped toolchain var %q (would break builds); env:\n%s", keep, env)
		}
	}
}

// TestPlacementRunnerScrubsSecretsForAlternateRoot pins that placement-provider
// environment construction through buildCommandRunnerForRoot with a root DISTINCT from cfg.Workspace) scrubs
// secrets identically to the main-session runner (buildCommandRunner). Both
// route through the ONE buildCommandRunnerForRoot, so the secret-scrubbing
// cannot drift between the default-root and alternate-root paths (security
// review "Finding B"). It mirrors the main-runner oracle but exercises the
// factory closure as the Service would: an alternate root that is NOT
// cfg.Workspace.
//
// MUTATION-TEST DISCIPLINE: if buildCommandRunnerForRoot is reverted to an
// inline osfs.NewCommandRunnerShell(root, ...) WITHOUT envscrub.Scrub, this
// test FAILS — it is not vacuous.
func TestPlacementRunnerScrubsSecretsForAlternateRoot(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-MUST-NOT-LEAK")
	t.Setenv("OPENAI_API_KEY", "sk-oa-MUST-NOT-LEAK")
	t.Setenv("ANTHROPIC_API_KEY", "sk-an-MUST-NOT-LEAK")
	t.Setenv("MECATL_AUTH_TOKEN", "tok-MUST-NOT-LEAK")
	t.Setenv("GH_TOKEN", "ghp-MUST-NOT-LEAK")

	cfg := teamCfg(t)
	// An alternate root distinct from cfg.Workspace — the worktree-session path.
	altRoot := t.TempDir()
	if altRoot == cfg.Workspace {
		t.Fatal("precondition: alt root must differ from cfg.Workspace")
	}
	// The private placement-provider construction closure.
	factory := func(root string) tool.CommandRunner {
		return buildCommandRunnerForRoot(cfg, root)
	}
	runner := factory(altRoot)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil factory runner for the alternate root")
	}

	res, err := runner.Run(context.Background(), "env")
	if err != nil {
		t.Fatalf("runner.Run(env): %v", err)
	}
	env := res.Stdout

	for _, secret := range []string{
		"OPENROUTER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
		"MECATL_AUTH_TOKEN", "GH_TOKEN",
	} {
		if strings.Contains(env, secret+"=") || strings.Contains(env, "MUST-NOT-LEAK") {
			t.Errorf("factory runner (alternate root) leaked secret %q to the child shell env:\n%s", secret, env)
		}
	}
	for _, keep := range []string{"PATH=", "HOME="} {
		if !strings.Contains(env, keep) {
			t.Errorf("factory runner (alternate root) dropped toolchain var %q (would break builds); env:\n%s", keep, env)
		}
	}
}

// TestSandboxedCommandRunnerScrubsSecrets confirms the hardened (sandboxed
// subagent/team-member/force-copy) runner is ALSO secret-safe — it was previously
// only git-scrubbed (gitenv.Scrub drops GIT_*/PAGER, never secrets), so the same
// exfiltration was reachable from a read-only subagent shell. envscrub now layers
// under gitenv for every hardened runner.
func TestSandboxedCommandRunnerScrubsSecrets(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-MUST-NOT-LEAK")
	t.Setenv("EXA_API_KEY", "exa-MUST-NOT-LEAK")
	t.Setenv("MECATL_DRIVER_AUTH_TOKEN", "drv-MUST-NOT-LEAK")

	cfg := teamCfg(t) // TrustProject:true so the sandboxed runner is non-nil
	runner := buildSandboxedCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil sandboxed runner (trusted workspace, shell set)")
	}

	res, err := runner.Run(context.Background(), "env")
	if err != nil {
		t.Fatalf("runner.Run(env): %v", err)
	}
	env := res.Stdout

	if strings.Contains(env, "MUST-NOT-LEAK") {
		t.Errorf("sandboxed command runner leaked a secret to the child shell env:\n%s", env)
	}
	// The git neutralisation still holds (defence-in-depth, not regressed).
	if !strings.Contains(env, "GIT_CONFIG_NOSYSTEM=1") {
		t.Errorf("sandboxed runner lost its git neutralisation; env:\n%s", env)
	}
	if !strings.Contains(env, "PATH=") {
		t.Errorf("sandboxed runner dropped PATH (would break builds); env:\n%s", env)
	}
}
