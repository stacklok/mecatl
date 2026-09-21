package main

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stacklok/mecatl/cmd/mecatui/customization"
)

func TestADR_0344_Scenario1_ControllerOwnsSerializedOSC0(t *testing.T) {
	var output lockedBuffer
	controller := newTerminalTitleController(&output, true, mustTitleRenderer(t, "{{.Session.Title}} · {{.MainAgent.State}}"))

	controller.Set(customization.Input{Session: customization.Session{Title: "first"}, MainAgent: customization.MainAgent{State: "idle"}})
	if got := output.String(); got != "" {
		t.Fatalf("View callback wrote before a Bubble Tea frame: %q", got)
	}
	if _, err := controller.Write([]byte("frame one")); err != nil {
		t.Fatalf("write first frame: %v", err)
	}
	if got := output.String(); got != "\x1b]0;first · idle\aframe one" {
		t.Fatalf("title was not flushed immediately before first frame: %q", got)
	}
	controller.Set(customization.Input{Session: customization.Session{Title: "second"}, MainAgent: customization.MainAgent{State: "thinking"}})
	if got := output.String(); got != "\x1b]0;first · idle\aframe one" {
		t.Fatalf("changed title wrote before the next frame: %q", got)
	}
	if _, err := controller.Write([]byte("frame two")); err != nil {
		t.Fatalf("write second frame: %v", err)
	}
	if got := output.String(); got != "\x1b]0;first · idle\aframe one\x1b]0;second · thinking\aframe two" {
		t.Fatalf("changed title was not flushed immediately before second frame: %q", got)
	}

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			controller.Set(customization.Input{Session: customization.Session{Title: "concurrent"}, MainAgent: customization.MainAgent{State: "thinking"}})
		}()
		go func() {
			defer wg.Done()
			if _, err := controller.Write([]byte("frame")); err != nil {
				t.Errorf("write concurrent frame: %v", err)
			}
		}()
	}
	wg.Wait()

	got := output.String()
	if strings.Count(got, "\x1b]0;") < 2 {
		t.Fatalf("OSC 0 writes = %d, want at least 2: %q", strings.Count(got, "\x1b]0;"), got)
	}
	if strings.Contains(got, "\x1b]2;") {
		t.Fatalf("Bubble Tea OSC 2 reached output: %q", got)
	}
	if !strings.Contains(got, "\x1b]0;first · idle\a") || !strings.Contains(got, "\x1b]0;second · thinking\a") {
		t.Fatalf("missing serialized OSC 0 titles: %q", got)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestADR_0344_Scenario1_DeduplicatesConditionalCleanupAndDisables(t *testing.T) {
	t.Run("deduplicates and clears after a title", func(t *testing.T) {
		var output bytes.Buffer
		controller := newTerminalTitleController(&output, true, mustTitleRenderer(t, "{{.Session.Title}}"))
		input := customization.Input{Session: customization.Session{Title: "same"}}
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
	t.Run("does not clear before a title or emit when disabled", func(t *testing.T) {
		for _, enabled := range []bool{true, false} {
			var output bytes.Buffer
			controller := newTerminalTitleController(&output, enabled, mustTitleRenderer(t, "{{.Session.Title}}"))
			controller.Set(customization.Input{Session: customization.Session{Title: "must-not-emit-when-disabled"}})
			if _, err := controller.Write([]byte("frame")); err != nil {
				t.Fatalf("write enabled=%t: %v", enabled, err)
			}
			if err := controller.Close(); err != nil {
				t.Fatalf("close enabled=%t: %v", enabled, err)
			}
			got := output.String()
			if !enabled && strings.Contains(got, "\x1b]0;") {
				t.Fatalf("disabled controller wrote an OSC title: %q", got)
			}
			if enabled && strings.Count(got, "\x1b]0;") != 2 {
				t.Fatalf("enabled controller title and cleanup count = %d, want 2: %q", strings.Count(got, "\x1b]0;"), got)
			}
		}
	})
}

func TestTerminalTitleCleanupAfterGracefulCancellation(t *testing.T) {
	var output bytes.Buffer
	controller := newTerminalTitleController(&output, true, mustTitleRenderer(t, "{{.Session.Title}}"))
	controller.Set(customization.Input{Session: customization.Session{Title: "running"}})
	if _, err := controller.Write([]byte("frame")); err != nil {
		t.Fatalf("write running frame: %v", err)
	}

	if err := closeTerminalTitle(controller); err != nil {
		t.Fatalf("close title after context cancellation: %v", err)
	}
	if got := output.String(); got != "\x1b]0;running\aframe\x1b]0;\a" {
		t.Fatalf("graceful cancellation cleanup = %q, want framed title followed by one clear", got)
	}
}

func TestTerminalTitleWriteErrorIsReturnedWithFrameWrite(t *testing.T) {
	controller := newTerminalTitleController(failingTitleWriter{}, true, mustTitleRenderer(t, "{{.Session.Title}}"))
	controller.Set(customization.Input{Session: customization.Session{Title: "running"}})

	if _, err := controller.Write([]byte("frame")); !errors.Is(err, errTitleWrite) {
		t.Fatalf("Write() error = %v, want title output error", err)
	}
	if err := controller.Close(); !errors.Is(err, errTitleWrite) {
		t.Fatalf("Close() error = %v, want retained title output error", err)
	}
}

var errTitleWrite = errors.New("title output failed")

type failingTitleWriter struct{}

func (failingTitleWriter) Write([]byte) (int, error) { return 0, errTitleWrite }

func TestADR_0344_Scenario1_SanitizesRenderedTitle(t *testing.T) {
	if got := sanitizeTerminalTitle("one\u0085two"); got != "onetwo" {
		t.Fatalf("terminal control must be stripped before whitespace collapse: %q", got)
	}

	var output bytes.Buffer
	controller := newTerminalTitleController(&output, true, mustTitleRenderer(t, "{{.Session.Title}}"))
	controller.Set(customization.Input{Session: customization.Session{Title: " one\x1b]2;injected\a\u007f\u0085\u2000two\u200b\nthree\t " + strings.Repeat("x", 512)}})
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
			cfg := config{terminalTitle: tc.flag, terminalTitleFlagSet: tc.flagSet, terminalTitleOff: tc.flag == "off"}
			settings := defaultClientSettings()
			settings.TerminalTitle = terminalTitleSettings{Enabled: tc.setting, Template: "configured"}
			var output bytes.Buffer
			status, title, err := buildClientPresentation(cfg, settings, &output)
			if err != nil {
				t.Fatalf("build client presentation: %v", err)
			}
			t.Cleanup(func() { _ = status.Close(t.Context()) })
			title.Set(customization.Input{})
			if _, err := title.Write([]byte("frame")); err != nil {
				t.Fatalf("write frame: %v", err)
			}
			if got := strings.Contains(output.String(), "\x1b]0;configured\a"); got != tc.want {
				t.Fatalf("configured title emitted = %t, want %t: %q", got, tc.want, output.String())
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
	input := customization.Input{Session: customization.Session{Title: "Fix tests", Handle: "sess-123"}, MainAgent: customization.MainAgent{State: "running_tool", Activity: "go test"}}
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
	controller.Set(customization.Input{Session: customization.Session{Title: "debug target", Handle: "sess-123"}, MainAgent: customization.MainAgent{State: "connecting"}})
	_, _ = controller.Write([]byte("frame"))
	if got := output.String(); !strings.Contains(got, "\x1b]0;DEBUG debug target · connecting · mecatui\a") || strings.Contains(got, "sess-123") {
		t.Fatalf("debug title = %q", got)
	}
}

func TestADR_0344_Scenario3_LocalAndRemotePresentation(t *testing.T) {
	settings := defaultClientSettings()
	settings.TerminalTitle.Template = "{{.Session.Title}} · {{.MainAgent.State}}"
	input := customization.Input{Session: customization.Session{Title: "shared", Handle: "sess-123"}, MainAgent: customization.MainAgent{State: "idle"}, Workspace: customization.Workspace{Path: "/private/workspace"}}

	outputs := make([]string, 0, 2)
	for _, mode := range []string{"embedded", "connect"} {
		var output bytes.Buffer
		status, title, err := buildClientPresentation(config{}, settings, &output)
		if err != nil {
			t.Fatalf("build %s presentation: %v", mode, err)
		}
		t.Cleanup(func() { _ = status.Close(t.Context()) })
		input.Server.ConnectionMode = mode
		title.Set(input)
		if _, err := title.Write([]byte("frame")); err != nil {
			t.Fatalf("write %s presentation: %v", mode, err)
		}
		outputs = append(outputs, output.String())
	}
	if outputs[0] != outputs[1] || strings.Contains(outputs[0], "/private/workspace") {
		t.Fatalf("embedded=%q connect=%q; title must be connection-independent and omit workspace path unless configured", outputs[0], outputs[1])
	}
	if got, want := renderTitle(t, "{{.Workspace.Path}}", input), "/private/workspace"; got != want {
		t.Fatalf("explicit title workspace path = %q, want %q", got, want)
	}
	for _, source := range []string{"{{contextMeter .Context}}", "{{contextMeterCompact .Context}}", "{{contextMeterMinimal .Context}}"} {
		if _, err := customization.NewTitleRenderer(source); err == nil {
			t.Fatalf("title template unexpectedly accepts status-only function in %q", source)
		}
	}
}

func mustTitleRenderer(t *testing.T, source string) *customization.TitleRenderer {
	t.Helper()
	renderer, err := customization.NewTitleRenderer(source)
	if err != nil {
		t.Fatalf("new title renderer: %v", err)
	}
	return renderer
}

func renderTitle(t *testing.T, source string, input customization.Input) string {
	t.Helper()
	got, err := mustTitleRenderer(t, source).Render(input)
	if err != nil {
		t.Fatalf("render title: %v", err)
	}
	return got
}
