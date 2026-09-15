package boxenv

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/stacklok/mecatl/engine/tool"
)

// Workspace is a path-confined Box file workspace. Mutation CAS is serialized
// for one live Workspace handle and versioned with SHA-256, matching the core
// Workspace contract. Shell or another independently reattached handle remains
// a non-cooperating writer, the same residual documented by tool.Workspace.
type Workspace struct {
	client *apiClient
	boxID  string
	mu     sync.Mutex
}

var _ tool.Workspace = (*Workspace)(nil)

// Root returns the logical root exposed to Mecatl tools.
func (*Workspace) Root() string { return workspaceRoot }

// Read returns the contents of p from the Box workspace.
func (w *Workspace) Read(ctx context.Context, p string) ([]byte, error) {
	key, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	return w.client.readFile(ctx, w.boxID, boxPath(key))
}

// ReadVersion returns file contents and their opaque SHA-256 version.
func (w *Workspace) ReadVersion(ctx context.Context, p string) ([]byte, tool.FileVersion, error) {
	data, err := w.Read(ctx, p)
	if err != nil {
		return nil, tool.FileVersion{}, err
	}
	return data, versionOf(data), nil
}

// CreateFile creates p only when no file already exists at that path.
func (w *Workspace) CreateFile(ctx context.Context, p string, data []byte) (tool.FileVersion, error) {
	key, err := cleanPath(p)
	if err != nil {
		return tool.FileVersion{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.client.readFile(ctx, w.boxID, boxPath(key)); err == nil {
		return tool.FileVersion{}, &fs.PathError{Op: "create", Path: p, Err: fs.ErrExist}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return tool.FileVersion{}, err
	}
	if err := w.ensureParent(ctx, key); err != nil {
		return tool.FileVersion{}, err
	}
	if err := w.client.writeFile(ctx, w.boxID, boxPath(key), data); err != nil {
		return tool.FileVersion{}, err
	}
	return versionOf(data), nil
}

func (w *Workspace) ensureParent(ctx context.Context, key string) error {
	dir := path.Dir(key)
	if dir == "." {
		return nil
	}
	result, err := w.client.command(ctx, w.boxID, commandRequest{
		Command:        "mkdir -p -- " + shellQuote(dir),
		Cwd:            boxWorkspace,
		TimeoutSeconds: 10,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("boxenv: create parent directory exited %d", result.ExitCode)
	}
	return nil
}

// ReplaceFile atomically replaces p relative to this Workspace handle when old matches.
func (w *Workspace) ReplaceFile(ctx context.Context, p string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	key, err := cleanPath(p)
	if err != nil {
		return tool.FileVersion{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	current, err := w.client.readFile(ctx, w.boxID, boxPath(key))
	if err != nil {
		return tool.FileVersion{}, err
	}
	if !versionOf(current).Equal(old) {
		return tool.FileVersion{}, &tool.VersionMismatchError{Path: p}
	}
	if err := w.client.writeFile(ctx, w.boxID, boxPath(key), data); err != nil {
		return tool.FileVersion{}, err
	}
	return versionOf(data), nil
}

// Stat returns metadata for p from the Box workspace.
func (w *Workspace) Stat(ctx context.Context, p string) (tool.FileInfo, error) {
	key, err := cleanPath(p)
	if err != nil {
		return tool.FileInfo{}, err
	}
	q := shellQuote(key)
	cmd := "if [ ! -e " + q + " ] && [ ! -L " + q + " ]; then exit 44; fi; if [ -d " + q + " ]; then printf 'd\\n'; else printf 'f\\n'; fi; stat -c '%a\\n%s\\n%Y' -- " + q
	result, err := w.client.command(ctx, w.boxID, commandRequest{Command: cmd, Cwd: boxWorkspace, TimeoutSeconds: 10})
	if err != nil {
		return tool.FileInfo{}, err
	}
	if result.ExitCode == 44 {
		return tool.FileInfo{}, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
	}
	if result.ExitCode != 0 {
		return tool.FileInfo{}, fmt.Errorf("boxenv: stat %q exited %d", p, result.ExitCode)
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) < 4 {
		return tool.FileInfo{}, fmt.Errorf("boxenv: malformed stat response")
	}
	perm, err := strconv.ParseUint(lines[1], 8, 32)
	if err != nil {
		return tool.FileInfo{}, fmt.Errorf("boxenv: parse stat mode: %w", err)
	}
	size, err := strconv.ParseInt(lines[2], 10, 64)
	if err != nil {
		return tool.FileInfo{}, fmt.Errorf("boxenv: parse stat size: %w", err)
	}
	mtime, err := strconv.ParseInt(lines[3], 10, 64)
	if err != nil {
		return tool.FileInfo{}, fmt.Errorf("boxenv: parse stat mtime: %w", err)
	}
	isDir := lines[0] == "d"
	mode := fs.FileMode(perm)
	if isDir {
		mode |= fs.ModeDir
	}
	return tool.FileInfo{Name: path.Base(key), Size: size, Mode: mode, ModTime: time.Unix(mtime, 0), IsDir: isDir}, nil
}

// Glob returns sorted file paths matching pattern within the Box workspace.
func (w *Workspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	pat := normalizeGlobPattern(pattern)
	if pat == "" {
		return nil, nil
	}
	// Validate before doing remote I/O.
	if _, err := doublestar.Match(pat, "probe"); err != nil {
		return nil, err
	}
	result, err := w.client.command(ctx, w.boxID, commandRequest{Command: `find . -type f -print0`, Cwd: boxWorkspace, TimeoutSeconds: 30})
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("boxenv: glob inventory exited %d", result.ExitCode)
	}
	var out []string
	for _, raw := range strings.Split(result.Stdout, "\x00") {
		name := strings.TrimPrefix(raw, "./")
		if name == "" {
			continue
		}
		ok, err := doublestar.Match(pat, name)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Grep returns regexp matches from files selected by pathGlob.
func (w *Workspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	if pathGlob == "" {
		pathGlob = "**"
	}
	files, err := w.Glob(ctx, pathGlob)
	if err != nil {
		return nil, err
	}
	const maxMatches = 1000
	out := make([]tool.GrepMatch, 0)
	for _, name := range files {
		data, err := w.Read(ctx, name)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64*1024), 4<<20)
		line := 0
		for scanner.Scan() {
			line++
			text := scanner.Text()
			if re.MatchString(text) {
				out = append(out, tool.GrepMatch{Path: name, Line: line, Text: text})
				if len(out) >= maxMatches {
					return out, nil
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Runner executes shell commands in the same Box workspace namespace as Workspace.
type Runner struct {
	client *apiClient
	boxID  string
}

var _ tool.CommandRunner = (*Runner)(nil)

// BoundWorkspaceRoot reports the workspace root used for command execution.
func (*Runner) BoundWorkspaceRoot() string { return workspaceRoot }

// Run executes command inside the Box workspace and returns its captured result.
func (r *Runner) Run(ctx context.Context, command string) (tool.CommandResult, error) {
	timeout := 60
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return tool.CommandResult{}, ctx.Err()
		}
		timeout = int((remaining + time.Second - 1) / time.Second)
		if timeout < 1 {
			timeout = 1
		}
		if timeout > 60 {
			timeout = 60
		}
	}
	result, err := r.client.command(ctx, r.boxID, commandRequest{Command: command, Cwd: boxWorkspace, TimeoutSeconds: timeout})
	if err != nil {
		return tool.CommandResult{}, err
	}
	return tool.CommandResult{Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: result.ExitCode}, nil
}

func cleanPath(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, '\x00') || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: %q", ErrPathEscape, p)
		}
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, p)
	}
	return cleaned, nil
}

func boxPath(key string) string { return boxWorkspace + "/" + key }

func versionOf(data []byte) tool.FileVersion {
	sum := sha256.Sum256(data)
	return tool.NewFileVersion(hex.EncodeToString(sum[:]))
}

func normalizeGlobPattern(pattern string) string {
	pat := strings.TrimPrefix(pattern, "/")
	for strings.HasPrefix(pat, "./") {
		pat = pat[2:]
	}
	pat = strings.TrimPrefix(pat, "/")
	if pat == "." {
		return ""
	}
	return pat
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
