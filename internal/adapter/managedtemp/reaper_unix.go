//go:build unix

package managedtemp

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// SweepOptions controls one bounded, interval-gated recovery pass. Now is
// supplied by composition so tests and scheduled maintenance are deterministic.
type SweepOptions struct {
	Now              time.Time
	Interval         time.Duration
	CommandReapAfter time.Duration
}

// SweepResult reports whether this caller scanned rather than observing a
// recent completion or another process's root sweep lock.
type SweepResult struct {
	Scanned bool
	Deleted int
}

// Sweep performs one non-blocking, root-coordinated recovery pass. It never
// waits for an active command's lease lock and only removes validated leases.
func (n *Namespace) Sweep(ctx context.Context, opts SweepOptions) (SweepResult, error) {
	if n == nil || n.root == nil {
		return SweepResult{}, errors.New("managedtemp: namespace is closed")
	}
	if err := ctx.Err(); err != nil {
		return SweepResult{}, err
	}
	if opts.Now.IsZero() || opts.Interval <= 0 || opts.CommandReapAfter <= 0 {
		return SweepResult{}, errors.New("managedtemp: invalid sweep options")
	}
	lock, err := n.root.OpenFile("gc.lock", os.O_RDWR, 0)
	if err != nil {
		return SweepResult{}, err
	}
	defer func() { _ = lock.Close() }()
	if err := lockExclusive(lock, true); err != nil {
		if errors.Is(err, syscallEWOULDBLOCK()) {
			return SweepResult{}, nil
		}
		return SweepResult{}, err
	}
	defer func() { _ = unlockClose(lock) }()

	completed, err := n.ReadSweepCompletion()
	if err == nil && opts.Now.Sub(completed) < opts.Interval {
		return SweepResult{}, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return SweepResult{}, err
	}
	deleted, err := n.sweepWorkspaces(ctx, opts)
	if err != nil {
		return SweepResult{Scanned: true, Deleted: deleted}, err
	}
	if err := ctx.Err(); err != nil {
		return SweepResult{Scanned: true, Deleted: deleted}, err
	}
	data, err := json.Marshal(sweepCompletion{Version: manifestVersion, CompletedAt: opts.Now.UTC()})
	if err != nil {
		return SweepResult{Scanned: true, Deleted: deleted}, err
	}
	var completionErr error
	if _, statErr := n.root.Lstat("last-successful-sweep.json"); errors.Is(statErr, fs.ErrNotExist) {
		completionErr = writePrivateFile(n.root, "last-successful-sweep.json", data)
	} else if statErr == nil {
		completionErr = replacePrivateFile(n.root, "last-successful-sweep.json", data)
	} else {
		completionErr = statErr
	}
	if completionErr != nil {
		return SweepResult{Scanned: true, Deleted: deleted}, completionErr
	}
	return SweepResult{Scanned: true, Deleted: deleted}, nil
}

func (n *Namespace) sweepWorkspaces(ctx context.Context, opts SweepOptions) (int, error) {
	if err := validatePrivateDir(n.root, "workspaces"); err != nil {
		return 0, err
	}
	workspaces, err := n.root.OpenRoot("workspaces")
	if err != nil {
		return 0, err
	}
	defer func() { _ = workspaces.Close() }()
	dir, err := workspaces.Open(".")
	if err != nil {
		return 0, err
	}
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err != nil {
		return 0, err
	}
	if closeErr != nil {
		return 0, closeErr
	}
	deleted := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if !entry.IsDir() || !validWorkspaceKey(entry.Name()) {
			continue
		}
		workspace, err := workspaces.OpenRoot(entry.Name())
		if err != nil {
			continue
		}
		count, sweepErr := sweepWorkspace(ctx, workspace, opts)
		_ = workspace.Close()
		if sweepErr != nil {
			return deleted, sweepErr
		}
		deleted += count
	}
	return deleted, nil
}

//nolint:gocyclo // each fail-closed validation check is intentionally explicit.
func sweepWorkspace(ctx context.Context, workspace *os.Root, opts SweepOptions) (int, error) {
	if err := validatePrivateDir(workspace, "."); err != nil {
		return 0, nil
	}
	data, err := readPrivateFile(workspace, "workspace.manifest")
	if err != nil {
		return 0, nil
	}
	var manifest workspaceManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.Version != manifestVersion || manifest.Key != filepath.Base(workspace.Name()) || !validWorkspaceKey(manifest.Key) || !validCanonicalWorkspacePath(manifest.CurrentPath) {
		return 0, nil
	}
	if err := validatePrivateDir(workspace, "commands"); err != nil {
		return 0, nil
	}
	commands, err := workspace.OpenRoot("commands")
	if err != nil {
		return 0, nil
	}
	defer func() { _ = commands.Close() }()
	dir, err := commands.Open(".")
	if err != nil {
		return 0, err
	}
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err != nil {
		return 0, err
	}
	if closeErr != nil {
		return 0, closeErr
	}
	deleted := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if !entry.IsDir() {
			continue
		}
		if reapLease(ctx, commands, entry.Name(), opts) {
			deleted++
		}
	}
	return deleted, nil
}

// reapLease returns true only after a complete validation, non-blocking lock, and
// exact handle-rooted deletion sequence.
//
//nolint:gocyclo // each fail-closed validation check is intentionally explicit.
func reapLease(ctx context.Context, commands *os.Root, name string, opts SweepOptions) bool {
	kind, id, ok := strings.Cut(name, "-")
	if !ok || (kind != "cmd" && kind != "job") || !validAllocationID(id) {
		return false
	}
	leaseRoot, err := commands.OpenRoot(name)
	if err != nil {
		return false
	}
	defer func() { _ = leaseRoot.Close() }()
	if err := validatePrivateDir(leaseRoot, "."); err != nil || ctx.Err() != nil {
		return false
	}
	if err := validateLeaseParentEntry(commands, name, leaseRoot); err != nil {
		return false
	}
	lock, err := leaseRoot.OpenFile("lease.lock", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer func() { _ = lock.Close() }()
	if err := lockExclusive(lock, true); err != nil {
		return false
	}
	defer func() { _ = unlockClose(lock) }()
	if err := ctx.Err(); err != nil || validatePrivateFile(leaseRoot, "lease.lock") != nil {
		return false
	}
	data, err := readPrivateFile(leaseRoot, "manifest.json")
	if err != nil {
		return false
	}
	var manifest allocationManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.Version != manifestVersion || manifest.ID != id || manifest.Kind != kind || manifest.UID != os.Geteuid() {
		return false
	}
	transition := manifest.CreatedAt
	if !manifest.StartedAt.IsZero() {
		transition = manifest.StartedAt
	}
	if !manifest.TerminalAt.IsZero() {
		transition = manifest.TerminalAt
	}
	if transition.IsZero() || opts.Now.Sub(transition) < opts.CommandReapAfter || ctx.Err() != nil {
		return false
	}
	if err := validateLeaseParentEntry(commands, name, leaseRoot); err != nil {
		return false
	}
	if err := removeTreeNoLinks(leaseRoot); err != nil || ctx.Err() != nil {
		return false
	}
	if err := validateLeaseParentEntry(commands, name, leaseRoot); err != nil {
		return false
	}
	return commands.Remove(name) == nil
}

func validateLeaseParentEntry(parent *os.Root, name string, lease *os.Root) error {
	entry, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if err := validatePrivateDirInfo(entry); err != nil {
		return err
	}
	opened, err := lease.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(entry, opened) {
		return errors.New("managedtemp: lease parent entry was replaced")
	}
	return nil
}

// syscallEWOULDBLOCK avoids exposing syscall details in the sweep API.
func syscallEWOULDBLOCK() error { return syscall.EWOULDBLOCK }
