// Package osfs implements tool.FileSystem over the real operating-system
// filesystem and a tool.Workspace that scopes every path under a single session
// root. The canonical path forms a Workspace method accepts are:
//
//   - session-RELATIVE paths (the usual form), interpreted relative to the root;
//   - ABSOLUTE paths that canonicalize INSIDE the workspace root (the same
//     physical file a relative path would reach, addressed by its absolute
//     alias) — accepted by all five FS tools (Read/Write/Stat and, via the Edit
//     read-ledger, Edit/Glob/Grep's callers); and
//   - for Read/Stat ONLY, an absolute path under an explicit READ-ONLY allowed
//     root (WithReadRoots) — in practice the per-skill directories of discovered
//     skills, which may live outside the workspace (~/.claude/skills/…).
//
// Every other path is rejected as a correctness invariant: an absolute path that
// resolves OUTSIDE the workspace root, any ".." traversal that climbs out, and a
// symlink that escapes (whether addressed relatively or absolutely) all fail with
// ErrPathEscape.
//
// Absolute-path acceptance is canonicalize-then-reject: an absolute path is
// EvalSymlinks-resolved against the deepest EXISTING ancestor (so a not-yet-
// existing leaf being Written is still vetted through its real parent), and the
// resulting real path is compared against the (already EvalSymlinks-resolved at
// construction) workspace root. A symlink inside the workspace whose target
// resolves OUTSIDE the workspace is rejected at resolution time, BEFORE the path
// reaches *os.Root — defense-in-depth on top of *os.Root's own containment. On
// accept the path is reduced to its slash-separated root-relative form and flows
// through the SAME *os.Root as a relative path, so the os.Root symlink
// containment for in-root paths is preserved.
//
// The ONE other carve-out is the explicit READ-ONLY allowed roots
// (WithReadRoots) described above: each gets its own os.Root, so symlinks inside
// it that escape it are refused exactly like workspace escapes. Write, Glob,
// Grep, and the Edit ledger's mutation path stay workspace-only (Glob/Grep never
// route patterns through absolute resolution — patterns are not paths; a leading
// "/" in a pattern is stripped as before).
//
// The Workspace also carries the per-session Edit read-ledger
// (RecordRead/WasReadUnchanged) backed by a sha256 content fingerprint, the seam
// WP7's Edit tool uses to enforce read-before-edit-and-unchanged. The ledger
// NORMALIZES its keys via resolvePath, so a file read by absolute path and then
// edited by relative path (or vice versa) matches — the key is the canonical
// root-relative form, not the verbatim argument.
package osfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
var ErrPathEscape = errors.New("osfs: path escapes workspace root")

// defaultCommandTimeout bounds CommandRunner.Run when the caller's context has
// no deadline of its own.
const defaultCommandTimeout = 30 * time.Second

// defaultCommandWaitDelay bounds how long cmd.Wait may block on the stdout/stderr
// pipes AFTER the context is cancelled or the shell itself has exited (A7). The
// hazard it closes: a GRANDCHILD that inherited the pipes (e.g. `slow-thing &`)
// keeps them open after the shell exits/dies, and without a WaitDelay cmd.Wait
// parks on the pipe-copy goroutines until the grandchild exits — the unbounded
// residual the agent loop's run-end drain cap would otherwise be the only brake
// on. After the delay exec closes the pipes and Wait returns (the captured
// output so far stands).
const defaultCommandWaitDelay = 5 * time.Second

// maxCommandOutput caps each of stdout/stderr captured by CommandRunner.Run.
const maxCommandOutput = 1 << 20 // 1 MiB

// FileSystem implements tool.FileSystem over the real OS filesystem, rooted at a
// session workspace directory. The canonical path forms its methods accept are
// session-relative paths, absolute paths that resolve inside the workspace root
// (reduced to their root-relative form via resolveInRoot), and — for Read/Stat
// only — absolute paths under an explicit read-only allowed root.
//
// Every file operation goes through an *os.Root opened on the workspace root,
// which refuses both lexical ".." escapes and symlink traversal that would leave
// the root. This closes the gap a purely lexical cleanPath left open: the model
// can create a symlink inside the workspace (via Bash `ln -s /etc/passwd evil`),
// and both resolveInRoot (for absolute addresses) and os.Root (for all
// in-root paths) refuse to follow it out of the root.
type FileSystem struct {
	root string
	r    *os.Root
	// readRoots are the explicit READ-ONLY allowed roots (WithReadRoots), each
	// canonicalized at construction and served through its OWN *os.Root so the same
	// symlink containment that confines the workspace root applies inside each
	// allowed root. Only Read and Stat consult them; Write/Glob/Grep never do.
	readRoots []allowedRoot
	// relaxedReads enables the WithRelaxedReads out-of-root absolute read
	// carve-out (default off) — see relaxedReadRoot.
	relaxedReads bool
	// relaxedWrites enables the WithRelaxedWrites out-of-root absolute write
	// carve-out (default off) — see relaxedWriteRoot.
	relaxedWrites bool
}

// allowedRoot is one canonicalized read-only allowed root plus the os.Root it is
// served through.
type allowedRoot struct {
	path string
	r    *os.Root
}

// Option configures a FileSystem (and the Workspace composing it) at
// construction.
type Option func(*fsOptions)

// fsOptions collects the construction-time options.
type fsOptions struct {
	readRoots     []string
	relaxedReads  bool
	relaxedWrites bool
}

// WithReadRoots adds explicit READ-ONLY allowed roots: absolute directories that
// Read and Stat — and ONLY Read and Stat — may serve by absolute path even though
// they lie outside the workspace root. The composition root derives them from the
// DISCOVERED skills (one directory per skill, never a whole source tree), so the
// skills carve-out inherits the workspace-trust gate by construction. Each
// directory is canonicalized (abs + EvalSymlinks) and opened as its own os.Root
// at construction; a directory that does not exist is SKIPPED (a skill dir
// deleted after discovery must not brick workspace construction — Read of its
// files then fails with the ordinary escape error), while a present-but-
// unopenable directory is a construction error. Write, Glob, Grep, and the Edit
// mutation path remain workspace-only regardless of these roots.
func WithReadRoots(dirs ...string) Option {
	return func(o *fsOptions) {
		o.readRoots = append(o.readRoots, dirs...)
	}
}

// WithRelaxedReads lets Read and Stat — and ONLY Read and Stat — serve an
// ABSOLUTE path that canonicalizes OUTSIDE the workspace root and outside
// every WithReadRoots read-only root. It is an EXPLICIT construction option,
// DEFAULT OFF: the zero-value workspace keeps the ordinary
// canonicalize-then-reject behaviour (ErrPathEscape). The composition layer
// enables it for the MAIN session's workspace only, at the yolo/auto operator
// postures where Bash already reads the same bytes (the honesty fix —
// docs/acceptance/path-escape-posture.md Scenario 2), and always pairs it
// with the root-aware wrapping permission policy that refuses pseudo-fs
// (/proc, /sys, /dev) before the tool body: this option exists for THAT
// pairing, and a relaxed workspace without the policy wrapper is a mis-wire.
//
// Serving opens a FRESH *os.Root on the target's LEXICAL parent directory and
// serves the leaf through it — never a bare os.Open — so a symlink inside the
// target dir that escapes further is refused by that root's containment,
// exactly as the workspace root's own containment refuses an in-root escape.
// Write, Edit, Glob, and Grep stay workspace-confined regardless of this
// option (the relax is read-only).
func WithRelaxedReads() Option {
	return func(o *fsOptions) {
		o.relaxedReads = true
	}
}

// WithRelaxedWrites lets Write — and, through it, the Edit tool's mutation
// path — serve an ABSOLUTE path that canonicalizes OUTSIDE the workspace root
// (never a WithReadRoots read-only root: those stay read-only at every
// posture). It is an EXPLICIT construction option, DEFAULT OFF: the
// zero-value workspace keeps the ordinary canonicalize-then-reject behaviour
// (ErrPathEscape). The composition layer enables it for the MAIN session's
// workspace only, at the yolo/auto operator postures (never a child engine),
// and always pairs it with the root-aware wrapping permission policy that
// resolves a write escape Allow at yolo / Ask at auto and hard-denies
// pseudo-fs (/proc, /sys, /dev) before the tool body — docs/acceptance/
// path-escape-posture.md Scenario 3. A relaxed workspace without the policy
// wrapper is a mis-wire: the workspace's job is only to SERVE the path the
// policy already authorized.
//
// Serving opens a FRESH *os.Root on the target's LEXICAL parent directory
// (vetted by the same vetRelaxedParent ancestor walk the relaxed read uses)
// and writes the leaf through it — never a bare os.WriteFile — so a symlinked
// component that escapes further is refused by that root's containment,
// exactly as the workspace root's own containment refuses an in-root escape
// (ADR-0047). Glob and Grep stay workspace-confined regardless of this
// option.
func WithRelaxedWrites() Option {
	return func(o *fsOptions) {
		o.relaxedWrites = true
	}
}

// NewFileSystem returns a FileSystem rooted at the given directory. The root is
// resolved to an absolute, symlink-evaluated path, created if missing, then
// opened as an *os.Root so all subsequent operations are confined to it.
// Optional read-only allowed roots are attached via WithReadRoots.
func NewFileSystem(root string, opts ...Option) (*FileSystem, error) {
	var o fsOptions
	for _, opt := range opts {
		opt(&o)
	}
	abs, err := resolveRoot(root)
	if err != nil {
		return nil, err
	}
	// os.OpenRoot requires the directory to exist; create it so the constructor
	// preserves its previous lenient behavior (Write created missing dirs).
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	f := &FileSystem{root: abs, r: r, relaxedReads: o.relaxedReads, relaxedWrites: o.relaxedWrites}
	// Dedup canonical roots; the workspace root itself never needs an allowlist
	// entry (relative paths already reach it; absolute aliases of it are still
	// outside the contract).
	seen := map[string]bool{abs: true}
	for _, dir := range o.readRoots {
		if dir == "" {
			continue
		}
		canon, rerr := resolveRoot(dir)
		if rerr != nil {
			return nil, fmt.Errorf("osfs: read root %q: %w", dir, rerr)
		}
		if seen[canon] {
			continue
		}
		rr, rerr := os.OpenRoot(canon)
		if rerr != nil {
			// Absent dir (deleted between discovery and construction): skip — the
			// carve-out simply does not open, and reads under it fail with the
			// ordinary escape error. Anything else (e.g. permissions) is loud.
			if errors.Is(rerr, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("osfs: read root %q: %w", dir, rerr)
		}
		seen[canon] = true
		f.readRoots = append(f.readRoots, allowedRoot{path: canon, r: rr})
	}
	return f, nil
}

// Root returns the absolute workspace root.
func (f *FileSystem) Root() string { return f.root }

// resolvePath maps a caller-supplied path onto the slash-cleaned, os.Root-ready
// root-relative form. A session-relative path (one that is neither absolute nor
// slash-prefixed) is cleaned lexically — the lexical ".." check and *os.Root's
// containment catch relative escapes. An ABSOLUTE path (or a slash-prefixed one)
// is canonicalized by resolveInRoot: if it resolves inside the workspace root it
// is reduced to its root-relative form; otherwise it fails with ErrPathEscape
// (out-of-root absolute paths, including escaping symlinks addressed absolutely).
//
// Read/Stat consult the read-only allowed roots (WithReadRoots) for absolute
// paths that resolve OUTSIDE the workspace root — see resolveRead, which layers
// that carve-out on top of resolvePath.
func (f *FileSystem) resolvePath(path string) (string, error) {
	if path == "" {
		return ".", nil
	}
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, "/") {
		return filepath.Clean(filepath.FromSlash(path)), nil
	}
	return f.resolveInRoot(path)
}

// resolveInRoot canonicalizes an ABSOLUTE path and, if it resolves inside the
// workspace root, returns its slash-separated root-relative form. It mirrors
// internal/adapter/acp/fsworkspace.go:confineSymlinks exactly in shape: for a
// not-yet-existing leaf (a file being Written) it resolves the deepest EXISTING
// ancestor's symlinks and re-appends the unresolved tail, so a symlinked PARENT
// component that escapes is caught at resolution time. f.root is already
// EvalSymlinks-resolved at construction, so the comparison is canonical-to-
// canonical. Out-of-root → ErrPathEscape; a non-ErrNotExist stat error on an
// ancestor fails safe (reject).
func (f *FileSystem) resolveInRoot(abs string) (string, error) {
	// Find the deepest existing ancestor and resolve it.
	existing := abs
	var tail []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			// An ambiguous stat error on an ancestor: fail safe (reject) rather
			// than operate on a path we cannot vet.
			return "", fmt.Errorf("%w: %q cannot be verified: %v", ErrPathEscape, abs, err)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			// Reached the filesystem root without an existing ancestor; the
			// workspace root itself exists, so this means the absolute path is
			// wholly outside it — reject as an escape.
			return "", fmt.Errorf("%w: %q", ErrPathEscape, abs)
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("%w: %q resolving: %v", ErrPathEscape, abs, err)
	}
	realPath := resolved
	if len(tail) > 0 {
		realPath = filepath.Join(append([]string{resolved}, tail...)...)
	}
	// Canonical-to-canonical: f.root is EvalSymlinks-resolved at construction.
	if realPath != f.root && !strings.HasPrefix(realPath, f.root+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, abs)
	}
	rel, _ := filepath.Rel(f.root, realPath) // cannot error: realPath is under f.root
	return filepath.ToSlash(rel), nil
}

// mapEscape rewrites os.Root's escape/insecure-path errors to the package's
// ErrPathEscape sentinel, leaving ordinary errors (e.g. not-exist) untouched.
func mapEscape(path string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrInvalid) {
		return fmt.Errorf("%w: %q", ErrPathEscape, path)
	}
	// os.Root reports traversal that would leave the root via a *PathError whose
	// message mentions escaping the root; detect it without depending on an
	// unexported error type.
	if strings.Contains(err.Error(), "path escapes from parent") {
		return fmt.Errorf("%w: %q", ErrPathEscape, path)
	}
	return err
}

// Read returns the entire contents of the file at the session-relative path.
// maxReadBytes caps how much a single Read will pull into memory. It is far
// larger than any realistic source file but guards against an OOM DoS from a
// pathologically large file in the workspace (a tool reads the whole file before
// the model-facing output is truncated).
const maxReadBytes = 64 << 20 // 64 MiB

func (f *FileSystem) Read(_ context.Context, path string) ([]byte, error) {
	r, rel, err := f.resolveRead(path)
	if err != nil {
		return nil, err
	}
	if info, statErr := r.Stat(rel); statErr == nil && info.Size() > maxReadBytes {
		return nil, fmt.Errorf("osfs: file %q is %d bytes, exceeds the %d-byte read limit", path, info.Size(), int64(maxReadBytes))
	}
	data, err := r.ReadFile(rel)
	if err != nil {
		return nil, mapEscape(path, err)
	}
	return data, nil
}

// resolveRead maps a Read/Stat path onto the os.Root that serves it. It tries
// resolvePath first: a relative path, and an absolute path that resolves INSIDE
// the workspace root, both land on the workspace os.Root. An absolute path that
// resolves OUTSIDE the workspace root may still be served by an explicit
// read-only allowed root (WithReadRoots) — the skills carve-out — so on a
// resolvePath error resolveRead falls through to allowedReadRoot. Only if that
// too declines is the original escape error returned. Read and Stat are the ONLY
// callers; Write routes through resolvePath directly (in-root absolute only, no
// allowed-root carve-out for writes), and Glob/Grep never resolve paths at all.
func (f *FileSystem) resolveRead(path string) (*os.Root, string, error) {
	rel, err := f.resolvePath(path)
	if err == nil {
		return f.r, rel, nil
	}
	if r, sub, ok := f.allowedReadRoot(path); ok {
		return r, sub, nil
	}
	if r, sub, ok := f.relaxedReadRoot(path); ok {
		return r, sub, nil
	}
	return nil, "", err
}

// relaxedReadRoot serves an out-of-root ABSOLUTE path under the
// WithRelaxedReads option (default off — a zero-value FileSystem never
// reaches here). It opens a FRESH *os.Root on the LEXICAL parent directory of
// the cleaned verbatim path and returns that root plus the leaf name, so the
// read itself flows through that root's traversal — never a bare os.Open.
//
// Containment survives the relax TWO ways:
//
//  1. SERVE: a symlink inside the target dir that escapes further is refused
//     by the serving root's own traversal (mapEscape at the call site),
//     exactly as the workspace root refuses an in-root escape.
//  2. VET: before opening, the resolveInRoot ancestor algorithm (deepest
//     EXISTING ancestor + EvalSymlinks) canonicalizes the verbatim PARENT dir
//     with the containment comparison inside the walk: any resolved ancestor
//     that jumps ABOVE the not-yet-resolved verbatim prefix means a symlinked
//     component escapes the verbatim path — refuse, so the relax never
//     becomes a channel for a symlink escape the in-root path would reject.
//     (EvalSymlinks alone on the whole path resolves to the target and launders
//     the escape; the ancestor walk vets each component's jump.)
//
// It only ever fires after resolvePath AND allowedReadRoot declined, so the
// target is provably outside the workspace root and every read root. An
// unverifiable ancestor or an unopenable parent fails safe with no root (the
// caller returns the original escape error). Only Read/Stat consult it (via
// resolveRead); writes never do.
func (f *FileSystem) relaxedReadRoot(path string) (*os.Root, string, bool) {
	if !f.relaxedReads || !filepath.IsAbs(path) {
		return nil, "", false
	}
	cleaned := filepath.Clean(path)
	parent, leaf := filepath.Dir(cleaned), filepath.Base(cleaned)
	if leaf == "." || leaf == string(filepath.Separator) || leaf == "" {
		return nil, "", false
	}
	if !vetRelaxedParent(parent) {
		return nil, "", false
	}
	rr, err := os.OpenRoot(parent)
	if err != nil {
		return nil, "", false
	}
	return rr, leaf, true
}

// vetRelaxedParent canonicalizes the verbatim parent dir with the
// resolveInRoot ancestor walk, refusing when ANY resolved ancestor jumps
// above the not-yet-resolved verbatim prefix (a symlinked component that
// escapes the verbatim path). The parent itself need not exist yet (the read
// then fails not-exist at the root open); an unverifiable ancestor fails safe.
func vetRelaxedParent(parent string) bool {
	existing := parent
	remainder := ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false
		}
		next := filepath.Dir(existing)
		if next == existing {
			return true // reached the fs root: every component stays under it
		}
		remainder = filepath.Base(existing) + string(filepath.Separator) + remainder
		existing = next
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return false
	}
	prefix := strings.TrimSuffix(parent, remainder)
	return resolved == prefix || strings.HasPrefix(resolved, prefix+string(filepath.Separator))
}

// allowedReadRoot tests an absolute path against the explicit read-only allowed
// roots and, on a match, returns that root's os.Root plus the root-relative
// remainder ("." for the root itself). Matching is lexical on the CLEANED path
// against each canonical root — exact equality or containment under the root.
// A symlink INSIDE an allowed root that escapes it is refused later by that
// root's os.Root (mapEscape), the same containment the workspace root has.
func (f *FileSystem) allowedReadRoot(path string) (*os.Root, string, bool) {
	if len(f.readRoots) == 0 || !filepath.IsAbs(path) {
		return nil, "", false
	}
	cleaned := filepath.Clean(path)
	for _, ar := range f.readRoots {
		if cleaned == ar.path {
			return ar.r, ".", true
		}
		if strings.HasPrefix(cleaned, ar.path+string(filepath.Separator)) {
			return ar.r, cleaned[len(ar.path)+1:], true
		}
	}
	return nil, "", false
}

// Write replaces the contents of the file at the session-relative path, creating
// it and any parent directories if needed. An absolute path that canonicalizes
// OUTSIDE the workspace root is served only under the explicit WithRelaxedWrites
// option (default off), through a fresh *os.Root on the target's vetted parent —
// never a bare os.WriteFile (relaxedWriteRoot).
func (f *FileSystem) Write(_ context.Context, path string, data []byte) error {
	rel, err := f.resolvePath(path)
	if err != nil {
		if r, leaf, ok := f.relaxedWriteRoot(path); ok {
			return mapEscape(path, r.WriteFile(leaf, data, 0o644))
		}
		return err
	}
	if dir := filepath.Dir(rel); dir != "." {
		// An "already exists" error here is benign (the dir, or a symlink in its
		// place, is present); let WriteFile make the final escape decision so a
		// symlinked parent component surfaces as ErrPathEscape rather than being
		// masked by MkdirAll's "file exists".
		if err := f.r.MkdirAll(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return mapEscape(path, err)
		}
	}
	if err := f.r.WriteFile(rel, data, 0o644); err != nil {
		return mapEscape(path, err)
	}
	return nil
}

// relaxedWriteRoot serves an out-of-root ABSOLUTE write under the
// WithRelaxedWrites option (default off — a zero-value FileSystem never
// reaches here). It mirrors relaxedReadRoot exactly: the SAME vetRelaxedParent
// ancestor walk refuses a verbatim parent whose resolved ancestor escapes the
// verbatim prefix, then a FRESH *os.Root on the lexical parent serves the leaf,
// so a symlinked component inside the parent that escapes further is refused
// by that root's own traversal (mapEscape at the call site) — the write never
// becomes a bare os.WriteFile that would follow the symlink out.
//
// Unlike relaxedReadRoot it additionally requires the verbatim parent to EXIST:
// out-of-root writes create the LEAF only, never ancestor directories (the
// in-root Write's MkdirAll has no relaxed analogue — mkdir-ing a host path is a
// strictly wider mutation the Scenario 3 relax does not grant). A path under a
// WithReadRoots read-only root is EXPLICITLY refused (the lexical allowedReadRoot
// match): read roots stay read-only at every posture — the relax never turns the
// skills carve-out writable. Only Write consults it; Glob/Grep never resolve
// paths at all.
func (f *FileSystem) relaxedWriteRoot(path string) (*os.Root, string, bool) {
	if !f.relaxedWrites || !filepath.IsAbs(path) {
		return nil, "", false
	}
	if _, _, ok := f.allowedReadRoot(path); ok {
		return nil, "", false // a READ-ONLY root never serves a write, relaxed or not
	}
	cleaned := filepath.Clean(path)
	parent, leaf := filepath.Dir(cleaned), filepath.Base(cleaned)
	if leaf == "." || leaf == string(filepath.Separator) || leaf == "" {
		return nil, "", false
	}
	if !vetRelaxedParent(parent) {
		return nil, "", false
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return nil, "", false
	}
	rr, err := os.OpenRoot(parent)
	if err != nil {
		return nil, "", false
	}
	return rr, leaf, true
}

// Stat returns metadata for the file at the session-relative path (or, like
// Read, an absolute path under an explicit read-only allowed root).
func (f *FileSystem) Stat(_ context.Context, path string) (tool.FileInfo, error) {
	r, rel, err := f.resolveRead(path)
	if err != nil {
		return tool.FileInfo{}, err
	}
	fi, err := r.Stat(rel)
	if err != nil {
		return tool.FileInfo{}, mapEscape(path, err)
	}
	return toFileInfo(fi), nil
}

// Glob returns session-relative paths matching the glob pattern, sorted. The
// pattern supports the "**" globstar (matching across directory separators
// recursively) in addition to the usual shell-style "*", "?", "[…]" and "{…}"
// metacharacters. The pattern is interpreted relative to the root; matches that
// resolve outside the root are discarded.
func (f *FileSystem) Glob(_ context.Context, pattern string) ([]string, error) {
	// doublestar patterns are root-relative, slash-separated paths. normalizeGlobPattern
	// preserves the old filepath.Join leniency (silently absorbing a leading "/"
	// or "./", which would otherwise be an invalid absolute pattern). An empty or
	// "." pattern normalizes to "" → match-nothing; the Glob tool already rejects
	// "" upstream, but stay robust here rather than globbing the entire root.
	pat := normalizeGlobPattern(filepath.ToSlash(pattern))
	if pat == "" {
		return nil, nil
	}

	// Walk the os.Root-confined fs.FS. f.r.FS() (Go 1.24+) returns an fs.FS that
	// refuses to traverse any symlink that would leave the root, so escaping
	// intermediate-directory components are rejected by construction — closing
	// the filename-enumeration leak filepath.Glob had. WithNoFollow keeps the
	// walk from descending into symlinked directories.
	var matches []string
	err := doublestar.GlobWalk(f.r.FS(), pat, func(p string, d fs.DirEntry) error {
		// Drop leaf symlink matches to preserve the previous behavior exactly: a
		// symlink inside the root can still target a file outside it, and Glob
		// must not be a channel for following links out of the workspace. This
		// mirrors the old Lstat + fs.ModeSymlink drop.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		// A bare "**" matches the root itself as "."; surfacing the workspace root
		// to the model is meaningless, so drop it (filepath.Glob never produced it).
		if p == "." {
			return nil
		}
		// p is already root-relative and slash-separated.
		matches = append(matches, p)
		return nil
	}, doublestar.WithNoFollow())
	if err != nil {
		// doublestar returns ErrBadPattern (wrapping path.ErrBadPattern) for a
		// malformed pattern; surface it like filepath.Glob's ErrBadPattern.
		// IO errors are not requested (no WithFailOnIOErrors), matching the old
		// lenient "skip unreadable" behavior.
		return nil, err
	}
	// filepath.Glob returned sorted matches and the contract advertises sorted
	// output; GlobWalk visits in directory order, so sort to preserve it.
	sort.Strings(matches)
	return matches, nil
}

// normalizeGlobPattern applies the leniency the old filepath.Join-based Glob
// had: it strips a leading "/" and any leading "./" segments so callers passing
// "/**/*.go" or "./**/*.go" get the same result as "**/*.go". An empty or "."
// pattern normalizes to "" (the caller treats it as match-nothing). The input
// is expected slash-separated (callers pass filepath.ToSlash(pattern)).
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

// toRel converts an absolute path under root into a slash-separated
// session-relative path, rejecting anything outside root.
func (f *FileSystem) toRel(abs string) (string, error) {
	rel, err := filepath.Rel(f.root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrPathEscape
	}
	return filepath.ToSlash(rel), nil
}

// toFileInfo maps an fs.FileInfo onto the domain tool.FileInfo.
func toFileInfo(fi fs.FileInfo) tool.FileInfo {
	return tool.FileInfo{
		Name:    fi.Name(),
		Size:    fi.Size(),
		Mode:    fi.Mode(),
		ModTime: fi.ModTime(),
		IsDir:   fi.IsDir(),
	}
}

// resolveRoot makes root absolute and evaluates symlinks where possible so that
// later escape checks compare canonical paths. When the path itself does not
// exist yet (e.g. a SkillsDraftDir validated before it is created), EvalSymlinks
// fails on the leaf; we then resolve the deepest EXISTING ancestor and re-append
// the non-existent tail. Without this, a non-existent path under a symlinked
// root (macOS /var/folders -> /private/var/folders) keeps the unresolved form
// while an existing sibling resolves through the symlink, so two paths referring
// to the same on-disk location compare unequal — defeating the dirsOverlap
// containment check in validateSkillDraftConfig (a security-boundary bypass).
func resolveRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	// The leaf does not exist (or is otherwise unresolvable). Canonicalize the
	// longest existing prefix and re-append the non-existent tail, so a
	// not-yet-created dir under a symlinked root lands in the same canonical
	// form as its existing parent.
	existing := abs
	var tail []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			// An ambiguous stat error: best-effort — fall back to the cleaned
			// absolute form rather than failing (resolveRoot has no error
			// sentinel for "unverifiable" and callers treat error as fatal).
			return filepath.Clean(abs), nil
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			// Reached the filesystem root without an existing ancestor; nothing
			// to canonicalize against. Cleaned abs is the best we can do.
			return filepath.Clean(abs), nil
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return filepath.Clean(abs), nil
	}
	if len(tail) == 0 {
		return resolved, nil
	}
	return filepath.Join(append([]string{resolved}, tail...)...), nil
}

// ResolveRoot exposes the EXACT path canonicalization the Workspace uses to confine
// Write/Edit (abs + EvalSymlinks, falling back to a cleaned abs path when the path
// does not yet exist). Callers that reason about whether a directory is inside or
// outside a workspace root (e.g. the SkillDraft quarantine trust-boundary check in
// cmd/mecated) MUST canonicalize through this so their comparison matches the
// enforcement layer — using filepath.Abs alone diverges on a symlinked workspace
// and would let a dir validation believes is "outside" actually resolve inside the
// os.Root.
func ResolveRoot(path string) (string, error) { return resolveRoot(path) }

// Canonicalize resolves a path — absolute, or relative against base (an
// ALREADY-canonicalized root, as produced by ResolveRoot) — to its canonical
// absolute form WITHOUT opening an *os.Root and WITHOUT serving any content:
// the exact resolveInRoot/resolveRoot algorithm (deepest EXISTING ancestor +
// EvalSymlinks + unresolved tail re-appended). An unverifiable ancestor (a
// non-ErrNotExist stat error) fails safe with an ErrPathEscape error, matching
// resolveInRoot. The only I/O is the Lstat/EvalSymlinks ancestor resolution
// resolveInRoot itself performs. The path-escape-posture composition
// classifier consumes this (plus LocalizeInRoot and MatchReadRoot) so its
// in-root/escape verdict is single-sourced with the tool body
// (docs/acceptance/path-escape-posture.md Scenario 1) instead of
// reimplementing the algorithms.
func Canonicalize(base, path string) (string, error) {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(base, filepath.FromSlash(path))
	}
	// The resolveInRoot ancestor walk: resolve the deepest EXISTING ancestor's
	// symlinks and re-append the unresolved tail, so a not-yet-existing leaf
	// is vetted through its real parent.
	existing := abs
	var tail []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %q cannot be verified: %v", ErrPathEscape, abs, err)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return filepath.Clean(abs), nil
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("%w: %q resolving: %v", ErrPathEscape, abs, err)
	}
	if len(tail) == 0 {
		return resolved, nil
	}
	return filepath.Join(append([]string{resolved}, tail...)...), nil
}

// LocalizeInRoot reports the lexical root-relative form of a session-RELATIVE
// path: the exact lexical computation the tool body and *os.Root perform on a
// relative operand (filepath.Clean — resolvePath cleans a relative path
// lexically and hands it to the os.Root, which refuses any ".." traversal that
// climbs out of the root). It performs NO filesystem I/O — the first
// containment gate is lexical — so this predicate is precisely the question
// "would the workspace root's os.Root refuse this relative path before any
// symlink check?". An absolute or slash-prefixed path is NOT a relative
// operand (resolvePath routes those to resolveInRoot) and reports not-in-root.
func LocalizeInRoot(path string) (string, bool) {
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") {
		return "", false
	}
	rel := filepath.Clean(filepath.FromSlash(path))
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// MatchReadRoot reports whether the ABSOLUTE path lies under one of the
// CANONICAL read-only roots, using the exact LEXICAL match allowedReadRoot
// performs for Read/Stat (cleaned-path equality or containment, never
// canonicalized): a symlinked absolute path whose lexical form walks through
// a read root matches exactly as the tool body serves it. No *os.Root is
// opened and no stat is performed; whether a matched path may actually be
// served (Read/Stat only) stays the tool body's business.
func MatchReadRoot(path string, readRoots []string) bool {
	if len(readRoots) == 0 || !filepath.IsAbs(path) {
		return false
	}
	cleaned := filepath.Clean(path)
	for _, root := range readRoots {
		if cleaned == root || strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Workspace is the session-scoped seam over the real OS filesystem. It composes
// a FileSystem, performs an in-Go recursive Grep, and carries the Edit
// read-ledger. Command execution is NOT part of the Workspace: it lives behind
// the separate CommandRunner type (see NewCommandRunner) so the harness can run
// without any shell at all.
type Workspace struct {
	fs *FileSystem

	mu     sync.Mutex
	ledger map[string]string // session-relative path -> recorded fingerprint
}

// NewWorkspace returns a Workspace rooted at the given directory. The root is
// created if it does not already exist (NewFileSystem creates it before opening
// the os.Root). Options (e.g. WithReadRoots) are passed through to the
// underlying FileSystem.
func NewWorkspace(root string, opts ...Option) (*Workspace, error) {
	fsys, err := NewFileSystem(root, opts...)
	if err != nil {
		return nil, err
	}
	return &Workspace{fs: fsys, ledger: make(map[string]string)}, nil
}

// Compile-time assertion that Workspace satisfies the frozen port.
var _ tool.Workspace = (*Workspace)(nil)

// Root returns the absolute session root all paths are scoped to.
func (w *Workspace) Root() string { return w.fs.Root() }

// Read returns the contents of the file at the session-relative path.
func (w *Workspace) Read(ctx context.Context, path string) ([]byte, error) {
	return w.fs.Read(ctx, path)
}

// Write replaces the contents of the file at the session-relative path, creating
// it and parent directories if needed.
func (w *Workspace) Write(ctx context.Context, path string, data []byte) error {
	return w.fs.Write(ctx, path, data)
}

// Stat returns metadata for the file at the session-relative path.
func (w *Workspace) Stat(ctx context.Context, path string) (tool.FileInfo, error) {
	return w.fs.Stat(ctx, path)
}

// Glob returns session-relative paths matching the shell-style pattern.
func (w *Workspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	return w.fs.Glob(ctx, pattern)
}

// Grep returns the matches of a regular expression across files selected by an
// optional path glob (relative to root). When pathGlob is empty, the whole tree
// under root is searched. Binary-looking files (those containing a NUL byte) are
// skipped. The search honors ctx cancellation.
func (w *Workspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("osfs: invalid grep pattern: %w", err)
	}

	var files []string
	if pathGlob == "" {
		files, err = w.walkAll(ctx)
	} else {
		files, err = w.fs.Glob(ctx, pathGlob)
	}
	if err != nil {
		return nil, err
	}

	var matches []tool.GrepMatch
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := w.fs.Read(ctx, rel)
		if err != nil {
			continue // unreadable / vanished file: skip
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue // binary file
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

// walkAll returns every regular file under root as a session-relative path.
func (w *Workspace) walkAll(ctx context.Context) ([]string, error) {
	var out []string
	err := filepath.WalkDir(w.fs.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() {
			return nil
		}
		// Skip symlinks entirely: WalkDir does not descend into them, but a
		// symlinked FILE could still point outside the root. Excluding them here
		// keeps Grep from surfacing out-of-root content (and Read would refuse
		// it anyway via os.Root).
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, rerr := w.fs.toRel(p)
		if rerr != nil {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CommandRunner runs shell commands via /bin/sh -c with a fixed working
// directory (the session root). It is the local implementation of
// tool.CommandRunner; a Workspace no longer runs commands itself, so a
// shell-less deployment simply omits this runner.
type CommandRunner struct {
	root  string
	shell string
	// env, when set, is the COMPLETE process environment for every Run (it REPLACES
	// the inherited os.Environ(), it does not augment it). It is nil for an
	// unhardened runner (the main session, which inherits the operator's full
	// environment unchanged). The team-member sandboxed runner populates it via
	// WithCommandEnvList with a fully-scrubbed-and-neutralised environment computed
	// in composition (internal/app.buildSandboxedCommandRunner via gitenv.Scrub):
	// inherited git danger is REMOVED, not merely overridden. osfs stays free of
	// git-specific knowledge — it just sets whatever complete env it is handed.
	env []string
	// waitDelay is the per-command cmd.WaitDelay (defaultCommandWaitDelay unless
	// overridden via WithCommandWaitDelay — tests use a short one). See
	// defaultCommandWaitDelay for the grandchild-pipe rationale (A7).
	waitDelay time.Duration
}

// CommandRunnerOption configures a CommandRunner at construction.
type CommandRunnerOption func(*CommandRunner)

// WithCommandEnvList sets the COMPLETE process environment ("KEY=VALUE" entries)
// used for every Run, REPLACING the inherited os.Environ() rather than augmenting
// it. This is how the composition root hardens the team-member shell against a
// shared `.git`: it computes a fully scrubbed-and-neutralised environment (via
// gitenv.Scrub — inherited GIT_* danger REMOVED, not just overridden) and hands
// the complete list here. Because the option REPLACES the environment, removing an
// inherited variable (e.g. GIT_EXTERNAL_DIFF) is possible — an append-only option
// could not. A nil/empty list leaves the runner unhardened (the main-session
// default, which inherits os.Environ() unchanged). osfs holds no git knowledge: it
// just runs with whatever complete environment it is given.
func WithCommandEnvList(env []string) CommandRunnerOption {
	return func(r *CommandRunner) {
		r.env = append([]string(nil), env...)
	}
}

// WithCommandWaitDelay overrides the runner's cmd.WaitDelay (default
// defaultCommandWaitDelay): the bound on how long Run waits for the output pipes
// inherited by grandchildren to close after the shell exits or the context is
// cancelled. Non-positive values are ignored (keep the default — a zero WaitDelay
// would restore the unbounded pipe wait). Primarily a test seam.
func WithCommandWaitDelay(d time.Duration) CommandRunnerOption {
	return func(r *CommandRunner) {
		if d > 0 {
			r.waitDelay = d
		}
	}
}

// NewCommandRunner returns a local tool.CommandRunner that executes commands via
// /bin/sh -c, rooted at dir as the working directory. dir is resolved to an
// absolute, symlink-evaluated path so the runner's cwd matches the Workspace
// root. Use this from the composition root only when a shell is desired; omit it
// (and the Bash tool) to run shell-less.
func NewCommandRunner(dir string) (tool.CommandRunner, error) {
	return NewCommandRunnerShell(dir, "/bin/sh")
}

// NewCommandRunnerShell is like NewCommandRunner but lets the caller pick the
// shell binary (e.g. "/bin/bash"). An empty shell is rejected: a shell-less
// deployment must omit the runner (and the Bash tool) entirely rather than
// construct a runner with no shell.
func NewCommandRunnerShell(dir, shell string, opts ...CommandRunnerOption) (tool.CommandRunner, error) {
	if shell == "" {
		return nil, errors.New("osfs: command runner requires a non-empty shell")
	}
	abs, err := resolveRoot(dir)
	if err != nil {
		return nil, err
	}
	r := &CommandRunner{root: abs, shell: shell, waitDelay: defaultCommandWaitDelay}
	for _, o := range opts {
		o(r)
	}
	return r, nil
}

// Compile-time assertion that CommandRunner satisfies the runner port.
var _ tool.CommandRunner = (*CommandRunner)(nil)

// Run runs command via /bin/sh -c, capturing (and truncating) stdout/stderr and
// the exit code. The working directory is workdir (the session/fork Workspace
// root the Bash tool executes against); an EMPTY workdir falls back to the
// runner's configured root, so the runner is usable standalone. workdir is used
// as-is and is intentionally NOT confined to the runner's configured root: a
// forked child lives under an isolated temp base OUTSIDE that root, and running
// its Bash there (not in the shared parent base) is exactly what fork isolation
// requires. Cancellation and timeout are governed by ctx; when ctx has no
// deadline a default timeout is applied. A non-zero exit is reported via the
// returned CommandResult.ExitCode, not as an error.
func (r *CommandRunner) Run(ctx context.Context, command, workdir string) (tool.CommandResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCommandTimeout)
		defer cancel()
	}

	var stdout, stderr cappedBuffer
	stdout.cap = maxCommandOutput
	stderr.cap = maxCommandOutput

	dir := workdir
	if dir == "" {
		dir = r.root
	}
	cmd := exec.CommandContext(ctx, r.shell, "-c", command)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Bound the post-exit/post-cancel pipe wait (A7): without it, a grandchild that
	// inherited stdout/stderr (e.g. `slow-thing &`) keeps cmd.Wait parked on the
	// pipe-copy goroutines until the grandchild exits, long after the shell itself
	// is gone. WaitDelay closes the pipes after this bound; the output captured so
	// far stands.
	cmd.WaitDelay = r.waitDelay
	// A hardened (team-member) runner carries a COMPLETE, pre-scrubbed environment
	// (computed in composition via gitenv.Scrub: inherited GIT_* danger removed,
	// neutralising config appended); use it verbatim so removal of an inherited
	// variable actually takes effect. An unhardened runner has r.env == nil, so
	// cmd.Env stays nil and exec inherits os.Environ() unchanged, as before.
	if r.env != nil {
		cmd.Env = r.env
	}

	err := cmd.Run()
	res := tool.CommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
	}

	if cerr := ctx.Err(); cerr != nil {
		// Context cancellation/timeout is a harness-level failure.
		return res, cerr
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		// WaitDelay expired with the pipes still open but the shell itself EXITED
		// SUCCESSFULLY (a backgrounded grandchild holds the inherited fds — e.g.
		// `daemon &`). That is a success with the output captured so far, not a
		// harness failure: before WaitDelay existed this command simply blocked
		// until the grandchild exited and then succeeded.
		if errors.Is(err, exec.ErrWaitDelay) {
			return res, nil
		}
		return res, err
	}
	return res, nil
}

// ledgerKey normalizes a ledger path to its canonical root-relative form so a
// file read by absolute path and then edited by relative path (or vice versa)
// matches in the ledger. It resolves through fs.resolvePath; an in-root absolute
// path is reduced to its root-relative form, a relative path is cleaned, and an
// out-of-root absolute path (a skills-base read served by an allowed root, or a
// relaxed-read/write escape) has no root-relative form, so it keys by its
// CANONICAL ABSOLUTE form (deepest-existing-ancestor + EvalSymlinks, the
// resolveInRoot algorithm) — a path with ".." components, or one riding a
// symlinked ancestor, normalizes to the SAME key as the canonical absolute
// path of the same file. The key is stable across the two cross-form call
// sites (RecordRead and WasReadUnchanged) because both apply the same
// normalization.
func (w *Workspace) ledgerKey(path string) string {
	if rel, err := w.fs.resolvePath(path); err == nil {
		return rel
	}
	if filepath.IsAbs(path) {
		if canon, err := Canonicalize("", path); err == nil {
			return filepath.ToSlash(canon)
		}
	}
	return filepath.Clean(filepath.ToSlash(path))
}

// RecordRead stores the current on-disk fingerprint of path under the session
// ledger. The version argument is accepted for interface conformance but the
// adapter computes and stores its own authoritative fingerprint so that
// WasReadUnchanged can compare against the live file. The ledger key is the
// canonical root-relative form (see ledgerKey), so an absolute path and the
// equivalent relative path share one entry.
func (w *Workspace) RecordRead(path string, version string) {
	key := w.ledgerKey(path)
	fp, err := w.fingerprint(path)
	if err != nil {
		// Record the caller-supplied token as a best-effort fallback so a later
		// unchanged-comparison can still be attempted.
		fp = version
	}
	w.mu.Lock()
	w.ledger[key] = fp
	w.mu.Unlock()
}

// WasReadUnchanged reports whether path was previously recorded via RecordRead
// and its current on-disk fingerprint still equals the recorded one. It returns
// false if path was never read or if the file changed (or vanished) since. The
// lookup uses the same canonical ledger key as RecordRead, so a read by absolute
// path and a check by relative path (or the reverse) agree.
func (w *Workspace) WasReadUnchanged(_ context.Context, path string) (bool, error) {
	key := w.ledgerKey(path)
	w.mu.Lock()
	recorded, ok := w.ledger[key]
	w.mu.Unlock()
	if !ok {
		return false, nil
	}
	current, err := w.fingerprint(path)
	if err != nil {
		// File unreadable/removed since the read: treat as changed, not an error.
		return false, nil
	}
	return current == recorded, nil
}

// fingerprint computes a sha256-based content fingerprint for a session-relative
// path.
func (w *Workspace) fingerprint(path string) (string, error) {
	// Route through the os.Root-backed FileSystem.Read so a symlink that escapes
	// the workspace cannot be fingerprinted (and thus cannot be followed out of
	// root for the read-before-edit ledger).
	data, err := w.fs.Read(context.Background(), path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// cappedBuffer is a bytes.Buffer-like writer that stops accepting bytes once cap
// is reached, so command output is bounded.
type cappedBuffer struct {
	buf bytes.Buffer
	cap int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	remaining := c.cap - c.buf.Len()
	if remaining <= 0 {
		return len(p), nil // discard, but report full consumption
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string { return c.buf.String() }
