//go:build darwin

package managedtemp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestProcessStartIdentityPersistsKernelIdentity(t *testing.T) {
	ns, err := Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	workspace, err := ns.OpenWorkspace("osfs", "darwin-process-identity", "/workspace/darwin-process-identity")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	lease, err := workspace.Allocate("cmd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })

	if err := lease.Started(os.Getpid()); err != nil {
		t.Fatalf("Started: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(lease.Path(), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest allocationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ProcessStart == "" || manifest.ProcessStart == strconv.Itoa(os.Getpid()) {
		t.Fatalf("persisted process identity = %q, want non-PID-only identity", manifest.ProcessStart)
	}
}

func TestProcessStartIdentityRejectsNonexistentPID(t *testing.T) {
	if _, err := processStartIdentity(1 << 30); err == nil {
		t.Fatal("processStartIdentity accepted a nonexistent PID")
	}
}
