//go:build linux || darwin

// Package managedtemp owns the private Unix filesystem namespace used by managed
// command temporary storage. It intentionally exposes no deletion primitives.
package managedtemp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	privateDirMode          = 0o700
	privateFileMode         = 0o600
	managedIdentifierBytes  = 12 // 96 bits.
	managedIdentifierLength = 16 // base64.RawURLEncoding characters for 96 bits.
)

// Namespace is a private managed root. Every mutation is relative to root.
type Namespace struct {
	path string
	root *os.Root
}

// Workspace is one private workspace namespace below a Namespace.
type Workspace struct {
	path string
	root *os.Root
}

// Open creates or adopts an absolute managed root. Existing objects are never
// chmodded: ownership, type, link, and mode must already be private.
func Open(path string) (*Namespace, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("managedtemp: managed root must be absolute")
	}
	path = filepath.Clean(path)
	parent, name, err := openParent(path)
	if err != nil {
		return nil, fmt.Errorf("managedtemp: open root parent: %w", err)
	}
	defer func() { _ = parent.Close() }()
	if err := ensurePrivateDir(parent, name); err != nil {
		return nil, fmt.Errorf("managedtemp: managed root: %w", err)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("managedtemp: open managed root: %w", err)
	}
	if err := validatePrivateDir(root, "."); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("managedtemp: validate managed root: %w", err)
	}
	n := &Namespace{path: path, root: root}
	if err := n.initialize(); err != nil {
		_ = root.Close()
		return nil, err
	}
	return n, nil
}

func (n *Namespace) initialize() error {
	if err := ensurePrivateDir(n.root, "workspaces"); err != nil {
		return err
	}
	return ensurePrivateFile(n.root, "gc.lock")
}

// Close releases the root handle. It does not delete managed data.
func (n *Namespace) Close() error {
	if n == nil || n.root == nil {
		return nil
	}
	err := n.root.Close()
	n.root = nil
	return err
}

// Path returns the root path for adapter-internal composition only.
func (n *Namespace) Path() string { return n.path }

// OpenWorkspace creates or adopts the private namespace for one transient workspace
// identity. The digest key, never the raw identity, is used in the path. canonicalPath
// is retained only in the owner-readable manifest for operator diagnosis.
func (n *Namespace) OpenWorkspace(backend, identity, canonicalPath string) (*Workspace, error) {
	if n == nil || n.root == nil {
		return nil, errors.New("managedtemp: namespace is closed")
	}
	if backend == "" || identity == "" {
		return nil, errors.New("managedtemp: workspace backend and identity are required")
	}
	if canonicalPath == "" || !filepath.IsAbs(canonicalPath) || filepath.Clean(canonicalPath) != canonicalPath {
		return nil, errors.New("managedtemp: canonical workspace path is required")
	}
	key := workspaceKey(backend, identity)
	if err := validatePrivateDir(n.root, "workspaces"); err != nil {
		return nil, fmt.Errorf("managedtemp: workspaces: %w", err)
	}
	workspaces, err := n.root.OpenRoot("workspaces")
	if err != nil {
		return nil, fmt.Errorf("managedtemp: open workspaces: %w", err)
	}
	defer func() { _ = workspaces.Close() }()
	if err := ensurePrivateDir(workspaces, key); err != nil {
		return nil, fmt.Errorf("managedtemp: workspace: %w", err)
	}
	root, err := workspaces.OpenRoot(key)
	if err != nil {
		return nil, fmt.Errorf("managedtemp: open workspace: %w", err)
	}
	if err := validatePrivateDir(root, "."); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("managedtemp: validate workspace: %w", err)
	}
	w := &Workspace{path: filepath.Join(n.path, "workspaces", key), root: root}
	if err := ensurePrivateFile(root, "workspace.lock"); err != nil {
		_ = root.Close()
		return nil, err
	}
	workspaceLock, err := root.OpenFile("workspace.lock", os.O_RDWR, 0)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if err := lockExclusive(workspaceLock, false); err != nil {
		_ = workspaceLock.Close()
		_ = root.Close()
		return nil, err
	}
	defer func() { _ = unlockClose(workspaceLock) }()
	manifest, err := workspaceManifestJSON(key, canonicalPath)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if err := w.writeWorkspaceManifest(manifest, canonicalPath); err != nil {
		_ = root.Close()
		return nil, err
	}
	if err := validateWorkspaceManifest(root, key); err != nil {
		_ = root.Close()
		return nil, err
	}
	return w, nil
}

// Path returns the adapter-local path of the private workspace namespace.
func (w *Workspace) Path() string { return w.path }

// Close releases the workspace handle. It does not delete managed data.
func (w *Workspace) Close() error {
	if w == nil || w.root == nil {
		return nil
	}
	err := w.root.Close()
	w.root = nil
	return err
}

// CreatePrivateFile creates an empty private file below the workspace.
func (w *Workspace) CreatePrivateFile(name string, _ []byte) error {
	if w == nil || w.root == nil {
		return errors.New("managedtemp: workspace is closed")
	}
	parent, leaf, err := openPrivateParent(w.root, name)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return ensurePrivateFile(parent, leaf)
}

// WritePrivateFile creates a private file atomically. It refuses an existing
// target rather than changing its mode or treating it as managed state.
func (w *Workspace) WritePrivateFile(name string, data []byte) error {
	if w == nil || w.root == nil {
		return errors.New("managedtemp: workspace is closed")
	}
	return writePrivateFile(w.root, name, data)
}

// WritePrivateFile creates a private root-level protocol record atomically.
func (n *Namespace) WritePrivateFile(name string, data []byte) error {
	if n == nil || n.root == nil {
		return errors.New("managedtemp: namespace is closed")
	}
	return writePrivateFile(n.root, name, data)
}

func workspaceManifestJSON(key, canonicalPath string) ([]byte, error) {
	return json.Marshal(workspaceManifest{Version: manifestVersion, Key: key, CurrentPath: canonicalPath})
}

func (w *Workspace) writeWorkspaceManifest(manifest []byte, canonicalPath string) error {
	info, err := w.root.Lstat("workspace.manifest")
	if errors.Is(err, fs.ErrNotExist) {
		return writePrivateFile(w.root, "workspace.manifest", manifest)
	}
	if err != nil {
		return err
	}
	if err := validatePrivateFileInfo(info); err != nil {
		return err
	}
	data, err := readPrivateFile(w.root, "workspace.manifest")
	if err != nil {
		return err
	}
	var existing workspaceManifest
	if err := json.Unmarshal(data, &existing); err != nil {
		return fmt.Errorf("managedtemp: malformed workspace manifest: %w", err)
	}
	if existing.Version != manifestVersion {
		return ErrUnsupportedVersion
	}
	if existing.Key != filepath.Base(w.path) || !validWorkspaceKey(existing.Key) || !validCanonicalWorkspacePath(existing.CurrentPath) {
		return errors.New("managedtemp: workspace manifest identity mismatch")
	}
	if existing.CurrentPath == canonicalPath {
		return nil
	}
	return replacePrivateFile(w.root, "workspace.manifest", manifest)
}

func validCanonicalWorkspacePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func workspaceKey(backend, identity string) string {
	sum := sha256.Sum256([]byte("mecatl/managed-temp/workspace/v1\x00" + backend + "\x00" + identity))
	return base64.RawURLEncoding.EncodeToString(sum[:managedIdentifierBytes])
}

func validWorkspaceKey(key string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(key)
	return err == nil && len(key) == managedIdentifierLength && len(decoded) == managedIdentifierBytes && base64.RawURLEncoding.EncodeToString(decoded) == key
}

func openParent(path string) (*os.Root, string, error) {
	parent := filepath.Dir(path)
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		return nil, "", errors.New("invalid managed root")
	}
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, "", err
	}
	for _, component := range strings.Split(strings.TrimPrefix(parent, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, err := root.OpenRoot(component)
		if err != nil {
			_ = root.Close()
			return nil, "", err
		}
		_ = root.Close()
		root = next
	}
	return root, name, nil
}

func openPrivateParent(root *os.Root, name string) (*os.Root, string, error) {
	name = filepath.Clean(name)
	if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return nil, "", errors.New("managedtemp: invalid relative name")
	}
	components := strings.Split(name, string(filepath.Separator))
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, "", err
	}
	for _, component := range components[:len(components)-1] {
		if err := ensurePrivateDir(current, component); err != nil {
			_ = current.Close()
			return nil, "", err
		}
		next, err := current.OpenRoot(component)
		_ = current.Close()
		if err != nil {
			return nil, "", err
		}
		if err := validatePrivateDir(next, "."); err != nil {
			_ = next.Close()
			return nil, "", err
		}
		current = next
	}
	return current, components[len(components)-1], nil
}

func ensurePrivateDir(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := root.Mkdir(name, privateDirMode); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return err
	}
	return validatePrivateDirInfo(info)
}

func ensurePrivateFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		file, createErr := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
		if createErr == nil {
			if closeErr := file.Close(); closeErr != nil {
				return closeErr
			}
		} else if !errors.Is(createErr, fs.ErrExist) {
			return createErr
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return err
	}
	return validatePrivateFileInfo(info)
}

func validatePrivateFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	return validatePrivateFileInfo(info)
}

func validatePrivateDir(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	return validatePrivateDirInfo(info)
}

func validatePrivateDirInfo(info fs.FileInfo) error {
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != privateDirMode || !ownedByCurrentUser(info) {
		return errors.New("unsafe ownership, mode, type, or link")
	}
	return nil
}

func validatePrivateFileInfo(info fs.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != privateFileMode || !ownedByCurrentUser(info) {
		return errors.New("unsafe ownership, mode, type, or link")
	}
	return nil
}

func writePrivateFile(root *os.Root, name string, data []byte) error {
	parent, leaf, err := openPrivateParent(root, name)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if _, err := parent.Lstat(leaf); err == nil {
		return errors.New("managedtemp: refusing existing file")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
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
	return parent.Rename(temp, leaf)
}

func privateTemp() (string, error) {
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return "", err
	}
	return ".tmp-" + hex.EncodeToString(random[:]), nil
}

func ownedByCurrentUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	euid := os.Geteuid()
	return euid >= 0 && stat.Uid == uint32(euid) // #nosec G115 -- nonnegative is checked immediately before conversion.
}
