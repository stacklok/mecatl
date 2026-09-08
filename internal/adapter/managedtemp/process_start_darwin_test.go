//go:build darwin

package managedtemp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
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
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid())
	if err != nil {
		t.Fatalf("SysctlKinfoProc: %v", err)
	}
	if proc == nil || int(proc.Proc.P_pid) != os.Getpid() {
		t.Fatalf("kernel process = %#v, want pid %d", proc, os.Getpid())
	}
	want := fmt.Sprintf("%d.%06d", proc.Proc.P_starttime.Sec, proc.Proc.P_starttime.Usec)
	if manifest.ProcessStart != want {
		t.Fatalf("persisted process identity = %q, want kernel start identity %q", manifest.ProcessStart, want)
	}
}

func TestProcessStartIdentityRejectsNonexistentPID(t *testing.T) {
	if _, err := processStartIdentity(1 << 30); err == nil {
		t.Fatal("processStartIdentity accepted a nonexistent PID")
	}
}
