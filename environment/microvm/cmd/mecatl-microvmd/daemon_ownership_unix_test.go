//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProductionStartupRefusesCompetitorBeforeSocketOrConfigSideEffects(t *testing.T) {
	const helperEnv = "MECATL_TEST_DAEMON_OWNERSHIP_HELPER"
	if root := os.Getenv(helperEnv); root != "" {
		ready := filepath.Join(root, "ready")
		release := filepath.Join(root, "release")
		daemonAfterOwnership = func() {
			if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
				t.Fatal(err)
			}
			for {
				if _, err := os.Stat(release); err == nil {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		err := run(filepath.Join(root, "state"), filepath.Join(root, "microvmd.sock"), filepath.Join(root, "missing-config.json"))
		if err == nil {
			t.Fatal("paused daemon unexpectedly completed startup")
		}
		return
	}

	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "microvmd.sock")
	if err := os.WriteFile(socket, []byte("peer socket sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestProductionStartupRefusesCompetitorBeforeSocketOrConfigSideEffects$")
	cmd.Env = append(os.Environ(), helperEnv+"="+root)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child := cmd
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(root, "release"), nil, 0o600)
		_ = child.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first production startup did not pause after ownership")
		}
		time.Sleep(10 * time.Millisecond)
	}
	oldWait := daemonOwnershipWait
	daemonOwnershipWait = 75 * time.Millisecond
	t.Cleanup(func() { daemonOwnershipWait = oldWait })
	err := run(stateDir, socket, filepath.Join(root, "missing-config.json"))
	if err == nil || !strings.Contains(err.Error(), "another microvmd launch") {
		t.Fatalf("competing production startup = %v", err)
	}
	if got, readErr := os.ReadFile(socket); readErr != nil || string(got) != "peer socket sentinel" {
		t.Fatalf("competing production startup changed socket: %q, %v", got, readErr)
	}
	for _, path := range []string{filepath.Join(stateDir, "vsock"), filepath.Join(stateDir, "runtime-artifacts"), filepath.Join(stateDir, "repositories")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("competing production startup created runtime state %q: %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("paused production startup helper: %v", err)
	}
}

func TestDaemonOwnershipPrecedesSocketMutationAndIsLifetimeExclusive(t *testing.T) {
	stateDir := t.TempDir()
	socket := filepath.Join(stateDir, "microvmd.sock")
	if err := os.WriteFile(socket, []byte("peer socket sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner, err := acquireDaemonOwnership(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	oldWait := daemonOwnershipWait
	daemonOwnershipWait = 75 * time.Millisecond
	t.Cleanup(func() { daemonOwnershipWait = oldWait })
	_, err = acquireDaemonOwnership(stateDir)
	if err == nil || !strings.Contains(err.Error(), "another microvmd launch") {
		t.Fatalf("competing daemon ownership = %v", err)
	}
	if got, readErr := os.ReadFile(socket); readErr != nil || string(got) != "peer socket sentinel" {
		t.Fatalf("competing launch changed peer socket: %q, %v", got, readErr)
	}
	owner.close()

	restarted, err := acquireDaemonOwnership(stateDir)
	if err != nil {
		t.Fatalf("stopped daemon ownership did not restart: %v", err)
	}
	restarted.close()
}
