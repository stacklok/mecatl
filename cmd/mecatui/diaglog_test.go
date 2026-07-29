package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
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
	w, closer, toFile := openDiagLogWriter(stateEnv(dir), true)
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
	w, closer, toFile := openDiagLogWriter(stateEnv(dir), false)
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
	w, closer, toFile := openDiagLogWriter(env, false)
	defer func() { _ = closer.Close() }()
	if w != io.Discard || toFile {
		t.Fatalf("no resolvable state base must discard (w=%T toFile=%v)", w, toFile)
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

	installBaselineSlog(false, defaultLogLevel)
	slog.Default().Info("ambient line that must not reach the stderr stand-in")
	slog.Default().Log(context.Background(), slog.LevelError, "nor this one")

	if stderrStandIn.Len() != 0 {
		t.Fatalf("global slog default still writes to the stderr stand-in after installBaselineSlog: %q", stderrStandIn.String())
	}
}

// TestLogLevelsMapping locks the ONE --log-level mapping (issue #319): the four accepted
// tokens resolve to the matching port.Level/slog.Level PAIR, the zero value falls to the
// default (so a config built directly is never rejected), and anything else is reported as
// NOT ok so config.validate can fail loudly rather than silently keeping the old floor.
func TestLogLevelsMapping(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    port.Level
		wantLog slog.Level
		ok      bool
	}{
		{"debug", port.LevelDebug, slog.LevelDebug, true},
		{"info", port.LevelInfo, slog.LevelInfo, true},
		{"warn", port.LevelWarn, slog.LevelWarn, true},
		{"error", port.LevelError, slog.LevelError, true},
		{"", port.LevelInfo, slog.LevelInfo, true}, // unset → the default
		{"trace", port.LevelInfo, slog.LevelInfo, false},
		{"INFO", port.LevelInfo, slog.LevelInfo, false}, // case-sensitive, deliberately
	} {
		got, gotLog, ok := logLevels(tc.in)
		if got != tc.want || gotLog != tc.wantLog || ok != tc.ok {
			t.Errorf("logLevels(%q) = (%v, %v, %v), want (%v, %v, %v)",
				tc.in, got, gotLog, ok, tc.want, tc.wantLog, tc.ok)
		}
		if l := slogLevel(tc.in); l != tc.wantLog {
			t.Errorf("slogLevel(%q) = %v, want %v", tc.in, l, tc.wantLog)
		}
	}
}

// TestValidateRejectsUnknownLogLevel proves the flag fails LOUDLY: an operator raising
// the level is debugging, so silently keeping the info floor would look like the flag did
// nothing. The default and every accepted token must still validate.
func TestValidateRejectsUnknownLogLevel(t *testing.T) {
	base := func(level string) config {
		return config{workspace: "/ws", mode: "default", mock: true, logLevel: level}
	}
	if err := base("trace").validate(); err == nil {
		t.Fatal("validate() must reject an unknown --log-level")
	} else if !strings.Contains(err.Error(), "--log-level") {
		t.Fatalf("error must name the flag, got %v", err)
	}
	for _, ok := range []string{"", "debug", "info", "warn", "error"} {
		if err := base(ok).validate(); err != nil {
			t.Errorf("validate() rejected --log-level %q: %v", ok, err)
		}
	}
}

// TestInstallBaselineSlogHonoursLogLevel closes half of the --log-level gap: nothing
// asserted that the resolved level actually reaches the handler installed as the global
// default, so reverting installBaselineSlog to a hardcoded slog.LevelInfo was invisible.
// The baseline writer is always io.Discard (there is nothing of mecatui's own to log), so
// the observable is the installed handler's own Enabled predicate — which is exactly what
// decides whether a Debug line survives.
func TestInstallBaselineSlogHonoursLogLevel(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	ctx := context.Background()

	installBaselineSlog(false, "debug")
	if !slog.Default().Enabled(ctx, slog.LevelDebug) {
		t.Error("--log-level debug must enable Debug on the global default; the level never reached the handler")
	}

	installBaselineSlog(false, "error")
	if slog.Default().Enabled(ctx, slog.LevelInfo) {
		t.Error("--log-level error must DISABLE Info on the global default")
	}
	if !slog.Default().Enabled(ctx, slog.LevelError) {
		t.Error("--log-level error must still enable Error")
	}
}

// TestNewLogSinksHonoursLogLevelInAllThree closes the other half: the host-embedded path
// wires THREE sinks over one writer (the engine-facing port.Diagnostics, the perf
// surface's slog.Logger, and the handler that becomes the global slog default), and before
// newLogSinks existed each was constructed inline, so reverting ANY one of them to the old
// hardcoded info floor was invisible to the suite — while "the Debug lines were
// unobtainable without rebuilding the binary" is literally the bug --log-level fixes.
//
// Each sink is exercised through its OWN write path (not by re-reading a level value), so
// the assertion is that a line does or does not land in the shared writer.
func TestNewLogSinksHonoursLogLevelInAllThree(t *testing.T) {
	ctx := context.Background()
	const marker = "LEVEL-PROBE"

	// debug: a Debug line must reach the writer through all three sinks.
	for _, tc := range []struct {
		name  string
		write func(w io.Writer, s logSinks)
	}{
		{"diagnostics", func(_ io.Writer, s logSinks) { s.diag.Log(ctx, port.LevelDebug, marker) }},
		{"perf logger", func(_ io.Writer, s logSinks) { s.perf.Debug(marker) }},
		{"ambient default", func(_ io.Writer, s logSinks) { slog.New(s.ambient).Debug(marker) }},
	} {
		t.Run("debug reaches the "+tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			sinks := newLogSinks(&buf, "debug")
			tc.write(&buf, sinks)
			if !strings.Contains(buf.String(), marker) {
				t.Fatalf("--log-level debug did not reach the %s sink (it kept a hardcoded floor); got %q", tc.name, buf.String())
			}
		})
		t.Run("error suppresses Info on the "+tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			sinks := newLogSinks(&buf, "error")
			switch tc.name {
			case "diagnostics":
				sinks.diag.Log(ctx, port.LevelInfo, marker)
			case "perf logger":
				sinks.perf.Info(marker)
			default:
				slog.New(sinks.ambient).Info(marker)
			}
			if strings.Contains(buf.String(), marker) {
				t.Fatalf("--log-level error must suppress an Info line on the %s sink; got %q", tc.name, buf.String())
			}
		})
	}
}
