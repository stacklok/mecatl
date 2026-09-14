//go:build linux || darwin

package privatefile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

var testHook func(string) error

func supported() bool { return true }

func preflight(path, conventionalPath string, maxBytes int64, validate func([]byte) error) error {
	parent, leaf, err := resolveParent(path, conventionalPath, false)
	if err != nil {
		return err
	}
	before, err := readTarget(filepath.Join(parent, leaf), maxBytes)
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
	target := filepath.Join(parent, leaf)
	before, err := readTarget(target, maxBytes)
	if err != nil {
		return CommitNotApplied, err
	}
	out, noop, err := mutate(before.data)
	if err != nil {
		return CommitNotApplied, err
	}
	if noop {
		return CommitNoop, nil
	}
	if int64(len(out)) > maxBytes {
		return CommitNotApplied, errors.New("updated document exceeds size limit")
	}
	temp, err := os.CreateTemp(parent, "."+leaf+"-*.tmp")
	if err != nil {
		return CommitNotApplied, errors.New("create temporary file")
	}
	tempName := temp.Name()
	tempOpen := true
	defer func() {
		if tempOpen {
			_ = temp.Close()
		}
		_ = os.Remove(tempName)
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
	if err := ctx.Err(); err != nil {
		return CommitNotApplied, err
	}
	if err := temp.Close(); err != nil {
		return CommitNotApplied, errors.New("close temporary file")
	}
	tempOpen = false
	if testHook != nil {
		if err := testHook("before-compare"); err != nil {
			return CommitNotApplied, errors.New("prepare comparison")
		}
	}
	if err := ctx.Err(); err != nil {
		return CommitNotApplied, err
	}
	current, err := readTarget(target, maxBytes)
	if err != nil || !sameSnapshot(before, current) {
		return CommitNotApplied, ErrConfigurationChanged
	}
	if err := ctx.Err(); err != nil {
		return CommitNotApplied, err
	}
	if err := os.Rename(tempName, target); err != nil {
		return CommitNotApplied, errors.New("replace target")
	}
	if testHook != nil {
		if err := testHook("after-rename"); err != nil {
			return CommitReplacementAppliedDurabilityUnknown, errors.New("sync parent after replacement")
		}
	}
	if err := syncDir(parent); err != nil {
		return CommitReplacementAppliedDurabilityUnknown, errors.New("sync parent after replacement")
	}
	return CommitDurable, nil
}

type snapshot struct {
	data       []byte
	dev, inode uint64
	exists     bool
}

func readTarget(path string, maxBytes int64) (snapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot{}, nil
	}
	if err != nil {
		return snapshot{}, errors.New("inspect target file")
	}
	uid, ok := ownerUID(info)
	current, currentOK := currentUID()
	if !ok || !currentOK || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || uid != current {
		return snapshot{}, errors.New("target must be a regular owner-only file")
	}
	file, err := os.Open(path)
	if err != nil {
		return snapshot{}, errors.New("open target file")
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return snapshot{}, errors.New("inspect opened target file")
	}
	openedUID, openedUIDOK := ownerUID(openedInfo)
	dev, inode, identityOK := fileIdentity(openedInfo)
	if !openedUIDOK || !identityOK || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm() != 0o600 || openedUID != current {
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

func resolveParent(path, conventionalPath string, create bool) (string, string, error) {
	if path == "" {
		return "", "", errors.New("path is unavailable")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", errors.New("path is unavailable")
	}
	parentPath := filepath.Dir(abs)
	parent, err := filepath.EvalSymlinks(parentPath)
	if errors.Is(err, os.ErrNotExist) && samePath(abs, conventionalPath) {
		base, baseErr := filepath.EvalSymlinks(filepath.Dir(parentPath))
		if baseErr != nil {
			return "", "", errors.New("conventional config directory is unavailable")
		}
		parent = filepath.Join(base, filepath.Base(parentPath))
		if !create {
			if _, statErr := os.Lstat(parent); errors.Is(statErr, os.ErrNotExist) {
				return parent, filepath.Base(abs), nil
			}
		} else if mkdirErr := os.Mkdir(parent, 0o700); mkdirErr == nil {
			if syncErr := syncDir(base); syncErr != nil {
				return "", "", errors.New("sync conventional config directory")
			}
		} else if !errors.Is(mkdirErr, os.ErrExist) {
			return "", "", errors.New("create conventional config directory")
		}
		err = nil
	}
	if err != nil {
		return "", "", errors.New("parent must already exist")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return "", "", errors.New("parent is unavailable")
	}
	uid, ok := ownerUID(info)
	current, currentOK := currentUID()
	if !ok || !currentOK || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || uid != current {
		return "", "", errors.New("parent must be an owner-only non-link directory")
	}
	return filepath.Clean(parent), filepath.Base(abs), nil
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
	//nolint:unconvert // Darwin exposes these fields with narrower integer types.
	return uint64(stat.Dev), uint64(stat.Ino), true
}

func currentUID() (uint32, bool) {
	uid := os.Geteuid()
	return uint32(uid), uid >= 0 // #nosec G115 -- validity is returned to the caller.
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}
