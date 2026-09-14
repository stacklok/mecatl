//go:build linux || darwin

package privatefile

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

var testHook func(string) error

func supported() bool { return true }

func preflight(path, conventionalPath string, maxBytes int64, validate func([]byte) error) error {
	parent, leaf, err := resolveParent(path, conventionalPath, false)
	if err != nil || parent == nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	before, err := readTarget(parent, leaf, maxBytes)
	if err != nil || !before.exists {
		return err
	}
	return validate(before.data)
}

//nolint:gocyclo // The linear commit protocol keeps pre/post-rename states explicit.
func update(ctx context.Context, path, conventionalPath string, maxBytes int64, mutate Mutate) (CommitState, error) {
	if err := ctx.Err(); err != nil {
		return CommitNotApplied, err
	}
	parent, leaf, err := resolveParent(path, conventionalPath, true)
	if err != nil {
		return CommitNotApplied, err
	}
	defer func() { _ = parent.Close() }()
	before, err := readTarget(parent, leaf, maxBytes)
	if err != nil {
		return CommitNotApplied, err
	}
	out, noop, err := mutate(before.data)
	if err != nil {
		return CommitNotApplied, err
	}
	if err := ctx.Err(); err != nil {
		return CommitNotApplied, err
	}
	if !sameParent(parent) {
		return CommitNotApplied, ErrConfigurationChanged
	}
	var tempName string
	if !noop {
		if int64(len(out)) > maxBytes {
			return CommitNotApplied, errors.New("updated document exceeds size limit")
		}
		tempName = "." + leaf + "-" + rand.Text() + ".tmp"
		fd, err := unix.Openat(int(parent.Fd()), tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			return CommitNotApplied, errors.New("create temporary file")
		}
		temp := os.NewFile(uintptr(fd), tempName)
		defer func() {
			_ = temp.Close()
			_ = unix.Unlinkat(int(parent.Fd()), tempName, 0)
		}()
		if err := temp.Chmod(0o600); err != nil {
			return CommitNotApplied, errors.New("set temporary file mode")
		}
		if _, err := temp.Write(out); err != nil {
			return CommitNotApplied, errors.New("write temporary file")
		}
		if err := temp.Sync(); err != nil {
			return CommitNotApplied, errors.New("sync temporary file")
		}
		if testHook != nil {
			if err := testHook("after-temp-sync"); err != nil {
				return CommitNotApplied, errors.New("prepare replacement")
			}
		}
		if err := temp.Close(); err != nil {
			return CommitNotApplied, errors.New("close temporary file")
		}
	}
	if testHook != nil {
		if err := testHook("before-compare"); err != nil {
			return CommitNotApplied, errors.New("prepare comparison")
		}
	}
	if err := ctx.Err(); err != nil {
		return CommitNotApplied, err
	}
	current, err := readTarget(parent, leaf, maxBytes)
	if err != nil || !sameSnapshot(before, current) || !sameParent(parent) {
		return CommitNotApplied, ErrConfigurationChanged
	}
	if err := ctx.Err(); err != nil {
		return CommitNotApplied, err
	}
	if noop {
		return CommitNoop, nil
	}
	if err := unix.Renameat(int(parent.Fd()), tempName, int(parent.Fd()), leaf); err != nil {
		return CommitNotApplied, errors.New("replace target")
	}
	if testHook != nil {
		if err := testHook("after-rename"); err != nil {
			return CommitReplacementAppliedDurabilityUnknown, errors.New("sync parent after replacement")
		}
	}
	if !sameParent(parent) {
		return CommitReplacementAppliedDurabilityUnknown, errors.New("parent changed after replacement")
	}
	if err := syncAndCloseDir(parent, "parent"); err != nil {
		return CommitReplacementAppliedDurabilityUnknown, errors.New("sync parent after replacement")
	}
	return CommitDurable, nil
}

type snapshot struct {
	data       []byte
	dev, inode uint64
	exists     bool
}

func readTarget(parent *os.File, leaf string, maxBytes int64) (snapshot, error) {
	if testHook != nil {
		if err := testHook("before-target-open"); err != nil {
			return snapshot{}, errors.New("prepare target read")
		}
	}
	// NONBLOCK prevents a substituted FIFO from parking the open before fstat.
	fd, err := unix.Openat(int(parent.Fd()), leaf, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return snapshot{}, nil
	}
	if err != nil {
		return snapshot{}, errors.New("open target file")
	}
	file := os.NewFile(uintptr(fd), leaf)
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return snapshot{}, errors.New("inspect opened target file")
	}
	openedUID, openedUIDOK := ownerUID(openedInfo)
	current, currentOK := currentUID()
	dev, inode, identityOK := fileIdentity(openedInfo)
	if !openedUIDOK || !currentOK || !identityOK || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm() != 0o600 || openedUID != current {
		return snapshot{}, errors.New("target must be a regular owner-only file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return snapshot{}, errors.New("target exceeds size limit or cannot be read")
	}
	return snapshot{data: data, dev: dev, inode: inode, exists: true}, nil
}

func sameSnapshot(a, b snapshot) bool {
	return a.exists == b.exists && a.dev == b.dev && a.inode == b.inode && bytes.Equal(a.data, b.data)
}

func resolveParent(path, conventionalPath string, create bool) (*os.File, string, error) {
	if path == "" {
		return nil, "", errors.New("path is unavailable")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", errors.New("path is unavailable")
	}
	parentPath := filepath.Dir(abs)
	canonical, err := filepath.EvalSymlinks(parentPath)
	var parent *os.File
	if errors.Is(err, os.ErrNotExist) && samePath(abs, conventionalPath) {
		parent, err = conventionalParent(parentPath, create)
		if err != nil {
			return nil, "", err
		}
		if parent == nil {
			return nil, filepath.Base(abs), nil
		}
	} else if err == nil {
		parent, err = openDir(canonical)
	}
	if err != nil {
		return nil, "", errors.New("parent must already exist or be the creatable conventional directory")
	}
	if !sameParent(parent) {
		_ = parent.Close()
		return nil, "", errors.New("parent must be an owner-only non-link directory")
	}
	return parent, filepath.Base(abs), nil
}

func conventionalParent(path string, create bool) (*os.File, error) {
	canonicalBase, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, errors.New("conventional config directory is unavailable")
	}
	base, err := openDir(canonicalBase)
	if err != nil {
		return nil, err
	}
	defer func() { _ = base.Close() }()
	leaf := filepath.Base(path)
	if create {
		if err := unix.Mkdirat(int(base.Fd()), leaf, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, errors.New("create conventional config directory")
		}
	}
	fd, err := unix.Openat(int(base.Fd()), leaf, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if !create && errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("open conventional config directory")
	}
	parent := os.NewFile(uintptr(fd), filepath.Join(canonicalBase, leaf))
	if create {
		// Persist the new directory's link before any target can be committed in it.
		if err := syncAndCloseDir(base, "new-parent"); err != nil {
			_ = parent.Close()
			return nil, errors.New("sync conventional config directory: directory may remain; target was not replaced")
		}
	}
	return parent, nil
}

func openDir(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("open parent directory")
	}
	return os.NewFile(uintptr(fd), path), nil
}

func sameParent(parent *os.File) bool {
	info, err := parent.Stat()
	if err != nil {
		return false
	}
	uid, ok := ownerUID(info)
	current, currentOK := currentUID()
	if !ok || !currentOK || !info.IsDir() || info.Mode().Perm() != 0o700 || uid != current {
		return false
	}
	currentInfo, err := os.Lstat(parent.Name())
	return err == nil && currentInfo.Mode()&os.ModeSymlink == 0 && os.SameFile(info, currentInfo)
}

func samePath(abs, conventional string) bool {
	if conventional == "" {
		return false
	}
	candidate, err := filepath.Abs(conventional)
	return err == nil && filepath.Clean(abs) == filepath.Clean(candidate)
}

func ownerUID(info os.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat.Uid, ok
}

func fileIdentity(info os.FileInfo) (uint64, uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	//nolint:unconvert,gosec // Darwin exposes narrower signed fields; kernel IDs are opaque bit patterns.
	return uint64(stat.Dev), uint64(stat.Ino), true
}

func currentUID() (uint32, bool) {
	uid := os.Geteuid()
	return uint32(uid), uid >= 0 // #nosec G115 -- validity is returned to the caller.
}

func syncAndCloseDir(dir *os.File, stage string) error {
	if testHook != nil {
		if err := testHook("before-" + stage + "-sync"); err != nil {
			return err
		}
	}
	if err := dir.Sync(); err != nil {
		return err
	}
	if testHook != nil {
		if err := testHook("before-" + stage + "-close"); err != nil {
			return err
		}
	}
	return dir.Close()
}
