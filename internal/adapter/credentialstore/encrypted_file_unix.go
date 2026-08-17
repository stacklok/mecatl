//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package credentialstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
)

func canonicalPrivateRoot(root string) (string, error) {
	info, err := os.Lstat(root)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", unavailable("validate credential root", errors.New("unsafe path component"))
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", unavailable("inspect credential root", err)
	}

	var missing []string
	ancestor := root
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", unavailable("inspect credential root", err)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", unavailable("validate credential root", errors.New("no existing ancestor"))
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	physical, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", unavailable("resolve credential root ancestor", err)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		physical = filepath.Join(physical, missing[i])
	}
	return physical, nil
}

func ensurePrivateRoot(root string, syncDir func(*os.File) error) error {
	canonicalRoot, err := canonicalPrivateRoot(root)
	if err != nil {
		return err
	}
	root = canonicalRoot
	volume := filepath.VolumeName(root)
	current := volume + string(os.PathSeparator)
	rel := root[len(current):]
	for _, component := range splitPath(rel) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			created := false
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return unavailable("create credential root", err)
			} else if err == nil {
				created = true
			}
			if created {
				if err := syncContainingDirectory(current, syncDir); err != nil {
					return unavailable("sync credential root parent", err)
				}
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return unavailable("inspect credential root", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return unavailable("validate credential root", errors.New("unsafe path component"))
		}
	}
	info, err := os.Lstat(root)
	if err != nil {
		return unavailable("inspect credential root", err)
	}
	return validatePrivateDirInfo(info)
}

func splitPath(path string) []string {
	var out []string
	for path != "." && path != "" {
		dir, base := filepath.Split(path)
		if base != "" {
			out = append([]string{base}, out...)
		}
		path = filepath.Clean(dir)
		if path == string(os.PathSeparator) {
			break
		}
	}
	return out
}

func ensurePrivateDir(root *os.Root, name string, syncDir func(*os.File) error) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		created := false
		if err := root.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return unavailable("create credential namespace", err)
		} else if err == nil {
			created = true
		}
		if created {
			dir, openErr := root.Open(".")
			if openErr != nil {
				return unavailable("open credential root for sync", openErr)
			}
			syncErr := syncDir(dir)
			closeErr := dir.Close()
			if syncErr != nil {
				return unavailable("sync credential root", syncErr)
			}
			if closeErr != nil {
				return unavailable("close credential root after sync", closeErr)
			}
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return unavailable("inspect credential namespace", err)
	}
	return validatePrivateDirInfo(info)
}

func ensurePrivateFile(root *os.Root, name string, create bool) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && create {
		file, createErr := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr == nil {
			if closeErr := file.Close(); closeErr != nil {
				return unavailable("close credential lock", closeErr)
			}
		} else if !errors.Is(createErr, os.ErrExist) {
			return unavailable("create credential lock", createErr)
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return unavailable("inspect credential file", err)
	}
	return validatePrivateFileInfo(info)
}

func validatePrivateDirInfo(info os.FileInfo) error {
	uid, _, ok := unixIdentity(info)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0o700 || !currentEUID(uid) {
		return unavailable("validate credential directory", errors.New("unsafe ownership, mode, or type"))
	}
	return nil
}

func validatePrivateFileHandle(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return unavailable("inspect credential file", err)
	}
	return validatePrivateFileInfo(info)
}

func validatePrivateFileInfo(info os.FileInfo) error {
	uid, links, ok := unixIdentity(info)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !currentEUID(uid) || links != 1 {
		return unavailable("validate credential file", errors.New("unsafe ownership, mode, type, or link count"))
	}
	return nil
}

func unixIdentity(info os.FileInfo) (uint32, uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return stat.Uid, reflect.ValueOf(stat.Nlink).Uint(), true
}

func currentEUID(uid uint32) bool {
	euid := os.Geteuid()
	return euid >= 0 && uid == uint32(euid) // #nosec G115 -- nonnegative is checked immediately before conversion.
}

func syncContainingDirectory(path string, syncDir func(*os.File) error) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}

func syncDirectory(dir *os.File) error {
	if err := dir.Sync(); err != nil {
		if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
			return nil
		}
		return fmt.Errorf("directory sync: %w", err)
	}
	return nil
}
