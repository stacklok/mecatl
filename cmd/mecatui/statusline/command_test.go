package statusline

import (
	"context"
	"os"
	"path/filepath"
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
	source.Submit(Input{Workspace: Workspace{Location: "remote", Path: "/untrusted/remote"}, Terminal: Terminal{HeaderAvailCols: 80, FooterAvailCols: 80}})
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
	got := commandEnv(Input{Terminal: Terminal{Cols: 120, Rows: 40}})
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

func TestStatusLineCommandCWDUsesLocalSessionWorkspace(t *testing.T) {
	launch, workspace := t.TempDir(), t.TempDir()
	physicalWorkspace := physicalPath(t, workspace)
	result, err := runCommand(context.Background(), commandTest(launch, "boundary", physicalWorkspace), Input{Workspace: Workspace{Location: "local", Path: workspace}})
	if err != nil || !strings.Contains(string(result), "command header") {
		t.Fatalf("local workspace CWD was not used: err=%v result=%q", err, result)
	}
	if filepath.Clean(commandCWD(Command{LaunchDir: launch}, Input{Workspace: Workspace{Location: "remote", Path: workspace}})) != filepath.Clean(launch) {
		t.Fatal("remote workspace became command CWD")
	}
}
