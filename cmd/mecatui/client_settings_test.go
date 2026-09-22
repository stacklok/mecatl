package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	customization "github.com/stacklok/mecatl/cmd/mecatui/customization"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

func mustReadClientSettings(t *testing.T) clientSettings {
	t.Helper()
	settings, err := readClientSettings()
	if err != nil {
		t.Fatalf("read client settings: %v", err)
	}
	return settings
}

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

func TestGoccyYAMLMigration_Scenario5_MecatuiReadersRetainFallbacks(t *testing.T) {
	const attackerKey = "attacker-key"
	const attackerValue = "attacker-value"

	t.Run("malformed state falls back to zero selection", func(t *testing.T) {
		stateHome := t.TempDir()
		store := newSelectionStore(fakeStateEnv(stateHome))
		if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.path, []byte("version: 1\n  "+attackerKey+": "+attackerValue+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := store.Load(t.TempDir()); got != (client.ModelSelection{}) {
			t.Fatalf("Load(malformed state) = %+v, want zero fallback", got)
		}
	})

	t.Run("malformed client keymap remains a value-free startup error", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeSettings(t, "mecatui", "keymap:\n  "+attackerKey+": [unterminated\n")
		_, _, err := readClientKeymap()
		if err == nil {
			t.Fatal("malformed client keymap must remain a startup error")
		}
		for _, forbidden := range []string{attackerKey, attackerValue, "[unterminated"} {
			if strings.Contains(err.Error(), forbidden) {
				t.Fatalf("keymap error leaked YAML content %q: %q", forbidden, err)
			}
		}
		if !strings.Contains(err.Error(), "line ") || !strings.Contains(err.Error(), "column ") {
			t.Fatalf("keymap syntax error = %q, want line and column", err)
		}
	})
}

func TestStatusCustomizationCommandIntervalReachesCommandSource(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "runs")
	source := newSource(statusCustomization{
		Command:  &statusCommand{Path: "/bin/sh", Args: []string{"-c", `read input; printf x >> "$1"; printf '<footer><text>fixed</text></footer>'`, "--", marker}},
		Interval: time.Second,
	})
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	source.Submit(customization.Input{Terminal: customization.Terminal{FooterAvailCols: 80}})
	select {
	case <-source.Changed():
	case <-time.After(2 * time.Second):
		t.Fatal("initial command did not publish")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := os.ReadFile(marker); err == nil && len(body) >= 2 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("configured command interval did not schedule a refresh")
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

func TestReadClientSettingsDecodeErrorIsSafeAndExplainsSettingsOwnership(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const secret = "SUPER-SECRET-TITLE-MODEL"
	path := writeSettings(t, "mecatui", "models:\n  slots:\n    title: "+secret+"\n")

	_, err := readClientSettings()
	if err == nil {
		t.Fatal("server models in the strict client file must be rejected")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("client settings error leaked YAML value %q: %v", secret, err)
	}
	for _, want := range []string{
		path,
		"client-owned settings file",
		"models:",
		"~/.config/mecatl/settings.yaml",
		"line ",
		"column ",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q", err, want)
		}
	}
}

func TestReadClientSettingsIgnoresServerModelsWithoutTitleSlot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatl", "models:\n  slots:\n    cheap: cheap-model\n")

	got, err := readClientSettings()
	if err != nil {
		t.Fatalf("server settings without models.slots.title must not affect client settings: %v", err)
	}
	if !reflect.DeepEqual(got, defaultClientSettings()) {
		t.Fatalf("client settings = %#v, want defaults when the client file is absent", got)
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
	writeSettings(t, "mecatl", "permissions:\n  allow:\n    - Shell(git status)\nguardrails:\n  defaultMode: advisory\nkeymap:\n  Agents: ctrl+f12\n")
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
	clientMap, clientSet, err := readClientKeymap()
	if err != nil || clientMap != nil || clientSet {
		t.Errorf("readClientKeymap absent = (%v, %v, %v), want (nil, false, nil)", clientMap, clientSet, err)
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
	if err := applyKeyOverridesToDeps(cfg, mustReadClientSettings(t), &deps); err != nil {
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
	if err := applyKeyOverridesToDeps(config{}, mustReadClientSettings(t), &deps); err != nil {
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
	if err := applyKeyOverridesToDeps(cfg, mustReadClientSettings(t), &deps); err != nil {
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
		if err := applyKeyOverridesToDeps(config{}, mustReadClientSettings(t), &deps); err != nil {
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

func TestCanonicalDebugPrintsKeymapDiagnostics(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var deps ui.Deps
	out := captureStderr(t, func() {
		if err := applyKeyOverridesToDeps(config{debugKeymap: true}, mustReadClientSettings(t), &deps); err != nil {
			t.Fatalf("apply: %v", err)
		}
	})
	for _, layer := range []string{"legacy YAML", "client YAML", "CLI", "merged"} {
		if !strings.Contains(out, "mecatui keymap ("+layer+")") {
			t.Errorf("canonical debug output missing %s layer: %q", layer, out)
		}
	}
}

func TestKeymapDeprecationWarnSilentWithoutLegacyKeymap(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// A legacy file with server keys but NO keymap: contributes nothing.
	writeSettings(t, "mecatl", "permissions:\n  allow:\n    - Shell(git status)\n")
	writeSettings(t, "mecatui", "keymap:\n  Agents: ctrl+f3\n")
	var deps ui.Deps
	out := captureStderr(t, func() {
		if err := applyKeyOverridesToDeps(config{}, mustReadClientSettings(t), &deps); err != nil {
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
	err := applyKeyOverridesToDeps(config{}, mustReadClientSettings(t), &deps)
	if err == nil {
		t.Fatal("an unknown action name must surface as an error")
	}
	if !strings.Contains(err.Error(), "keymap:") || !strings.Contains(err.Error(), "NotAnAction") {
		t.Errorf("error must carry the keymap: prefix and name the bad action: %v", err)
	}
}

// TestStatusCustomization_Scenario1_UserSettingsOwnCustomization pins the
// client/server settings boundary: status customization is a mecatui-only
// setting and an absent client value selects the shipped templates.
func TestStatusCustomization_Scenario1_UserSettingsOwnCustomization(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatl", "status_customization:\n  templates:\n    wide: legacy\n")

	got, err := readStatusCustomization()
	if err != nil {
		t.Fatalf("read absent client customization: %v", err)
	}
	if !reflect.DeepEqual(got, shippedStatusCustomization()) {
		t.Fatalf("absent client customization = %#v, want shipped default %#v", got, shippedStatusCustomization())
	}

	writeSettings(t, "mecatui", "keymap:\n  Agents: ctrl+f12\nstatus_customization:\n  templates:\n    header:\n      full: client header\n      compact: client compact\n      minimal: client\n  interval: 2s\n")
	got, err = readStatusCustomization()
	if err != nil {
		t.Fatalf("read client customization: %v", err)
	}
	if got.Templates == nil || got.Templates.Header == nil || got.Templates.Header.Full != "client header" || got.Templates.Header.Compact != "client compact" || got.Templates.Header.Minimal != "client" {
		t.Fatalf("templates = %#v, want client-owned responsive templates", got.Templates)
	}
	if got.Interval != 2*time.Second {
		t.Errorf("interval = %s, want 2s", got.Interval)
	}
}

func TestReadStatusCustomizationRejectsInvalidConfigurationWithoutEchoingValues(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantError string
		forbidden []string
	}{
		{"both sources", "status_customization:\n  templates:\n    wide: status\n  command:\n    executable: /usr/local/bin/status\n", "", nil},
		{"interval too short", "status_customization:\n  templates:\n    wide: status\n  interval: 500ms\n", "", nil},
		{"unsafe command path", "status_customization:\n  command:\n    executable: ' bad-command '\n", "", []string{"bad-command"}},
		{"invalid passthrough environment name", "status_customization:\n  command:\n    executable: /usr/local/bin/status\n    passthrough_env: [INVALID-PASSTHROUGH]\n", "Invalid passthrough_env value. Values must match [A-Za-z_][A-Za-z0-9_]*.", []string{"INVALID-PASSTHROUGH"}},
		{"passthrough name starts with digit", "status_customization:\n  command:\n    executable: /usr/local/bin/status\n    passthrough_env: [1LEADING]\n", "Invalid passthrough_env value. Values must match [A-Za-z_][A-Za-z0-9_]*.", []string{"1LEADING"}},
		{"non-ASCII passthrough name", "status_customization:\n  command:\n    executable: /usr/local/bin/status\n    passthrough_env: [NÁME]\n", "Invalid passthrough_env value. Values must match [A-Za-z_][A-Za-z0-9_]*.", []string{"NÁME"}},
		{"reserved passthrough name", "status_customization:\n  command:\n    executable: /usr/local/bin/status\n    passthrough_env: [COLUMNS]\n", "Invalid passthrough_env value. You cannot override reserved variable name COLUMNS.", nil},
		{"removed shell fields", "status_customization:\n  command:\n    shell: /bin/sh\n    source: 'printf status'\n", "", nil},
		{"unknown nested key", "status_customization:\n  templates:\n    tablet: status\n", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			writeSettings(t, "mecatui", tc.body)
			_, err := readStatusCustomization()
			if err == nil {
				t.Fatal("invalid status customization must fail")
			}
			if tc.wantError != "" && !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("error = %q, want %q", err, tc.wantError)
			}
			for _, forbidden := range tc.forbidden {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("error must not echo configuration value %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestReadStatusCustomizationAcceptsValidatedDirectExecutable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatui", "status_customization:\n  command:\n    executable: /usr/local/bin/status\n    args: [--format, statusml]\n    passthrough_env: [TMUX, STATUS_EMPTY, _NAME1, STATUS_SECRET]\n")
	got, err := readStatusCustomization()
	if err != nil {
		t.Fatalf("read command customization: %v", err)
	}
	if got.Command == nil || got.Command.Path != "/usr/local/bin/status" || !reflect.DeepEqual(got.Command.Args, []string{"--format", "statusml"}) || !reflect.DeepEqual(got.Command.PassthroughEnv, []string{"TMUX", "STATUS_EMPTY", "_NAME1", "STATUS_SECRET"}) {
		t.Fatalf("command = %#v, want direct executable with literal args and validated passthrough environment", got.Command)
	}
}

func TestStatusCustomizationCommandPassthroughEnvReachesExecution(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMUX", "configured-tmux")
	writeSettings(t, "mecatui", "status_customization:\n  command:\n    executable: /bin/sh\n    args: [-c, 'read input; test \"$TMUX\" = \"$1\" && printf \"<footer><text>tmux available</text></footer>\"', --, configured-tmux]\n    passthrough_env: [TMUX]\n")
	statusConfig, err := readStatusCustomization()
	if err != nil {
		t.Fatalf("read status customization: %v", err)
	}
	source := newSource(statusConfig)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(customization.Input{Terminal: customization.Terminal{FooterAvailCols: 80}})
	select {
	case <-source.Changed():
	case <-time.After(time.Second):
		t.Fatal("configured command did not publish")
	}
	if got, want := source.Latest().Footer.Spans[0].Text, "tmux available"; got != want {
		t.Fatalf("configured command footer = %q, want %q", got, want)
	}
}

func TestReadStatusCustomizationRejectsPartialSurfaceVariants(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatui", "status_customization:\n  templates:\n    header:\n      full: '<header><text>x</text></header>'\n")
	if _, err := readStatusCustomization(); err == nil {
		t.Fatal("a configured surface must supply full, compact, and minimal variants")
	}
}

func TestADR_0344_Scenario2_DefaultPresentationOmitsHandle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	settings, err := readClientSettings()
	if err != nil {
		t.Fatalf("read absent settings: %v", err)
	}
	title, err := newTitleRenderer(settings.TerminalTitle)
	if err != nil {
		t.Fatalf("build shipped title renderer: %v", err)
	}
	got, err := title.Render(customization.Input{Session: customization.Session{Handle: "session-123"}})
	if err != nil {
		t.Fatalf("render shipped title: %v", err)
	}
	if got != "mecatui" {
		t.Fatalf("shipped title = %q, want fallback without session handle", got)
	}

	source := customization.NewDefaultSource(0)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(customization.Input{Session: customization.Session{Handle: "session-123"}, Terminal: customization.Terminal{HeaderAvailCols: 80}})
	select {
	case <-source.Changed():
	case <-time.After(time.Second):
		t.Fatal("default status source did not publish")
	}
	if got := source.Latest().Header.Spans[0].Text; strings.Contains(got, "session-123") {
		t.Fatalf("shipped status header leaked session handle: %q", got)
	}
}

func TestADR_0344_Scenario2_SharedTemplateProjectionAndElide(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatui", "terminal_title:\n  template: '{{.Session.Handle}} {{elide 4 .Session.Title}}'\nstatus_customization:\n  templates:\n    header:\n      full: '<header><text>{{elide 4 .Session.Title}}</text></header>'\n      compact: '<header><text>{{elide 4 .Session.Title}}</text></header>'\n      minimal: '<header><text>{{elide 4 .Session.Title}}</text></header>'\n")

	settings, err := readClientSettings()
	if err != nil {
		t.Fatalf("read configured settings: %v", err)
	}
	title, err := newTitleRenderer(settings.TerminalTitle)
	if err != nil {
		t.Fatalf("build title renderer: %v", err)
	}
	input := customization.Input{Session: customization.Session{Handle: "session-123", Title: "abcdef"}, Terminal: customization.Terminal{HeaderAvailCols: 80}}
	if got, err := title.Render(input); err != nil || got != "session-123 abc…" {
		t.Fatalf("custom title = %q, %v; want %q", got, err, "session-123 abc…")
	}
	for _, tc := range []struct {
		width int
		want  string
	}{{0, ""}, {-1, ""}, {6, "abcdef"}, {1, "…"}, {4, "abc…"}} {
		renderer, err := newTitleRenderer(terminalTitleSettings{Enabled: true, Template: "{{elide " + strconv.Itoa(tc.width) + " .Session.Title}}"})
		if err != nil {
			t.Fatalf("build width %d renderer: %v", tc.width, err)
		}
		got, err := renderer.Render(input)
		if err != nil || got != tc.want || ansi.StringWidth(got) > max(tc.width, 0) {
			t.Errorf("elide(%d) = %q, %v (width %d), want %q within %d", tc.width, got, err, ansi.StringWidth(got), tc.want, max(tc.width, 0))
		}
	}
	wideRenderer, err := newTitleRenderer(terminalTitleSettings{Enabled: true, Template: "{{elide 5 .Session.Title}}"})
	if err != nil {
		t.Fatalf("build wide-character renderer: %v", err)
	}
	if got, err := wideRenderer.Render(customization.Input{Session: customization.Session{Title: "界界界"}}); err != nil || got != "界界…" || ansi.StringWidth(got) > 5 {
		t.Fatalf("wide elide = %q, %v (width %d), want %q within 5", got, err, ansi.StringWidth(got), "界界…")
	}

	unsafeInput := customization.Input{Session: customization.Session{Title: "a<b>&def"}, Terminal: customization.Terminal{HeaderAvailCols: 80}}
	if got, err := mustTitleRenderer(t, "{{elide 10 .Session.Title}}").Render(unsafeInput); err != nil || got != "a<b>&def" {
		t.Fatalf("title elide must measure visible text and preserve it as plain text: %q, %v", got, err)
	}

	if _, err := customization.NewTitleRenderer("{{contextMeter .Context}}"); err == nil {
		t.Fatal("title template must not expose StatusML-producing contextMeter")
	}

	source := newSource(*settings.StatusCustomization)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(unsafeInput)
	select {
	case <-source.Changed():
	case <-time.After(time.Second):
		t.Fatal("template status source did not publish")
	}
	if got := source.Latest().Header.Spans[0].Text; got != "a<b…" {
		t.Fatalf("status template elide must preserve escaped visible text = %q, want %q", got, "a<b…")
	}

	escapedSource := customization.NewTemplateSource(customization.TemplateSet{Header: customization.SurfaceTemplates{
		Full:    "<header><text>{{elide 10 .Session.Title}}</text></header>",
		Compact: "<header><text>{{elide 10 .Session.Title}}</text></header>",
		Minimal: "<header><text>{{elide 10 .Session.Title}}</text></header>",
	}}, 0)
	t.Cleanup(func() { _ = escapedSource.Close(context.Background()) })
	escapedSource.Submit(unsafeInput)
	select {
	case <-escapedSource.Changed():
	case <-time.After(time.Second):
		t.Fatal("escaped template status source did not publish")
	}
	if got := escapedSource.Latest().Header.Spans[0].Text; got != "a<b>&def" {
		t.Fatalf("status template elide must retain visible escaped text = %q, want %q", got, "a<b>&def")
	}
}

func TestADR_0344_Scenario2_InvalidConfigurationFailsActionably(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"unknown field", "terminal_title:\n  unexpected: true\n", "terminal_title"},
		{"invalid YAML", "terminal_title: [\n", "terminal_title"},
		{"parse failure", "terminal_title:\n  template: '{{'\n", "terminal_title.template"},
		{"execution failure", "terminal_title:\n  template: '{{index .Session.Title 1}}'\n", "terminal_title.template"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			writeSettings(t, "mecatui", tc.body)
			if _, err := readClientSettings(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("readClientSettings() error = %v, want actionable %q error", err, tc.want)
			}
		})
	}

	t.Run("data-dependent runtime failure is safe and actionable", func(t *testing.T) {
		renderer, err := customization.NewTitleRenderer("{{if .Session.Title}}{{index .Session.Title 99}}{{else}}mecatui{{end}}")
		if err != nil {
			t.Fatalf("build conditionally valid title renderer: %v", err)
		}
		var output bytes.Buffer
		controller := newTerminalTitleController(&output, true, renderer)
		controller.Set(customization.Input{Session: customization.Session{Title: "short"}})
		_, err = controller.Write([]byte("frame"))
		if err == nil || !strings.Contains(err.Error(), "terminal_title.template") || !strings.Contains(err.Error(), "index out of range") {
			t.Fatalf("runtime title write error = %v, want field and actionable cause", err)
		}
		if output.Len() != 0 {
			t.Fatalf("failed runtime title wrote output: %q", output.String())
		}
	})
}

func TestADR_0344_Scenario2_CommandStatusCannotControlTitle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatui", "terminal_title:\n  template: '{{.Session.Title}} · mecatui'\nstatus_customization:\n  command:\n    executable: /bin/echo\n    args: ['<header><text>command title</text></header>']\n")

	settings, err := readClientSettings()
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if settings.StatusCustomization == nil || settings.StatusCustomization.Command == nil {
		t.Fatal("test setup must select the command status source")
	}
	title, err := newTitleRenderer(settings.TerminalTitle)
	if err != nil {
		t.Fatalf("build title renderer: %v", err)
	}
	got, err := title.Render(customization.Input{Session: customization.Session{Title: "trusted title"}})
	if err != nil {
		t.Fatalf("render title: %v", err)
	}
	if got != "trusted title · mecatui" || strings.Contains(got, "command title") {
		t.Fatalf("command-backed status influenced title %q", got)
	}
}
