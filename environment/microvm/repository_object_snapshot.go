package microvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	defaultRepositoryObjectSnapshotBytes   int64 = 20 << 30
	defaultRepositoryObjectSnapshotEntries int64 = 1_000_000
)

type objectSnapshotLimit struct {
	bytes   int64
	entries int64
}

func snapshotRepositoryObjects(ctx context.Context, common, destinationParent string) (string, error) {
	return snapshotRepositoryObjectsWithOwnership(ctx, common, destinationParent, prepareRepositoryOwnership)
}

func snapshotRepositoryObjectsWithOwnership(ctx context.Context, common, destinationParent string, prepareOwnership repositoryOwnershipPreparer) (_ string, retErr error) {
	commonDir, err := openAbsoluteDirectoryNoSymlinks(common)
	if err != nil {
		return "", errors.New("repository Git common directory is not a real directory")
	}
	defer func() { _ = commonDir.Close() }()

	objectsFD, err := openatOpaque(int(commonDir.Fd()), "objects", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", errors.New("repository Git object store is not a real directory")
	}
	objects := os.NewFile(uintptr(objectsFD), "objects")
	defer func() { _ = objects.Close() }()

	snapshot, err := os.MkdirTemp(destinationParent, ".git-objects-")
	if err != nil {
		return "", fmt.Errorf("create private Git object snapshot: %w", err)
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, removeObjectSnapshot(snapshot))
		}
	}()
	// The snapshot must remain traversable while it is populated; its parent is
	// already owner-only and makeObjectSnapshotReadOnly removes write access.
	if err := os.Chmod(snapshot, 0o700); err != nil { //nolint:gosec // G302: owner-only directory, not a file
		return "", err
	}
	destination, err := os.OpenRoot(snapshot)
	if err != nil {
		return "", err
	}
	defer func() { _ = destination.Close() }()

	limit := objectSnapshotLimit{bytes: defaultRepositoryObjectSnapshotBytes, entries: defaultRepositoryObjectSnapshotEntries}
	if err := copyObjectDirectory(ctx, objects, destination, ".", &limit); err != nil {
		return "", fmt.Errorf("snapshot repository Git objects: %w", err)
	}
	if err := prepareOwnership(ctx, snapshot, "."); err != nil {
		return "", fmt.Errorf("prepare guest ownership for repository Git object snapshot: %w", err)
	}
	if err := makeObjectSnapshotReadOnly(snapshot); err != nil {
		return "", err
	}
	return snapshot, nil
}

func openAbsoluteDirectoryNoSymlinks(name string) (*os.File, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, errors.New("path is not clean and absolute")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), string(filepath.Separator))
	for _, component := range strings.Split(strings.TrimPrefix(name, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		nextFD, openErr := openatOpaque(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		_ = current.Close()
		current = os.NewFile(uintptr(nextFD), component)
	}
	return current, nil
}

func copyObjectDirectory(ctx context.Context, source *os.File, destination *os.Root, relative string, limit *objectSnapshotLimit) error {
	if strings.Count(relative, string(filepath.Separator)) >= 16 {
		return errors.New("git object snapshot directory depth exceeded")
	}
	dupFD, err := unix.Dup(int(source.Fd()))
	if err != nil {
		return err
	}
	reader := os.NewFile(uintptr(dupFD), source.Name())
	entries, err := reader.ReadDir(-1)
	_ = reader.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := copyObjectEntry(ctx, source, destination, relative, entry, limit); err != nil {
			return err
		}
	}
	return nil
}

func copyObjectEntry(ctx context.Context, source *os.File, destination *os.Root, relative string, entry os.DirEntry, limit *objectSnapshotLimit) (retErr error) {
	name := entry.Name()
	fd, err := openatOpaque(int(source.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open object entry %q: %w", name, err)
	}
	input := os.NewFile(uintptr(fd), name)
	defer func() { retErr = errors.Join(retErr, input.Close()) }()

	info, err := input.Stat()
	if err != nil {
		return err
	}
	limit.entries--
	if limit.entries < 0 {
		return errors.New("git object snapshot entry limit exceeded")
	}

	target := filepath.Join(relative, name)
	switch {
	case info.IsDir():
		if err := destination.Mkdir(target, 0o700); err != nil {
			return err
		}
		return copyObjectDirectory(ctx, input, destination, target, limit)
	case info.Mode().IsRegular():
		return copyObjectFile(ctx, input, destination, target, info.Size(), limit)
	default:
		return fmt.Errorf("git object entry %q is not a private regular file or directory", target)
	}
}

func copyObjectFile(ctx context.Context, input *os.File, destination *os.Root, target string, size int64, limit *objectSnapshotLimit) (retErr error) {
	if target == filepath.Join("info", "alternates") || target == filepath.Join("info", "http-alternates") {
		return errors.New("git object alternates are not permitted")
	}
	if size < 0 || size > limit.bytes {
		return errors.New("git object snapshot byte limit exceeded")
	}
	limit.bytes -= size

	output, err := destination.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, output.Close()) }()
	copied, err := io.Copy(output, io.LimitReader(contextReader{ctx: ctx, reader: input}, size+1))
	if err != nil {
		return err
	}
	if copied != size {
		return errors.New("git object changed while snapshotting")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func removeObjectSnapshot(root string) error {
	_ = chmodObjectSnapshot(root, 0o700, 0o600)
	return os.RemoveAll(root)
}

func makeObjectSnapshotReadOnly(root string) error {
	// Directories stay owner-writable so lifecycle cleanup can remove immutable
	// object files; every copied object itself is read-only.
	return chmodObjectSnapshot(root, 0o700, 0o400) //nolint:gosec // G302: owner-only directory, not a file
}

func chmodObjectSnapshot(name string, directoryMode, fileMode os.FileMode) error {
	root, err := os.OpenRoot(name)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := fileMode
		if entry.IsDir() {
			mode = directoryMode
		}
		return root.Chmod(path, mode)
	})
}
