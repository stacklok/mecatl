// Package remoteenv is a deterministic, in-process, protocol-level reference fake
// of a REMOTE execution environment (ADR 0106, issue #462 phase 3). It is a
// CONTRACT PROOF only — it has NO network, NO external dependency, and NO global
// singleton. It proves the phase-3 persistence/reattachment/fork/merge contract
// against a non-in-tree EnvironmentKind without waiting for a real vendor
// transport, and it is deliberately NOT wired by default app.Build.
//
// The fake is structured around a Backend that owns an ID→namespace registry.
// Each namespace is an isolated in-memory file map with content-hash version
// tokens (the same version discipline the in-tree memfs adapter uses, so the
// conditional-CAS / create-only semantics hold across multiple handles to the
// same namespace). NewEnvironment(id) / Resolve(ctx, ref) return a Workspace +
// CommandRunner bound to the SAME opaque namespace; EnvironmentForker returns a
// complete isolated child Environment with a fresh opaque child ID and cleanup;
// EnvironmentMerger applies child changes to the parent by REF, preserving the
// child on conflict.
//
// The fake runner implements a deliberately tiny, documented TEST protocol
// (`cat <path>` and `write <path> <content>`); it does not hand-roll a general
// shell. The file API write/read and the fake Bash runner observe the SAME
// namespace in both directions, so a forked child's Bash observes the same
// namespace its Read/Write do.
//
// The fake's EnvironmentKind label is `remote-fake` (a package-level const in
// THIS package, NEVER a constant in the public engine/session — the
// EnvironmentKind set is open). The zero session.EnvironmentRef and the in-tree
// Kinds (local/mem/nofs) never reach this package.
package remoteenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// Kind is the EnvironmentKind label this fake mints. It is a package-level
// constant in THIS adapter package, never a constant in the public
// engine/session — the EnvironmentKind set is open, and a real remote transport
// (or another out-of-tree backend) adds its own label without widening the
// session package.
const Kind session.EnvironmentKind = "remote-fake"

// ErrUnknownNamespace is returned by Resolve/NewEnvironment when the requested
// namespace id does not exist in the Backend's registry. A non-in-tree ref
// MUST name a namespace the Backend created; the fake does not fabricate one on
// demand (honest failure, never a silent empty workspace).
var ErrUnknownNamespace = errors.New("remoteenv: unknown namespace id")

// ErrEmptyCommand is returned by the runner for a blank command string.
var ErrEmptyCommand = errors.New("remoteenv: empty command")

// file is a single in-memory file entry in a namespace.
type file struct {
	data    []byte
	modTime time.Time
}

// namespace is the shared backing state a Backend owns per id. Multiple
// Workspace/runner handles bound to the same id observe the SAME mutex-guarded
// file map, so a version minted by one handle is authoritative for every other
// handle to the same namespace — the contract a real remote CAS would provide
// at the storage layer.
//
// base, when non-nil, is the FORK-BASE snapshot a child namespace carries: a
// deep copy of the parent's file map at fork time. It is the merge anchor —
// the merger compares child-vs-base and parent-vs-base to detect a TRUE
// divergent conflict (both changed the same path) rather than conflating a
// child's modification of a file the parent did not touch post-fork with a
// conflict. A non-forked namespace has a nil base.
type namespace struct {
	id    string
	mu    sync.RWMutex
	files map[string]*file
	base  map[string][]byte // fork-base snapshot (child namespaces only)
	now   func() time.Time
}

// versionOf mints a FileVersion from content bytes (sha256), the same primitive
// the in-tree memfs adapter uses so a recorded version and a freshly-minted
// version are byte-identical for the same content.
func versionOf(data []byte) tool.FileVersion {
	sum := sha256.Sum256(data)
	return tool.NewFileVersion(hex.EncodeToString(sum[:]))
}

// read returns a copy of the file at key (under the read lock).
func (n *namespace) read(key string) ([]byte, *file, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	f, ok := n.files[key]
	if !ok {
		return nil, nil, fs.ErrNotExist
	}
	out := make([]byte, len(f.data))
	copy(out, f.data)
	return out, f, nil
}

// Backend owns an ID→namespace registry and is the single construction site for
// fake remote Environments. It is deterministic, in-process, and holds no
// network or external resource. The zero value is NOT usable; construct with
// NewBackend. It is safe for concurrent use.
type Backend struct {
	mu         sync.Mutex
	namespaces map[string]*namespace
	seq        uint64
}

// NewBackend returns an empty Backend.
func NewBackend() *Backend {
	return &Backend{namespaces: make(map[string]*namespace)}
}

// NewEnvironment creates a FRESH namespace under a new opaque id and returns a
// complete Environment (Workspace + CommandRunner bound to that namespace). It
// is the construction path: it does not resolve an existing ref (use Resolve
// for reattachment). The id is opaque and Backend-owned.
func (b *Backend) NewEnvironment(label string) (tool.Environment, error) {
	id := b.mintID(label)
	ns := b.createNamespace(id)
	ws := &workspace{ns: ns}
	runner := &runner{ns: ns}
	return tool.NewEnvironment(session.EnvironmentRef{Kind: Kind, ID: id}, ws, runner)
}

// Resolve reattaches a LIVE Environment to the namespace named by ref.ID,
// proving the restart-reattachment contract: a restarted process with a
// persisted ref resolves it back to the SAME backend state. The ref's Kind
// MUST be Kind; a mismatch is an honest error. The returned Environment's Ref()
// equals the requested ref (the Backend never rewrites identity).
func (b *Backend) Resolve(_ context.Context, ref session.EnvironmentRef) (tool.Environment, error) {
	if ref.Kind != Kind {
		return tool.Environment{}, fmt.Errorf("remoteenv: ref kind %q does not match %q", ref.Kind, Kind)
	}
	ns, err := b.namespace(ref.ID)
	if err != nil {
		return tool.Environment{}, err
	}
	ws := &workspace{ns: ns}
	runner := &runner{ns: ns}
	return tool.NewEnvironment(ref, ws, runner)
}

// mintID mints a fresh opaque namespace id. The label is folded in for
// observability only; the id is unique per call.
func (b *Backend) mintID(label string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	return fmt.Sprintf("rf-%s-%d", sanitizeLabel(label), b.seq)
}

// createNamespace inserts a fresh namespace under id. Caller has already minted
// the id (no collision under the Backend lock).
func (b *Backend) createNamespace(id string) *namespace {
	ns := &namespace{id: id, files: make(map[string]*file), now: time.Now}
	b.mu.Lock()
	b.namespaces[id] = ns
	b.mu.Unlock()
	return ns
}

// namespace returns the registered namespace for id, or ErrUnknownNamespace.
func (b *Backend) namespace(id string) (*namespace, error) {
	b.mu.Lock()
	ns, ok := b.namespaces[id]
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownNamespace, id)
	}
	return ns, nil
}

// copyNamespace returns a DEEP copy of the namespace's current file map — the
// state a fork snapshots so the child starts from the parent's contents and the
// two then diverge independently.
func (b *Backend) copyNamespace(id string) (map[string]*file, error) {
	ns, err := b.namespace(id)
	if err != nil {
		return nil, err
	}
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	out := make(map[string]*file, len(ns.files))
	for k, f := range ns.files {
		cp := &file{data: make([]byte, len(f.data)), modTime: f.modTime}
		copy(cp.data, f.data)
		out[k] = cp
	}
	return out, nil
}

// RehydrateForTest re-seeds the backend with a namespace under id, copying the
// current file state from src (a tool.Workspace from another Backend that
// minted the same id). It is the durable-stand-in for a remote backend's
// persistent state across a simulated restart: a real transport reattaches
// without copying (the state survives the process), but the in-memory fake
// simulates that by re-seeding so the Resolve path proves it reattaches to the
// same state. It is intended for the phase-3 reattachment contract proof only.
//
// It FAILS HONESTLY rather than panicking/clobbering (issue #462 phase-3
// finding #5): a nil src returns ErrNilSource, and an id that ALREADY names a
// registered namespace returns ErrNamespaceExists rather than silently
// overwriting it (a stale or accidental second rehydrate would otherwise mask
// the live namespace's state and hide a real bug). A test that intends to
// replace must explicitly delete first (the fake exposes no Delete; build a
// fresh Backend for a clean slate).
var (
	// ErrNilSource is returned by RehydrateForTest when src is nil.
	ErrNilSource = errors.New("remoteenv: rehydrate source workspace is nil")
	// ErrNamespaceExists is returned by RehydrateForTest when id already names a
	// registered namespace (refuse-to-clobber).
	ErrNamespaceExists = errors.New("remoteenv: namespace id already exists (refuse to clobber)")
)

// RehydrateForTest re-seeds the backend with a namespace under id, copying the
// current file state from src. See the package-level comment above for the
// full contract (the phase-3 reattachment stand-in, the nil/clobber guards).
func (b *Backend) RehydrateForTest(id string, src tool.Workspace) error {
	if src == nil {
		return ErrNilSource
	}
	files := make(map[string]*file)
	// Read every file out of src via its public Read surface by first enumerating
	// the keys through Glob.
	keys, _ := src.Glob(context.Background(), "**")
	for _, k := range keys {
		data, err := src.Read(context.Background(), k)
		if err != nil {
			continue
		}
		cp := make([]byte, len(data))
		copy(cp, data)
		files[k] = &file{data: cp, modTime: time.Now()}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.namespaces[id]; exists {
		return fmt.Errorf("%w: %q", ErrNamespaceExists, id)
	}
	b.namespaces[id] = &namespace{id: id, files: files, now: time.Now}
	return nil
}

// workspace is the tool.Workspace bound to a single namespace. Multiple
// workspaces over the same namespace share the mutex-guarded file map, so the
// version ledger is per-handle (RecordRead/RecordedVersion) while the
// authoritative content+version lives on the shared namespace.
type workspace struct {
	ns *namespace

	mu     sync.Mutex
	ledger map[string]tool.FileVersion // clean path -> recorded version
}

// Compile-time assertion that workspace satisfies the frozen seam.
var _ tool.Workspace = (*workspace)(nil)

// Root returns a logical root string. The fake has no on-disk root; this is the
// namespace id so a caller can correlate it with the ref.
func (w *workspace) Root() string { return w.ns.id }

// Read returns the contents of the file at the session-relative path.
func (w *workspace) Read(_ context.Context, p string) ([]byte, error) {
	key, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	data, _, rerr := w.ns.read(key)
	if rerr != nil {
		return nil, fmt.Errorf("remoteenv: open %q: %w", p, rerr)
	}
	return data, nil
}

// ReadVersion returns the contents AND the authoritative FileVersion (sha256 of
// the content), read under the namespace lock so content and version are a
// consistent snapshot.
func (w *workspace) ReadVersion(_ context.Context, p string) ([]byte, tool.FileVersion, error) {
	key, err := cleanPath(p)
	if err != nil {
		return nil, tool.FileVersion{}, err
	}
	data, f, rerr := w.ns.read(key)
	if rerr != nil {
		return nil, tool.FileVersion{}, fmt.Errorf("remoteenv: open %q: %w", p, rerr)
	}
	return data, versionOf(f.data), nil
}

// Stat returns metadata for the file at the session-relative path.
func (w *workspace) Stat(_ context.Context, p string) (tool.FileInfo, error) {
	key, err := cleanPath(p)
	if err != nil {
		return tool.FileInfo{}, err
	}
	w.ns.mu.RLock()
	f, ok := w.ns.files[key]
	w.ns.mu.RUnlock()
	if !ok {
		return tool.FileInfo{}, fmt.Errorf("remoteenv: stat %q: %w", p, fs.ErrNotExist)
	}
	return tool.FileInfo{
		Name:    baseName(key),
		Size:    int64(len(f.data)),
		Mode:    0o644,
		ModTime: f.modTime,
		IsDir:   false,
	}, nil
}

// CreateFile creates a NEW file at path, atomically. It fails (wrapping
// fs.ErrExist) if a file already exists. It returns the new file's FileVersion.
func (w *workspace) CreateFile(_ context.Context, p string, data []byte) (tool.FileVersion, error) {
	key, err := cleanPath(p)
	if err != nil {
		return tool.FileVersion{}, err
	}
	stored := make([]byte, len(data))
	copy(stored, data)
	w.ns.mu.Lock()
	defer w.ns.mu.Unlock()
	if _, exists := w.ns.files[key]; exists {
		return tool.FileVersion{}, fmt.Errorf("remoteenv: create %q: %w", p, fs.ErrExist)
	}
	w.ns.files[key] = &file{data: stored, modTime: w.ns.now()}
	return versionOf(stored), nil
}

// ReplaceFile conditionally replaces the contents of the file at path, only if
// the CURRENT authoritative version equals old. On a mismatch it returns a
// *tool.VersionMismatchError; on a missing file it wraps fs.ErrNotExist. The
// authoritative version is read under the SAME lock as the write, so two
// handles to the same namespace cannot race past the CAS.
func (w *workspace) ReplaceFile(_ context.Context, p string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	key, err := cleanPath(p)
	if err != nil {
		return tool.FileVersion{}, err
	}
	stored := make([]byte, len(data))
	copy(stored, data)
	w.ns.mu.Lock()
	defer w.ns.mu.Unlock()
	f, ok := w.ns.files[key]
	if !ok {
		return tool.FileVersion{}, fmt.Errorf("remoteenv: replace %q: %w", p, fs.ErrNotExist)
	}
	cur := versionOf(f.data)
	if !cur.Equal(old) {
		return tool.FileVersion{}, &tool.VersionMismatchError{Path: p}
	}
	w.ns.files[key] = &file{data: stored, modTime: w.ns.now()}
	return versionOf(stored), nil
}

// Glob returns session-relative paths matching the pattern. The fake supports
// a leading-slash strip and "*" / "**" via a simple doublestar-free matcher
// (sufficient for the contract proof; the real adapters use doublestar).
func (w *workspace) Glob(_ context.Context, pattern string) ([]string, error) {
	pat := normalizeGlobPattern(pattern)
	if pat == "" {
		return nil, nil
	}
	w.ns.mu.RLock()
	keys := make([]string, 0, len(w.ns.files))
	for k := range w.ns.files {
		if globMatch(pat, k) {
			keys = append(keys, k)
		}
	}
	w.ns.mu.RUnlock()
	sort.Strings(keys)
	return keys, nil
}

// Grep returns matches of a regular expression across files selected by an
// optional path glob. It mirrors memfs's in-memory grep.
func (w *workspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	return grepInMemory(ctx, w.ns, pattern, pathGlob)
}

// RecordRead stores the EXACT version for path under this handle's ledger (no
// I/O). The ledger is per-handle: a second handle to the same namespace has its
// own ledger, so a stale-version conflict between two handles is observable.
func (w *workspace) RecordRead(p string, version tool.FileVersion) {
	key, err := cleanPath(p)
	if err != nil {
		return // fail-safe: an uncleanable path stays unrecorded
	}
	w.mu.Lock()
	if w.ledger == nil {
		w.ledger = make(map[string]tool.FileVersion)
	}
	w.ledger[key] = version
	w.mu.Unlock()
}

// RecordedVersion returns the version previously recorded for path (no I/O).
func (w *workspace) RecordedVersion(p string) (tool.FileVersion, bool) {
	key, err := cleanPath(p)
	if err != nil {
		return tool.FileVersion{}, false
	}
	w.mu.Lock()
	v, ok := w.ledger[key]
	w.mu.Unlock()
	return v, ok
}

// namespaceFiles returns a snapshot of the namespace's current (path -> content)
// for fork/merge. It reads under the namespace read lock.
func (w *workspace) namespaceFiles() map[string][]byte {
	w.ns.mu.RLock()
	defer w.ns.mu.RUnlock()
	out := make(map[string][]byte, len(w.ns.files))
	for k, f := range w.ns.files {
		cp := make([]byte, len(f.data))
		copy(cp, f.data)
		out[k] = cp
	}
	return out
}

// namespaceBase returns a copy of the fork-base snapshot (child namespaces
// only; a non-forked namespace returns nil). It is the merge anchor.
func (w *workspace) namespaceBase() map[string][]byte {
	w.ns.mu.RLock()
	defer w.ns.mu.RUnlock()
	if w.ns.base == nil {
		return nil
	}
	out := make(map[string][]byte, len(w.ns.base))
	for k, d := range w.ns.base {
		cp := make([]byte, len(d))
		copy(cp, d)
		out[k] = cp
	}
	return out
}

// applyFiles merges the given files into the namespace, returning the paths
// that CONFLICTED (already present with DIFFERENT content). It is the merge
// primitive for the base-LESS fallback: a caller-supplied map of child changes
// is applied; a conflict leaves the existing parent content intact for that
// path and reports it.
func (w *workspace) applyFiles(changes map[string][]byte) []string {
	w.ns.mu.Lock()
	defer w.ns.mu.Unlock()
	var conflicts []string
	for k, d := range changes {
		if existing, ok := w.ns.files[k]; ok {
			if versionOf(existing.data).Equal(versionOf(d)) {
				continue // identical — not a conflict
			}
			conflicts = append(conflicts, k)
			continue // preserve parent on conflict
		}
		cp := make([]byte, len(d))
		copy(cp, d)
		w.ns.files[k] = &file{data: cp, modTime: w.ns.now()}
	}
	sort.Strings(conflicts)
	return conflicts
}

// forceApplyFiles overwrites the namespace with the given files UNCONDITIONALLY
// (new paths are added, existing paths are replaced). It is the base-aware
// merge's clean-landing primitive: the caller has already classified each path
// as a clean child change (parent unchanged from the fork-base), so the
// overwrite is the correct "apply the child's diff to the parent" step. It
// returns the list of paths applied.
func (w *workspace) forceApplyFiles(changes map[string][]byte) []string {
	w.ns.mu.Lock()
	defer w.ns.mu.Unlock()
	var applied []string
	for k, d := range changes {
		cp := make([]byte, len(d))
		copy(cp, d)
		w.ns.files[k] = &file{data: cp, modTime: w.ns.now()}
		applied = append(applied, k)
	}
	sort.Strings(applied)
	return applied
}

// runner is the bound tool.CommandRunner for a namespace. It implements a tiny
// documented TEST protocol so the contract proof can show the file API and the
// fake Bash runner observe the SAME namespace in both directions:
//
//	cat <path>           — print the file's contents to stdout
//	write <path> <text>  — set the file's contents (create or replace)
//
// Anything else exits non-zero with a usage message. It is NOT a general shell.
type runner struct {
	ns *namespace
}

// Compile-time assertion that runner satisfies the runner port.
var _ tool.CommandRunner = (*runner)(nil)

// Run executes the tiny test protocol against the bound namespace.
func (r *runner) Run(ctx context.Context, command string) (tool.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.CommandResult{}, err
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return tool.CommandResult{ExitCode: 2, Stderr: ErrEmptyCommand.Error()}, nil
	}
	verb, rest, ok := strings.Cut(command, " ")
	if !ok {
		return tool.CommandResult{ExitCode: 2, Stderr: "remoteenv: unknown command (supported: cat <path>, write <path> <content>)"}, nil
	}
	switch verb {
	case "cat":
		path := strings.TrimSpace(rest)
		if path == "" {
			return tool.CommandResult{ExitCode: 2, Stderr: "remoteenv: cat requires a path"}, nil
		}
		key, kerr := runnerClean(path)
		if kerr != nil {
			return tool.CommandResult{ExitCode: 2, Stderr: kerr.Error()}, nil
		}
		data, _, err := r.ns.read(key)
		if err != nil {
			return tool.CommandResult{ExitCode: 1, Stderr: fmt.Sprintf("remoteenv: cat: %v", err)}, nil
		}
		return tool.CommandResult{Stdout: string(data), ExitCode: 0}, nil
	case "write":
		path, content, ok := strings.Cut(rest, " ")
		if !ok {
			return tool.CommandResult{ExitCode: 2, Stderr: "remoteenv: write requires a path and content"}, nil
		}
		key, kerr := runnerClean(path)
		if kerr != nil {
			return tool.CommandResult{ExitCode: 2, Stderr: kerr.Error()}, nil
		}
		r.ns.mu.Lock()
		r.ns.files[key] = &file{data: []byte(content), modTime: r.ns.now()}
		r.ns.mu.Unlock()
		return tool.CommandResult{ExitCode: 0}, nil
	default:
		return tool.CommandResult{ExitCode: 2, Stderr: "remoteenv: unknown command (supported: cat <path>, write <path> <content>)"}, nil
	}
}

// cleanPath normalizes a session-relative path to a clean, slash-separated key
// and rejects absolute paths and ".." traversal (mirrors memfs.cleanPath).
func cleanPath(p string) (string, error) {
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("remoteenv: path escapes namespace root: %q is absolute", p)
	}
	cleaned := pathClean(p)
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("remoteenv: path escapes namespace root: empty path")
	}
	if hasDotDot(p) {
		return "", fmt.Errorf("remoteenv: path escapes namespace root: %q", p)
	}
	return cleaned, nil
}

// runnerClean is the runner's path normalization for the test protocol. It
// strips a leading slash (so `cat /foo` and `cat foo` agree) then applies the
// SAME escape-rejecting cleanPath the Workspace uses — a runner command path is
// ALIGNED with the Workspace's cleanPath, so a `cat ../escape` or `write /etc/x`
// that the Workspace would reject is rejected here too (issue #462 phase-3
// finding #4). The runner protocol is a documented test surface, but its paths
// address the SAME namespace the file API does, so they must obey the SAME
// escape discipline: a path that escapes the namespace root is never silently
// normalized away.
func runnerClean(p string) (string, error) {
	return cleanPath(strings.TrimPrefix(p, "/"))
}

// pathClean is a stdlib-free clean (path.Clean equivalent) to keep the fake
// dependency-light; it collapses "." and duplicate slashes.
func pathClean(p string) string {
	if p == "" {
		return ""
	}
	segs := strings.Split(p, "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		switch s {
		case "", ".":
			continue
		default:
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return "."
	}
	return strings.Join(out, "/")
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

// baseName returns the last path component.
func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// normalizeGlobPattern strips a leading "/" and "./" so "/**/*.go" and
// "**/*.go" match the same keys (mirrors memfs.normalizeGlobPattern).
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

// globMatch is a minimal "**" glob matcher sufficient for the contract proof.
// It supports "*" (within a segment), "**" (across "/"), and literal text.
func globMatch(pattern, name string) bool {
	return globMatchSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func globMatchSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if globMatchSegs(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if !segmentMatch(pat[0], name[0]) {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// segmentMatch matches a single path segment against a "*" wildcard pattern.
func segmentMatch(pat, s string) bool {
	pi, si := 0, 0
	for pi < len(pat) {
		if pat[pi] == '*' {
			if pi == len(pat)-1 {
				return true // trailing * matches the rest
			}
			for j := si; j <= len(s); j++ {
				if segmentMatch(pat[pi+1:], s[j:]) {
					return true
				}
			}
			return false
		}
		if si >= len(s) || pat[pi] != s[si] {
			return false
		}
		pi, si = pi+1, si+1
	}
	return si == len(s)
}

// sanitizeLabel reduces an arbitrary label to a short, filesystem-safe token
// (mirrors forker.sanitizeLabel so ids are observable).
func sanitizeLabel(label string) string {
	const maxLen = 24
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= maxLen {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "x"
	}
	return s
}
