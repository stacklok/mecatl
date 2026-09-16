package main

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stacklok/mecatl/cmd/mecatui/statusline"
)

func TestADR_0344_Scenario1_ControllerOwnsSerializedOSC0(t *testing.T) {
	var output bytes.Buffer
	controller := newTerminalTitleController(&output, true, mustTitleRenderer(t, "{{.Session.Title}} · {{.MainAgent.State}}"))

	controller.Set(statusline.Input{Session: statusline.Session{Title: "first"}, MainAgent: statusline.MainAgent{State: "idle"}})
	if _, err := controller.Write([]byte("frame one")); err != nil {
		t.Fatalf("write first frame: %v", err)
	}
	controller.Set(statusline.Input{Session: statusline.Session{Title: "second"}, MainAgent: statusline.MainAgent{State: "thinking"}})
	if _, err := controller.Write([]byte("frame two")); err != nil {
		t.Fatalf("write second frame: %v", err)
	}

	got := output.String()
	if strings.Count(got, "\x1b]0;") != 2 {
		t.Fatalf("OSC 0 writes = %d, want 2: %q", strings.Count(got, "\x1b]0;"), got)
	}
	if strings.Contains(got, "\x1b]2;") {
		t.Fatalf("Bubble Tea OSC 2 reached output: %q", got)
	}
	if !strings.Contains(got, "\x1b]0;first · idle\a") || !strings.Contains(got, "\x1b]0;second · thinking\a") {
		t.Fatalf("missing serialized OSC 0 titles: %q", got)
	}
}

func TestADR_0344_Scenario1_DeduplicatesConditionalCleanupAndDisables(t *testing.T) {
	t.Run("deduplicates and clears after a title", func(t *testing.T) {
		var output bytes.Buffer
		controller := newTerminalTitleController(&output, true, mustTitleRenderer(t, "{{.Session.Title}}"))
		input := statusline.Input{Session: statusline.Session{Title: "same"}}
		controller.Set(input)
		_, _ = controller.Write([]byte("frame one"))
		controller.Set(input)
		_, _ = controller.Write([]byte("frame two"))
		if err := controller.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if err := controller.Close(); err != nil {
			t.Fatalf("second close: %v", err)
		}
		got := output.String()
		if strings.Count(got, "\x1b]0;same\a") != 1 || strings.Count(got, "\x1b]0;\a") != 1 {
			t.Fatalf("dedupe/cleanup output = %q", got)
		}
	})
	t.Run("does not clear before a title or when disabled", func(t *testing.T) {
		for _, enabled := range []bool{true, false} {
			var output bytes.Buffer
			controller := newTerminalTitleController(&output, enabled, mustTitleRenderer(t, "{{.Session.Title}}"))
			if err := controller.Close(); err != nil {
				t.Fatalf("close enabled=%t: %v", enabled, err)
			}
			if got := output.String(); strings.Contains(got, "\x1b]0;") {
				t.Fatalf("enabled=%t wrote an unexpected OSC title: %q", enabled, got)
			}
		}
	})
}

func TestADR_0344_Scenario1_SanitizesRenderedTitle(t *testing.T) {
	var output bytes.Buffer
	controller := newTerminalTitleController(&output, true, mustTitleRenderer(t, "{{.Session.Title}}"))
	controller.Set(statusline.Input{Session: statusline.Session{Title: " one\x1b]2;injected\a\u007f\u0085\u2000two\u200b\nthree\t " + strings.Repeat("x", 512)}})
	_, _ = controller.Write([]byte("frame"))

	got := output.String()
	if strings.Contains(got, "\x1b]2;") || strings.Contains(got, "\x1b]0;one\x1b") || strings.Contains(got, "\u200b") {
		t.Fatalf("control data reached OSC construction: %q", got)
	}
	if !strings.Contains(got, "\x1b]0;one]2;injected twothree ") {
		t.Fatalf("sanitized OSC title missing expected plain text: %q", got)
	}
	if !utf8.ValidString(got) || len([]rune(strings.TrimSuffix(strings.TrimPrefix(got, "\x1b]0;"), "\aframe"))) > terminalTitleRunes {
		t.Fatalf("title is invalid or unbounded: %q", got)
	}
}

func TestADR_0344_Scenario2_ExplicitDisablementPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, flag             string
		flagSet, setting, want bool
	}{
		{"explicit off", "off", true, true, false},
		{"explicit on", "on", true, false, true},
		{"settings off", "on", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalTitleEnabled(config{terminalTitle: tc.flag, terminalTitleFlagSet: tc.flagSet, terminalTitleOff: tc.flag == "off"}, terminalTitleSettings{Enabled: tc.setting}); got != tc.want {
				t.Fatalf("enabled = %t, want %t", got, tc.want)
			}
		})
	}
	t.Setenv("MECATUI_NO_TERMINAL_TITLE", "1")
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parse environment opt-out: %v", err)
	}
	if terminalTitleEnabled(cfg, terminalTitleSettings{Enabled: true}) {
		t.Fatal("environment opt-out enabled title output")
	}
}

func TestADR_0344_Scenario3_TitleAndCustomHandle(t *testing.T) {
	input := statusline.Input{Session: statusline.Session{Title: "Fix tests", Handle: "sess-123"}, MainAgent: statusline.MainAgent{State: "running_tool", Activity: "go test"}}
	if got := renderTitle(t, "{{.Session.Title}} · {{.MainAgent.Activity}} · mecatui", input); got != "Fix tests · go test · mecatui" {
		t.Fatalf("default-style title = %q", got)
	}
	if got := renderTitle(t, "{{.Session.Handle}}: {{.Session.Title}}", input); got != "sess-123: Fix tests" {
		t.Fatalf("custom handle title = %q", got)
	}
}

func TestADR_0344_Scenario3_DebugTitle(t *testing.T) {
	var output bytes.Buffer
	controller := newTerminalTitleController(&output, true, mustTitleRenderer(t, "{{.Session.Title}} · {{.MainAgent.State}} · mecatui"))
	controller.debug = true
	controller.Set(statusline.Input{Session: statusline.Session{Title: "debug target", Handle: "sess-123"}, MainAgent: statusline.MainAgent{State: "connecting"}})
	_, _ = controller.Write([]byte("frame"))
	if got := output.String(); !strings.Contains(got, "\x1b]0;DEBUG debug target · connecting · mecatui\a") || strings.Contains(got, "sess-123") {
		t.Fatalf("debug title = %q", got)
	}
}

func TestADR_0344_Scenario3_LocalAndRemotePresentation(t *testing.T) {
	input := statusline.Input{Session: statusline.Session{Title: "shared", Handle: "sess-123"}, MainAgent: statusline.MainAgent{State: "idle"}, Workspace: statusline.Workspace{Path: "/private/workspace"}}
	local := renderTitle(t, "{{.Session.Title}} · {{.MainAgent.State}}", input)
	input.Server.ConnectionMode = "connect"
	remote := renderTitle(t, "{{.Session.Title}} · {{.MainAgent.State}}", input)
	if local != remote || strings.Contains(local, "/private/workspace") {
		t.Fatalf("local=%q remote=%q; title must be connection-independent and path-free", local, remote)
	}
}

func mustTitleRenderer(t *testing.T, source string) *statusline.TitleRenderer {
	t.Helper()
	renderer, err := statusline.NewTitleRenderer(source)
	if err != nil {
		t.Fatalf("new title renderer: %v", err)
	}
	return renderer
}

func renderTitle(t *testing.T, source string, input statusline.Input) string {
	t.Helper()
	got, err := mustTitleRenderer(t, source).Render(input)
	if err != nil {
		t.Fatalf("render title: %v", err)
	}
	return got
}
