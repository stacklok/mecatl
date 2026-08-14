package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type handoffTestModel struct{ id string }

func (handoffTestModel) Init() tea.Cmd                         { return tea.Quit }
func (m handoffTestModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (handoffTestModel) View() tea.View {
	view := tea.NewView("handoff process harness")
	view.AltScreen = true
	return view
}
func (m handoffTestModel) ActiveSessionID() string { return m.id }

func runExitHandoffProcessHarness(id string) {
	final, err := tea.NewProgram(
		handoffTestModel{id: id},
		tea.WithInput(nil),
		tea.WithOutput(os.Stderr),
	).Run()
	maybeWriteFinalSessionHandoff(os.Stderr, final, err, false)
}

func TestSessionContinuityUX_Scenario7_FinalIDAfterTeardown(t *testing.T) {
	const id = "opaque session\n雪-\u2028-id"
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), "MECATUI_TEST_EXIT_HANDOFF_ID="+id)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("handoff process: %v; stderr=%q", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want untouched", stdout.String())
	}
	got := stderr.String()
	teardownAt := strings.Index(got, "\x1b[?1049l")
	lineAt := strings.Index(got, finalSessionHandoffPrefix)
	if teardownAt < 0 || lineAt <= teardownAt {
		t.Fatalf("handoff did not follow teardown: %q", got)
	}
	lines := strings.Split(strings.TrimPrefix(got[lineAt:], finalSessionHandoffPrefix), "\n")
	if len(lines) != 2 || lines[1] != "" {
		t.Fatalf("handoff suffix = %q, want exactly one line", got[lineAt:])
	}
	var decoded string
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("handoff value is not JSON: %v (line %q)", err, lines[0])
	}
	if decoded != id {
		t.Fatalf("decoded session ID = %q, want byte-exact %q", decoded, id)
	}
}

func TestSessionContinuityUX_Scenario7_OutputContract(t *testing.T) {
	tests := []struct {
		name        string
		model       tea.Model
		runErr      error
		interrupted bool
		want        bool
	}{
		{name: "embedded", model: handoffTestModel{id: "embedded-id"}, want: true},
		{name: "connect", model: handoffTestModel{id: "connect-id"}, want: true},
		{name: "no session", model: handoffTestModel{}},
		{name: "invalid UTF-8", model: handoffTestModel{id: string([]byte{0xff})}},
		{name: "setup failure", model: nil, runErr: errors.New("setup failed")},
		{name: "run failure", model: handoffTestModel{id: "failed-id"}, runErr: errors.New("run failed")},
		{name: "forced exit", model: handoffTestModel{id: "forced-id"}, interrupted: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			stdout.WriteString("existing stdout\n")
			wrote := maybeWriteFinalSessionHandoff(&stderr, tc.model, tc.runErr, tc.interrupted)
			if wrote != tc.want {
				t.Fatalf("wrote = %v, want %v; stderr=%q", wrote, tc.want, stderr.String())
			}
			if stdout.String() != "existing stdout\n" {
				t.Fatalf("stdout changed: %q", stdout.String())
			}
			if tc.want {
				if strings.Count(stderr.String(), finalSessionHandoffPrefix) != 1 || !strings.HasSuffix(stderr.String(), "\n") {
					t.Fatalf("stderr grammar = %q", stderr.String())
				}
			} else if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want no misleading handoff", stderr.String())
			}
		})
	}
}
