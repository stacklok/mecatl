package main

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// baselineSlogWriter picks the writer for the UNIVERSAL global-slog floor the TUI
// installs before the alt-screen starts (see installBaselineSlog). mecatui has no
// in-process diagnostics of its own and owns the alt-screen, so the floor is always
// io.Discard: a client-only TUI (the connect mode, dialling an already-running
// mecated) has nothing of its own to log, and any ambient/third-party
// slog.Default() use during the alt-screen must NOT reach stderr. The
// host-embedded path later REFINES this floor to the file sink
// ($XDG_STATE_HOME/mecatl/mecatui.log) so the embedded server's ambient slog is
// captured and operator-recoverable. The quiet bool is taken for symmetry with
// openDiagLogWriter and to make the contract explicit (both quiet and not-quiet
// floor to discard for the client-only case).
func baselineSlogWriter(quiet bool) io.Writer {
	_ = quiet
	return io.Discard
}

// installBaselineSlog redirects the GLOBAL slog default onto the baseline writer
// (io.Discard) so NO transport mode — connect or host-embedded — leaks an
// ambient/third-party slog line onto the Bubble Tea alt-screen. It runs
// once at the very top of run(), before resolveTransport and tea.NewProgram. The
// host-embedded branch in resolveTransport installs a SECOND default over the file
// writer, which wins for that path; this baseline stands for the client-only modes.
// cmd/ mains are the only layer permitted to call slog.SetDefault (internal/ flows
// through the injected port.Diagnostics, ban-guarded). See docs/adr/0020-diagnostics.md.
func installBaselineSlog(quiet bool) {
	w := baselineSlogWriter(quiet)
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

// mecatuiLogSubpath is the per-user state-relative path of the embedded server's
// diagnostics log: <state>/mecatl/mecatui.log, where <state> is $XDG_STATE_HOME
// (fallback ~/.local/state). It is the state-base twin of the soul/usermodel
// config paths — diagnostics are MACHINE-WRITTEN runtime state, not human config.
const mecatuiLogSubpath = "mecatl/mecatui.log"

// maxDiagLogBytes is the fixed startup-only retention limit for embedded-server
// diagnostics. It is deliberately not configurable: the log is local runtime
// state, and retention must not add another operator setting.
const maxDiagLogBytes int64 = 10 << 20

// retainDiagLog replaces an oversized regular log with its bounded recent tail.
// The caller holds the stable sibling lock for the writer's entire lifetime.
// O_NOFOLLOW makes every open of the data path reject a raced-in symlink.
func retainDiagLog(path string) error {
	return retainDiagLogWith(path, func(f *os.File) error { return f.Sync() }, os.Rename, syncDiagLogDir)
}

func openRegularNoFollow(path string, flag int, perm os.FileMode) (*os.File, os.FileInfo, error) {
	f, err := os.OpenFile(path, flag|syscall.O_NOFOLLOW, perm) //nolint:gosec // O_NOFOLLOW is the final-path symlink guard.
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, os.ErrInvalid
	}
	return f, info, nil
}

func readDiagLogTail(f *os.File, size int64) ([]byte, error) {
	if _, err := f.Seek(size-maxDiagLogBytes, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, maxDiagLogBytes))
}

func syncDiagLogDir(path string) error {
	dir, err := os.Open(filepath.Dir(path)) //nolint:gosec // operator-selected diagnostics destination; opened only to fsync its directory.
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func retainDiagLogWith(path string, syncFile func(*os.File) error, rename func(string, string) error, syncDir func(string) error) error {
	f, info, err := openRegularNoFollow(path, os.O_RDONLY, 0)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if info.Size() <= maxDiagLogBytes {
		return nil
	}

	tail, err := readDiagLogTail(f, info.Size())
	if err != nil {
		return err
	}
	if newline := bytes.IndexByte(tail, '\n'); newline >= 0 {
		tail = tail[newline+1:]
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".mecatui.log-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = os.Remove(tmpPath) //nolint:gosec // path was returned by CreateTemp above.
		}
	}()
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	n, err := tmp.Write(tail)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if n != len(tail) {
		_ = tmp.Close()
		return io.ErrShortWrite
	}
	if err := syncFile(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := rename(tmpPath, path); err != nil {
		return err
	}
	keepTemp = false
	return syncDir(path)
}

type lockedDiagLog struct {
	file *os.File
	lock *flock.Flock
}

func (l *lockedDiagLog) Write(p []byte) (int, error) { return l.file.Write(p) }

func (l *lockedDiagLog) Close() error {
	return errors.Join(l.file.Close(), l.lock.Close())
}

// resolveDiagLogPath returns the absolute path of the embedded server's
// diagnostics log under the XDG state base, or "" when no state base can be
// resolved (no $XDG_STATE_HOME and no home dir — the caller then discards). It is
// split out (taking the injectable env) so a test can assert the path resolution
// against a faked XDG_STATE_HOME without touching the real ~/.local/state.
func resolveDiagLogPath(env xdgconfig.ResolveEnv) string {
	base := xdgconfig.UserStateDir(env)
	if base == "" {
		return ""
	}
	return filepath.Join(base, mecatuiLogSubpath)
}

// diagLogSink is the resolved destination for the embedded server's operational
// diagnostics. Path is the file that was ACTUALLY opened (empty when the sink
// discards), so the caller never has to re-derive it and cannot report a path
// that differs from the one being written — including under --diagnostics-log
// and under the per-process fallback below.
type diagLogSink struct {
	Writer io.Writer
	Closer io.Closer
	// Path is the opened log file, or "" when diagnostics are discarded.
	Path string
	// Contended reports that another live process held the intended log's
	// sibling lock, so Path (when non-empty) is the per-process fallback. It is
	// the signal the caller needs to tell the operator that this instance's
	// diagnostics are NOT in the shared log.
	Contended bool
}

// errDiagLogLocked reports that another live process holds the sibling lock of
// the log we tried to open. It is deliberately distinct from every other open
// failure: lock contention is the one case that EARNS a per-process fallback,
// while a symlinked sentinel, a failed retention, or a non-regular path must
// still fail closed to io.Discard.
var errDiagLogLocked = errors.New("diagnostics log is locked by another process")

// fallbackDiagLogPath derives the per-process sibling of path by inserting
// ".<pid>" before the extension: <dir>/mecatui.log ⇒ <dir>/mecatui.<pid>.log.
// The pid is the discriminator because a diagnostics log belongs to exactly one
// mecatui process for that process's lifetime, and flock is released by the
// kernel when that process exits, so a pid-named sibling is never orphaned by a
// crash. Two writers inside ONE process (only reachable from tests) still
// collide on the same sibling; the second falls through to io.Discard, which is
// correct — a single process has a single diagnostics stream.
func fallbackDiagLogPath(path string, pid int) string {
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(filepath.Base(path), ext)
	return filepath.Join(filepath.Dir(path), stem+"."+strconv.Itoa(pid)+ext)
}

// openLockedDiagLog takes path's stable sibling lock, applies retention once,
// and opens path for APPEND. The lock is never renamed and is held until the
// returned writer closes, so a concurrent process can never retain an inode
// this one is still appending to.
//
// It returns errDiagLogLocked when the lock is held elsewhere, and the
// underlying error for every other failure, so the caller can fall back only in
// the former case.
func openLockedDiagLog(path string, retain func(string) error) (*lockedDiagLog, error) {
	lock := flock.New(path+".lock",
		flock.SetFlag(os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW),
		flock.SetPermissions(0o600),
	)
	locked, err := lock.TryLock()
	if err != nil || !locked {
		_ = lock.Close()
		if err != nil {
			return nil, err
		}
		return nil, errDiagLogLocked
	}
	if err := retain(path); err != nil {
		_ = lock.Close()
		return nil, err
	}
	f, _, err := openRegularNoFollow(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	return &lockedDiagLog{file: f, lock: lock}, nil
}

// diagLogContentionNotice returns the one-line operator notice owed for a
// diagnostics sink, or "" when none is. It exists because lock contention is the
// ONE sink outcome an operator cannot discover from the log itself: anything we
// would write about it lands in another instance's file or nowhere at all.
//
// It is a pure helper so the wording and the quiet/contended matrix are
// table-testable without standing up an embedded server (the guardrailsPostureLine
// idiom). --quiet returns "": that operator asked for no diagnostics, and a
// stderr line is still a diagnostic.
func diagLogContentionNotice(sink diagLogSink, quiet bool) string {
	if quiet || !sink.Contended {
		return ""
	}
	if sink.Path != "" {
		return "mecatui: another mecatui holds the shared diagnostics log; this instance logs to " + sink.Path
	}
	return "mecatui: another mecatui holds the shared diagnostics log and the per-process fallback could not be opened; diagnostics are disabled for this instance"
}

// openDiagLogWriterAndReport opens the diagnostics sink and immediately reports
// contention while stderr is still plain terminal output. Reporting here keeps
// the fallback discoverable even when later startup work blocks or fails.
func openDiagLogWriterAndReport(env xdgconfig.ResolveEnv, quiet bool, overridePath string, stderr io.Writer) diagLogSink {
	sink := openDiagLogWriter(env, quiet, overridePath)
	if notice := diagLogContentionNotice(sink, quiet); notice != "" {
		_, _ = io.WriteString(stderr, notice+"\n")
	}
	return sink
}

// openDiagLogWriter resolves the destination for the embedded server's operational
// diagnostics. The contract (and the render-leak fix it exists for):
//
//   - quiet ⇒ io.Discard (the operator asked for zero on-disk diagnostics);
//   - else open overridePath when non-empty (--diagnostics-log), else
//     $XDG_STATE_HOME/mecatl/mecatui.log (fallback ~/.local/state/mecatl/mecatui.log)
//     for APPEND, creating the dir 0700. Before opening, an existing regular file
//     is atomically reduced once to a recent 10 MiB tail; symlinks and non-regular
//     paths fail closed;
//   - when ANOTHER LIVE mecatui holds that log's lock, open the per-process
//     sibling <stem>.<pid>.log instead and mark the sink Contended. A concurrent
//     instance therefore keeps a recoverable diagnostics stream rather than
//     silently discarding it (the shared log is single-writer because the
//     retention step rewrites the file);
//   - on ANY other failure (no resolvable path, mkdir/retention/open error, a
//     symlinked sentinel, or a fallback that cannot be opened either) ⇒
//     io.Discard, so the TUI never crashes on a diagnostics-sink problem.
//
// It NEVER returns os.Stderr/os.Stdout: a diagnostics line on either corrupts the
// Bubble Tea alt-screen (the bug this whole path fixes). The returned Closer is a
// real file closer only when a file was opened (the caller closes it on
// shutdown); for the discard paths it is a no-op closer.
func openDiagLogWriter(env xdgconfig.ResolveEnv, quiet bool, overridePath string) diagLogSink {
	return openDiagLogWriterWithRetainer(env, quiet, overridePath, retainDiagLog)
}

func openDiagLogWriterWithRetainer(env xdgconfig.ResolveEnv, quiet bool, overridePath string, retain func(string) error) diagLogSink {
	discard := diagLogSink{Writer: io.Discard, Closer: io.NopCloser(nil)}
	if quiet {
		return discard
	}
	path := overridePath
	if path == "" {
		path = resolveDiagLogPath(env)
	}
	if path == "" {
		return discard
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { //nolint:gosec // operator-selected diagnostics destination.
		return discard
	}

	writer, err := openLockedDiagLog(path, retain)
	if err == nil {
		return diagLogSink{Writer: writer, Closer: writer, Path: path}
	}
	if !errors.Is(err, errDiagLogLocked) {
		return discard
	}

	// Another live instance owns the shared log. Its diagnostics are ITS own;
	// ours go to a per-process sibling so they are not lost with no trace. A
	// fallback that cannot be opened either still fails closed, but reports the
	// contention so the caller can say so out loud.
	contended := diagLogSink{Writer: io.Discard, Closer: io.NopCloser(nil), Contended: true}
	fallback := fallbackDiagLogPath(path, os.Getpid())
	writer, err = openLockedDiagLog(fallback, retain)
	if err != nil {
		return contended
	}
	contended.Writer, contended.Closer, contended.Path = writer, writer, fallback
	return contended
}
