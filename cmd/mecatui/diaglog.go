package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// baselineSlogWriter picks the writer for the UNIVERSAL global-slog floor the TUI
// installs before the alt-screen starts (see installBaselineSlog). mecatui has no
// in-process diagnostics of its own and owns the alt-screen, so the floor is always
// io.Discard: a client-only TUI (--server or reuse-an-already-running mecated) has
// nothing of its own to log, and any ambient/third-party slog.Default() use during
// the alt-screen must NOT reach stderr. The host-embedded path later REFINES this
// floor to the file sink ($XDG_STATE_HOME/mecatl/mecatui.log) so the embedded
// server's ambient slog is captured and operator-recoverable. The quiet bool is
// taken for symmetry with openDiagLogWriter and to make the contract explicit (both
// quiet and not-quiet floor to discard for the client-only case).
func baselineSlogWriter(quiet bool) io.Writer {
	_ = quiet
	return io.Discard
}

// installBaselineSlog redirects the GLOBAL slog default onto the baseline writer
// (io.Discard) so NO transport mode — external --server, reuse, or host-embedded —
// leaks an ambient/third-party slog line onto the Bubble Tea alt-screen. It runs
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
//   - else open $XDG_STATE_HOME/mecatl/mecatui.log (fallback
//     ~/.local/state/mecatl/mecatui.log) for APPEND, creating the dir 0700;
//   - on ANY failure (no resolvable state base, mkdir/open error) ⇒ io.Discard,
//     so the TUI never crashes on a diagnostics-sink problem.
//
// It NEVER returns os.Stderr/os.Stdout: a diagnostics line on either corrupts the
// Bubble Tea alt-screen (the bug this whole path fixes). The returned closer is
// non-nil only when a real file was opened (so the caller closes it on shutdown);
// for the discard paths it is a no-op closer. The bool reports whether a file was
// actually opened (logged once by the caller, off the TUI render path).
func openDiagLogWriter(env xdgconfig.ResolveEnv, quiet bool) (w io.Writer, closer io.Closer, toFile bool) {
	noop := io.NopCloser(nil)
	if quiet {
		return io.Discard, noop, false
	}
	path := resolveDiagLogPath(env)
	if path == "" {
		return io.Discard, noop, false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return io.Discard, noop, false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return io.Discard, noop, false
	}
	return f, f, true
}
