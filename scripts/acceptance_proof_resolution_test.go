package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runProof(t *testing.T, env []string, proof string) error {
	t.Helper()
	cmd := exec.Command("node", "scripts/resolve-task-proof.mjs", proof)
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), env...)
	return cmd.Run()
}

func taskListEnv(t *testing.T, tasks string) []string {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "task")
	program := "#!/bin/sh\nif [ \"$1\" = --list ]; then printf '%s' '" + tasks + "'; exit 0; fi\nexit 99\n"
	if err := os.WriteFile(fake, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + dir}
}

func TestResolveTaskProof_ResolvesRegisteredAllowlistedTargets(t *testing.T) {
	env := taskListEnv(t, `{"tasks":[{"name":"api:check"},{"name":"test:engine-standalone"},{"name":"site:build"}]}`)
	for _, proof := range []string{"api:check", "test:engine-standalone", "site:build"} {
		if err := runProof(t, env, proof); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveTaskProof_DoesNotExecuteRegisteredTarget(t *testing.T) {
	if err := runProof(t, taskListEnv(t, `{"tasks":[{"name":"api:check"}]}`), "api:check"); err != nil {
		t.Fatal(err)
	}
}

func TestResolveTaskProof_RejectsUnregisteredOrUnsupportedTarget(t *testing.T) {
	if runProof(t, nil, "unsupported") == nil {
		t.Fatal("unsupported proof resolved")
	}
}
