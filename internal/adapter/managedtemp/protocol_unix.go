//go:build unix

package managedtemp

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const manifestVersion = 1

// ErrUnsupportedVersion reports a record whose format cannot be safely read.
var ErrUnsupportedVersion = errors.New("managedtemp: unsupported manifest version")

type workspaceManifest struct {
	Version     int    `json:"version"`
	Key         string `json:"key"`
	CurrentPath string `json:"current_path"`
}

type allocationManifest struct {
	Version      int       `json:"version"`
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	TerminalAt   time.Time `json:"terminal_at,omitempty"`
	UID          int       `json:"uid"`
	OwnerPID     int       `json:"owner_pid,omitempty"`
	ProcessStart string    `json:"process_start,omitempty"`
	ProcessGroup int       `json:"process_group,omitempty"`
	State        string    `json:"state"`
}

type sweepCompletion struct {
	Version     int       `json:"version"`
	CompletedAt time.Time `json:"completed_at"`
}

// Lease is one locked command or job allocation. Its path is adapter-internal.
type Lease struct {
	path   string
	name   string
	id     string
	kind   string
	root   *os.Root
	lock   *os.File
	parent *Workspace
}

// ID returns the opaque 96-bit random allocation ID.
func (l *Lease) ID() string { return l.id }

// Path returns the adapter-local allocation path.
func (l *Lease) Path() string { return l.path }

// TempDir returns the private temporary directory supplied to the command.
func (l *Lease) TempDir() string { return filepath.Join(l.path, "tmp") }

// Started records the process identity after the runner successfully starts its
// dedicated process group.
func (l *Lease) Started(pid int) error { return l.transition("active", pid) }

// Terminal records that the runner has joined its direct command process. A
// still-live process group is retained for the reaper; a gone group may be
// removed immediately by the runner.
func (l *Lease) Terminal() error { return l.transition("terminal", 0) }

func (l *Lease) transition(state string, pid int) error {
	if err := validateLease(l); err != nil {
		return err
	}
	data, err := readPrivateFile(l.root, "manifest.json")
	if err != nil {
		return err
	}
	var manifest allocationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("managedtemp: malformed allocation manifest: %w", err)
	}
	now := time.Now().UTC()
	manifest.State = state
	if pid != 0 {
		start, err := processStartIdentity(pid)
		if err != nil {
			return err
		}
		manifest.OwnerPID = pid
		manifest.ProcessStart = start
		manifest.ProcessGroup = pid
		manifest.StartedAt = now
	} else {
		manifest.TerminalAt = now
	}
	updated, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return replacePrivateFile(l.root, "manifest.json", updated)
}

// ValidTestHomeMarker reports whether marker names a private command lease
// created by this protocol. It rejects ordinary paths and malformed metadata;
// test-home uses it before placing process-wide HOME state under the lease.
//
//nolint:gocyclo // each marker validation check is intentionally explicit.
func ValidTestHomeMarker(marker string) bool {
	if marker == "" || !filepath.IsAbs(marker) || filepath.Base(marker) == "." {
		return false
	}
	info, err := os.Lstat(marker)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != privateDirMode || !ownedByCurrentUser(info) {
		return false
	}
	name := filepath.Base(marker)
	id, ok := strings.CutPrefix(name, "cmd-")
	if !ok || !validAllocationID(id) {
		return false
	}
	if !validTestHomeNamespace(marker) {
		return false
	}
	data, err := os.ReadFile(filepath.Join(marker, "manifest.json"))
	if err != nil {
		return false
	}
	var manifest allocationManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.Version != manifestVersion || manifest.Kind != "cmd" || manifest.ID != id {
		return false
	}
	for _, child := range []string{"lease.lock", "tmp"} {
		childInfo, err := os.Lstat(filepath.Join(marker, child))
		if err != nil || childInfo.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(childInfo) {
			return false
		}
	}
	lock, err := os.OpenFile(filepath.Join(marker, "lease.lock"), os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer func() { _ = lock.Close() }()
	// A marker is valid only while its runner owns the active lease. This rejects
	// arbitrary same-UID directories that imitate the public lease layout.
	return syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == syscall.EWOULDBLOCK
}

// Close releases the active lease lock without removing the allocation.
func (l *Lease) Close() error {
	if l == nil || l.lock == nil {
		return nil
	}
	err := unlockClose(l.lock)
	l.lock = nil
	if l.root != nil {
		closeErr := l.root.Close()
		l.root = nil
		if err == nil {
			err = closeErr
		}
	}
	return err
}

// Allocate creates and locks a private cmd or job lease before publishing its manifest.
func (w *Workspace) Allocate(kind string) (*Lease, error) {
	if w == nil || w.root == nil {
		return nil, errors.New("managedtemp: workspace is closed")
	}
	if kind != "cmd" && kind != "job" {
		return nil, errors.New("managedtemp: allocation kind must be cmd or job")
	}
	if err := validateWorkspace(w); err != nil {
		return nil, err
	}
	if err := ensurePrivateDir(w.root, "commands"); err != nil {
		return nil, err
	}
	id, err := allocationID()
	if err != nil {
		return nil, err
	}
	name := kind + "-" + id
	if err := ensurePrivateDir(w.root, "commands/"+name); err != nil {
		return nil, err
	}
	root, err := w.root.OpenRoot("commands/" + name)
	if err != nil {
		return nil, err
	}
	if err := validatePrivateDir(root, "."); err != nil {
		_ = root.Close()
		return nil, err
	}
	if err := ensurePrivateFile(root, "lease.lock"); err != nil {
		_ = root.Close()
		return nil, err
	}
	lock, err := root.OpenFile("lease.lock", os.O_RDWR, 0)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if err := lockExclusive(lock, false); err != nil {
		_ = lock.Close()
		_ = root.Close()
		return nil, err
	}
	manifest, err := json.Marshal(allocationManifest{Version: manifestVersion, ID: id, Kind: kind, CreatedAt: time.Now().UTC(), UID: os.Geteuid(), State: "active"})
	if err == nil {
		err = writePrivateFile(root, "manifest.json", manifest)
	}
	if err == nil {
		err = ensurePrivateDir(root, "tmp")
	}
	if err != nil {
		_ = unlockClose(lock)
		_ = root.Close()
		return nil, err
	}
	return &Lease{path: filepath.Join(w.path, "commands", name), name: name, id: id, kind: kind, root: root, lock: lock, parent: w}, nil
}

// Remove removes this exact validated lease. It refuses a replacement at the parent
// handle and never follows links while traversing the retained lease handle.
func (l *Lease) Remove() error {
	if l == nil || l.root == nil || l.parent == nil || l.parent.root == nil {
		return errors.New("managedtemp: lease is closed")
	}
	if err := validateLease(l); err != nil {
		return err
	}
	if err := removeTreeNoLinks(l.root); err != nil {
		return err
	}
	if err := l.parent.root.Remove("commands/" + l.name); err != nil {
		return err
	}
	return l.Close()
}

// ReadSweepCompletion reads the durable sweep record without changing it.
func (n *Namespace) ReadSweepCompletion() (time.Time, error) {
	if n == nil || n.root == nil {
		return time.Time{}, errors.New("managedtemp: namespace is closed")
	}
	data, err := readPrivateFile(n.root, "last-successful-sweep.json")
	if err != nil {
		return time.Time{}, err
	}
	var record sweepCompletion
	if err := json.Unmarshal(data, &record); err != nil {
		return time.Time{}, fmt.Errorf("managedtemp: malformed sweep completion: %w", err)
	}
	if record.Version != manifestVersion {
		return time.Time{}, ErrUnsupportedVersion
	}
	return record.CompletedAt, nil
}

func replacePrivateFile(root *os.Root, name string, data []byte) error {
	if err := validatePrivateFile(root, name); err != nil {
		return err
	}
	parent, leaf, err := openPrivateParent(root, name)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	temp, err := privateTemp()
	if err != nil {
		return err
	}
	defer func() { _ = parent.Remove(temp) }()
	file, err := parent.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := validatePrivateFile(parent, temp); err != nil {
		return err
	}
	if err := validatePrivateFile(parent, leaf); err != nil {
		return err
	}
	return parent.Rename(temp, leaf)
}

func validateWorkspace(w *Workspace) error {
	if err := validatePrivateDir(w.root, "."); err != nil {
		return err
	}
	return validateWorkspaceManifest(w.root, filepath.Base(w.path))
}

func validateLease(l *Lease) error {
	if err := validateWorkspace(l.parent); err != nil {
		return err
	}
	if err := validatePrivateDir(l.parent.root, "commands"); err != nil {
		return err
	}
	// This parent-relative check detects a same-UID replacement after allocation.
	info, err := l.parent.root.Lstat("commands/" + l.name)
	if err != nil {
		return err
	}
	if err := validatePrivateDirInfo(info); err != nil {
		return err
	}
	if err := sameLeaseParentEntry(l.parent.root, l.name, l.root); err != nil {
		return err
	}
	if err := validatePrivateDir(l.root, "."); err != nil {
		return err
	}
	if err := validatePrivateFile(l.root, "lease.lock"); err != nil {
		return err
	}
	data, err := readPrivateFile(l.root, "manifest.json")
	if err != nil {
		return err
	}
	var manifest allocationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("managedtemp: malformed allocation manifest: %w", err)
	}
	if manifest.Version != manifestVersion {
		return ErrUnsupportedVersion
	}
	if !validAllocationID(manifest.ID) || manifest.ID != l.id || manifest.Kind != l.kind {
		return errors.New("managedtemp: allocation manifest identity mismatch")
	}
	return nil
}

func sameLeaseParentEntry(parent *os.Root, name string, lease *os.Root) error {
	entry, err := parent.Lstat("commands/" + name)
	if err != nil {
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

func validateWorkspaceManifest(root *os.Root, key string) error {
	data, err := readPrivateFile(root, "workspace.manifest")
	if err != nil {
		return err
	}
	var manifest workspaceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("managedtemp: malformed workspace manifest: %w", err)
	}
	if manifest.Version != manifestVersion {
		return ErrUnsupportedVersion
	}
	if manifest.Key != key || !validWorkspaceKey(key) || !validCanonicalWorkspacePath(manifest.CurrentPath) {
		return errors.New("managedtemp: workspace manifest identity mismatch")
	}
	return nil
}

func readPrivateFile(root *os.Root, name string) ([]byte, error) {
	if err := validatePrivateFile(root, name); err != nil {
		return nil, err
	}
	return root.ReadFile(name)
}

func allocationID() (string, error) {
	var random [managedIdentifierBytes]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}

func validTestHomeNamespace(marker string) bool {
	commands, workspace, workspaces, namespace := testHomeNamespacePaths(marker)
	if filepath.Base(commands) != "commands" || filepath.Base(workspaces) != "workspaces" || !validPrivateTestHomeDirs(commands, workspace, workspaces, namespace) {
		return false
	}
	gc, err := os.Lstat(filepath.Join(namespace, "gc.lock"))
	if err != nil || !gc.Mode().IsRegular() || gc.Mode()&os.ModeSymlink != 0 || gc.Mode().Perm() != privateFileMode || !ownedByCurrentUser(gc) {
		return false
	}
	_, ok := validTestHomeWorkspaceManifest(workspace)
	return ok
}

func testHomeNamespacePaths(marker string) (commands, workspace, workspaces, namespace string) {
	commands = filepath.Dir(marker)
	workspace = filepath.Dir(commands)
	workspaces = filepath.Dir(workspace)
	namespace = filepath.Dir(workspaces)
	return commands, workspace, workspaces, namespace
}

func validPrivateTestHomeDirs(paths ...string) bool {
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != privateDirMode || !ownedByCurrentUser(info) {
			return false
		}
	}
	return true
}

func validTestHomeWorkspaceManifest(workspace string) (workspaceManifest, bool) {
	workspaceData, err := os.ReadFile(filepath.Join(workspace, "workspace.manifest"))
	if err != nil {
		return workspaceManifest{}, false
	}
	var manifest workspaceManifest
	if json.Unmarshal(workspaceData, &manifest) != nil || manifest.Version != manifestVersion || manifest.Key != filepath.Base(workspace) || !validWorkspaceKey(manifest.Key) || !validCanonicalWorkspacePath(manifest.CurrentPath) {
		return workspaceManifest{}, false
	}
	return manifest, true
}

func validAllocationID(id string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil && len(id) == managedIdentifierLength && len(decoded) == managedIdentifierBytes && base64.RawURLEncoding.EncodeToString(decoded) == id
}

func lockExclusive(file *os.File, nonBlocking bool) error {
	how := syscall.LOCK_EX
	if nonBlocking {
		how |= syscall.LOCK_NB
	}
	return syscall.Flock(int(file.Fd()), how)
}

func unlockClose(file *os.File) error {
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func removeTreeNoLinks(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		info, err := root.Lstat(entry.Name())
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return errors.New("managedtemp: refusing link or special file during removal")
		}
		if info.IsDir() {
			child, err := root.OpenRoot(entry.Name())
			if err != nil {
				return err
			}
			err = removeTreeNoLinks(child)
			if closeErr := child.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return err
			}
		}
		if err := root.Remove(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}
