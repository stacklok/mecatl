//go:build unix

package osfs_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/managedtemp"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// TestManagedTemporaryCommandLeases_Scenario4_EndToEnd exercises the offline
// local Bash path: normal completion deletes its exact lease, while a simulated
// crash residue remains until an eligible deterministic sweep owns its removal.

// TestADR_0281_ForegroundLeaseOverlayAndMetadataPrivacy pins the foreground
// managed-command overlay: it supplies one private lease tmp directory without
// injecting that path into the shell text, and its manifest is lifecycle-only.
func TestADR_0281_ForegroundLeaseOverlayAndMetadataPrivacy(t *testing.T) {
	ns, err := managedtemp.Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	root := t.TempDir()
	workspace, err := ns.OpenWorkspace("osfs", "foreground-privacy", root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	runner, err := osfs.NewCommandRunnerShell(root, "/bin/sh", osfs.WithCommandEnvList([]string{"SAFE=value"}), osfs.WithManagedTemporaryWorkspace(workspace))
	if err != nil {
		t.Fatal(err)
	}
	const command = `printf '%s\n%s' "$TMPDIR" "$GOTMPDIR" > temporary-paths; printf '%s' "${GENERIC_TOKEN-unset}" > secret-status; sleep 30`
	done := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(context.Background(), command)
		done <- runErr
	}()

	pathsFile := filepath.Join(root, "temporary-paths")
	var paths []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(pathsFile)
		if readErr == nil {
			paths = strings.Split(strings.TrimSpace(string(data)), "\n")
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(paths) != 2 || paths[0] == "" || paths[0] != paths[1] || filepath.Base(paths[0]) != "tmp" || !strings.HasPrefix(filepath.Base(filepath.Dir(paths[0])), "cmd-") {
		t.Fatalf("TMPDIR/GOTMPDIR = %q, want one cmd-<allocation-id>/tmp directory", paths)
	}
	if strings.Contains(command, paths[0]) {
		t.Fatalf("shell text contains injected lease temporary path %q", paths[0])
	}
	if data, readErr := os.ReadFile(filepath.Join(root, "secret-status")); readErr != nil || string(data) != "unset" {
		t.Fatalf("managed overlay restored scrubbed credential-shaped variable: %q, %v", data, readErr)
	}
	manifest, readErr := os.ReadFile(filepath.Join(filepath.Dir(paths[0]), "manifest.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var fields map[string]any
	if json.Unmarshal(manifest, &fields) != nil {
		t.Fatalf("manifest is not JSON: %q", manifest)
	}
	for _, forbidden := range []string{"command", "output", "environment", "credential", "transcript"} {
		if _, found := fields[forbidden]; found || strings.Contains(string(manifest), forbidden) {
			t.Fatalf("manifest leaks %q: %s", forbidden, manifest)
		}
	}
	if err := syscall.Kill(-mustPIDFromLease(t, filepath.Dir(paths[0])), syscall.SIGKILL); err != nil {
		t.Fatalf("kill foreground process group: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("foreground managed command: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("foreground managed command did not finish")
	}
}

func mustPIDFromLease(t *testing.T, leasePath string) int {
	t.Helper()
	manifest, err := os.ReadFile(filepath.Join(leasePath, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		OwnerPID int `json:"owner_pid"`
	}
	if err := json.Unmarshal(manifest, &fields); err != nil || fields.OwnerPID <= 0 {
		t.Fatalf("manifest owner PID = %d, %v", fields.OwnerPID, err)
	}
	return fields.OwnerPID
}

func TestManagedTemporaryCommandLeases_Scenario4_EndToEnd(t *testing.T) {
	ns, err := managedtemp.Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	workspaceRoot := t.TempDir()
	workspace, err := ns.OpenWorkspace("osfs", "scenario-four", workspaceRoot)
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
