// Package virtiofs builds the explicit host/guest mount plan for a microVM.
package virtiofs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	gomicrovm "github.com/stacklok/go-microvm"

	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

const (
	// GuestSkillAssets is the only guest namespace used for materialized skill payloads.
	GuestSkillAssets = "/run/mecatl/skill-assets"

	workspaceTag = "mecatl-workspace"
	metadataTag  = "mecatl-git-metadata"
	objectsTag   = "mecatl-git-objects"
	assetsTag    = "mecatl-skill-assets"
)

var (
	// ErrUnsafeMount rejects an absent, symlinked, overlapping, or otherwise
	// implicit host mount input.
	ErrUnsafeMount = errors.New("unsafe virtio-fs mount input")
)

// Asset names one payload file to copy into a session-owned materialization.
// Name is its logical slash-separated guest name; SourcePath is never mounted.
type Asset struct {
	Name       string
	SourcePath string
}

// GoMicroVMRuntime is the legacy test seam for the mount plan. Production uses
// GoMicroVMMounts and go-microvm v0.0.40's host-enforced ReadOnly field directly.
type GoMicroVMRuntime interface {
	SetGuestVirtioFSMount(tag, guestPath string) error
	AddVirtioFS(tag, hostPath string) error
	AddVirtioFS3(tag, hostPath string, daxWindowSize uint64, readOnly bool) error
}

type access uint8

const (
	readWrite access = iota
	hostReadOnly
)

type mount struct {
	tag, hostPath, guestPath string
	access                   access
}

// MountPlan is an ordered, validated set of virtio-fs devices. Reconstructed
// metadata is mounted separately and guest exec pins GIT_DIR to that path, so
// the linked worktree's host-path .git file is never consumed.
type MountPlan struct {
	mounts []mount
}

// MaterializedAssets is a session-owned copy of explicitly named skill files.
// Its host path is intentionally private so callers cannot turn an arbitrary
// directory (for example, a user home) into the skill-assets capability.
type MaterializedAssets struct {
	root string
}

// Plan builds the fixed worktree/Git mount set and, when non-nil, one
// session-owned materialized skill-assets mount.
func Plan(prepared *worktree.Prepared, assets *MaterializedAssets) (MountPlan, error) {
	if prepared == nil {
		return MountPlan{}, fmt.Errorf("%w: nil prepared worktree", ErrUnsafeMount)
	}
	inputs := []mount{
		{tag: workspaceTag, hostPath: prepared.WorktreePath, guestPath: worktree.GuestWorkspace, access: readWrite},
		{tag: metadataTag, hostPath: prepared.MetadataPath, guestPath: worktree.GuestMetadata, access: readWrite},
		{tag: objectsTag, hostPath: prepared.CommonObjectStore, guestPath: worktree.GuestObjectStore, access: hostReadOnly},
	}
	if assets != nil {
		inputs = append(inputs, mount{tag: assetsTag, hostPath: assets.root, guestPath: GuestSkillAssets, access: hostReadOnly})
	}

	seenHost := make(map[string]string, len(inputs))
	seenGuest := make(map[string]struct{}, len(inputs))
	for i := range inputs {
		canonical, err := canonicalRealDir(inputs[i].hostPath)
		if err != nil {
			return MountPlan{}, fmt.Errorf("%w: %s: %v", ErrUnsafeMount, inputs[i].tag, err)
		}
		inputs[i].hostPath = canonical
		if other, exists := seenHost[canonical]; exists {
			return MountPlan{}, fmt.Errorf("%w: %s and %s share a host path", ErrUnsafeMount, other, inputs[i].tag)
		}
		seenHost[canonical] = inputs[i].tag
		if _, exists := seenGuest[inputs[i].guestPath]; exists {
			return MountPlan{}, fmt.Errorf("%w: duplicate guest path %s", ErrUnsafeMount, inputs[i].guestPath)
		}
		seenGuest[inputs[i].guestPath] = struct{}{}
	}
	return MountPlan{mounts: inputs}, nil
}

// PrepareWorkloadAccess preserves host ownership and modes. The Linux libkrun
// backend maps guest workload UID/GID 65532 to the daemon user through its
// unprivileged user namespace, so making host trees world-accessible is neither
// necessary nor permitted.
func (MountPlan) PrepareWorkloadAccess() error { return nil }

// GoMicroVMMounts projects the validated plan onto go-microvm v0.0.40's
// host-enforced ReadOnly mount contract.
func (p MountPlan) GoMicroVMMounts() []gomicrovm.VirtioFSMount {
	mounts := make([]gomicrovm.VirtioFSMount, 0, len(p.mounts))
	for _, mount := range p.mounts {
		mounts = append(mounts, gomicrovm.VirtioFSMount{
			Tag: mount.tag, HostPath: mount.hostPath, ReadOnly: mount.access == hostReadOnly,
		})
	}
	return mounts
}

// Configure registers every guest target and creates its libkrun device. A
// read-only device always uses krun_add_virtiofs3 with read_only=true; it never
// falls back to go-microvm's guest-only ReadOnly flag.
func (p MountPlan) Configure(runtime GoMicroVMRuntime) error {
	if runtime == nil {
		return errors.New("virtio-fs runtime is nil")
	}
	for _, m := range p.mounts {
		if err := runtime.SetGuestVirtioFSMount(m.tag, m.guestPath); err != nil {
			return fmt.Errorf("register guest virtio-fs mount %s: %w", m.tag, err)
		}
		var err error
		if m.access == hostReadOnly {
			err = runtime.AddVirtioFS3(m.tag, m.hostPath, 0, true)
		} else {
			err = runtime.AddVirtioFS(m.tag, m.hostPath)
		}
		if err != nil {
			return fmt.Errorf("add virtio-fs device %s: %w", m.tag, err)
		}
	}
	return nil
}

// MaterializeAssets copies only the explicitly named regular files into a new
// session-owned directory. The source tree (and therefore a user's home or
// config directory) is never itself a mount input.
func MaterializeAssets(destination string, assets []Asset) (_ *MaterializedAssets, retErr error) {
	if len(assets) == 0 {
		return nil, errors.New("materialize skill assets: no assets")
	}
	root, err := newDirectoryPath(destination)
	if err != nil {
		return nil, fmt.Errorf("materialize skill assets: %w", err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return nil, fmt.Errorf("materialize skill assets: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(root)
		}
	}()

	ordered := append([]Asset(nil), assets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	seen := make(map[string]struct{}, len(ordered))
	for _, asset := range ordered {
		if err := validAssetName(asset.Name); err != nil {
			return nil, fmt.Errorf("materialize skill asset: %w", err)
		}
		if _, exists := seen[asset.Name]; exists {
			return nil, fmt.Errorf("materialize skill asset: duplicate name %q", asset.Name)
		}
		seen[asset.Name] = struct{}{}
		if err := copyRegularFile(asset.SourcePath, filepath.Join(root, filepath.FromSlash(asset.Name))); err != nil {
			return nil, fmt.Errorf("materialize skill asset %q: %w", asset.Name, err)
		}
	}
	return &MaterializedAssets{root: root}, nil
}

func copyRegularFile(source, destination string) (retErr error) {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("source must be a regular file, not a symlink or directory")
	}
	in, err := os.Open(source) // #nosec G304 -- explicit operator-admitted asset path; identity is checked below.
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, in.Close()) }()
	opened, err := in.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return errors.New("source changed while materializing")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	return errors.Join(copyErr, closeErr)
}

func validAssetName(name string) error {
	if name == "" || strings.Contains(name, "\\") || path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return fmt.Errorf("unsafe logical name %q", name)
	}
	return nil
}

func newDirectoryPath(name string) (string, error) {
	if name == "" {
		return "", errors.New("destination is required")
	}
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(absolute); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return "", errors.New("destination already exists")
		}
		return "", err
	}
	parent, err := canonicalRealDir(filepath.Dir(absolute))
	if err != nil {
		return "", fmt.Errorf("destination parent: %w", err)
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

func canonicalRealDir(name string) (string, error) {
	if name == "" {
		return "", errors.New("path is required")
	}
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path is not a real directory")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	if filepath.Clean(absolute) != canonical {
		return "", errors.New("path traverses a symlink")
	}
	return canonical, nil
}
