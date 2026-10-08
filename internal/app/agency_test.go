package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/prompt"
)

// TestAgencyDelta verifies the agency contract is SUPPLIED for EVERY model
// family — Claude included (issue #49: Claude was observed announcing actions
// without taking them, so the per-Claude omission was reversed) — and the
// empty/unknown id.
func TestAgencyDelta(t *testing.T) {
	for _, model := range []string{
		"gpt-5.1", "", "o3", "codex", "GPT-4O",
		"claude-opus-4-8", "Claude-Sonnet", "anthropic/CLAUDE-x",
	} {
		if agencyDelta(model) == "" {
			t.Errorf("agencyDelta(%q): want non-empty delta, got empty", model)
		}
	}
}

// TestPromptConfigThreadsAgencyDelta proves promptConfig folds the agency delta
// onto the role for both a non-Claude and a Claude model (issue #49: Claude now
// receives the contract too), in each case prefixed by the default framing.
func TestPromptConfigThreadsAgencyDelta(t *testing.T) {
	gpt := promptConfig(Config{Model: "gpt-x"}, "")
	if !strings.Contains(gpt.Role, "Keep going until the task is actually resolved") {
		t.Errorf("non-Claude promptConfig: Role missing agency delta\nRole=%q", gpt.Role)
	}
	if !strings.HasPrefix(gpt.Role, prompt.DefaultRole()) {
		t.Errorf("non-Claude promptConfig: Role must start with the default framing\nRole=%q", gpt.Role)
	}

	claude := promptConfig(Config{Model: "claude-x"}, "")
	if !strings.Contains(claude.Role, "Keep going until the task is actually resolved") {
		t.Errorf("Claude promptConfig: Role missing agency delta\nRole=%q", claude.Role)
	}
	if !strings.HasPrefix(claude.Role, prompt.DefaultRole()) {
		t.Errorf("Claude promptConfig: Role must start with the default framing\nRole=%q", claude.Role)
	}
}

// TestAgencyDeltaInStablePrefixCacheStable proves the GPT agency delta, once folded
// onto the Role by promptConfig, lands in the cache-stable StablePrefix (not the
// volatile suffix) AND is byte-identical across two prompt.Build calls — so it is a
// genuine cache-stable input, not merely returned by the helper. (FIX 5)
func TestAgencyDeltaInStablePrefixCacheStable(t *testing.T) {
	pc := promptConfig(Config{Model: "gpt-5.1"}, "")
	const delta = "Keep going until the task is actually resolved"

	first := prompt.Build(pc)
	if !strings.Contains(first.StablePrefix, delta) {
		t.Errorf("agency delta missing from StablePrefix\nprefix=%q", first.StablePrefix)
	}
	if strings.Contains(first.VolatileSuffix, delta) {
		t.Errorf("agency delta leaked into VolatileSuffix\nsuffix=%q", first.VolatileSuffix)
	}

	// Byte-identical across two builds → genuinely cache-stable.
	second := prompt.Build(pc)
	if first.StablePrefix != second.StablePrefix {
		t.Error("StablePrefix not byte-identical across two builds: agency delta is not cache-stable")
	}
}

// TestAgentPromptConfigKeysDeltaOnResolvedModel proves R3: agentPromptConfig
// keys the prompt on the RESOLVED model, not cfg.Model — Env.Model reflects the
// model the def will actually run on. Since issue #49 the agency contract is
// uniform across families, so a def resolved to EITHER family carries the def
// body AND the agency delta; the keying still governs Env.Model.
func TestAgentPromptConfigKeysDeltaOnResolvedModel(t *testing.T) {
	def := agents.AgentDef{Name: "explorer", Body: "EXPLORER PLAYBOOK BODY"}

	// A Claude-resolved def (parent on GPT) carries the body, the delta, and a
	// Claude Env.Model.
	pc := agentPromptConfig(Config{Model: "gpt-x"}, def, "claude-opus", "")
	if !strings.Contains(pc.Role, "EXPLORER PLAYBOOK BODY") {
		t.Errorf("agent role missing def body\nRole=%q", pc.Role)
	}
	if !strings.Contains(pc.Role, "Keep going until the task is actually resolved") {
		t.Errorf("Claude-resolved def must carry the agency delta (issue #49)\nRole=%q", pc.Role)
	}
	if pc.Env.Model != "claude-opus" {
		t.Errorf("Env.Model must reflect the resolved model, got %q", pc.Env.Model)
	}

	// A GPT-resolved def (parent on Claude) likewise carries the delta and a GPT
	// Env.Model.
	pcGPT := agentPromptConfig(Config{Model: "claude-x"}, def, "gpt-5.1", "")
	if !strings.Contains(pcGPT.Role, "Keep going until the task is actually resolved") {
		t.Errorf("GPT-resolved def must carry the agency delta\nRole=%q", pcGPT.Role)
	}
	if pcGPT.Env.Model != "gpt-5.1" {
		t.Errorf("Env.Model must reflect the resolved model, got %q", pcGPT.Env.Model)
	}
}

// initTestRepo creates a hermetic git repo in dir with one commit, returning the
// branch name. It configures a deterministic identity and an explicit initial
// branch so the snapshot is reproducible regardless of host git defaults.
func initTestRepo(t *testing.T, dir string) string {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// Deterministic, hermetic identity; no global/system config leak.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "f.txt")
	run("commit", "-q", "-m", "initial commit")
	return "main"
}

// TestGitSnapshotPositivePath builds a hermetic t.TempDir git repo with one commit
// and asserts the TRUSTED snapshot names the branch and the commit — no reliance on
// the surrounding /workspace checkout.
func TestGitSnapshotPositivePath(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo)

	snap := gitSnapshot(repo, "/bin/sh", true)
	if snap == "" {
		t.Fatal("gitSnapshot(trusted repo): want non-empty snapshot")
	}
	if !strings.Contains(snap, "branch: main") {
		t.Errorf("snapshot missing branch line\ngot=%q", snap)
	}
	if !strings.Contains(snap, "initial commit") {
		t.Errorf("snapshot missing recent commit\ngot=%q", snap)
	}

	// A non-git temp dir (still trusted) yields "" (fail-soft, rev-parse fails).
	if got := gitSnapshot(t.TempDir(), "/bin/sh", true); got != "" {
		t.Errorf("gitSnapshot(non-repo): want empty, got %q", got)
	}
}

// TestGitSnapshotUntrustedYieldsNothing proves the trust gate: an UNTRUSTED
// workspace (TrustProject=false) yields "" even for a valid git repo, so no
// <git-status> block renders and no git ever runs against the untrusted repo.
func TestGitSnapshotUntrustedYieldsNothing(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo)

	if got := gitSnapshot(repo, "/bin/sh", false); got != "" {
		t.Errorf("gitSnapshot(untrusted): want empty, got %q", got)
	}
}

// TestGitSnapshotNeutralizesFsmonitorRCE is the security regression for FIX 1: a
// repo-local core.fsmonitor points at a script that writes a marker file; `git
// status` would normally execute it. The hardened runner (gitenv-scrubbed env with a
// precedence-winning core.fsmonitor=false override) must NEUTRALIZE it, so the marker
// is NEVER written. WITHOUT the gitenv.Scrub hardening this test fails (the marker
// appears); WITH it, the snapshot still succeeds and the marker is absent.
func TestGitSnapshotNeutralizesFsmonitorRCE(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo)

	marker := filepath.Join(repo, "PWNED")
	hook := filepath.Join(repo, "fsmonitor.sh")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// Set core.fsmonitor in the repo-local .git/config — the attacker-controlled value
	// a malicious clone would ship. (core.fsmonitor as a hook program fires on git
	// status.)
	cmd := exec.Command("git", "config", "core.fsmonitor", hook)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config core.fsmonitor: %v\n%s", err, out)
	}

	// Hardened + trusted snapshot: must run without executing the fsmonitor program.
	_ = gitSnapshot(repo, "/bin/sh", true)

	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("RCE marker %s was written: core.fsmonitor executed — gitenv hardening did NOT neutralize the vector", marker)
	}
}
