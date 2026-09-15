package boxenv

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
)

var _ tool.WorkspaceNamespace = (*Workspace)(nil)

// ReadDir lists the immediate entries in p within the Box workspace.
func (w *Workspace) ReadDir(ctx context.Context, p string) ([]tool.FileInfo, error) {
	dir, err := cleanDirPath(p)
	if err != nil {
		return nil, err
	}
	q := shellQuote(dir)
	cmd := "if [ " + q + " != '.' ] && [ ! -e " + q + " ] && [ ! -L " + q + " ]; then exit 44; fi; " +
		"if [ ! -d " + q + " ]; then exit 46; fi; " +
		"find " + q + " -mindepth 1 -maxdepth 1 -printf '%f\\0%y\\0%s\\0%T@\\0'"
	result, err := w.client.command(ctx, w.boxID, commandRequest{Command: cmd, Cwd: boxWorkspace, TimeoutSeconds: 20})
	if err != nil {
		return nil, err
	}
	switch result.ExitCode {
	case 44:
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: fs.ErrNotExist}
	case 46:
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: fs.ErrInvalid}
	case 0:
	default:
		return nil, fmt.Errorf("boxenv: readdir %q exited %d", p, result.ExitCode)
	}
	if result.Stdout == "" {
		return []tool.FileInfo{}, nil
	}
	fields := strings.Split(result.Stdout, "\x00")
	if len(fields) > 0 && fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%4 != 0 {
		return nil, fmt.Errorf("boxenv: malformed readdir response")
	}
	entries := make([]tool.FileInfo, 0, len(fields)/4)
	for i := 0; i < len(fields); i += 4 {
		size, err := strconv.ParseInt(fields[i+2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("boxenv: parse readdir size: %w", err)
		}
		seconds, err := strconv.ParseFloat(fields[i+3], 64)
		if err != nil {
			return nil, fmt.Errorf("boxenv: parse readdir mtime: %w", err)
		}
		isDir := fields[i+1] == "d"
		mode := fs.FileMode(0o644)
		if isDir {
			mode = fs.ModeDir | 0o755
		}
		entries = append(entries, tool.FileInfo{
			Name: fields[i], Size: size, Mode: mode,
			ModTime: time.Unix(0, int64(seconds*float64(time.Second))), IsDir: isDir,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// Remove deletes p without recursively removing non-empty directories.
func (w *Workspace) Remove(ctx context.Context, p string) error {
	key, err := cleanPath(p)
	if err != nil {
		return err
	}
	q := shellQuote(key)
	cmd := "if [ ! -e " + q + " ] && [ ! -L " + q + " ]; then exit 44; fi; " +
		"if [ -d " + q + " ] && [ ! -L " + q + " ]; then rmdir -- " + q + " 2>/dev/null || exit 45; else rm -f -- " + q + " || exit 46; fi"
	w.mu.Lock()
	defer w.mu.Unlock()
	result, err := w.client.command(ctx, w.boxID, commandRequest{Command: cmd, Cwd: boxWorkspace, TimeoutSeconds: 10})
	if err != nil {
		return err
	}
	switch result.ExitCode {
	case 0:
		return nil
	case 44:
		return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrNotExist}
	case 45:
		return &fs.PathError{Op: "remove", Path: p, Err: tool.ErrDirectoryNotEmpty}
	default:
		return fmt.Errorf("boxenv: remove %q exited %d", p, result.ExitCode)
	}
}

// Rename moves oldPath to newPath without overwriting an existing destination.
func (w *Workspace) Rename(ctx context.Context, oldPath, newPath string) error {
	oldKey, err := cleanPath(oldPath)
	if err != nil {
		return err
	}
	newKey, err := cleanPath(newPath)
	if err != nil {
		return err
	}
	oldQ, newQ := shellQuote(oldKey), shellQuote(newKey)
	parentQ := shellQuote(path.Dir(newKey))
	cmd := "if [ ! -e " + oldQ + " ] && [ ! -L " + oldQ + " ]; then exit 44; fi; " +
		"if [ -e " + newQ + " ] || [ -L " + newQ + " ]; then exit 45; fi; " +
		"mkdir -p -- " + parentQ + " || exit 46; mv -- " + oldQ + " " + newQ + " || exit 46"
	w.mu.Lock()
	defer w.mu.Unlock()
	result, err := w.client.command(ctx, w.boxID, commandRequest{Command: cmd, Cwd: boxWorkspace, TimeoutSeconds: 20})
	if err != nil {
		return err
	}
	switch result.ExitCode {
	case 0:
		return nil
	case 44:
		return &fs.PathError{Op: "rename", Path: oldPath, Err: fs.ErrNotExist}
	case 45:
		return &fs.PathError{Op: "rename", Path: newPath, Err: fs.ErrExist}
	default:
		return fmt.Errorf("boxenv: rename %q to %q exited %d", oldPath, newPath, result.ExitCode)
	}
}

// CopyFile copies source to a new destination and returns the destination version.
func (w *Workspace) CopyFile(ctx context.Context, source, destination string) (tool.FileVersion, error) {
	srcKey, err := cleanPath(source)
	if err != nil {
		return tool.FileVersion{}, err
	}
	dstKey, err := cleanPath(destination)
	if err != nil {
		return tool.FileVersion{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := w.client.readFile(ctx, w.boxID, boxPath(srcKey))
	if err != nil {
		return tool.FileVersion{}, err
	}
	if _, err := w.client.readFile(ctx, w.boxID, boxPath(dstKey)); err == nil {
		return tool.FileVersion{}, &fs.PathError{Op: "copy", Path: destination, Err: fs.ErrExist}
	} else if !isFileMissing(err) {
		return tool.FileVersion{}, err
	}
	if err := w.ensureParent(ctx, dstKey); err != nil {
		return tool.FileVersion{}, err
	}
	if err := w.client.writeFile(ctx, w.boxID, boxPath(dstKey), data); err != nil {
		return tool.FileVersion{}, err
	}
	return versionOf(data), nil
}

func cleanDirPath(p string) (string, error) {
	if p == "" || p == "." {
		return ".", nil
	}
	return cleanPath(p)
}

func isFileMissing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || isMissingFileError(err)
}
