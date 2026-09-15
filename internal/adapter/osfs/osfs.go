// Package osfs implements tool.FileSystem over the real operating-system
// filesystem and a tool.Workspace that scopes every path under a single session
// root. The canonical path forms a Workspace method accepts are:
//
//   - session-RELATIVE paths (the usual form), interpreted relative to the root;
//   - ABSOLUTE paths that canonicalize INSIDE the workspace root (the same
//     physical file a relative path would reach, addressed by its absolute
//     alias) — accepted by all five FS tools (Read/Write/Stat and, via the Edit
//     read-ledger, Edit/Glob/Grep's callers).
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
// The Workspace also carries the per-session version ledger
// (RecordRead/RecordedVersion) and mints sha256 content versions from
// ReadVersion. Edit uses the recorded/current comparison plus final ReplaceFile
// to enforce read-before-edit-and-unchanged. The ledger normalizes keys via
// resolvePath, so a file read by absolute path and then edited by relative path
// (or vice versa) matches — the key is canonical, not the verbatim argument.
package osfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/maphash"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hashutil"
	"github.com/stacklok/mecatl/internal/adapter/managedtemp"
	"github.com/stacklok/mecatl/internal/adapter/procgroup"
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
// session-relative paths and absolute paths that resolve inside the workspace
// root (reduced to their root-relative form via resolveInRoot).
//
// Every file operation goes through an *os.Root opened on the workspace root,
// which refuses both lexical ".." escapes and symlink traversal that would leave
// the root. This closes the gap a purely lexical cleanPath left open: the model
// can create a symlink inside the workspace (via Shell `ln -s /etc/passwd evil`),
// and both resolveInRoot (for absolute addresses) and os.Root (for all
// in-root paths) refuse to follow it out of the root.
type FileSystem struct {
	root string
	r    *os.Root
	// relaxedReads enables the WithRelaxedReads out-of-root absolute read
	// carve-out (default off) — see relaxedReadRoot.
	relaxedReads bool
	// relaxedWrites enables the WithRelaxedWrites out-of-root absolute write
	// carve-out (default off) — see relaxedWriteRoot.
	relaxedWrites bool
}

// Option configures a FileSystem (and the Workspace composing it) at
// construction.
type Option func(*fsOptions)

// fsOptions collects the construction-time options.
type fsOptions struct {
	relaxedReads  bool
	relaxedWrites bool
}

// WithRelaxedReads lets Read and Stat — and ONLY Read and Stat — serve an
// ABSOLUTE path that canonicalizes OUTSIDE the workspace root. It is an
// EXPLICIT construction option, DEFAULT OFF: the zero-value workspace keeps the
// canonicalize-then-reject behaviour (ErrPathEscape). The composition layer
// enables it for the MAIN session's workspace only, at the yolo/auto operator
// postures where Shell already reads the same bytes (the honesty fix —
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
// path — serve an ABSOLUTE path that canonicalizes OUTSIDE the workspace root.
// It is an EXPLICIT construction option, DEFAULT OFF: the
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
// (vetted by the same Canonicalize-based vetRelaxedParent containment check
// the relaxed read uses) and writes the leaf through it — never a bare os.WriteFile — so a symlinked
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
	return &FileSystem{root: abs, r: r, relaxedReads: o.relaxedReads, relaxedWrites: o.relaxedWrites}, nil
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

// resolveRead maps a Read/Stat path onto the os.Root that serves it. Relative
// paths and absolute paths that resolve inside the workspace use the workspace
// root. WithRelaxedReads may serve an out-of-root absolute path; otherwise the
// original escape error is returned.
func (f *FileSystem) resolveRead(path string) (*os.Root, string, error) {
	rel, err := f.resolvePath(path)
	if err == nil {
		return f.r, rel, nil
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
//  2. VET: before opening, vetRelaxedParent walks the verbatim PARENT one
//     component at a time through the shared Canonicalize (deepest EXISTING
//     ancestor + EvalSymlinks). Each resolved component must remain at or
//     below its resolved predecessor. This permits a top-level system alias
//     such as macOS /var -> /private/var while rejecting a nested symlink that
//     jumps outside the directory reached before it. Canonicalizing only the
//     whole path would resolve to the target and launder that escape.
//
// It only ever fires after resolvePath declined, so the target is provably
// outside the workspace root. An unverifiable ancestor or unopenable parent
// fails safe with no root (the caller returns the original escape error). Only
// Read/Stat consult it (via resolveRead); writes never do.
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

// vetRelaxedParent canonicalizes the verbatim parent dir one component at a
// time and refuses when a component resolves above or outside its canonical
// predecessor. Starting at the filesystem root deliberately permits a
// top-level system alias such as macOS /var -> /private/var; all later
// components are constrained by the canonical directory reached before them.
// It delegates every resolution to the already-extracted Canonicalize
// (AC-W2-F2) — no third hand-rolled ancestor-resolution algorithm — so future
// canonicalization changes cannot drift this containment check. The parent
// itself need not exist yet (Canonicalize re-appends unresolved tails). Any
// unverifiable component fails safe.
func vetRelaxedParent(parent string) bool {
	cleaned := filepath.Clean(parent)
	if !filepath.IsAbs(cleaned) {
		return false
	}

	volume := filepath.VolumeName(cleaned)
	prefix := volume + string(filepath.Separator)
	previous, err := Canonicalize("", prefix)
	if err != nil {
		return false
	}

	rel, err := filepath.Rel(prefix, cleaned)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		prefix = filepath.Join(prefix, component)
		canon, err := Canonicalize("", prefix)
		if err != nil || !pathAtOrBelow(previous, canon) {
			return false
		}
		previous = canon
	}
	return true
}

func pathAtOrBelow(base, candidate string) bool {
	rel, err := filepath.Rel(base, candidate)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Write is intentionally NOT a method of osfs.FileSystem: the agent-facing tools
// use the version-bearing Workspace mutations (CreateFile/ReplaceFile) on the
// composed Workspace. The unconditional write survives only as the
// adapter-public bootstrap operation osfs.Workspace.Write (used by tests and
// cmd/mecademo to seed fixtures), which performs its OWN resolve/lock through
// the Workspace seam.

// relaxedWriteRoot serves an out-of-root ABSOLUTE write under the
// WithRelaxedWrites option (default off — a zero-value FileSystem never
// reaches here). It mirrors relaxedReadRoot exactly: the SAME vetRelaxedParent
// Canonicalize-based containment vet refuses a verbatim parent whose resolved
// ancestor escapes the verbatim prefix, then a FRESH *os.Root on the lexical
// parent serves the leaf,
// so a symlinked component inside the parent that escapes further is refused
// by that root's own traversal (mapEscape at the call site) — the write never
// becomes a bare os.WriteFile that would follow the symlink out.
//
// Unlike relaxedReadRoot it additionally requires the verbatim parent to EXIST:
// out-of-root writes create the LEAF only, never ancestor directories (the
// in-root Write's MkdirAll has no relaxed analogue — mkdir-ing a host path is a
// strictly wider mutation the Scenario 3 relax does not grant). Only Write
// consults it; Glob/Grep never resolve paths at all.
func (f *FileSystem) relaxedWriteRoot(path string) (*os.Root, string, bool) {
	if !f.relaxedWrites || !filepath.IsAbs(path) {
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

// Stat returns metadata for the file at the session-relative path.
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
func (f *FileSystem) Glob(ctx context.Context, pattern string) ([]string, error) {
	var matches []string
	if err := f.globWalk(ctx, pattern, func(path string, _ fs.DirEntry) error {
		matches = append(matches, path)
		return nil
	}); err != nil {
		return nil, err
	}
	// The public contract advertises sorted output independently of traversal
	// order.
	sort.Strings(matches)
	return matches, nil
}

// globWalk visits root-relative matches without first materializing them.
func (f *FileSystem) globWalk(ctx context.Context, pattern string, visit func(string, fs.DirEntry) error) error {
	// doublestar patterns are root-relative, slash-separated paths. normalizeGlobPattern
	// preserves the old filepath.Join leniency (silently absorbing a leading "/"
	// or "./", which would otherwise be an invalid absolute pattern). An empty or
	// "." pattern normalizes to "" → match-nothing; the Glob tool already rejects
	// "" upstream, but stay robust here rather than globbing the entire root.
	pat := normalizeGlobPattern(filepath.ToSlash(pattern))
	if pat == "" {
		return nil
	}

	// Walk the os.Root-confined fs.FS. f.r.FS() (Go 1.24+) returns an fs.FS that
	// refuses to traverse any symlink that would leave the root, so escaping
	// intermediate-directory components are rejected by construction — closing
	// the filename-enumeration leak filepath.Glob had. WithNoFollow keeps the
	// walk from descending into symlinked directories.
	return doublestar.GlobWalk(f.r.FS(), pat, func(path string, entry fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Drop leaf symlink matches to preserve the previous behavior exactly: a
		// symlink inside the root can still target a file outside it, and Glob
		// must not be a channel for following links out of the workspace. This
		// mirrors the old Lstat + fs.ModeSymlink drop.
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		// A bare "**" matches the root itself as "."; surfacing the workspace root
		// to the model is meaningless, so drop it (filepath.Glob never produced it).
		if path == "." {
			return nil
		}
		// path is already root-relative and slash-separated. Returning the visitor
		// error stops GlobWalk immediately.
		return visit(path, entry)
	}, doublestar.WithNoFollow())
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
// classifier consumes this with LocalizeInRoot so its in-root/escape verdict is
// single-sourced with the tool body (docs/acceptance/path-escape-posture.md
// Scenario 1) instead of reimplementing the algorithms.
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

// pathLocks is a fixed process-wide set of striped mutexes. Hashing a physical
// canonical mutation target to the same stripe serializes aliases of that file across
// Workspace instances over the same root (ADR 0208). Stripe collisions only
// serialize unrelated files; the fixed array avoids an unbounded path-key map.
//
// This is PROCESS-SCOPED same-process cooperation, not a POSIX lock:
//   - It serializes only cooperating Workspace writers in THIS process. A
//     shell command, another process, or an editor writing the same file
//     directly does not participate and can still race the final write.
//   - The lock identity is derived from the canonical mutation target
//     (canonicalMutationPath: full-target canonicalization for an existing
//     target, canonical parent + basename for a missing one). A NON-COOPERATING
//     writer that races target EXISTENCE or a SYMLINK IDENTITY during that
//     pre-lock canonicalization step (e.g. swaps the file for a symlink between
//     the Lstat and the Canonicalize) can cause a cooperating writer to lock a
//     different identity than the bytes it ultimately writes. osfs does NOT
//     re-resolve under the lock; the final confined write still flows through
//     *os.Root, which refuses a symlink traversal that escapes the root, but a
//     race against an in-root symlink swap is not closed by the lock alone.
//     A future remote backend provides true backend CAS, which closes the gap
//     by making the conditional replace atomic at the storage layer (ADR 0208,
//     remote transport deferred).
const pathLockStripes = 256

var pathLocks [pathLockStripes]sync.Mutex

// pathLockSeed is a process-local hash/maphash.Seed so the stripe assignment is
// stable within a process (a fixed array MUST hash deterministically per
// process so an alias always lands on the same stripe) without a hand-rolled
// FNV-1a.
var pathLockSeed = maphash.MakeSeed()

func pathLock(canon string) *sync.Mutex {
	var h maphash.Hash
	h.SetSeed(pathLockSeed)
	_, _ = h.WriteString(canon)
	return &pathLocks[h.Sum64()%pathLockStripes]
}

// Workspace is the session-scoped seam over the real OS filesystem. It composes
// a FileSystem, performs an in-Go recursive Grep, and carries the read-ledger
// plus the explicit create-only / conditional-replace mutation operations
// (ADR 0208). Command execution is NOT part of the Workspace: it lives behind
// the separate CommandRunner type (see NewCommandRunner) so the harness can run
// without any shell at all.
type Workspace struct {
	fs *FileSystem
}

// NewWorkspace returns a content Workspace rooted at the given directory.
func NewWorkspace(root string, opts ...Option) (*Workspace, error) {
	fsys, err := NewFileSystem(root, opts...)
	if err != nil {
		return nil, err
	}
	return &Workspace{fs: fsys}, nil
}

// Compile-time assertions that Workspace satisfies the filesystem and authority seams.
var _ tool.Workspace = (*Workspace)(nil)
var _ tool.WorkspaceNamespace = (*Workspace)(nil)
var _ tool.AuthorityResourceResolver = (*Workspace)(nil)

// Root returns the absolute session root all paths are scoped to.
func (w *Workspace) Root() string { return w.fs.Root() }

// AuthorityResourcePath resolves path to the physical target identity used for
// ordinary confined workspace access. A target outside the physical workspace root
// is rejected; relaxed serving must opt in through RelaxedAuthorityResourcePath.
func (w *Workspace) AuthorityResourcePath(path string) (target, workspace string, err error) {
	target, workspace, err = w.authorityResourcePath(path)
	if err != nil {
		return "", "", err
	}
	if target != workspace && !strings.HasPrefix(target, workspace+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%w: %q", ErrPathEscape, path)
	}
	return target, workspace, nil
}

// RelaxedAuthorityResourcePath resolves the physical target identity for the
// explicitly relaxed serving path. It is adapter-specific: escapeWorkspace uses
// it only after its ordinary escape policy has authorized the operation. The
// relaxed filesystem seam serves absolute operands only; relative traversal is
// never forwarded into the out-of-root carve-out.
func (w *Workspace) RelaxedAuthorityResourcePath(path string) (target, workspace string, err error) {
	if !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("%w: %q", ErrPathEscape, path)
	}
	return w.authorityResourcePath(path)
}

func (w *Workspace) authorityResourcePath(path string) (target, workspace string, err error) {
	workspace = w.fs.Root()
	target, err = Canonicalize(workspace, path)
	if err != nil {
		return "", "", err
	}
	return target, workspace, nil
}

// Read returns the contents of the file at the session-relative path.
func (w *Workspace) Read(ctx context.Context, path string) ([]byte, error) {
	return w.fs.Read(ctx, path)
}

// ReadDir returns the immediate children of a confined physical directory.
func (w *Workspace) ReadDir(ctx context.Context, path string) ([]tool.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rel, err := w.fs.resolvePath(path)
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(w.fs.r.FS(), filepath.ToSlash(rel))
	if err != nil {
		return nil, mapEscape(path, err)
	}
	out := make([]tool.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, mapEscape(path, err)
		}
		out = append(out, toFileInfo(info))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Remove deletes one file or empty directory. It never recursively removes a
// directory and does not participate in the read ledger.
func (w *Workspace) Remove(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rel, err := w.fs.resolvePath(path)
	if err != nil {
		return err
	}
	canon, err := canonicalMutationPath(w.fs.root, path)
	if err != nil {
		return err
	}
	if !pathAtOrBelow(w.fs.root, canon) {
		return fmt.Errorf("%w: %q", ErrPathEscape, path)
	}
	mu := pathLock(canon)
	mu.Lock()
	defer mu.Unlock()
	if err := w.fs.r.Remove(rel); err != nil {
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
			return &fs.PathError{Op: "remove", Path: path, Err: tool.ErrDirectoryNotEmpty}
		}
		return mapEscape(path, err)
	}
	return nil
}

// Rename moves a confined file or directory without intentionally replacing an
// existing destination. Cooperating workspace mutations are serialized; as with
// the existing local CAS contract, arbitrary external POSIX writers do not
// participate in these process-local locks.
func (w *Workspace) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	oldRel, err := w.fs.resolvePath(oldPath)
	if err != nil {
		return err
	}
	newRel, err := w.fs.resolvePath(newPath)
	if err != nil {
		return err
	}
	oldCanon, err := canonicalMutationPath(w.fs.root, oldPath)
	if err != nil {
		return err
	}
	newCanon, err := canonicalMutationPath(w.fs.root, newPath)
	if err != nil {
		return err
	}
	release := lockMutationPair(oldCanon, newCanon)
	defer release()
	if _, err := w.fs.r.Lstat(newRel); err == nil {
		return &fs.PathError{Op: "rename", Path: newPath, Err: fs.ErrExist}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return mapEscape(newPath, err)
	}
	if dir := filepath.Dir(newRel); dir != "." {
		if err := w.fs.r.MkdirAll(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return mapEscape(newPath, err)
		}
	}
	return mapEscape(oldPath, w.fs.r.Rename(oldRel, newRel))
}

// CopyFile copies one regular file to a new destination. The destination create
// is exclusive and the operation is independent of the read ledger.
func (w *Workspace) CopyFile(ctx context.Context, source, destination string) (tool.FileVersion, error) {
	if err := ctx.Err(); err != nil {
		return tool.FileVersion{}, err
	}
	data, _, err := w.ReadVersion(ctx, source)
	if err != nil {
		return tool.FileVersion{}, err
	}
	stat, err := w.Stat(ctx, source)
	if err != nil {
		return tool.FileVersion{}, err
	}
	if !stat.Mode.IsRegular() {
		return tool.FileVersion{}, &fs.PathError{Op: "copy", Path: source, Err: fs.ErrInvalid}
	}
	return w.CreateFile(ctx, destination, data)
}

func lockMutationPair(first, second string) func() {
	firstLock, secondLock := pathLock(first), pathLock(second)
	if firstLock == secondLock {
		firstLock.Lock()
		return firstLock.Unlock
	}
	// Canonical lexical order gives every caller the same lock order and avoids
	// deadlocks when two concurrent renames swap operands.
	if first > second {
		firstLock, secondLock = secondLock, firstLock
	}
	firstLock.Lock()
	secondLock.Lock()
	return func() {
		secondLock.Unlock()
		firstLock.Unlock()
	}
}

// Write replaces the contents of the file at the session-relative path, creating
// parents as needed. This adapter-public bootstrap operation is deliberately not
// part of tool.Workspace; tools use CreateFile/ReplaceFile.
func (w *Workspace) Write(ctx context.Context, path string, data []byte) error {
	rel, root, leaf, release, err := w.lockMutationTarget(ctx, path)
	if err != nil {
		return err
	}
	defer release()
	return w.writeResolved(rel, root, leaf, path, data)
}

// lockMutationTarget is the shared mutation preamble: it checks ctx, resolves
// the confined write target, opens the per-path lock stripe, and returns a
// release closure the caller MUST defer. The release unlocks the stripe and, if
// a relaxed out-of-root os.Root was opened, closes it. Confinement is unchanged
// — this is pure factoring of the resolve/lock/release sequence Write/CreateFile/
// ReplaceFile all repeated.
func (w *Workspace) lockMutationTarget(ctx context.Context, path string) (rel string, root *os.Root, leaf string, release func(), err error) {
	if cerr := ctx.Err(); cerr != nil {
		return "", nil, "", nil, cerr
	}
	canon, rel, r, l, rerr := w.resolveWriteAbs(path)
	if rerr != nil {
		return "", nil, "", nil, rerr
	}
	mu := pathLock(canon)
	mu.Lock()
	release = func() {
		mu.Unlock()
		if r != nil {
			_ = r.Close()
		}
	}
	return rel, r, l, release, nil
}

// ReadVersion returns the contents of the file at path AND the authoritative
// FileVersion (sha256 of the content). It reads through the same resolvePath/
// resolveInRoot/relaxed-read path as the plain Read, so the content and the
// version are a consistent snapshot of the on-disk file.
func (w *Workspace) ReadVersion(ctx context.Context, path string) ([]byte, tool.FileVersion, error) {
	data, err := w.fs.Read(ctx, path)
	if err != nil {
		return nil, tool.FileVersion{}, err
	}
	return data, osfsVersion(data), nil
}

// ReadVersionBounded reads through the confined os.Root and limits allocation
// before content is materialized. A concurrent growth past maxBytes is detected
// by the maxBytes+1 sentinel read and rejected.
func (w *Workspace) ReadVersionBounded(_ context.Context, path string, maxBytes int64) ([]byte, tool.FileVersion, error) {
	if maxBytes < 0 {
		return nil, tool.FileVersion{}, errors.New("osfs: negative bounded read limit")
	}
	r, rel, err := w.fs.resolveRead(path)
	if err != nil {
		return nil, tool.FileVersion{}, err
	}
	if info, statErr := r.Stat(rel); statErr == nil && info.Size() > maxBytes {
		return nil, tool.FileVersion{}, fmt.Errorf("osfs: file %q is %d bytes, exceeds the %d-byte bounded read limit", path, info.Size(), maxBytes)
	}
	file, err := r.Open(rel)
	if err != nil {
		return nil, tool.FileVersion{}, mapEscape(path, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, tool.FileVersion{}, err
	}
	if int64(len(data)) > maxBytes {
		return nil, tool.FileVersion{}, fmt.Errorf("osfs: file %q exceeds the %d-byte bounded read limit", path, maxBytes)
	}
	return data, osfsVersion(data), nil
}

// ReadVersionRangeBounded reads one range through the confined file descriptor,
// hashes the opened snapshot without materializing the rest, and rejects the
// source before page allocation when its size exceeds totalLimit.
func (w *Workspace) ReadVersionRangeBounded(ctx context.Context, path string, offset, maxBytes, totalLimit int64) ([]byte, tool.FileVersion, int64, error) {
	if offset < 0 || maxBytes < 0 || totalLimit < 0 {
		return nil, tool.FileVersion{}, 0, errors.New("osfs: negative bounded range")
	}
	r, rel, err := w.fs.resolveRead(path)
	if err != nil {
		return nil, tool.FileVersion{}, 0, err
	}
	file, err := r.Open(rel)
	if err != nil {
		return nil, tool.FileVersion{}, 0, mapEscape(path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, tool.FileVersion{}, 0, err
	}
	total := info.Size()
	if total > totalLimit || offset > total {
		return nil, tool.FileVersion{}, total, fmt.Errorf("osfs: file %q exceeds bounded range", path)
	}
	pageSize := min(maxBytes, total-offset)
	page := make([]byte, pageSize)
	if pageSize > 0 {
		if _, err := file.ReadAt(page, offset); err != nil && !errors.Is(err, io.EOF) {
			return nil, tool.FileVersion{}, total, err
		}
	}
	hash := sha256.New()
	buf := make([]byte, 32*1024)
	var readTotal int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, tool.FileVersion{}, total, err
		}
		n, readErr := file.Read(buf)
		if n > 0 {
			readTotal += int64(n)
			_, _ = hash.Write(buf[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, tool.FileVersion{}, total, readErr
		}
	}
	after, err := file.Stat()
	if err != nil || readTotal != total || after.Size() != total {
		return nil, tool.FileVersion{}, total, fmt.Errorf("osfs: file %q changed during bounded range read", path)
	}
	return page, tool.NewFileVersion(fmt.Sprintf("%x", hash.Sum(nil))), total, nil
}

// osfsVersion mints a FileVersion from content bytes (sha256 via
// hashutil.SHA256Hex, the shared adapter-layer fingerprint primitive). It is
// the single osfs version primitive, shared by ReadVersion/CreateFile/
// ReplaceFile so a recorded version and a freshly-minted version are
// byte-identical for the same content.
func osfsVersion(data []byte) tool.FileVersion {
	return tool.NewFileVersion(hashutil.SHA256Hex(data))
}

// canonicalMutationPath returns the physical absolute identity used for mutation
// locking. Existing targets reuse Canonicalize on the full operand (including a
// symlink leaf). Missing targets intentionally canonicalize the parent and append
// only the basename, making the create identity explicit. The Lstat only selects
// those semantics; a dangling/ambiguous symlink fails safe.
func canonicalMutationPath(base, path string) (string, error) {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(base, filepath.FromSlash(path))
	}
	abs = filepath.Clean(abs)
	_, err := os.Lstat(abs)
	switch {
	case err == nil:
		return Canonicalize("", abs)
	case errors.Is(err, fs.ErrNotExist):
		parent, leaf := filepath.Dir(abs), filepath.Base(abs)
		resolvedParent, rerr := Canonicalize("", parent)
		if rerr != nil {
			return "", rerr
		}
		return filepath.Join(resolvedParent, leaf), nil
	default:
		return "", fmt.Errorf("%w: %q cannot be verified: %v", ErrPathEscape, path, err)
	}
}

// resolveWriteAbs returns the physical canonical lock identity and the confined
// operation path. Existing in-root symlink aliases are reduced to their target;
// a missing target resolves its parent before appending the basename. The
// resulting in-root operation still flows through the workspace *os.Root.
func (w *Workspace) resolveWriteAbs(path string) (canon string, rel string, root *os.Root, leaf string, err error) {
	canon, err = canonicalMutationPath(w.fs.root, path)
	if err != nil {
		return "", "", nil, "", err
	}
	if pathAtOrBelow(w.fs.root, canon) {
		rel, _ = filepath.Rel(w.fs.root, canon)
		return canon, filepath.ToSlash(rel), nil, "", nil
	}

	// A relative operand, or an absolute operand lexically inside the workspace
	// whose physical target escaped it, is never eligible for relaxed writes.
	if !filepath.IsAbs(path) || pathAtOrBelow(w.fs.root, filepath.Clean(path)) {
		return "", "", nil, "", fmt.Errorf("%w: %q", ErrPathEscape, path)
	}
	// Out-of-root relaxed write path (WithRelaxedWrites), preserving the existing
	// lexical-parent vet. canon remains the physical lock identity so aliases
	// converge across Workspace instances.
	if r, relaxedLeaf, ok := w.fs.relaxedWriteRoot(path); ok {
		return canon, "", r, relaxedLeaf, nil
	}
	return "", "", nil, "", fmt.Errorf("%w: %q", ErrPathEscape, path)
}

// writeResolved writes data to the already-resolved write target: the in-root
// (rel, os.Root) path or the relaxed (root, leaf) path. It mirrors
// FileSystem.Write's directory-creation + WriteFile, minus the resolve (already
// done) so CreateFile/ReplaceFile can reuse it under the per-path lock.
func (w *Workspace) writeResolved(rel string, root *os.Root, leaf string, path string, data []byte) error {
	if root == nil {
		// In-root write through the workspace's *os.Root.
		if dir := filepath.Dir(rel); dir != "." {
			if err := w.fs.r.MkdirAll(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return mapEscape(path, err)
			}
		}
		if err := w.fs.r.WriteFile(rel, data, 0o644); err != nil {
			return mapEscape(path, err)
		}
		return nil
	}
	// Relaxed out-of-root write through a fresh *os.Root on the parent.
	return mapEscape(path, root.WriteFile(leaf, data, 0o644))
}

// createResolved performs an actual exclusive create through the confined
// *os.Root. The O_EXCL check is the final defense against non-cooperating
// creators that do not participate in pathLocks.
func (w *Workspace) createResolved(rel string, root *os.Root, leaf string, path string, data []byte) error {
	targetRoot, target := root, leaf
	if targetRoot == nil {
		targetRoot, target = w.fs.r, rel
		if dir := filepath.Dir(rel); dir != "." {
			if err := targetRoot.MkdirAll(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return mapEscape(path, err)
			}
		}
	}
	file, err := targetRoot.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return mapEscape(path, err)
	}
	// os.File.Write on a regular file writes all of data or returns an error;
	// the short-write (n < len(data), err == nil) path is unreachable here, so a
	// Write error is the complete write failure.
	if _, writeErr := file.Write(data); writeErr != nil {
		_ = file.Close()
		return mapEscape(path, writeErr)
	}
	return mapEscape(path, file.Close())
}

// readResolved reads the current content (and existence) at the already-resolved
// write target, so CreateFile/ReplaceFile can do the compare half under the
// per-path lock WITHOUT a re-resolve race.
func (w *Workspace) readResolved(rel string, root *os.Root, leaf string) ([]byte, bool, error) {
	if root == nil {
		// In-root read through the workspace's *os.Root.
		data, err := w.fs.r.ReadFile(rel)
		if err == nil {
			return data, true, nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, mapEscape(rel, err)
	}
	// Relaxed out-of-root read through a fresh *os.Root on the parent.
	data, err := root.ReadFile(leaf)
	if err == nil {
		return data, true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, mapEscape(leaf, err)
}

// CreateFile creates a NEW file at path with the given content, atomically. It
// fails (wrapping fs.ErrExist) if a file already exists. Parent directories are
// created as needed. It serializes aliases through process-wide physical-target
// lock striping and performs the create with O_CREATE|O_EXCL (ADR 0208 §5).
func (w *Workspace) CreateFile(ctx context.Context, path string, data []byte) (tool.FileVersion, error) {
	rel, root, leaf, release, err := w.lockMutationTarget(ctx, path)
	if err != nil {
		return tool.FileVersion{}, err
	}
	defer release()
	if err := w.createResolved(rel, root, leaf, path, data); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return tool.FileVersion{}, &fs.PathError{Op: "create", Path: path, Err: fs.ErrExist}
		}
		return tool.FileVersion{}, err
	}
	return osfsVersion(data), nil
}

// ReplaceFile conditionally replaces the contents of the file at path with data,
// only if the file's current authoritative version equals old. On a version
// mismatch it returns a *tool.VersionMismatchError; on a missing file it returns
// an error wrapping fs.ErrNotExist. It serializes against other same-path
// mutations through process-wide canonical-path lock striping, so the
// compare+write is atomic with respect to cooperating Workspace writers
// (ADR 0208 §5).
func (w *Workspace) ReplaceFile(ctx context.Context, path string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	rel, root, leaf, release, err := w.lockMutationTarget(ctx, path)
	if err != nil {
		return tool.FileVersion{}, err
	}
	defer release()
	cur, exists, err := w.readResolved(rel, root, leaf)
	if err != nil {
		return tool.FileVersion{}, err
	}
	if !exists {
		return tool.FileVersion{}, fmt.Errorf("osfs: replace %q: %w", path, fs.ErrNotExist)
	}
	have := osfsVersion(cur)
	if !have.Equal(old) {
		return tool.FileVersion{}, &tool.VersionMismatchError{Path: path}
	}
	if err := w.writeResolved(rel, root, leaf, path, data); err != nil {
		return tool.FileVersion{}, err
	}
	return osfsVersion(data), nil
}

// Stat returns metadata for the file at the session-relative path.
func (w *Workspace) Stat(ctx context.Context, path string) (tool.FileInfo, error) {
	return w.fs.Stat(ctx, path)
}

// Glob returns session-relative paths matching the shell-style pattern.
func (w *Workspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	return w.fs.Glob(ctx, pattern)
}

// maxGrepFiles and maxGrepBytes bound every search before a broad path glob can
// turn a workspace root into an unbounded traversal.
const (
	maxGrepFiles   = 10_000
	maxGrepBytes   = 64 << 20 // 64 MiB
	grepMatchLimit = 201      // GrepTool renders 200 matches plus its truncation marker.
)

var errGrepMatchLimit = errors.New("osfs: grep match limit reached")

const grepSafetyBudgetMessage = "grep search exceeds the workspace safety budget; narrow the path (for example, internal/**/*.go)"

// Grep returns the matches of a regular expression across files selected by an
// optional path glob (relative to root), within an aggregate safety budget.
// Binary-looking files (those containing a NUL byte) are skipped. The search
// honors ctx cancellation.
func (w *Workspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("osfs: invalid grep pattern: %w", err)
	}
	search := grepSearch{
		ctx:      ctx,
		re:       re,
		maxFiles: maxGrepFiles,
		maxBytes: maxGrepBytes,
	}
	if pathGlob == "" {
		err = w.grepAll(&search)
	} else {
		err = w.grepGlob(pathGlob, &search)
	}
	if errors.Is(err, errGrepMatchLimit) {
		return search.matches, nil
	}
	if err != nil {
		return nil, err
	}
	return search.matches, nil
}

type grepSearch struct {
	ctx       context.Context
	re        *regexp.Regexp
	matches   []tool.GrepMatch
	files     int
	readBytes int64
	maxFiles  int
	maxBytes  int64
}

func (s *grepSearch) scan(w *Workspace, rel string, info fs.FileInfo) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	s.files++
	if s.files > s.maxFiles || info.Size() > s.maxBytes-s.readBytes {
		return errors.New(grepSafetyBudgetMessage)
	}
	s.readBytes += info.Size()
	data, err := w.fs.Read(s.ctx, rel)
	if err != nil {
		return nil // unreadable / vanished file: skip
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil // binary file
	}
	for lineNo, line := range strings.Split(string(data), "\n") {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		if s.re.MatchString(line) {
			s.matches = append(s.matches, tool.GrepMatch{Path: rel, Line: lineNo + 1, Text: line})
			if len(s.matches) == grepMatchLimit {
				return errGrepMatchLimit
			}
		}
	}
	return nil
}

func (w *Workspace) grepGlob(pathGlob string, search *grepSearch) error {
	return w.fs.globWalk(search.ctx, pathGlob, func(rel string, entry fs.DirEntry) error {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		return search.scan(w, rel, info)
	})
}

func (w *Workspace) grepAll(search *grepSearch) error {
	return filepath.WalkDir(w.fs.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := search.ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := w.fs.toRel(path)
		if err != nil {
			return nil
		}
		return search.scan(w, rel, info)
	})
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
	// managedWorkspace owns foreground command and background-job leases. It is
	// nil for system temporary storage and for runners that cannot make the managed
	// guarantee.
	managedWorkspace *managedtemp.Workspace
	// systemTempDir is the configured/inherited host temporary directory applied
	// only when the trusted caller selects the system scope.
	systemTempDir string
	// waitDelay is the per-command cmd.WaitDelay (defaultCommandWaitDelay unless
	// overridden via WithCommandWaitDelay — tests use a short one). See
	// defaultCommandWaitDelay for the grandchild-pipe rationale (A7).
	waitDelay time.Duration
}

// BoundWorkspaceRoot reports the immutable command namespace root. Placement
// binding uses it to prove that a returned runner and Workspace share one root.
func (r *CommandRunner) BoundWorkspaceRoot() string { return r.root }

// ShellPath reports the configured shell path without resolving symlinks. The
// Shell tool uses its basename only for the bounded POSIX compatibility
// diagnostic; execution continues to use this runner's exact configured path.
func (r *CommandRunner) ShellPath() string { return r.shell }

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

// WithManagedTemporaryWorkspace makes foreground Run calls allocate one private
// managed command lease from workspace. The overlay is constructed internally
// after the runner's already-scrubbed base environment; callers cannot supply
// lease paths through shell text or tool arguments.
func WithManagedTemporaryWorkspace(workspace *managedtemp.Workspace) CommandRunnerOption {
	return func(r *CommandRunner) { r.managedWorkspace = workspace }
}

// WithSystemTemporaryDirectory sets the configured/inherited system temporary
// directory used for explicit system-scope Shell calls.
func WithSystemTemporaryDirectory(dir string) CommandRunnerOption {
	return func(r *CommandRunner) { r.systemTempDir = dir }
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
// (and the Shell tool) to run shell-less.
func NewCommandRunner(dir string) (tool.CommandRunner, error) {
	return NewCommandRunnerShell(dir, "/bin/sh")
}

// NewCommandRunnerShell is like NewCommandRunner but lets the caller pick the
// shell binary (e.g. "/bin/bash"). An empty shell is rejected: a shell-less
// deployment must omit the runner (and the Shell tool) entirely rather than
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

// Compile-time assertion that CommandRunner satisfies the runner port and the
// OPTIONAL streaming capability (a background command's tail-ring capture runs
// through it).
var (
	_ tool.CommandRunner                 = (*CommandRunner)(nil)
	_ tool.CommandStreamer               = (*CommandRunner)(nil)
	_ tool.CommandEnvironmentRunner      = (*CommandRunner)(nil)
	_ tool.CommandEnvironmentStreamer    = (*CommandRunner)(nil)
	_ tool.CommandTemporaryScopeRunner   = (*CommandRunner)(nil)
	_ tool.CommandTemporaryScopeStreamer = (*CommandRunner)(nil)
)

// Run runs command via /bin/sh -c, capturing (and truncating) stdout/stderr and
// the exit code. The working directory is the runner's BOUND root (issue #462):
// the runner is bound to a single namespace at construction, so the command's
// cwd always matches the workspace the tool executes against. Cancellation and
// timeout are governed by ctx; when ctx has no deadline a default timeout is
// applied. On cancel/timeout the WHOLE process group is SIGKILLed (POSIX; see
// procgroup.Configure), so a backgrounded grandchild (e.g. `make`'s compiler
// children) dies with the shell instead of being orphaned; WaitDelay stays the
// portable backstop. A non-zero exit is reported via the returned
// CommandResult.ExitCode, not as an error.
func (r *CommandRunner) Run(ctx context.Context, command string) (tool.CommandResult, error) {
	return r.runResult(ctx, command, tool.CommandEnvironmentOverlay{}, true)
}

// RunWithTemporaryScope selects the closed temporary-storage scope for this
// invocation. System scope never allocates a managed lease.
func (r *CommandRunner) RunWithTemporaryScope(ctx context.Context, command string, scope tool.TemporaryScope) (tool.CommandResult, error) {
	overlay := tool.CommandEnvironmentOverlay{}
	managed := scope == tool.TemporaryScopeManaged
	if scope == tool.TemporaryScopeSystem || r.managedWorkspace == nil {
		managed = false
		if r.systemTempDir != "" {
			overlay.TempDir = r.systemTempDir
			overlay.GoTempDir = r.systemTempDir
		}
	}
	return r.runResult(ctx, command, overlay, managed)
}

// RunWithEnvironment runs command with overlay applied only to this invocation.
// It preserves the runner's bound root and does not retain the overlay.
func (r *CommandRunner) RunWithEnvironment(ctx context.Context, command string, overlay tool.CommandEnvironmentOverlay) (tool.CommandResult, error) {
	return r.runResult(ctx, command, overlay, true)
}

func (r *CommandRunner) runResult(ctx context.Context, command string, overlay tool.CommandEnvironmentOverlay, managed bool) (tool.CommandResult, error) {
	var stdout, stderr cappedBuffer
	stdout.cap = maxCommandOutput
	stderr.cap = maxCommandOutput

	exitCode, err := r.run(ctx, command, overlay, managed, "cmd", &stdout, &stderr)
	res := tool.CommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}
	return res, err
}

// RunStreaming is the tool.CommandStreamer half of the runner: it runs command
// exactly as Run does (same shell resolution, bound-root cwd, default timeout,
// process-group kill, WaitDelay backstop, env) but streams stdout and stderr
// INTERLEAVED into out in the order the OS delivers them, instead of capturing
// them into the capped buffers. The CALLER owns bounding (e.g. a bounded tail
// ring for a background command's recent output); this path does NOT cap or
// retain the stream itself. The returned exitCode replaces CommandResult for
// this path: a non-zero exit is reported there, not as an error.
func (r *CommandRunner) RunStreaming(ctx context.Context, command string, out io.Writer) (int, error) {
	return r.run(ctx, command, tool.CommandEnvironmentOverlay{}, false, "", out, out)
}

// RunStreamingWithTemporaryScope is RunStreaming with a trusted temporary
// scope selection. System scope never allocates a managed lease.
func (r *CommandRunner) RunStreamingWithTemporaryScope(ctx context.Context, command string, scope tool.TemporaryScope, out io.Writer) (int, error) {
	overlay := tool.CommandEnvironmentOverlay{}
	managed := scope == tool.TemporaryScopeManaged
	if scope == tool.TemporaryScopeSystem || r.managedWorkspace == nil {
		managed = false
		if r.systemTempDir != "" {
			overlay.TempDir = r.systemTempDir
			overlay.GoTempDir = r.systemTempDir
		}
	}
	return r.run(ctx, command, overlay, managed, "job", out, out)
}

// RunStreamingWithEnvironment streams command output with overlay applied only
// to this invocation. It preserves the runner's bound root and does not retain
// the overlay.
func (r *CommandRunner) RunStreamingWithEnvironment(ctx context.Context, command string, overlay tool.CommandEnvironmentOverlay, out io.Writer) (int, error) {
	return r.run(ctx, command, overlay, false, "", out, out)
}

// run is the ONE spawn/wait tail Run and RunStreaming share, so the two cannot
// drift: it applies the default timeout when ctx has no deadline, runs in the
// runner's BOUND root, wires the given stdout/stderr writers plus the
// process-group kill, WaitDelay backstop and env, and runs the command to
// completion. The exit code is returned separately from the error: a non-zero
// exit yields (code, nil); a ctx cancel/timeout yields (0, ctx.Err()) with
// whatever output the writers captured so far standing; and a WaitDelay expiry
// on a successfully-exited shell is a SUCCESS carrying the partial output (see
// the exec.ErrWaitDelay branch below), not a harness failure.
func (r *CommandRunner) run(ctx context.Context, command string, overlay tool.CommandEnvironmentOverlay, managed bool, leaseKind string, stdout, stderr io.Writer) (exitCode int, err error) {
	if !managed {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, defaultCommandTimeout)
			defer cancel()
		}
	}

	lease, overlay, leaseErr := r.managedLease(managed, leaseKind, overlay)
	if leaseErr != nil {
		return 0, leaseErr
	}

	cmd := exec.CommandContext(ctx, r.shell, "-c", command)
	cmd.Dir = r.root
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Bound the post-exit/post-cancel pipe wait (A7): without it, a grandchild that
	// inherited stdout/stderr (e.g. `slow-thing &`) keeps cmd.Wait parked on the
	// pipe-copy goroutines until the grandchild exits, long after the shell itself
	// is gone. WaitDelay closes the pipes after this bound; the output captured so
	// far stands.
	cmd.WaitDelay = r.waitDelay
	// On cancel/timeout, kill the whole process group, not just the shell: a
	// backgrounded grandchild would otherwise be orphaned (and keep holding the
	// pipes past the WaitDelay until it exits on its own).
	procgroup.Configure(cmd)
	// A hardened runner carries a COMPLETE, pre-scrubbed environment. A per-call
	// overlay is merged over that same base only for this command, so it cannot
	// change the runner's namespace or affect later invocations.
	if overlay != (tool.CommandEnvironmentOverlay{}) {
		env, envErr := overlayEnvironment(r.env, overlay)
		if envErr != nil {
			if lease != nil {
				_ = lease.Remove()
			}
			return 0, envErr
		}
		cmd.Env = env
	} else if r.env != nil {
		cmd.Env = r.env
	}

	if startErr := cmd.Start(); startErr != nil {
		if lease != nil {
			_ = lease.Remove()
		}
		return 0, startErr
	}
	if lease != nil {
		if leaseErr := lease.Started(cmd.Process.Pid); leaseErr != nil {
			_ = procgroup.Kill(cmd.Process.Pid)
			_ = cmd.Wait()
			_ = lease.Close()
			return 0, fmt.Errorf("osfs: record managed command lease: %w", leaseErr)
		}
	}
	runErr := cmd.Wait()
	finishManagedLease(lease, cmd.Process.Pid, ctx.Err() != nil)

	if cerr := ctx.Err(); cerr != nil {
		// Context cancellation/timeout is a harness-level failure.
		return 0, cerr
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		// WaitDelay expired with the pipes still open but the shell itself EXITED
		// SUCCESSFULLY (a backgrounded grandchild holds the inherited fds — e.g.
		// `daemon &`). That is a success with the output captured so far, not a
		// harness failure: before WaitDelay existed this command simply blocked
		// until the grandchild exited and then succeeded.
		if errors.Is(runErr, exec.ErrWaitDelay) {
			return 0, nil
		}
		return 0, runErr
	}
	return 0, nil
}

func (r *CommandRunner) managedLease(managed bool, kind string, overlay tool.CommandEnvironmentOverlay) (*managedtemp.Lease, tool.CommandEnvironmentOverlay, error) {
	if !managed || r.managedWorkspace == nil {
		return nil, overlay, nil
	}
	lease, err := r.managedWorkspace.Allocate(kind)
	if err != nil {
		return nil, overlay, fmt.Errorf("osfs: allocate managed command lease: %w", err)
	}
	overlay.TempDir = lease.TempDir()
	overlay.GoTempDir = lease.TempDir()
	overlay.TestHomeMarker = lease.Path()
	return lease, overlay, nil
}

func finishManagedLease(lease *managedtemp.Lease, pid int, cancelled bool) {
	if lease == nil {
		return
	}
	_ = lease.Terminal()
	groupGone := !procgroup.GroupAlive(pid)
	if !groupGone && cancelled {
		groupGone = procgroup.WaitGone(pid, 100*time.Millisecond)
	}
	if groupGone {
		_ = lease.Remove()
		return
	}
	_ = lease.Close()
}

// overlayEnvironment merges a trusted one-call temporary-storage overlay over
// base without retaining either slice. A nil base means the process environment,
// matching exec.Cmd's ordinary inheritance semantics. Replacing an existing key
// avoids duplicate entries whose resolution varies by platform.
func overlayEnvironment(base []string, overlay tool.CommandEnvironmentOverlay) ([]string, error) {
	overlaid := map[string]string{}
	if overlay.TempDir != "" {
		overlaid["TMPDIR"] = overlay.TempDir
	}
	if overlay.GoTempDir != "" {
		overlaid["GOTMPDIR"] = overlay.GoTempDir
	}
	if overlay.TestHomeMarker != "" {
		overlaid["MECATL_TEST_TEMP_LEASE"] = overlay.TestHomeMarker
	}
	keys := make([]string, 0, len(overlaid))
	for key, value := range overlaid {
		if strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("osfs: invalid command environment value for %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if base == nil {
		base = os.Environ()
	}
	merged := make([]string, 0, len(base)+len(keys))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overlaid[key]; !replaced {
			merged = append(merged, entry)
		}
	}
	for _, key := range keys {
		merged = append(merged, key+"="+overlaid[key])
	}
	return merged, nil
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
		// Cut on a rune boundary (issue #402): a byte-offset cut lands mid-rune
		// roughly 2 times in 3 for multibyte output, manufacturing invalid UTF-8
		// from a command whose own output was perfectly valid — and a protobuf
		// string field rejects that at marshal time. The tail past the cap is
		// discarded anyway, so dropping the partial rune's lead bytes loses
		// nothing. Mirrors fstools truncate, which already cuts this way.
		cut := remaining
		for cut > 0 && !utf8.RuneStart(p[cut]) {
			cut--
		}
		c.buf.Write(p[:cut])
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string { return c.buf.String() }
