package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
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
	sink1 := openDiagLogWriter(stateEnv(dir), true, "")
	defer func() { _ = sink1.Closer.Close() }()
	if sink1.Writer != io.Discard {
		t.Fatalf("--quiet writer = %T, want io.Discard", sink1.Writer)
	}
	if sink1.Path != "" {
		t.Fatal("--quiet must not open a file")
	}
	if _, err := os.Stat(filepath.Join(dir, "mecatl")); !os.IsNotExist(err) {
		t.Fatalf("--quiet must not create the state dir; stat err = %v", err)
	}
}

func TestOpenDiagLogWriterOpensFileForAppend(t *testing.T) {
	dir := t.TempDir()
	sink2 := openDiagLogWriter(stateEnv(dir), false, "")
	t.Cleanup(func() { _ = sink2.Closer.Close() })
	if sink2.Path == "" {
		t.Fatal("a resolvable state base (not quiet) must open a file")
	}
	if sink2.Writer == io.Discard {
		t.Fatal("non-quiet with a resolvable state base must NOT discard")
	}
	// The dir must be created 0700 and the file writable.
	path := filepath.Join(dir, "mecatl", "mecatui.log")
	if _, err := sink2.Writer.Write([]byte("hello\n")); err != nil {
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
	sink3 := openDiagLogWriter(env, false, "")
	defer func() { _ = sink3.Closer.Close() }()
	if sink3.Writer != io.Discard || sink3.Path != "" {
		t.Fatalf("no resolvable state base must discard (w=%T toFile=%v)", sink3.Writer, sink3.Path != "")
	}
}

func TestOpenDiagLogWriterOverridePath(t *testing.T) {
	// --diagnostics-log <path> opens THAT exact file (created mode 0600, parent
	// 0700) instead of the shared per-user mecatui.log, so one instance can route
	// its diagnostics to an operator-chosen location (multi-instance testing).
	dir := t.TempDir()
	override := filepath.Join(dir, "custom", "steer-test.log")
	sink4 := openDiagLogWriter(stateEnv(dir), false, override)
	t.Cleanup(func() { _ = sink4.Closer.Close() })
	if sink4.Path == "" {
		t.Fatal("an override path (not quiet) must open a file")
	}
	if _, err := sink4.Writer.Write([]byte("x\n")); err != nil {
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

	sink5 := openDiagLogWriter(stateEnv(dir), false, path)
	t.Cleanup(func() { _ = sink5.Closer.Close() })
	if sink5.Path == "" {
		t.Fatal("oversized regular file must open after retention")
	}
	if _, err := sink5.Writer.Write([]byte("next\n")); err != nil {
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
	sink6 := openDiagLogWriter(stateEnv(dir), false, "")
	if sink6.Path == "" {
		t.Fatal("default diagnostics path must use retention wiring")
	}
	if _, err := sink6.Writer.Write([]byte("next\n")); err != nil {
		t.Fatal(err)
	}
	if err := sink6.Closer.Close(); err != nil {
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
	sink7 := openDiagLogWriter(stateEnv(filepath.Dir(path)), false, path)
	if sink7.Path == "" {
		t.Fatal("oversized log must open after retention")
	}
	appended := []byte("+append+")
	if _, err := sink7.Writer.Write(appended); err != nil {
		t.Fatal(err)
	}
	if err := sink7.Closer.Close(); err != nil {
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

const diagLogLockHelperArg = "diaglog-lock-helper"

func TestDiagLogWriterLockHelper(t *testing.T) {
	args := flag.Args()
	if len(args) != 2 || args[0] != diagLogLockHelperArg {
		return
	}
	path := args[1]
	sink := openDiagLogWriter(stateEnv(filepath.Dir(path)), false, path)
	defer func() { _ = sink.Closer.Close() }()
	// The parent holds the shared log, so this process must NOT write to it —
	// but it must not lose its diagnostics either (issue #1696).
	if !sink.Contended {
		t.Fatal("a log locked by the parent process must report contention")
	}
	if sink.Path == path {
		t.Fatalf("subprocess opened the shared log its parent holds: %s", sink.Path)
	}
	want := fallbackDiagLogPath(path, os.Getpid())
	if sink.Path != want {
		t.Fatalf("fallback path = %q, want %q", sink.Path, want)
	}
	if sink.Writer == io.Discard {
		t.Fatal("a contended shared log must fall back to a per-process file, not io.Discard")
	}
	if _, err := sink.Writer.Write([]byte("child-line\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(want)
	if err != nil || string(got) != "child-line\n" {
		t.Fatalf("fallback log = %q, %v", got, err)
	}
}

func TestDiagLogWriterLockHeldForWriterLifetime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mecatui.log")
	sink9 := openDiagLogWriter(stateEnv(dir), false, path)
	if sink9.Path == "" {
		t.Fatal("first writer did not open")
	}
	if _, err := sink9.Writer.Write(bytes.Repeat([]byte("a"), int(maxDiagLogBytes)+1)); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// A concurrent open must never touch the active inode. It no longer discards
	// its stream, though: it takes the per-process sibling (issue #1696).
	sink10 := openDiagLogWriter(stateEnv(dir), false, path)
	if !sink10.Contended {
		t.Fatal("concurrent open must report contention on the held log")
	}
	if sink10.Path == path {
		t.Fatal("concurrent open must not open the log held by the first writer")
	}
	_ = sink10.Closer.Close()
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("concurrent open replaced the active log: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDiagLogWriterLockHelper$", "--", diagLogLockHelperArg, path) //nolint:gosec // fixed current test binary and test-owned path.
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock check failed: %v\n%s", err, out)
	}
	if _, err := sink9.Writer.Write([]byte("still-active")); err != nil {
		t.Fatal(err)
	}
	if err := sink9.Closer.Close(); err != nil {
		t.Fatal(err)
	}

	sink11 := openDiagLogWriter(stateEnv(dir), false, path)
	if sink11.Path != path || sink11.Contended {
		t.Fatalf("lock was not released when the writer closed: path=%q contended=%v", sink11.Path, sink11.Contended)
	}
	_ = sink11.Closer.Close()
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
			sink12 := openDiagLogWriter(stateEnv(dir), false, path)
			t.Cleanup(func() { _ = sink12.Closer.Close() })
			if sink12.Path == "" {
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
	sink13 := openDiagLogWriter(stateEnv(dir), false, path)
	t.Cleanup(func() { _ = sink13.Closer.Close() })
	if sink13.Path == "" {
		t.Fatal("oversized regular file must open after retention")
	}
	if _, err := sink13.Writer.Write([]byte("+append")); err != nil {
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
	var retained []string
	sink14 := openDiagLogWriterWithRetainer(stateEnv(filepath.Dir(path)), false, path, func(got string) error {
		retained = append(retained, got)
		return errors.New("retention failed")
	})
	defer func() { _ = sink14.Closer.Close() }()
	if sink14.Writer != io.Discard || sink14.Path != "" {
		t.Fatal("retention failure must degrade the writer to io.Discard")
	}
	if len(retained) != 1 || retained[0] != path {
		t.Fatalf("retention failure tried paths %q, want only %q", retained, path)
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
		sink15 := openDiagLogWriter(stateEnv(dir), false, path)
		if err := sink15.Closer.Close(); err != nil {
			t.Fatal(err)
		}
		if sink15.Writer != io.Discard || sink15.Path != "" {
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
	sink16 := openDiagLogWriter(stateEnv(dir), false, lockedPath)
	_ = sink16.Closer.Close()
	if sink16.Writer != io.Discard || sink16.Path != "" {
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
	sink17 := openDiagLogWriter(stateEnv(dir), false, path)
	t.Cleanup(func() { _ = sink17.Closer.Close() })
	if sink17.Path == "" {
		t.Fatal("override must receive the same retention behavior")
	}
	if _, err := sink17.Writer.Write([]byte("+next")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "override-tail+next" {
		t.Fatalf("override retained log = %q, %v", got, err)
	}
}

// --- issue #1696: a contended shared log must not silently discard ----------

func TestFallbackDiagLogPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		pid  int
		want string
	}{
		{"conventional log", "/state/mecatl/mecatui.log", 42, "/state/mecatl/mecatui.42.log"},
		{"no extension", "/state/mecatl/mecatui", 7, "/state/mecatl/mecatui.7"},
		{"multi-dot stem keeps every dot but the last", "/state/custom.diag.log", 9, "/state/custom.diag.9.log"},
		{"dotted directory is untouched", "/state/v1.2/mecatui.log", 3, "/state/v1.2/mecatui.3.log"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fallbackDiagLogPath(tc.in, tc.pid); got != tc.want {
				t.Fatalf("fallbackDiagLogPath(%q, %d) = %q, want %q", tc.in, tc.pid, got, tc.want)
			}
		})
	}
}

// TestDiagLogFallsBackWhenSharedLogIsHeld is the regression oracle for issue
// #1696: the second instance keeps a recoverable stream, the two streams stay
// separate, and the held inode is never touched.
func TestDiagLogFallsBackWhenSharedLogIsHeld(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "mecatui.log")

	owner := openDiagLogWriter(stateEnv(dir), false, shared)
	if owner.Path != shared || owner.Contended {
		t.Fatalf("first open must own the shared log: path=%q contended=%v", owner.Path, owner.Contended)
	}
	t.Cleanup(func() { _ = owner.Closer.Close() })
	if _, err := owner.Writer.Write([]byte("owner\n")); err != nil {
		t.Fatal(err)
	}

	second := openDiagLogWriter(stateEnv(dir), false, shared)
	t.Cleanup(func() { _ = second.Closer.Close() })
	if !second.Contended {
		t.Fatal("a held shared log must report contention")
	}
	want := fallbackDiagLogPath(shared, os.Getpid())
	if second.Path != want {
		t.Fatalf("fallback path = %q, want %q", second.Path, want)
	}
	if second.Writer == io.Discard {
		t.Fatal("contended open must fall back to a file, not io.Discard (the issue-#1696 bug)")
	}
	if _, err := second.Writer.Write([]byte("fallback\n")); err != nil {
		t.Fatal(err)
	}

	if got, err := os.ReadFile(shared); err != nil || string(got) != "owner\n" {
		t.Fatalf("shared log = %q, %v; the fallback must not write into it", got, err)
	}
	if got, err := os.ReadFile(want); err != nil || string(got) != "fallback\n" {
		t.Fatalf("fallback log = %q, %v", got, err)
	}
}

// TestDiagLogDiscardsWhenFallbackIsAlsoHeld pins the bounded end of the fallback:
// one process has one diagnostics stream, so a third open inside the same process
// (shared held, pid sibling held) still fails closed — but reports contention so
// the operator is told rather than left guessing.
func TestDiagLogDiscardsWhenFallbackIsAlsoHeld(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "mecatui.log")

	owner := openDiagLogWriter(stateEnv(dir), false, shared)
	t.Cleanup(func() { _ = owner.Closer.Close() })
	fallback := openDiagLogWriter(stateEnv(dir), false, shared)
	t.Cleanup(func() { _ = fallback.Closer.Close() })
	if owner.Path == "" || fallback.Path == "" {
		t.Fatalf("setup failed: owner=%q fallback=%q", owner.Path, fallback.Path)
	}

	third := openDiagLogWriter(stateEnv(dir), false, shared)
	t.Cleanup(func() { _ = third.Closer.Close() })
	if third.Writer != io.Discard || third.Path != "" {
		t.Fatalf("both destinations held must fail closed; got path=%q", third.Path)
	}
	if !third.Contended {
		t.Fatal("failing closed must still report contention so the operator is told")
	}
}

// TestDiagLogSymlinkedLockNeverMintsFallback keeps the security fail-closed
// separate from contention: a symlinked lock sentinel is an ERROR, not a busy
// peer, so it must discard WITHOUT creating a per-process file.
func TestDiagLogSymlinkedLockNeverMintsFallback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "lock-target")
	if err := os.WriteFile(target, []byte("lock target"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mecatui.log")
	if err := os.Symlink(target, path+".lock"); err != nil {
		t.Fatal(err)
	}

	sink := openDiagLogWriter(stateEnv(dir), false, path)
	t.Cleanup(func() { _ = sink.Closer.Close() })
	if sink.Writer != io.Discard || sink.Path != "" {
		t.Fatalf("symlinked lock sentinel must fail closed; got path=%q", sink.Path)
	}
	if sink.Contended {
		t.Fatal("a symlinked sentinel is not contention and must not be reported as it")
	}
	if _, err := os.Stat(fallbackDiagLogPath(path, os.Getpid())); !os.IsNotExist(err) {
		t.Fatalf("fail-closed path must not create a fallback log: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "lock target" {
		t.Fatalf("lock symlink target changed: %q, %v", got, err)
	}
}

func TestOpenDiagLogWriterAndReport(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "mecatui.log")
	owner := openDiagLogWriter(stateEnv(dir), false, shared)
	t.Cleanup(func() { _ = owner.Closer.Close() })

	var stderr bytes.Buffer
	fallback := openDiagLogWriterAndReport(stateEnv(dir), false, shared, &stderr)
	t.Cleanup(func() { _ = fallback.Closer.Close() })
	wantPath := fallbackDiagLogPath(shared, os.Getpid())
	if fallback.Path != wantPath || !fallback.Contended {
		t.Fatalf("reported sink path=%q contended=%v, want path=%q contended=true", fallback.Path, fallback.Contended, wantPath)
	}
	wantNotice := "mecatui: another mecatui holds the shared diagnostics log; this instance logs to " + wantPath + "\n"
	if stderr.String() != wantNotice {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantNotice)
	}

	stderr.Reset()
	quiet := openDiagLogWriterAndReport(stateEnv(dir), true, shared, &stderr)
	defer func() { _ = quiet.Closer.Close() }()
	if quiet.Writer != io.Discard || quiet.Path != "" || stderr.Len() != 0 {
		t.Fatalf("quiet sink path=%q stderr=%q, want discard and silence", quiet.Path, stderr.String())
	}
}

func TestDiagLogContentionNotice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sink  diagLogSink
		quiet bool
		want  string
	}{
		{"uncontended shared log owes nothing", diagLogSink{Path: "/s/mecatui.log"}, false, ""},
		{"discarding without contention owes nothing", diagLogSink{}, false, ""},
		{
			"fallback names the file actually written",
			diagLogSink{Path: "/s/mecatui.42.log", Contended: true},
			false,
			"mecatui: another mecatui holds the shared diagnostics log; this instance logs to /s/mecatui.42.log",
		},
		{
			"contended with no fallback says diagnostics are off",
			diagLogSink{Contended: true},
			false,
			"mecatui: another mecatui holds the shared diagnostics log and the per-process fallback could not be opened; diagnostics are disabled for this instance",
		},
		{"quiet stays silent even when contended", diagLogSink{Path: "/s/mecatui.42.log", Contended: true}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := diagLogContentionNotice(tc.sink, tc.quiet); got != tc.want {
				t.Fatalf("notice = %q, want %q", got, tc.want)
			}
		})
	}
}
