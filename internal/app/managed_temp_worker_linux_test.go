//go:build linux

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/managedtemp"
)

// TestADR_0281_BuildOwnsManagedTempWorkerLifecycle pins Build's ownership of the
// interval-gated startup sweep and Close's worker shutdown before namespace teardown.
func TestADR_0281_BuildOwnsManagedTempWorkerLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	built, err := Build(context.Background(), Config{
		UseMock:   true,
		Workspace: t.TempDir(),
		Shell:     "/bin/sh",
		temporaryStorage: temporaryStorageConfig{
			Mode: temporaryStorageManaged, ManagedRoot: root, SystemTempDir: t.TempDir(),
			CommandReapAfter: time.Hour, ReapInterval: time.Hour,
			ReapTimeout: time.Second, ShutdownReapTimeout: time.Second,
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ns, err := managedtemp.Open(root)
	if err != nil {
		built.Close()
		t.Fatal(err)
	}
	defer func() { _ = ns.Close() }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := ns.ReadSweepCompletion(); err == nil {
			break
		} else if time.Now().After(deadline) {
			built.Close()
			t.Fatalf("Build-owned startup sweep did not write completion: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	built.Close()

	noRoot := filepath.Join(t.TempDir(), "must-not-exist")
	noWorker := startManagedTempWorker(context.Background(), Config{temporaryStorage: temporaryStorageConfig{Mode: temporaryStorageSystem, ManagedRoot: noRoot}})
	noWorker()
	if _, err := os.Stat(noRoot); !os.IsNotExist(err) {
		t.Fatalf("system mode unexpectedly created managed root: %v", err)
	}
}
