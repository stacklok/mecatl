package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// stateEnv builds a ResolveEnv whose XDG_STATE_HOME points at dir (and whose home
// resolution fails), so the diaglog path resolves under a temp state base — never
// the developer's real ~/.local/state. Fully offline.
func stateEnv(dir string) xdgconfig.ResolveEnv {
	return xdgconfig.ResolveEnv{
		Getenv: func(k string) string {
			if k == "XDG_STATE_HOME" {
				return dir
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile:    os.ReadFile,
	}
}

// homeEnv builds a ResolveEnv with NO XDG_STATE_HOME but a home dir, so the path
// falls back to <home>/.local/state — the documented fallback.
func homeEnv(home string) xdgconfig.ResolveEnv {
	return xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return home, nil },
		ReadFile:    os.ReadFile,
	}
}

func TestResolveDiagLogPathUsesXDGStateHome(t *testing.T) {
	dir := t.TempDir()
	got := resolveDiagLogPath(stateEnv(dir))
	want := filepath.Join(dir, "mecatl", "mecatui.log")
	if got != want {
		t.Fatalf("resolveDiagLogPath under XDG_STATE_HOME = %q, want %q", got, want)
	}
}

func TestResolveDiagLogPathFallsBackToHomeLocalState(t *testing.T) {
	home := t.TempDir()
	got := resolveDiagLogPath(homeEnv(home))
	want := filepath.Join(home, ".local", "state", "mecatl", "mecatui.log")
	if got != want {
		t.Fatalf("resolveDiagLogPath fallback = %q, want %q", got, want)
	}
}

func TestResolveDiagLogPathEmptyWhenUnresolvable(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile:    os.ReadFile,
	}
	if got := resolveDiagLogPath(env); got != "" {
		t.Fatalf("resolveDiagLogPath with no state base = %q, want empty", got)
	}
}

func TestOpenDiagLogWriterQuietDiscards(t *testing.T) {
	// --quiet must yield io.Discard regardless of a resolvable state base, and open
	// no file (the dir/file must NOT be created).
	dir := t.TempDir()
	w, closer, toFile := openDiagLogWriter(stateEnv(dir), true, "")
	defer func() { _ = closer.Close() }()
	if w != io.Discard {
		t.Fatalf("--quiet writer = %T, want io.Discard", w)
	}
	if toFile {
		t.Fatal("--quiet must not open a file")
	}
	if _, err := os.Stat(filepath.Join(dir, "mecatl")); !os.IsNotExist(err) {
		t.Fatalf("--quiet must not create the state dir; stat err = %v", err)
	}
}

func TestOpenDiagLogWriterOpensFileForAppend(t *testing.T) {
	dir := t.TempDir()
	w, closer, toFile := openDiagLogWriter(stateEnv(dir), false, "")
	t.Cleanup(func() { _ = closer.Close() })
	if !toFile {
		t.Fatal("a resolvable state base (not quiet) must open a file")
	}
	if w == io.Discard {
		t.Fatal("non-quiet with a resolvable state base must NOT discard")
	}
	// The dir must be created 0700 and the file writable.
	path := filepath.Join(dir, "mecatl", "mecatui.log")
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write to diag log: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back diag log: %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("diag log content = %q, want %q", string(data), "hello\n")
	}
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("state dir perm = %o, want 0700", perm)
	}
	// The log FILE itself must be 0600 (operator diagnostics may carry prompt
	// text/paths — never group/world readable). A regression to 0644 would otherwise
	// stay green, so pin it explicitly.
	lfi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat diag log file: %v", err)
	}
	if perm := lfi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("diag log file perm = %o, want 0600", perm)
	}
}

func TestOpenDiagLogWriterDiscardsWhenUnresolvable(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile:    os.ReadFile,
	}
	w, closer, toFile := openDiagLogWriter(env, false, "")
	defer func() { _ = closer.Close() }()
	if w != io.Discard || toFile {
		t.Fatalf("no resolvable state base must discard (w=%T toFile=%v)", w, toFile)
	}
}

func TestOpenDiagLogWriterOverridePath(t *testing.T) {
	// --diagnostics-log <path> opens THAT exact file (created mode 0600, parent
	// 0700) instead of the shared per-user mecatui.log, so one instance can route
	// its diagnostics to an operator-chosen location (multi-instance testing).
	dir := t.TempDir()
	override := filepath.Join(dir, "custom", "steer-test.log")
	w, closer, toFile := openDiagLogWriter(stateEnv(dir), false, override)
	t.Cleanup(func() { _ = closer.Close() })
	if !toFile {
		t.Fatal("an override path (not quiet) must open a file")
	}
	if _, err := w.Write([]byte("x\n")); err != nil {
		t.Fatalf("write to override log: %v", err)
	}
	if _, err := os.Stat(override); err != nil {
		t.Fatalf("override log must exist at the given path: %v", err)
	}
	fi, err := os.Stat(filepath.Dir(override))
	if err != nil {
		t.Fatalf("stat override dir: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("override dir perm = %o, want 0700", perm)
	}
	lfi, err := os.Stat(override)
	if err != nil {
		t.Fatalf("stat override file: %v", err)
	}
	if perm := lfi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("override file perm = %o, want 0600", perm)
	}
}

// TestBaselineSlogWriterAlwaysDiscards pins the client-only-mode floor: the TUI has
// no in-process diagnostics of its own, so the universal baseline writer is
// io.Discard whether or not --quiet is set. (The host-embedded path REFINES this to
// the mecatui.log file writer; that refinement is openDiagLogWriter, tested above.)
func TestBaselineSlogWriterAlwaysDiscards(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		if got := baselineSlogWriter(quiet); got != io.Discard {
			t.Fatalf("baselineSlogWriter(quiet=%v) = %T, want io.Discard", quiet, got)
		}
	}
}

// TestInstallBaselineSlogRedirectsGlobalDefault proves the universal floor takes
// effect: after installBaselineSlog, the GLOBAL slog default no longer writes to a
// stderr stand-in. This is the property that makes the redirect cover the client-only
// transports (--server / reuse) — where resolveTransport returns early and never
// touches the default — so ambient/third-party slog cannot corrupt the alt-screen.
func TestInstallBaselineSlogRedirectsGlobalDefault(t *testing.T) {
	// Save and restore the process-global default so this test can't leak into others.
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Plant a default that writes to a buffer (stands in for the stderr the stdlib
	// default would use), then install the baseline and confirm nothing lands there.
	var stderrStandIn bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&stderrStandIn, nil)))

	installBaselineSlog(false)
	slog.Default().Info("ambient line that must not reach the stderr stand-in")
	slog.Default().Log(context.Background(), slog.LevelError, "nor this one")

	if stderrStandIn.Len() != 0 {
		t.Fatalf("global slog default still writes to the stderr stand-in after installBaselineSlog: %q", stderrStandIn.String())
	}
}

func TestDiagLogRetention_Scenario1_ExactRetainedTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mecatui.log")
	original := append(bytes.Repeat([]byte("x"), int(maxDiagLogBytes)), '\n')
	original = append(original, "recent\n"...)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	w, closer, toFile := openDiagLogWriter(stateEnv(dir), false, path)
	t.Cleanup(func() { _ = closer.Close() })
	if !toFile {
		t.Fatal("oversized regular file must open after retention")
	}
	if _, err := w.Write([]byte("next\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "recent\nnext\n"; string(got) != want {
		t.Fatalf("retained log = %q, want %q", got, want)
	}

	t.Run("without newline retains the exact final window", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mecatui.log")
		original := append(bytes.Repeat([]byte("x"), int(maxDiagLogBytes)), 'z')
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := retainDiagLog(path); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := original[len(original)-int(maxDiagLogBytes):]; !bytes.Equal(got, want) {
			t.Fatal("tail without a newline was not retained exactly")
		}
	})
}

func TestDiagLogRetention_ExactBoundaryIsNotRetained(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mecatui.log")
	original := bytes.Repeat([]byte("b"), int(maxDiagLogBytes))
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := retainDiagLog(path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("exactly 10 MiB log changed: size=%d err=%v", len(got), err)
	}
}

func TestDiagLogRetention_DefaultPathIsRetained(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, mecatuiLogSubpath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := append(bytes.Repeat([]byte("x"), int(maxDiagLogBytes)), '\n')
	original = append(original, "default-tail\n"...)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	w, closer, toFile := openDiagLogWriter(stateEnv(dir), false, "")
	if !toFile {
		t.Fatal("default diagnostics path must use retention wiring")
	}
	if _, err := w.Write([]byte("next\n")); err != nil {
		t.Fatal(err)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "default-tail\nnext\n" {
		t.Fatalf("default retained log = %q, %v", got, err)
	}
}

func TestDiagLogRetention_RetainedTailThenAppendNearCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mecatui.log")
	tail := append([]byte("cut\n"), bytes.Repeat([]byte("y"), int(maxDiagLogBytes)-4)...)
	original := append([]byte("old"), tail...)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	w, closer, toFile := openDiagLogWriter(stateEnv(filepath.Dir(path)), false, path)
	if !toFile {
		t.Fatal("oversized log must open after retention")
	}
	appended := []byte("+append+")
	if _, err := w.Write(appended); err != nil {
		t.Fatal(err)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	want := append(bytes.Repeat([]byte("y"), int(maxDiagLogBytes)-4), appended...)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("near-cap retained append mismatch: size=%d want=%d err=%v", len(got), len(want), err)
	}
}

func TestDiagLogRetention_PreservesMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mecatui.log")
	original := append(bytes.Repeat([]byte("x"), int(maxDiagLogBytes)), 'z')
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := retainDiagLog(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("retained mode = %o, want 640", info.Mode().Perm())
	}
}

const diagLogLockHelperPath = "MECATL_DIAGLOG_LOCK_HELPER_PATH"

func TestDiagLogWriterLockHelper(t *testing.T) {
	path := os.Getenv(diagLogLockHelperPath)
	if path == "" {
		return
	}
	w, closer, ok := openDiagLogWriter(stateEnv(filepath.Dir(path)), false, path)
	_ = closer.Close()
	if w != io.Discard || ok {
		t.Fatal("subprocess unexpectedly opened a diagnostics log locked by its parent")
	}
}

func TestDiagLogWriterLockHeldForWriterLifetime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mecatui.log")
	first, firstCloser, ok := openDiagLogWriter(stateEnv(dir), false, path)
	if !ok {
		t.Fatal("first writer did not open")
	}
	if _, err := first.Write(bytes.Repeat([]byte("a"), int(maxDiagLogBytes)+1)); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	second, secondCloser, secondOK := openDiagLogWriter(stateEnv(dir), false, path)
	if second != io.Discard || secondOK {
		t.Fatal("concurrent writer must fail safely while the lifetime lock is held")
	}
	_ = secondCloser.Close()
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("concurrent open replaced the active log: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDiagLogWriterLockHelper$") //nolint:gosec // fixed current test binary.
	cmd.Env = append(os.Environ(), diagLogLockHelperPath+"="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock check failed: %v\n%s", err, out)
	}
	if _, err := first.Write([]byte("still-active")); err != nil {
		t.Fatal(err)
	}
	if err := firstCloser.Close(); err != nil {
		t.Fatal(err)
	}

	_, thirdCloser, thirdOK := openDiagLogWriter(stateEnv(dir), false, path)
	if !thirdOK {
		t.Fatal("lock was not released when the writer closed")
	}
	_ = thirdCloser.Close()
}

func TestDiagLogRetention_SyncsDirectoryAfterRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mecatui.log")
	original := append(bytes.Repeat([]byte("x"), int(maxDiagLogBytes)), 'z')
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	var synced bool
	err := retainDiagLogWith(path, func(f *os.File) error { return f.Sync() }, os.Rename, func(got string) error {
		synced = got == path
		return nil
	})
	if err != nil || !synced {
		t.Fatalf("containing directory was not synced after rename: synced=%v err=%v", synced, err)
	}
}

func TestDiagLogRetention_Scenario1_SmallAndNewFilesUnchanged(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, body string
	}{
		{name: "new"},
		{name: "small", body: "existing\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".log")
			if tc.body != "" {
				if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, closer, toFile := openDiagLogWriter(stateEnv(dir), false, path)
			t.Cleanup(func() { _ = closer.Close() })
			if !toFile {
				t.Fatal("small or new regular file must open")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.body {
				t.Fatalf("bytes before append = %q, want %q", got, tc.body)
			}
		})
	}
}

func TestDiagLogRetention_Scenario1_AppendAfterCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mecatui.log")
	original := append(bytes.Repeat([]byte("x"), int(maxDiagLogBytes)), '\n')
	original = append(original, "tail"...)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	w, closer, toFile := openDiagLogWriter(stateEnv(dir), false, path)
	t.Cleanup(func() { _ = closer.Close() })
	if !toFile {
		t.Fatal("oversized regular file must open after retention")
	}
	if _, err := w.Write([]byte("+append")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "tail+append"; string(got) != want {
		t.Fatalf("retained-and-appended log = %q, want %q", got, want)
	}
	if int64(len(got)) > maxDiagLogBytes {
		t.Fatalf("retained prefix grew beyond cap: %d > %d", len(got), maxDiagLogBytes)
	}
}

func TestDiagLogRetention_Scenario1_AtomicFailurePreservesOriginal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sync   func(*os.File) error
		rename func(string, string) error
	}{
		{name: "sync", sync: func(*os.File) error { return errors.New("sync failed") }, rename: os.Rename},
		{name: "rename", sync: func(f *os.File) error { return f.Sync() }, rename: func(string, string) error { return errors.New("rename failed") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mecatui.log")
			original := append(bytes.Repeat([]byte("x"), int(maxDiagLogBytes)), 'z')
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := retainDiagLogWith(path, tc.sync, tc.rename, func(string) error { return nil }); err == nil {
				t.Fatal("retention failure must be returned")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, original) {
				t.Fatal("failed retention changed the original log")
			}
		})
	}
}

func TestInvariant_diagnostic_log_retention_atomic_failure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mecatui.log")
	original := append(bytes.Repeat([]byte("x"), int(maxDiagLogBytes)), 'z')
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := retainDiagLogWith(path, func(*os.File) error { return errors.New("sync failed") }, os.Rename, func(string) error { return nil }); err == nil {
		t.Fatal("retention failure must be returned")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("original was not preserved after failed retention: %v", err)
	}
	w, closer, toFile := openDiagLogWriterWithRetainer(stateEnv(filepath.Dir(path)), false, path, func(string) error { return errors.New("retention failed") })
	defer func() { _ = closer.Close() }()
	if w != io.Discard || toFile {
		t.Fatal("retention failure must degrade the writer to io.Discard")
	}
}

func TestDiagLogRetention_Scenario1_SymlinkAndNonRegularFailClosed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.log")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, dir} {
		w, closer, toFile := openDiagLogWriter(stateEnv(dir), false, path)
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
		if w != io.Discard || toFile {
			t.Fatalf("unsafe path %q must discard", path)
		}
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "target" {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}

	lockTarget := filepath.Join(dir, "lock-target")
	if err := os.WriteFile(lockTarget, []byte("lock target"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockedPath := filepath.Join(dir, "locked.log")
	if err := os.Symlink(lockTarget, lockedPath+".lock"); err != nil {
		t.Fatal(err)
	}
	w, closer, toFile := openDiagLogWriter(stateEnv(dir), false, lockedPath)
	_ = closer.Close()
	if w != io.Discard || toFile {
		t.Fatal("symlinked lock sentinel must fail closed")
	}
	if contents, readErr := os.ReadFile(lockTarget); readErr != nil || string(contents) != "lock target" {
		t.Fatalf("lock symlink target changed: %q, %v", contents, readErr)
	}
}

func TestDiagLogRetention_Scenario1_OverridePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "override.log")
	original := append(bytes.Repeat([]byte("x"), int(maxDiagLogBytes)), '\n')
	original = append(original, "override-tail"...)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	w, closer, toFile := openDiagLogWriter(stateEnv(dir), false, path)
	t.Cleanup(func() { _ = closer.Close() })
	if !toFile {
		t.Fatal("override must receive the same retention behavior")
	}
	if _, err := w.Write([]byte("+next")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "override-tail+next" {
		t.Fatalf("override retained log = %q, %v", got, err)
	}
}
