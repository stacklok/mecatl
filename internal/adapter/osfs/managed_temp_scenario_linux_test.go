//go:build linux

package osfs_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/managedtemp"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// TestManagedTemporaryCommandLeases_Scenario4_EndToEnd exercises the offline
// local Bash path: normal completion deletes its exact lease, while a simulated
// crash residue remains until an eligible deterministic sweep owns its removal.
func TestManagedTemporaryCommandLeases_Scenario4_EndToEnd(t *testing.T) {
	ns, err := managedtemp.Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	workspace, err := ns.OpenWorkspace("osfs", "scenario-four", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	runner, err := osfs.NewCommandRunnerShell(t.TempDir(), "/bin/sh", osfs.WithManagedTemporaryWorkspace(workspace))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.(tool.CommandTemporaryScopeRunner).RunWithTemporaryScope(context.Background(), "printf '%s' \"$TMPDIR\"", tool.TemporaryScopeManaged)
	if err != nil {
		t.Fatalf("managed Bash: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("managed Bash exit = %d", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "/commands/cmd-") {
		t.Fatalf("managed Bash did not receive a private command lease: %q", result.Stdout)
	}
	paths, err := filepath.Glob(filepath.Join(workspace.Path(), "commands", "cmd-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("normal completion retained managed command lease(s): %q", paths)
	}
	residue, err := workspace.Allocate("cmd")
	if err != nil {
		t.Fatal(err)
	}
	residuePath := residue.Path()
	if err := residue.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := ns.Sweep(context.Background(), managedtemp.SweepOptions{Now: time.Now(), Interval: time.Hour, CommandReapAfter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if before.Deleted != 0 {
		t.Fatalf("premature sweep deleted %d leases", before.Deleted)
	}
	if _, err := os.Stat(residuePath); err != nil {
		t.Fatalf("ineligible residue was removed: %v", err)
	}
	after, err := ns.Sweep(context.Background(), managedtemp.SweepOptions{Now: time.Now().Add(2 * time.Hour), Interval: time.Nanosecond, CommandReapAfter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if after.Deleted != 1 {
		t.Fatalf("eligible sweep deleted %d leases, want 1", after.Deleted)
	}
	if _, err := os.Stat(residuePath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("eligible residue remains: %v", err)
	}
}
