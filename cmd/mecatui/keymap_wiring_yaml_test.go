package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// writeSettings writes body to <XDG_CONFIG_HOME>/<app>/settings.yaml under the
// test's temp XDG root, creating the app dir.
func writeSettings(t *testing.T, app, body string) string {
	t.Helper()
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		t.Fatal("XDG_CONFIG_HOME must be set by the caller (t.Setenv)")
	}
	dir := filepath.Join(base, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureStderr swaps os.Stderr for a pipe, runs fn, restores stderr, and
// returns everything fn wrote. Process-global, so the caller must NOT be
// t.Parallel().
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestClientSettingsPathResolvesUnderXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	got := clientSettingsPath(xdgconfig.OSEnv)
	want := filepath.Join(tmp, "mecatui", "settings.yaml")
	if got != want {
		t.Errorf("clientSettingsPath = %q, want %q", got, want)
	}
}

func TestClientSettingsPathEmptyWhenBaseUnresolvable(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", os.ErrNotExist },
	}
	if got := clientSettingsPath(env); got != "" {
		t.Errorf("clientSettingsPath = %q, want empty", got)
	}
}

func TestReadClientKeymapRejectsUnknownTopLevelKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := writeSettings(t, "mecatui", "themee: dark\n")
	_, _, err := readClientKeymap()
	if err == nil {
		t.Fatal("an unknown top-level key must be a strict-decode error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error must name the file path %q: %v", path, err)
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("error must call out the unknown key: %v", err)
	}
}

func TestReadClientKeymapRejectsMultiDocument(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatui", "keymap:\n  Agents: ctrl+f12\n---\nkeymap:\n  Effort: ctrl+f5\n")
	_, _, err := readClientKeymap()
	if err == nil {
		t.Fatal("a multi-document client settings file must be rejected")
	}
	if !strings.Contains(err.Error(), "multiple documents") {
		t.Errorf("error must call out multiple documents: %v", err)
	}
}

func TestReadLegacyKeymapToleratesServerSiblingKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatl", "permissions:\n  allow:\n    - Bash(git status)\nguardrails:\n  defaultMode: advisory\nkeymap:\n  Agents: ctrl+f12\n")
	got, set, err := readLegacyKeymap()
	if err != nil {
		t.Fatalf("legacy file with server sibling keys must parse: %v", err)
	}
	if !set {
		t.Error("set = false, want true (the legacy file contributed a keymap)")
	}
	want := map[string][]string{"Agents": {"ctrl+f12"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keymap = %v, want %v (only the keymap: key extracted)", got, want)
	}
}

// TestReadLegacyKeymapTypeErrorDoesNotLeakValue pins the CWE-209 fix: a
// legacy keymap: holding a wrong-typed value (a scalar, not a map) produces a
// *yaml.TypeError that embeds a truncated slice of the offending VALUE. The
// returned error must NOT wrap it through — the operator needs the line, not
// the (possibly sensitive) value they wrote.
func TestReadLegacyKeymapTypeErrorDoesNotLeakValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatl", "keymap: SUPER-SECRET-TOKEN-abc123\n")
	_, _, err := readLegacyKeymap()
	if err == nil {
		t.Fatal("a scalar keymap: value must be a parse error")
	}
	if strings.Contains(err.Error(), "SUPER-SECRET") || strings.Contains(err.Error(), "abc123") {
		t.Errorf("error leaks the offending keymap value (CWE-209): %v", err)
	}
	// The error must still be actionable: it names the file and the shape.
	if !strings.Contains(err.Error(), "keymap") {
		t.Errorf("error should name the keymap: key so the operator can find it: %v", err)
	}
}

func TestKeymapReadersAbsentFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	legacy, legacySet, err := readLegacyKeymap()
	if err != nil || legacy != nil || legacySet {
		t.Errorf("readLegacyKeymap absent = (%v, %v, %v), want (nil, false, nil)", legacy, legacySet, err)
	}
	client, clientSet, err := readClientKeymap()
	if err != nil || client != nil || clientSet {
		t.Errorf("readClientKeymap absent = (%v, %v, %v), want (nil, false, nil)", client, clientSet, err)
	}
}

func TestSplitChordsByteCompatCommaFormat(t *testing.T) {
	// The byte-compatible comma format tolerates spaces around chords.
	got := splitChords("ctrl+a, ctrl+f12")
	want := []string{"ctrl+a", "ctrl+f12"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitChords = %v, want %v", got, want)
	}
	if got := splitChords(" , ,"); len(got) != 0 {
		t.Errorf("splitChords empties = %v, want none", got)
	}
}

func TestKeymapPrecedenceCLIBeatsClientBeatsLegacy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatl", "keymap:\n  Agents: ctrl+f1\n  Effort: ctrl+f2\n")
	writeSettings(t, "mecatui", "keymap:\n  Agents: ctrl+f3\n  ExpandTools: ctrl+f4\n")
	cfg := config{keymap: &cliconfig.KeyValueList{"Agents": "ctrl+f5"}}
	var deps ui.Deps
	if err := applyKeyOverridesToDeps(cfg, &deps); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := map[string][]string{
		"Agents":      {"ctrl+f5"}, // CLI wins over both files
		"Effort":      {"ctrl+f2"}, // legacy-only action survives
		"ExpandTools": {"ctrl+f4"}, // client-only action survives
	}
	if !reflect.DeepEqual(deps.KeyOverrides, want) {
		t.Errorf("merged overrides = %v, want %v", deps.KeyOverrides, want)
	}
}

func TestKeymapPrecedenceClientBeatsLegacyPerAction(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatl", "keymap:\n  Agents: ctrl+f1\n  Effort: ctrl+f2\n")
	writeSettings(t, "mecatui", "keymap:\n  Agents: ctrl+f3\n")
	var deps ui.Deps
	if err := applyKeyOverridesToDeps(config{}, &deps); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := map[string][]string{
		"Agents": {"ctrl+f3"}, // client wins the shared action
		"Effort": {"ctrl+f2"}, // per-action merge: the untouched legacy action survives
	}
	if !reflect.DeepEqual(deps.KeyOverrides, want) {
		t.Errorf("merged overrides = %v, want %v", deps.KeyOverrides, want)
	}
}

// TestKeymapLegacyOnlyBackCompat is the back-compat regression pin: a
// legacy-only keymap: with NO client file must merge to EXACTLY today's
// two-layer output (CLI > legacy) — the new layer contributes nothing when
// absent.
func TestKeymapLegacyOnlyBackCompat(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatl", "keymap:\n  Agents: ctrl+f1\n  Effort: ctrl+f2\n")
	cfg := config{keymap: &cliconfig.KeyValueList{"Agents": "ctrl+f5"}}
	var deps ui.Deps
	if err := applyKeyOverridesToDeps(cfg, &deps); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := map[string][]string{
		"Agents": {"ctrl+f5"}, // CLI over legacy, as before the split
		"Effort": {"ctrl+f2"},
	}
	if !reflect.DeepEqual(deps.KeyOverrides, want) {
		t.Errorf("merged overrides = %v, want %v (must equal the pre-split two-layer output)", deps.KeyOverrides, want)
	}
}

func TestKeymapDeprecationWarnFiresOnceOnLegacyKeymap(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatl", "keymap:\n  Agents: ctrl+f1\n")
	var deps ui.Deps
	out := captureStderr(t, func() {
		if err := applyKeyOverridesToDeps(config{}, &deps); err != nil {
			t.Fatalf("apply: %v", err)
		}
	})
	if n := strings.Count(out, "is deprecated"); n != 1 {
		t.Errorf("deprecation WARN count = %d, want exactly 1; output: %q", n, out)
	}
	if !strings.Contains(out, "~/.config/mecatui/settings.yaml") {
		t.Errorf("WARN must name the client settings file: %q", out)
	}
}

func TestKeymapDeprecationWarnSilentWithoutLegacyKeymap(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// A legacy file with server keys but NO keymap: contributes nothing.
	writeSettings(t, "mecatl", "permissions:\n  allow:\n    - Bash(git status)\n")
	writeSettings(t, "mecatui", "keymap:\n  Agents: ctrl+f3\n")
	var deps ui.Deps
	out := captureStderr(t, func() {
		if err := applyKeyOverridesToDeps(config{}, &deps); err != nil {
			t.Fatalf("apply: %v", err)
		}
	})
	if strings.Contains(out, "deprecated") {
		t.Errorf("no deprecation WARN without a legacy keymap: %q", out)
	}
	want := map[string][]string{"Agents": {"ctrl+f3"}}
	if !reflect.DeepEqual(deps.KeyOverrides, want) {
		t.Errorf("merged overrides = %v, want %v", deps.KeyOverrides, want)
	}
}

func TestApplyKeyOverridesInvalidActionStillFailsStartup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatui", "keymap:\n  NotAnAction: ctrl+f9\n")
	var deps ui.Deps
	err := applyKeyOverridesToDeps(config{}, &deps)
	if err == nil {
		t.Fatal("an unknown action name must surface as an error")
	}
	if !strings.Contains(err.Error(), "keymap:") || !strings.Contains(err.Error(), "NotAnAction") {
		t.Errorf("error must carry the keymap: prefix and name the bad action: %v", err)
	}
}
