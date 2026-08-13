// Package memfs implements an in-memory tool.FileSystem and tool.Workspace
// (map-backed) for fast, offline FS-tool tests. It enforces the same path-escape
// rejection and the same Edit read-ledger semantics as the osfs adapter, and
// performs Grep over the in-memory contents.
//
// memfs has no shell, so its Workspace deliberately does NOT execute commands.
// For deterministic Bash-tool stubbing it exposes a separate, programmable
// tool.CommandRunner (see CommandRunner / NewCommandRunner): by default Run
// returns ErrNoShell so tests cannot accidentally depend on shell behavior, and
// a canned result may be programmed via SetResult.
package memfs

import (
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
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/stacklok/mecatl/engine/tool"
)

// ErrPathEscape is returned when a session-relative path resolves outside the
// Workspace root.
var ErrPathEscape = errors.New("memfs: path escapes workspace root")

// ErrNotExist is returned when a file is not present in the in-memory store.
var ErrNotExist = fs.ErrNotExist

// ErrNoShell is returned by CommandRunner.Run when no canned result has been
// programmed: memfs has no shell to run commands against. It wraps
// tool.ErrNoShell so callers can match either sentinel.
var ErrNoShell = fmt.Errorf("memfs: %w; program a result with SetResult", tool.ErrNoShell)

// node is a single in-memory file entry.
type node struct {
	data    []byte
	modTime time.Time
}

// FileSystem is an in-memory, map-backed tool.FileSystem. Paths are normalized
// to clean, slash-separated, root-relative keys. It is safe for concurrent use.
type FileSystem struct {
	root string

	mu    sync.RWMutex
	files map[string]*node
	now   func() time.Time
}

// NewFileSystem returns an empty in-memory FileSystem with the given (logical)
// root. The root is used only for Root() reporting and escape checks; no real
// directory is created.
func NewFileSystem(root string) *FileSystem {
	if root == "" {
		root = "/"
	}
	return &FileSystem{
		root:  path.Clean(root),
		files: make(map[string]*node),
		now:   time.Now,
	}
}

// Root returns the logical workspace root.
func (f *FileSystem) Root() string { return f.root }

// Read returns the entire contents of the file at the session-relative path.
func (f *FileSystem) Read(_ context.Context, p string) ([]byte, error) {
	key, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	n, ok := f.files[key]
	if !ok {
		return nil, fmt.Errorf("memfs: open %q: %w", p, ErrNotExist)
	}
	out := make([]byte, len(n.data))
	copy(out, n.data)
	return out, nil
}

// Write replaces the contents of the file at the session-relative path, creating
// it if needed.
func (f *FileSystem) Write(_ context.Context, p string, data []byte) error {
	key, err := cleanPath(p)
	if err != nil {
		return err
	}
	stored := make([]byte, len(data))
	copy(stored, data)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[key] = &node{data: stored, modTime: f.now()}
	return nil
}

// Stat returns metadata for the file at the session-relative path.
func (f *FileSystem) Stat(_ context.Context, p string) (tool.FileInfo, error) {
	key, err := cleanPath(p)
	if err != nil {
		return tool.FileInfo{}, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	n, ok := f.files[key]
	if !ok {
		return tool.FileInfo{}, fmt.Errorf("memfs: stat %q: %w", p, ErrNotExist)
	}
	return tool.FileInfo{
		Name:    path.Base(key),
		Size:    int64(len(n.data)),
		Mode:    0o644,
		ModTime: n.modTime,
		IsDir:   false,
	}, nil
}

// Glob returns session-relative paths matching the glob pattern, in
// deterministic (sorted) order. The pattern supports the "**" globstar (matching
// across "/" recursively) in addition to "*", "?", "[…]" and "{…}". Matching uses
// doublestar.Match against each stored key. This mirrors the osfs adapter's
// globstar support so the in-memory test fabric and the real filesystem agree;
// the shared fsconformance suite pins both.
func (f *FileSystem) Glob(_ context.Context, pattern string) ([]string, error) {
	// Same normalization as osfs: strip a leading "/" and any leading "./" so an
	// absolute/leading-slash or "./"-prefixed pattern matches the root-relative,
	// slash-separated stored keys; empty/"." matches nothing.
	pat := normalizeGlobPattern(pattern)
	if pat == "" {
		return nil, nil
	}

	f.mu.RLock()
	keys := make([]string, 0, len(f.files))
	for k := range f.files {
		keys = append(keys, k)
	}
	f.mu.RUnlock()

	var out []string
	for _, k := range keys {
		ok, merr := doublestar.Match(pat, k)
		if merr != nil {
			// doublestar.Match returns ErrBadPattern for a malformed pattern;
			// surface it like the old path.Match error.
			return nil, merr
		}
		if ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

// normalizeGlobPattern applies the leniency the old filepath.Join-based osfs Glob
// had: it strips a leading "/" and any leading "./" segments so callers passing
// "/**/*.go" or "./**/*.go" get the same result as "**/*.go". An empty or "."
// pattern normalizes to "" (caller treats it as match-nothing).
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

// cleanPath normalizes a session-relative path to a clean, slash-separated key
// and rejects any path that escapes the root (absolute paths or "..").
func cleanPath(p string) (string, error) {
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: %q is absolute", ErrPathEscape, p)
	}
	cleaned := path.Clean("/" + p) // anchor at root, collapses ".." that would escape
	// path.Clean("/"+x) can never produce a leading "..", but a path like
	// "../x" becomes "/x" — detect escape by re-checking the original intent.
	if cleaned == "/" {
		return "", fmt.Errorf("%w: empty path", ErrPathEscape)
	}
	rel := strings.TrimPrefix(cleaned, "/")
	// Reject any component that attempted to traverse upward.
	if hasDotDot(p) {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, p)
	}
	return rel, nil
}

// hasDotDot reports whether the slash-separated path contains a ".." component.
func hasDotDot(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// Workspace is the in-memory session-scoped seam. It composes a FileSystem,
// performs Grep over in-memory contents, and carries the Edit read-ledger.
// Command execution is not part of the Workspace; use CommandRunner for that.
type Workspace struct {
	fs *FileSystem

	mu     sync.Mutex
	ledger map[string]tool.FileVersion // path -> recorded version
}

// NewWorkspace returns an empty in-memory Workspace with the given logical root.
func NewWorkspace(root string) *Workspace {
	return &Workspace{
		fs:     NewFileSystem(root),
		ledger: make(map[string]tool.FileVersion),
	}
}

// Compile-time assertion that Workspace satisfies the frozen port.
var _ tool.Workspace = (*Workspace)(nil)

// Root returns the absolute session root all paths are scoped to.
func (w *Workspace) Root() string { return w.fs.Root() }

// Read returns the contents of the file at the session-relative path.
func (w *Workspace) Read(ctx context.Context, p string) ([]byte, error) {
	return w.fs.Read(ctx, p)
}

// versionOf mints a FileVersion from content bytes (sha256). It is the single
// memfs version primitive: ReadVersion, CreateFile, and ReplaceFile all use it
// so a recorded version and a freshly-minted version are byte-identical for the
// same content.
func versionOf(data []byte) tool.FileVersion {
	sum := sha256.Sum256(data)
	return tool.NewFileVersion(hex.EncodeToString(sum[:]))
}

// ReadVersion returns the contents of the file at the session-relative path AND
// the authoritative FileVersion (sha256 of the content). It reads under the
// FileSystem's lock so the content and the version are a consistent snapshot.
func (w *Workspace) ReadVersion(_ context.Context, p string) ([]byte, tool.FileVersion, error) {
	key, err := cleanPath(p)
	if err != nil {
		return nil, tool.FileVersion{}, err
	}
	w.fs.mu.RLock()
	defer w.fs.mu.RUnlock()
	n, ok := w.fs.files[key]
	if !ok {
		return nil, tool.FileVersion{}, fmt.Errorf("memfs: open %q: %w", p, ErrNotExist)
	}
	out := bytes.Clone(n.data)
	return out, versionOf(n.data), nil
}

// Write is an adapter-public bootstrap operation, deliberately outside
// tool.Workspace; tools use CreateFile/ReplaceFile instead.
func (w *Workspace) Write(ctx context.Context, p string, data []byte) error {
	return w.fs.Write(ctx, p, data)
}

// CreateFile creates a NEW file at path with the given content, atomically. It
// fails (wrapping fs.ErrExist) if a file already exists. Parent directories are
// created as needed (memfs is flat — directories are implicit in the key). It
// returns the new file's FileVersion.
func (w *Workspace) CreateFile(_ context.Context, p string, data []byte) (tool.FileVersion, error) {
	key, err := cleanPath(p)
	if err != nil {
		return tool.FileVersion{}, err
	}
	stored := bytes.Clone(data)
	w.fs.mu.Lock()
	defer w.fs.mu.Unlock()
	if _, exists := w.fs.files[key]; exists {
		return tool.FileVersion{}, fmt.Errorf("memfs: create %q: %w", p, fs.ErrExist)
	}
	w.fs.files[key] = &node{data: stored, modTime: w.fs.now()}
	return versionOf(stored), nil
}

// ReplaceFile conditionally replaces the contents of the file at path with data,
// only if the file's current authoritative version equals old. On a version
// mismatch it returns a *tool.VersionMismatchError; on a missing file it returns
// an error wrapping fs.ErrNotExist. It is atomic under the FileSystem's lock.
func (w *Workspace) ReplaceFile(_ context.Context, p string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	key, err := cleanPath(p)
	if err != nil {
		return tool.FileVersion{}, err
	}
	stored := bytes.Clone(data)
	w.fs.mu.Lock()
	defer w.fs.mu.Unlock()
	n, ok := w.fs.files[key]
	if !ok {
		return tool.FileVersion{}, fmt.Errorf("memfs: replace %q: %w", p, ErrNotExist)
	}
	cur := versionOf(n.data)
	if !cur.Equal(old) {
		return tool.FileVersion{}, &tool.VersionMismatchError{Path: p}
	}
	w.fs.files[key] = &node{data: stored, modTime: w.fs.now()}
	return versionOf(stored), nil
}

// Stat returns metadata for the file at the session-relative path.
func (w *Workspace) Stat(ctx context.Context, p string) (tool.FileInfo, error) {
	return w.fs.Stat(ctx, p)
}

// Glob returns session-relative paths matching the shell-style pattern.
func (w *Workspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	return w.fs.Glob(ctx, pattern)
}

// Grep returns the matches of a regular expression across in-memory files
// selected by an optional path glob. When pathGlob is empty, all files are
// searched. Results are returned in deterministic order (by path, then line).
// The search honors ctx cancellation.
func (w *Workspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("memfs: invalid grep pattern: %w", err)
	}

	var files []string
	if pathGlob == "" {
		w.fs.mu.RLock()
		for k := range w.fs.files {
			files = append(files, k)
		}
		w.fs.mu.RUnlock()
		sort.Strings(files)
	} else {
		files, err = w.fs.Glob(ctx, pathGlob)
		if err != nil {
			return nil, err
		}
	}

	var matches []tool.GrepMatch
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, rerr := w.fs.Read(ctx, rel)
		if rerr != nil {
			continue
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue // binary
		}
		lineNo := 0
		for _, line := range strings.Split(string(data), "\n") {
			lineNo++
			if re.MatchString(line) {
				matches = append(matches, tool.GrepMatch{
					Path: rel,
					Line: lineNo,
					Text: line,
				})
			}
		}
	}
	return matches, nil
}

// CommandRunner is a programmable, in-memory tool.CommandRunner for
// deterministic Bash-tool stubbing. memfs has no shell, so by default Run
// returns ErrNoShell; program a canned result (or error) with SetResult.
type CommandRunner struct {
	mu     sync.Mutex
	result *tool.CommandResult
	err    error
}

// NewCommandRunner returns a programmable in-memory CommandRunner. Until
// SetResult is called, Run returns ErrNoShell.
func NewCommandRunner() *CommandRunner {
	return &CommandRunner{err: ErrNoShell}
}

// Compile-time assertion that CommandRunner satisfies the runner port.
var _ tool.CommandRunner = (*CommandRunner)(nil)

// SetResult programs the deterministic result (and/or error) that the next and
// subsequent Run calls return. Passing a nil result with a nil error makes Run
// return an empty successful result; this is the only way to get a non-error Run
// from memfs, since it has no shell.
func (r *CommandRunner) SetResult(res *tool.CommandResult, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = res
	r.err = err
}

// Run returns the programmed canned result. memfs has no shell, so absent a
// programmed result it returns ErrNoShell. It honors ctx cancellation. The
// command string is ignored beyond being a marker; this method exists for
// deterministic Bash-tool stubbing, not real execution.
func (r *CommandRunner) Run(ctx context.Context, _ string) (tool.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.CommandResult{}, err
	}
	r.mu.Lock()
	res, err := r.result, r.err
	r.mu.Unlock()
	if err != nil {
		return tool.CommandResult{}, err
	}
	if res == nil {
		return tool.CommandResult{}, nil
	}
	return *res, nil
}

// RecordRead stores the EXACT authoritative version for path under the session
// ledger. It performs NO I/O: it stores the FileVersion the caller supplies (the
// one ReadVersion minted), so a later RecordedVersion lookup compares against the
// recorded token without re-reading the file. The ledger key is the clean
// session-relative path (see cleanPath).
func (w *Workspace) RecordRead(p string, version tool.FileVersion) {
	key, err := cleanPath(p)
	if err != nil {
		// An uncleanable path cannot be recorded; leave it unrecorded (fail-safe:
		// a later mutation refuses as "not read").
		return
	}
	w.mu.Lock()
	w.ledger[key] = version
	w.mu.Unlock()
}

// RecordedVersion returns the version previously recorded for path via RecordRead,
// performing NO I/O. ok is false if path was never recorded. The lookup uses the
// same clean key as RecordRead.
func (w *Workspace) RecordedVersion(p string) (tool.FileVersion, bool) {
	key, err := cleanPath(p)
	if err != nil {
		return tool.FileVersion{}, false
	}
	w.mu.Lock()
	version, ok := w.ledger[key]
	w.mu.Unlock()
	return version, ok
}
