package main

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

// openDiagLogWriter resolves the destination for the embedded server's operational
// diagnostics. The contract (and the render-leak fix it exists for):
//
//   - quiet ⇒ io.Discard (the operator asked for zero on-disk diagnostics);
//   - else open overridePath when non-empty (--diagnostics-log), else
//     $XDG_STATE_HOME/mecatl/mecatui.log (fallback ~/.local/state/mecatl/mecatui.log)
//     for APPEND, creating the dir 0700. Before opening, an existing regular file
//     is atomically reduced once to a recent 10 MiB tail; symlinks and non-regular
//     paths fail closed;
//   - on ANY failure (no resolvable path, mkdir/retention/open error) ⇒ io.Discard,
//     so the TUI never crashes on a diagnostics-sink problem.
//
// It NEVER returns os.Stderr/os.Stdout: a diagnostics line on either corrupts the
// Bubble Tea alt-screen (the bug this whole path fixes). The returned closer is
// non-nil only when a real file was opened (so the caller closes it on shutdown);
// for the discard paths it is a no-op closer. The bool reports whether a file was
// actually opened (logged once by the caller, off the TUI render path).
func openDiagLogWriter(env xdgconfig.ResolveEnv, quiet bool, overridePath string) (w io.Writer, closer io.Closer, toFile bool) {
	return openDiagLogWriterWithRetainer(env, quiet, overridePath, retainDiagLog)
}

func openDiagLogWriterWithRetainer(env xdgconfig.ResolveEnv, quiet bool, overridePath string, retain func(string) error) (w io.Writer, closer io.Closer, toFile bool) {
	noop := io.NopCloser(nil)
	if quiet {
		return io.Discard, noop, false
	}
	path := overridePath
	if path == "" {
		path = resolveDiagLogPath(env)
	}
	if path == "" {
		return io.Discard, noop, false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { //nolint:gosec // operator-selected diagnostics destination.
		return io.Discard, noop, false
	}

	// A stable sibling lock is never renamed and remains held until the returned
	// writer closes. A second mecatui therefore fails safely instead of retaining
	// an inode to which the first process is still appending.
	lock := flock.New(path+".lock",
		flock.SetFlag(os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW),
		flock.SetPermissions(0o600),
	)
	locked, err := lock.TryLock()
	if err != nil || !locked {
		_ = lock.Close()
		return io.Discard, noop, false
	}
	fail := func() (io.Writer, io.Closer, bool) {
		_ = lock.Close()
		return io.Discard, noop, false
	}
	if err := retain(path); err != nil {
		return fail()
	}
	f, _, err := openRegularNoFollow(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fail()
	}
	writer := &lockedDiagLog{file: f, lock: lock}
	return writer, writer, true
}
