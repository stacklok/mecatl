package acp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hashutil"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// fsCallTimeout bounds a single outbound fs/read_text_file / fs/write_text_file
// round-trip to the editor. A wedged or hung editor must not block the turn
// indefinitely (CWE-400): without a per-call deadline a Read/Write would hang
// until session/cancel or disconnect. 30s matches the operator-path MCP connect
// budget (the generous end) — a real fs/* round-trip is sub-millisecond, so this
// only fires on a genuinely unresponsive client. The caller's own ctx still
// applies; this is an UPPER bound layered on top of it.
const fsCallTimeout = 30 * time.Second

// fsWorkspace is the per-session tool.Workspace that routes file Read/Write
// through the ACP client's editor buffers (fs/read_text_file /
// fs/write_text_file) instead of touching disk directly. It is the heart of the
// fs/* delegation: an edit issued by the model lands in the editor's in-memory
// buffer (including unsaved changes) rather than overwriting the file on disk,
// so the agent's view of a file and the editor's view never diverge on the
// MUTATION path.
//
// It is a HYBRID, not a full reimplementation:
//
//   - Read / Write — DELEGATED through fs/* over the ACP connection.
//   - ReadVersion / CreateFile / ReplaceFile — version-bearing reads and explicit
//     mutations over the editor buffer; versions are sha256 of fs/read content.
//   - RecordRead / RecordedVersion — the I/O-free session ledger, so Edit's
//     read-before-edit-and-unchanged invariant tracks the editor's BUFFER, not disk
//     (strictly better than osfs for an editor session).
//   - Root / Glob / Grep — COMPOSED from an osfs.Workspace rooted at the SAME
//     session cwd. ACP has no fs/list or fs/grep, so these read the local on-disk
//     tree. The residual: Grep/Glob see disk, not unsaved buffers. This is
//     acceptable — the Edit invariant forces a re-read-through-fs/* before any
//     edit, so the divergence is confined to search/discovery and never reaches
//     the mutation path. (Documented in docs/adr/0001-acp-adapter.md.)
//   - Stat — disk-primary BUT buffer-aware for EXISTENCE: when disk reports
//     not-exist it probes the editor via fs/read_text_file, so a file that exists
//     only as an unsaved buffer is reported as existing. This is load-bearing for
//     write integrity (see Stat) — otherwise the Write tool would treat an unsaved
//     buffer as a new file and clobber it with no read-before-overwrite check.
//
// It is registered per-session on the shared *server.Service via
// SetSessionWorkspace and evicted on editor disconnect; concurrent read-only
// dispatch may fire several Read (hence fs/read_text_file) calls at once, which
// the ACP Conn handles safely, and the local ledger has its OWN mutex.
type fsWorkspace struct {
	conn      *Conn
	sessionID string

	// local is an osfs.Workspace rooted at the same session cwd. It supplies
	// Root/Stat/Glob/Grep and the canonical root path; its OWN ledger is unused
	// (fsWorkspace carries a buffer-keyed ledger instead).
	local *osfs.Workspace

	// callTimeout bounds a single fs/* round-trip. It defaults to fsCallTimeout;
	// tests may shrink it to assert the bound fires against a non-responsive peer.
	callTimeout time.Duration

	// ledgerMu guards ONLY the in-memory ledger map. Ledger methods (RecordRead/
	// RecordedVersion) take it alone and perform NO I/O, so a parked RPC
	// mutation never blocks a ledger lookup or record.
	ledgerMu sync.Mutex
	ledger   map[string]tool.FileVersion // LedgerKey(path) -> version of last fs/read content

	// callMu serializes the buffer CAS / create RPC SEQUENCES in CreateFile and
	// ReplaceFile (the read-then-write compare-and-swap), so a same-instance
	// concurrent CreateFile/ReplaceFile cannot race the editor buffer. It is
	// held ONLY across the fs/* round-trips, never across ledger access.
	callMu sync.Mutex
}

// Compile-time assertion that fsWorkspace satisfies the tool.Workspace port.
var _ tool.Workspace = (*fsWorkspace)(nil)

// newFSWorkspace builds an fsWorkspace over conn for sessionID, composing an
// osfs.Workspace rooted at root for the local (Stat/Glob/Grep) view. It returns
// an error only if the osfs root cannot be opened (so a bad cwd fails loudly at
// session/new rather than silently falling back to disk).
func newFSWorkspace(conn *Conn, sessionID, root string) (*fsWorkspace, error) {
	local, err := osfs.NewWorkspace(root)
	if err != nil {
		return nil, fmt.Errorf("acp: fs workspace: %w", err)
	}
	return &fsWorkspace{
		conn:        conn,
		sessionID:   sessionID,
		local:       local,
		callTimeout: fsCallTimeout,
		ledger:      make(map[string]tool.FileVersion),
	}, nil
}

// Root returns the absolute session root all paths are scoped to — the SAME root
// the composed osfs view uses, so local (Glob/Grep/Stat) and delegated
// (Read/Write) operations address the same files.
func (w *fsWorkspace) Root() string { return w.local.Root() }

// absPath confines a session-relative (or absolute in-root) path under the root
// and returns the ABSOLUTE path the ACP fs/* contract requires. The model is
// UNTRUSTED even though the editor is trusted, so escapes are rejected here,
// BEFORE the path is handed to the editor — never delegate an unvalidated
// "../../etc/passwd".
//
// Confinement guarantee (stated honestly — this is NOT os.Root-grade):
//  1. LEXICAL: reject any ".." that climbs out of the root after Clean. This is
//     the same lexical check osfs.resolvePath applies to relative paths.
//  2. SYMLINK (best-effort, on the on-disk tree): EvalSymlinks the deepest
//     EXISTING ancestor of the joined target and re-verify the resolved real path
//     is still within the EvalSymlinks-resolved Root(); reject if it escapes. This
//     defends against a model creating an in-workspace symlink (e.g. `ln -s
//     /etc/passwd evil` via Bash) and then reading/writing it — without this the
//     editor would receive "<root>/evil" and might follow it out of root.
//
// An ABSOLUTE path is accepted iff confineSymlinks confirms it resolves inside
// Root() (mirroring osfs.resolveInRoot, so the ACP and osfs workspaces treat
// absolute in-root paths identically). A relative path takes the lexical + symlink
// check. Unlike osfs (every op flows through *os.Root, which refuses symlink
// traversal at the kernel level), this is a best-effort filesystem-side
// re-confinement: it resolves the existing parent for a buffer-only/non-existent
// leaf, so a not-yet-created path is still checked against its real parent. The
// editor is a trusted-local process and owns final filesystem policy; this layer
// rejects the obviously-escaping shapes the untrusted model can construct.
func (w *fsWorkspace) absPath(path string) (string, error) {
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") {
		// An absolute path is accepted iff it canonicalizes inside the workspace
		// root (mirroring osfs.resolveInRoot); an out-of-root absolute path
		// escapes and is rejected by confineSymlinks. No relative join happens.
		abs := filepath.Clean(path)
		if err := w.confineSymlinks(abs); err != nil {
			return "", err
		}
		return abs, nil
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("acp: fs workspace: %q escapes the workspace root", path)
	}
	abs := filepath.Join(w.Root(), clean)
	if err := w.confineSymlinks(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// confineSymlinks re-verifies that abs, after resolving symlinks on the deepest
// existing ancestor, still lives within the EvalSymlinks-resolved Root(). For a
// not-yet-existing leaf (a buffer-only or brand-new file) it resolves the closest
// existing parent and re-appends the unresolved tail, so a symlinked PARENT
// component that escapes is still caught. A resolution fault on the parent (other
// than not-exist) fails safe (rejects).
func (w *fsWorkspace) confineSymlinks(abs string) error {
	root := w.Root() // already EvalSymlinks-resolved by osfs.NewWorkspace.
	// Find the deepest existing ancestor and resolve it.
	existing := abs
	var tail []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			// An ambiguous stat error on an ancestor: fail safe (reject) rather than
			// delegate a path we cannot vet.
			return fmt.Errorf("acp: fs workspace: cannot verify %q: %w", abs, err)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			// Reached the filesystem root without finding an existing ancestor; this
			// should be impossible since Root() itself exists, but fail safe.
			return fmt.Errorf("acp: fs workspace: %q has no resolvable ancestor", abs)
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return fmt.Errorf("acp: fs workspace: resolving %q: %w", existing, err)
	}
	realPath := resolved
	if len(tail) > 0 {
		realPath = filepath.Join(append([]string{resolved}, tail...)...)
	}
	if realPath != root && !strings.HasPrefix(realPath, root+string(filepath.Separator)) {
		return fmt.Errorf("acp: fs workspace: %q resolves to %q, which escapes the workspace root", abs, realPath)
	}
	return nil
}

// Read returns the file's content by delegating to fs/read_text_file (the
// editor's buffer view). line/limit are omitted (whole-file); the Read tool does
// its own line slicing.
func (w *fsWorkspace) Read(ctx context.Context, path string) ([]byte, error) {
	abs, err := w.absPath(path)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, w.callTimeout)
	defer cancel()
	var resp fsReadTextFileResponse
	if err := w.conn.Call(callCtx, methodFSReadTextFile, fsReadTextFileRequest{
		SessionID: w.sessionID,
		Path:      abs,
	}, &resp); err != nil {
		return nil, fmt.Errorf("acp: fs/read_text_file %q: %w", path, err)
	}
	return []byte(resp.Content), nil
}

// ReadVersion returns the buffer content AND the authoritative FileVersion
// (sha256 of the buffer content). It delegates the read to fs/read_text_file
// (the SAME path as Read) and mints the version locally, so the version tracks
// the editor's BUFFER, not disk (strictly better than osfs for an editor
// session — Edit's read-before-edit-and-unchanged invariant tracks what the
// editor would actually overwrite).
func (w *fsWorkspace) ReadVersion(ctx context.Context, path string) ([]byte, tool.FileVersion, error) {
	data, err := w.Read(ctx, path)
	if err != nil {
		return nil, tool.FileVersion{}, err
	}
	return data, acpVersion(data), nil
}

// acpVersion mints a FileVersion from content bytes (sha256 via
// hashutil.SHA256Hex, the shared adapter-layer fingerprint primitive). It is the
// single ACP version primitive, shared by ReadVersion/CreateFile/ReplaceFile.
func acpVersion(data []byte) tool.FileVersion {
	return tool.NewFileVersion(hashutil.SHA256Hex(data))
}

// Write replaces the file's content by delegating to fs/write_text_file, so the
// write lands in the editor's buffer. This adapter-public bootstrap operation is
// deliberately not part of tool.Workspace; tools use CreateFile/ReplaceFile,
// which gate the same delegation on a version check under the callMu CAS
// sequence.
func (w *fsWorkspace) Write(ctx context.Context, path string, data []byte) error {
	abs, err := w.absPath(path)
	if err != nil {
		return err
	}
	w.callMu.Lock()
	defer w.callMu.Unlock()
	callCtx, cancel := context.WithTimeout(ctx, w.callTimeout)
	defer cancel()
	if err := w.conn.Call(callCtx, methodFSWriteTextFile, fsWriteTextFileRequest{
		SessionID: w.sessionID,
		Path:      abs,
		Content:   string(data),
	}, nil); err != nil {
		return fmt.Errorf("acp: fs/write_text_file %q: %w", path, err)
	}
	return nil
}

// CreateFile creates a NEW file at path by delegating to fs/write_text_file, but
// only if the buffer does not already exist. It fails (wrapping fs.ErrExist) if
// the editor's buffer already holds the path (checked via fs/read_text_file,
// mirroring Stat's buffer-aware existence). The compare+write is serialized
// under callMu (the RPC CAS sequence — ADR 0103 §5), so a concurrent
// CreateFile/ReplaceFile on the same instance cannot race. The ledger is NOT
// held across the RPC: callMu and ledgerMu are independent.
func (w *fsWorkspace) CreateFile(ctx context.Context, path string, data []byte) (tool.FileVersion, error) {
	abs, err := w.absPath(path)
	if err != nil {
		return tool.FileVersion{}, err
	}
	w.callMu.Lock()
	defer w.callMu.Unlock()
	// Existence probe through the SAME delegated read the mutation uses, under
	// callMu, so the create-only check and the write are atomic w.r.t. other
	// fsWorkspace CAS sequences.
	_, readErr := w.bufferRead(ctx, abs)
	switch {
	case readErr == nil:
		return tool.FileVersion{}, fmt.Errorf("acp: create %q: %w", path, fs.ErrExist)
	case isFSNotFound(readErr, abs):
		// Genuinely new — fall through to write.
	default:
		// Ambiguous read fault -> fail safe: refuse the create rather than risk
		// clobbering a buffer we could not probe.
		return tool.FileVersion{}, fmt.Errorf("acp: create %q: cannot verify existence: %w", path, readErr)
	}
	callCtx, cancel := context.WithTimeout(ctx, w.callTimeout)
	defer cancel()
	if err := w.conn.Call(callCtx, methodFSWriteTextFile, fsWriteTextFileRequest{
		SessionID: w.sessionID,
		Path:      abs,
		Content:   string(data),
	}, nil); err != nil {
		return tool.FileVersion{}, fmt.Errorf("acp: fs/write_text_file %q: %w", path, err)
	}
	return acpVersion(data), nil
}

// ReplaceFile conditionally replaces the buffer content at path, only if the
// editor's current buffer version equals old. It reads the buffer, mints the
// current version, compares, and writes — all under callMu (the RPC CAS
// sequence — ADR 0103 §5). On a version mismatch it returns a
// *tool.VersionMismatchError; on a missing buffer it returns an error wrapping
// fs.ErrNotExist. Because fs/write_text_file is unconditional, the CAS is only
// as atomic as callMu; there is exactly one fsWorkspace per session, so a
// same-session concurrent ReplaceFile is serialized.
func (w *fsWorkspace) ReplaceFile(ctx context.Context, path string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	abs, err := w.absPath(path)
	if err != nil {
		return tool.FileVersion{}, err
	}
	w.callMu.Lock()
	defer w.callMu.Unlock()
	content, readErr := w.bufferRead(ctx, abs)
	if readErr != nil {
		if isFSNotFound(readErr, abs) {
			return tool.FileVersion{}, fmt.Errorf("acp: replace %q: %w", path, fs.ErrNotExist)
		}
		return tool.FileVersion{}, fmt.Errorf("acp: replace %q: read buffer: %w", path, readErr)
	}
	have := acpVersion([]byte(content))
	if !have.Equal(old) {
		return tool.FileVersion{}, &tool.VersionMismatchError{Path: path}
	}
	callCtx, cancel := context.WithTimeout(ctx, w.callTimeout)
	defer cancel()
	if err := w.conn.Call(callCtx, methodFSWriteTextFile, fsWriteTextFileRequest{
		SessionID: w.sessionID,
		Path:      abs,
		Content:   string(data),
	}, nil); err != nil {
		return tool.FileVersion{}, fmt.Errorf("acp: fs/write_text_file %q: %w", path, err)
	}
	return acpVersion(data), nil
}

// Stat returns metadata for path. Disk is PRIMARY (ACP has no fs/stat): a file
// present on disk is reported with full local metadata. But existence is ALSO
// buffer-aware — when disk reports not-exist, Stat probes the editor via
// fs/read_text_file, because a file may exist ONLY as an unsaved editor buffer
// (never yet written to disk). This is load-bearing for write-integrity: the
// Write tool uses a not-exist Stat to mean "new file, no read-before-overwrite
// required". Without the buffer probe, an unsaved buffer would look new and Write
// would clobber it through fs/write_text_file with NO unchanged-since check — the
// exact divergence this feature exists to prevent.
//
// Classification when disk says not-exist:
//   - fs/read succeeds            -> EXISTS (synthesized FileInfo) so Write's
//     existing-file gate (read-before-overwrite) engages.
//   - fs/read is a clean not-found -> ErrNotExist (genuinely new; Write allowed).
//   - fs/read fails ambiguously   -> FAIL SAFE: report EXISTS, forcing the
//     read-before-overwrite gate rather than allowing an unguarded write.
func (w *fsWorkspace) Stat(ctx context.Context, path string) (tool.FileInfo, error) {
	info, err := w.local.Stat(ctx, path)
	if err == nil {
		return info, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		// A real stat fault (e.g. an escape rejection): surface it unchanged.
		return tool.FileInfo{}, err
	}
	// Disk says not-exist; consult the editor buffer.
	abs, aerr := w.absPath(path)
	if aerr != nil {
		// An escaping path: keep the original not-exist semantics (it was never a
		// delegatable path anyway). Return the disk error.
		return tool.FileInfo{}, err
	}
	content, readErr := w.bufferRead(ctx, abs)
	switch {
	case readErr == nil:
		// Buffer exists -> synthesize an EXISTS FileInfo. Name is the leaf; Size is
		// the buffer length; a regular-file mode and a zero modtime are sane stand-ins
		// (the Write tool only consults existence, not the metadata).
		return tool.FileInfo{
			Name:    filepath.Base(path),
			Size:    int64(len(content)),
			Mode:    0o644,
			ModTime: time.Time{},
			IsDir:   false,
		}, nil
	case isFSNotFound(readErr, abs):
		// Editor confirms the file does not exist anywhere -> genuinely new.
		return tool.FileInfo{}, err
	default:
		// Ambiguous fs/read fault -> fail safe: report EXISTS so Write's
		// read-before-overwrite gate engages rather than permitting an unguarded write.
		return tool.FileInfo{
			Name:    filepath.Base(path),
			Mode:    0o644,
			ModTime: time.Time{},
			IsDir:   false,
		}, nil
	}
}

// bufferRead issues one fs/read_text_file for an already-confined absolute
// path and returns the underlying call error WITHOUT adding path context. Its
// callers classify that trusted structured error first; only then may they add
// the model-controlled path to an outward-facing error. It does not go through
// Read (which re-confines), so the caller must pass an abs that absPath produced.
func (w *fsWorkspace) bufferRead(ctx context.Context, abs string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, w.callTimeout)
	defer cancel()
	var resp fsReadTextFileResponse
	if err := w.conn.Call(callCtx, methodFSReadTextFile, fsReadTextFileRequest{
		SessionID: w.sessionID,
		Path:      abs,
	}, &resp); err != nil {
		// Return the trusted underlying RPC/transport error verbatim. Callers must
		// classify it BEFORE adding the model-controlled path as context.
		return "", err
	}
	return resp.Content, nil
}

// isFSNotFound reports whether the trusted underlying structured editor error is
// a clean absence response for requestedPath. The exact model-controlled path
// (including common quote wrappers) is removed before normalization. Classification
// is an anchored equality check over a narrow vocabulary — never a substring scan.
// Generic/wrapped errors and structured permission/unavailable errors fail closed.
func isFSNotFound(err error, requestedPath string) bool {
	if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var re *rpcError
	if !errors.As(err, &re) {
		return false
	}
	msg := re.Message
	if requestedPath != "" {
		for _, form := range []string{
			`"` + requestedPath + `"`,
			"'" + requestedPath + "'",
			"`" + requestedPath + "`",
			requestedPath,
		} {
			msg = strings.ReplaceAll(msg, form, "")
		}
	}
	msg = normalizeFSAbsence(msg)
	switch msg {
	case "not found", "file not found", "no such file or directory", "does not exist", "file does not exist", "enoent":
		return true
	default:
		return false
	}
}

func normalizeFSAbsence(msg string) string {
	msg = strings.ToLower(strings.Join(strings.Fields(msg), " "))
	for _, pair := range [][2]string{
		{" :", ":"}, {" ,", ","}, {" ;", ";"},
	} {
		msg = strings.ReplaceAll(msg, pair[0], pair[1])
	}
	msg = strings.Trim(msg, " \t\r\n:;,.()[]{}\"'`")
	for _, prefix := range []string{"error:", "open:", "read:", "stat:", "fs/read_text_file:"} {
		if strings.HasPrefix(msg, prefix) {
			msg = strings.TrimSpace(strings.TrimPrefix(msg, prefix))
			break
		}
	}
	if strings.HasPrefix(msg, "enoent:") {
		msg = strings.TrimSpace(strings.TrimPrefix(msg, "enoent:"))
		if msg == "" {
			return "enoent"
		}
	}
	for _, suffix := range []string{", open", ", read", ", stat"} {
		msg = strings.TrimSuffix(msg, suffix)
	}
	return strings.Trim(msg, " \t\r\n:;,.()[]{}\"'`")
}

// Glob returns local on-disk matches (ACP has no fs/list/glob). Residual: it does
// not see files that exist only as unsaved editor buffers.
func (w *fsWorkspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	return w.local.Glob(ctx, pattern)
}

// Grep searches the local on-disk tree (ACP has no fs/grep). Residual: it sees
// disk content, not unsaved buffer content. Acceptable because the Edit invariant
// forces a re-read-through-fs/* before any mutation, so a stale grep hit can
// never become a stale EDIT.
func (w *fsWorkspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	return w.local.Grep(ctx, pattern, pathGlob)
}

// RecordRead stores the EXACT authoritative version for path under the session
// ledger, performing NO I/O: it stores the FileVersion the caller supplies (the
// one ReadVersion minted). The version authority is the BUFFER (sha256 of the
// fs/read content), so a later comparison tracks what the editor would actually
// overwrite. The I/O-free lexical key (tool.LedgerKey over the shared osfs root)
// makes ordinary absolute-root and relative forms share one entry; symlink
// aliases may require another Read.
func (w *fsWorkspace) RecordRead(path string, version tool.FileVersion) {
	key := tool.LedgerKey(w.Root(), path)
	w.ledgerMu.Lock()
	w.ledger[key] = version
	w.ledgerMu.Unlock()
}

// RecordedVersion returns the version previously recorded for path via
// RecordRead, performing NO I/O. ok is false if path was never recorded. The
// lookup uses the same lexical ledger key (tool.LedgerKey) as RecordRead, so
// ordinary absolute-root and relative forms agree without filesystem/editor I/O.
func (w *fsWorkspace) RecordedVersion(path string) (tool.FileVersion, bool) {
	key := tool.LedgerKey(w.Root(), path)
	w.ledgerMu.Lock()
	version, ok := w.ledger[key]
	w.ledgerMu.Unlock()
	return version, ok
}
