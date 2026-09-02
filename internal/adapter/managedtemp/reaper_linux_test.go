//go:build linux

package managedtemp

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestADR_0281_IntervalGatedSweepCoordination pins the root-lock protocol: a
// completed sweep suppresses a later scan until the configured interval elapses.
func TestADR_0281_IntervalGatedSweepCoordination(t *testing.T) {
	ns, err := Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	first, err := ns.Sweep(context.Background(), SweepOptions{Now: now, Interval: time.Hour, CommandReapAfter: time.Hour})
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if !first.Scanned {
		t.Fatal("first sweep did not scan")
	}
	second, err := ns.Sweep(context.Background(), SweepOptions{Now: now.Add(time.Minute), Interval: time.Hour, CommandReapAfter: time.Hour})
	if err != nil {
		t.Fatalf("interval-gated sweep: %v", err)
	}
	if second.Scanned {
		t.Fatal("sweep scanned despite a recent durable completion")
	}
}

func TestADR_0281_ReaperDeletesOnlyValidatedEligibleLease(t *testing.T) {
	ns, err := Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	workspace, err := ns.OpenWorkspace("osfs", "reaper-validation", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	eligible, err := workspace.Allocate("cmd")
	if err != nil {
		t.Fatal(err)
	}
	if err := eligible.Terminal(); err != nil {
		t.Fatal(err)
	}
	if err := eligible.Close(); err != nil {
		t.Fatal(err)
	}
	locked, err := workspace.Allocate("job")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Close() })
	if err := locked.Terminal(); err != nil {
		t.Fatal(err)
	}
	bad, err := workspace.Allocate("cmd")
	if err != nil {
		t.Fatal(err)
	}
	badPath := bad.Path()
	if err := bad.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badPath, "manifest.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrecognised := filepath.Join(workspace.Path(), "commands", "cmd-unrecognised")
	if err := os.Symlink(filepath.Join(eligible.Path(), "tmp"), unrecognised); err != nil {
		t.Fatal(err)
	}
	result, err := ns.Sweep(context.Background(), SweepOptions{Now: time.Now().Add(2 * time.Hour), Interval: time.Hour, CommandReapAfter: time.Hour})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Deleted != 1 {
		t.Fatalf("deleted = %d, want exactly the validated eligible lease", result.Deleted)
	}
	if _, err := os.Stat(eligible.Path()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("eligible lease remains: %v", err)
	}
	for _, path := range []string{locked.Path(), badPath, unrecognised} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("invalid or contended lease %q was deleted: %v", path, err)
		}
	}
}

func TestADR_0281_CrashRecoveryAndConcurrentReaping(t *testing.T) {
	ns, err := Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	workspace, err := ns.OpenWorkspace("osfs", "crash-recovery", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	lease, err := workspace.Allocate("cmd")
	if err != nil {
		t.Fatal(err)
	}
	path := lease.Path()
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	} // simulate a dead runner: OS released its lock.
	now := time.Now().Add(2 * time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ns.Sweep(ctx, SweepOptions{Now: now, Interval: time.Hour, CommandReapAfter: time.Hour}); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted sweep = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("interrupted sweep deleted crash residue: %v", err)
	}
	if _, err := ns.ReadSweepCompletion(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("interrupted sweep recorded completion: %v", err)
	}
	result, err := ns.Sweep(context.Background(), SweepOptions{Now: now, Interval: time.Hour, CommandReapAfter: time.Hour})
	if err != nil {
		t.Fatalf("recovery sweep: %v", err)
	}
	if !result.Scanned || result.Deleted != 1 {
		t.Fatalf("recovery result = %+v, want one completed deletion", result)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("crash residue remains: %v", err)
	}
}
