package statusline

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCommandRequiresDirectAbsoluteExecutable(t *testing.T) {
	t.Parallel()

	for name, command := range map[string]Command{
		"empty":                   {LaunchDir: t.TempDir()},
		"relative":                {Path: "echo", LaunchDir: t.TempDir()},
		"whitespace argument":     {Path: "/bin/echo", Args: []string{" status"}, LaunchDir: t.TempDir()},
		"shell without arguments": {Path: "/bin/sh", LaunchDir: t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			if command.Valid() {
				t.Fatalf("ambiguous command configuration was accepted: %#v", command)
			}
		})
	}
}

func TestStatusLineCommandShellRequiresExplicitArguments(t *testing.T) {
	t.Parallel()

	if !(Command{Path: "/bin/sh", Args: []string{"-c", "printf ok"}}).Valid() {
		t.Fatal("explicit /bin/sh arguments must remain a direct-executable opt-in")
	}
}

func TestStatusLine_Scenario3_DirectExecutableReceivesSharedInput(t *testing.T) {
	dir := t.TempDir()
	source := NewCommandSource(commandTest(dir, "input"))
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	source.Submit(Input{Terminal: Terminal{Cols: 123, FooterAvailCols: 80}})
	waitStatusChange(t, source)
	if got, want := statusSurfaceText(source.Latest().Footer), "123"; got != want {
		t.Fatalf("command footer = %q, want %q", got, want)
	}
}

func TestStatusCustomization_Scenario3_CommandAndTemplateShareSurfaces(t *testing.T) {
	dir := t.TempDir()
	source := NewCommandSource(commandTest(dir, "both"))
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(Input{Terminal: Terminal{HeaderAvailCols: 80, FooterAvailCols: 80}})
	waitStatusChange(t, source)
	line := source.Latest()
	if got, want := statusSurfaceText(line.Header), "command header"; got != want {
		t.Fatalf("header = %q, want %q", got, want)
	}
	if got, want := statusSurfaceText(line.Footer), "command footer"; got != want {
		t.Fatalf("footer = %q, want %q", got, want)
	}
}

func TestStatusCustomization_Scenario3_CommandBoundaryIsLocalAndSecretFree(t *testing.T) {
	dir := t.TempDir()
	physicalDir := physicalPath(t, dir)
	t.Setenv("STATUS_SECRET", "do-not-leak")
	source := NewCommandSource(commandTest(dir, "boundary", physicalDir))
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(Input{Workspace: Workspace{Location: "remote"}, Terminal: Terminal{HeaderAvailCols: 80, FooterAvailCols: 80}})
	waitStatusChange(t, source)
	if got, want := statusSurfaceText(source.Latest().Header), "command header"; got != want {
		t.Fatalf("remote command result = %q, want %q", got, want)
	}
}

func TestStatusCustomization_Scenario3_BoundsAndSanitizesCommandOutput(t *testing.T) {
	dir := t.TempDir()
	source := NewCommandSource(commandTest(dir, "overflow"))
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(Input{Terminal: Terminal{HeaderAvailCols: 80, FooterAvailCols: 80}})
	waitStatusChange(t, source)
	line := source.Latest()
	if got := statusSurfaceText(line.Header) + statusSurfaceText(line.Footer); strings.ContainsFunc(got, terminalControl) || strings.Contains(got, "do-not-render") {
		t.Fatalf("unsafe command output reached status line: %q", got)
	}
}

func commandTest(dir string, args ...string) Command {
	mode := args[0]
	extra := args[1:]
	var script string
	switch mode {
	case "input":
		script = `read input; case "$input" in *'"Cols":123'*) printf '<status><footer><accent>123</accent></footer></status>' ;; *) exit 1 ;; esac`
	case "both":
		script = `read input; printf '%s' '<status><header><accent>command header</accent></header><footer><muted>command footer</muted></footer></status>'`
	case "boundary":
		script = `read input; test "$PWD" = "$1" && test -z "$STATUS_SECRET" && printf '%s' '<status><header><accent>command header</accent></header></status>'`
	case "overflow":
		script = `read input; head -c 4097 /dev/zero | tr '\000' x; printf '\033[31mdo-not-render'`
	case "ready":
		script = `read input; printf '<status><footer><text>ready</text></footer></status>'`
	case "tick":
		script = `read input; printf '<status><footer><text>tick</text></footer></status>'`
	case "latest":
		script = `read input; case "$input" in *latest*) printf '<status><footer><text>latest</text></footer></status>' ;; *) sleep 5; printf '<status><footer><text>stale</text></footer></status>' ;; esac`
	case "fail":
		script = `read input; if test -f "$1"; then printf 'status-command-secret' >&2; exit 1; fi; : > "$1"; printf '<status><footer><text>good</text></footer></status>'`
	default:
		panic("unknown test command mode")
	}
	return Command{Path: "/bin/sh", Args: append([]string{"-c", script, "--"}, extra...), LaunchDir: dir}
}

func physicalPath(t *testing.T, path string) string {
	t.Helper()
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return physical
}

func waitStatusChange(t *testing.T, source Source) {
	t.Helper()
	select {
	case <-source.Changed():
	case <-time.After(time.Second):
		t.Fatal("command source did not publish")
	}
}

func TestStatusLineCommandReceivesNoPartialInitialInput(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	source := NewCommandSource(commandTest(t.TempDir(), "fail", marker))
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command ran before a complete Input was submitted: %v", err)
	}
	source.Submit(Input{Terminal: Terminal{Cols: 123, HeaderAvailCols: 80, FooterAvailCols: 80}})
	waitStatusChange(t, source)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("command did not run after Input submission: %v", err)
	}
}

func TestStatusLineCommandInvalidConfigurationDoesNotLeakArguments(t *testing.T) {
	const secret = "argument-secret-value"
	source := NewCommandSource(Command{Path: "relative-command", Args: []string{secret}})
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(Input{Terminal: Terminal{HeaderAvailCols: 80, FooterAvailCols: 80}})
	waitStatusChange(t, source)
	line := source.Latest()
	if strings.Contains(statusSurfaceText(line.Header)+statusSurfaceText(line.Footer), secret) {
		t.Fatal("command arguments leaked into generated status")
	}
}

func TestStatusLineCommandEnvironmentIsExactAllowlist(t *testing.T) {
	t.Setenv("HOME", "/home/operator")
	t.Setenv("PATH", "/bin")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("LC_ALL", "C")
	t.Setenv("STATUS_SECRET", "do-not-leak")
	got := commandEnv(Command{}, Input{Terminal: Terminal{Cols: 120, Rows: 40}})
	want := map[string]bool{
		"HOME=/home/operator": true, "PATH=/bin": true, "TERM=xterm-256color": true,
		"LANG=C.UTF-8": true, "LC_ALL=C": true, "COLUMNS=120": true, "LINES=40": true,
	}
	if len(got) != len(want) {
		t.Fatalf("environment = %q, want exact allowlist", got)
	}
	for _, entry := range got {
		if !want[entry] {
			t.Fatalf("environment leaked non-allowlisted entry %q", entry)
		}
	}
}

func TestStatusLineCommandEnvironmentPassesExplicitVariables(t *testing.T) {
	t.Setenv("HOME", "/home/operator")
	t.Setenv("PATH", "/bin")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("LC_ALL", "C")
	t.Setenv("TMUX", "socket,123,0")
	t.Setenv("STATUS_EMPTY", "")
	t.Setenv("COLUMNS", "999")
	t.Setenv("STATUS_SECRET", "do-not-leak")
	got := commandEnv(Command{PassthroughEnv: []string{"TMUX", "STATUS_EMPTY", "TMUX", "HOME", "COLUMNS", "MISSING"}}, Input{Terminal: Terminal{Cols: 120, Rows: 40}})
	want := []string{
		"HOME=/home/operator", "PATH=/bin", "TERM=xterm-256color", "LANG=C.UTF-8", "LC_ALL=C",
		"COLUMNS=120", "LINES=40", "TMUX=socket,123,0", "STATUS_EMPTY=",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %q, want exact environment %q", got, want)
	}
}

func TestStatusLineCommandTrimsASCIIOutputBoundary(t *testing.T) {
	dir := t.TempDir()
	source := NewCommandSource(Command{Path: "/bin/sh", Args: []string{"-c", `read input; printf ' \t\n<status><footer><text>inside  text</text></footer></status>\r\n\v\f'`}, LaunchDir: dir})
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(Input{Terminal: Terminal{FooterAvailCols: 80}})
	waitStatusChange(t, source)
	if got, want := statusSurfaceText(source.Latest().Footer), "inside  text"; got != want {
		t.Fatalf("trimmed command footer = %q, want %q", got, want)
	}
}

func TestStatusLineCommandDoesNotTrimNonASCIIOutputBoundary(t *testing.T) {
	dir := t.TempDir()
	source := NewCommandSource(Command{Path: "/bin/sh", Args: []string{"-c", `read input; printf '\302\240<status><footer><text>must not render</text></footer></status>'`}, LaunchDir: dir})
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(Input{Terminal: Terminal{FooterAvailCols: 80}})
	waitStatusChange(t, source)
	if got := statusSurfaceText(source.Latest().Footer); strings.Contains(got, "must not render") {
		t.Fatalf("non-ASCII boundary whitespace was trimmed: %q", got)
	}
}

func TestADR_0296_StatusCommandReceivesRootOnlyAsCWD(t *testing.T) {
	launch, workspace := t.TempDir(), t.TempDir()
	physicalWorkspace := physicalPath(t, workspace)
	expected := filepath.Join(launch, "expected-cwd")
	observedCWD := filepath.Join(launch, "observed-cwd")
	observedInput := filepath.Join(launch, "observed-input")
	observedEnv := filepath.Join(launch, "observed-env")
	if err := os.WriteFile(expected, []byte(physicalWorkspace), 0o600); err != nil {
		t.Fatal(err)
	}
	command := Command{
		Path:      "/bin/sh",
		Args:      []string{"-c", `read input; pwd -P > "$2"; printf %s "$input" > "$3"; env > "$4"; test "$(pwd -P)" = "$(cat "$1")" && printf '%s' '<status><header><accent>command header</accent></header></status>'`, "--", expected, observedCWD, observedInput, observedEnv},
		LaunchDir: launch,
	}
	source := NewCommandSource(command)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	SetCommandCWD(source, physicalWorkspace)
	source.Submit(Input{Workspace: Workspace{Location: "local", Basename: "safe-label"}, Terminal: Terminal{HeaderAvailCols: 80, FooterAvailCols: 80}})
	waitStatusChange(t, source)
	if got, want := statusSurfaceText(source.Latest().Header), "command header"; got != want {
		t.Fatalf("status command CWD/projection boundary failed: got %q, want %q", got, want)
	}
	for name, path := range map[string]string{"CWD": observedCWD, "Input": observedInput} {
		value, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read observed %s: %v", name, err)
		}
		if name == "CWD" && strings.TrimSpace(string(value)) != physicalWorkspace {
			t.Fatalf("command CWD = %q, want %q", value, physicalWorkspace)
		}
		if name != "CWD" && strings.Contains(string(value), physicalWorkspace) {
			t.Fatalf("session root leaked into command %s: %q", name, value)
		}
	}
}

func TestStatusLineCommandCWDUsesLaunchDirectoryWithoutContext(t *testing.T) {
	launch := t.TempDir()
	if got := commandCWD(Command{LaunchDir: launch}, Input{}); filepath.Clean(got) != filepath.Clean(launch) {
		t.Fatalf("command CWD = %q, want launch directory %q", got, launch)
	}
}
