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

func TestResolveTaskProof_ResolvesRegisteredAllowlistedTargets(t *testing.T) {
	for _, proof := range []string{"api:check", "test:engine-standalone", "site:build"} {
		if err := runProof(t, nil, proof); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveTaskProof_DoesNotExecuteRegisteredTarget(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "task")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nif [ \"$1\" = --list ]; then printf '%s' '{\"tasks\":[{\"name\":\"api:check\"}]}' ; exit 0; fi\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runProof(t, []string{"PATH=" + dir}, "api:check"); err != nil {
		t.Fatal(err)
	}
}

func TestResolveTaskProof_RejectsUnregisteredOrUnsupportedTarget(t *testing.T) {
	if runProof(t, nil, "unsupported") == nil {
		t.Fatal("unsupported proof resolved")
	}
}
