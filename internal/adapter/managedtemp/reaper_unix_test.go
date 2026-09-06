//go:build unix

package managedtemp

import (
	"context"
	"encoding/json"
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
	workspace, err := ns.OpenWorkspace("osfs", "reaper-validation", "/workspace/reaper-validation")
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
	currentIdentity, err := processStartIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	malformed := make([]string, 0, 6)
	for _, test := range []struct {
		name   string
		mutate func(*allocationManifest)
	}{
		{name: "missing-created-at", mutate: func(manifest *allocationManifest) { manifest.CreatedAt = time.Time{} }},
		{name: "unknown-state", mutate: func(manifest *allocationManifest) { manifest.State = "unknown" }},
		{name: "terminal-without-terminal-at", mutate: func(manifest *allocationManifest) { manifest.TerminalAt = time.Time{} }},
		{name: "active-without-started-at", mutate: func(manifest *allocationManifest) {
			manifest.State, manifest.TerminalAt, manifest.StartedAt = "active", time.Time{}, time.Time{}
		}},
		{name: "active-without-process-identity", mutate: func(manifest *allocationManifest) {
			manifest.State, manifest.TerminalAt, manifest.StartedAt, manifest.OwnerPID, manifest.ProcessStart = "active", time.Time{}, manifest.CreatedAt, 0, ""
		}},
		{name: "active-with-reused-process-identity", mutate: func(manifest *allocationManifest) {
			manifest.State, manifest.TerminalAt, manifest.StartedAt, manifest.OwnerPID, manifest.ProcessStart = "active", time.Time{}, manifest.CreatedAt, os.Getpid(), currentIdentity+"-reused"
		}},
	} {
		lease, err := workspace.Allocate("cmd")
		if err != nil {
			t.Fatal(err)
		}
		path := lease.Path()
		if err := lease.Terminal(); err != nil {
			t.Fatal(err)
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(path, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest allocationManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		test.mutate(&manifest)
		data, err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "manifest.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		malformed = append(malformed, path)
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
	for _, path := range append([]string{locked.Path(), badPath, unrecognised}, malformed...) {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("invalid or contended lease %q was deleted: %v", path, err)
		}
	}
}

// A syntactically valid parent entry must still name the handle that was
// validated. Otherwise a relative link can redirect recursive cleanup to a
// sibling whose manifest has been forged to match the candidate.
func TestADR_0281_ReaperDeletesOnlyValidatedEligibleLease_RetainsRelativeSymlinkedSibling(t *testing.T) {
	ns, err := Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	workspace, err := ns.OpenWorkspace("osfs", "relative-link", "/workspace/relative-link")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	candidate, err := workspace.Allocate("cmd")
	if err != nil {
		t.Fatal(err)
	}
	candidateName := filepath.Base(candidate.Path())
	if err := candidate.Terminal(); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	sibling, err := workspace.Allocate("job")
	if err != nil {
		t.Fatal(err)
	}
	if err := sibling.Terminal(); err != nil {
		t.Fatal(err)
	}
	if err := sibling.Close(); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(sibling.Path(), "tmp", "must-survive")
	if err := os.WriteFile(keep, []byte("sibling content"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Make the target look like the candidate to every manifest-only check.
	manifestPath := filepath.Join(sibling.Path(), "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest allocationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ID, manifest.Kind = candidate.ID(), "cmd"
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(candidate.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(sibling.Path()), candidate.Path()); err != nil {
		t.Fatal(err)
	}

	commands, err := workspace.root.OpenRoot("commands")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = commands.Close() }()
	if reapLease(context.Background(), commands, candidateName, SweepOptions{Now: time.Now().Add(2 * time.Hour), Interval: time.Hour, CommandReapAfter: time.Hour}) {
		t.Fatal("reaper deleted a relative-symlinked candidate")
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "sibling content" {
		t.Fatalf("relative symlink redirected cleanup into sibling: %q, %v", data, err)
	}
}

func TestADR_0281_CrashRecoveryAndConcurrentReaping(t *testing.T) {
	ns, err := Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	workspace, err := ns.OpenWorkspace("osfs", "crash-recovery", "/workspace/crash-recovery")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	lease, err := workspace.Allocate("cmd")
	if err != nil {
		t.Fatal(err)
	}
	path := lease.Path()
	if err := lease.Started(os.Getpid()); err != nil {
		t.Fatal(err)
	}
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
