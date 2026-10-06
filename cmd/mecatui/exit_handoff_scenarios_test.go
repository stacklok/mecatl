package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
)

type handoffSnapshotGetter func(context.Context, string) (client.SessionSnapshot, error)

func (f handoffSnapshotGetter) GetSession(ctx context.Context, id string) (client.SessionSnapshot, error) {
	return f(ctx, id)
}

func handoffOutput(t *testing.T, id string, embedded bool, getter handoffSnapshotGetter, runErr error, interrupted bool) string {
	t.Helper()
	var out bytes.Buffer
	var source sessionSnapshotGetter
	if getter != nil {
		source = handoffSnapshotGetter(func(ctx context.Context, id string) (client.SessionSnapshot, error) {
			if out.Len() != 0 {
				t.Fatalf("GetSession called after cleanup/output: %q", out.String())
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > time.Second || time.Until(deadline) <= 0 {
				t.Fatalf("snapshot deadline = %v, want future deadline <= 1s", deadline)
			}
			return getter(ctx, id)
		})
	}
	finishFinalSessionHandoff(&out, handoffTestModel{id: id}, runErr, interrupted, embedded, source, func() { out.WriteString("cleanup-complete\n") })
	got := out.String()
	if !strings.HasPrefix(got, "cleanup-complete\n") {
		t.Fatalf("handoff before cleanup: %q", got)
	}
	return strings.TrimPrefix(got, "cleanup-complete\n")
}

func runExitHandoffScenarioHarness() {
	final, err := tea.NewProgram(handoffTestModel{id: "final chat"}, tea.WithInput(nil), tea.WithOutput(os.Stderr)).Run()
	finishFinalSessionHandoff(os.Stderr, final, err, false, true, handoffSnapshotGetter(func(context.Context, string) (client.SessionSnapshot, error) {
		return client.SessionSnapshot{State: "completed"}, nil
	}), func() { _, _ = os.Stderr.WriteString("cleanup-complete\n") })
}

func TestMecatuiExitHandoff_Scenario1_EmbeddedResumeAfterTeardown(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), "MECATUI_TEST_EXIT_HANDOFF_SCENARIO=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("handoff process: %v; stderr=%q", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout changed: %q", stdout.String())
	}
	got := stderr.String()
	teardown := strings.Index(got, "\x1b[?1049l")
	cleanup := strings.Index(got, "cleanup-complete\n")
	line := strings.Index(got, "\nSession ID:    final chat\n")
	if teardown < 0 || cleanup <= teardown || line <= cleanup || strings.Contains(got, finalSessionHandoffPrefix) {
		t.Fatalf("teardown/cleanup/session ID order = %q", got)
	}
	if !strings.Contains(got, "Resume:        mecatui --resume 'final chat'\n") || !strings.Contains(got, "               mecatui --resume-latest (may select a different chat)\n") {
		t.Fatalf("missing exact/qualified continuation: %q", got)
	}
}

func TestMecatuiExitHandoff_Scenario1_AuthoritativeSummary(t *testing.T) {
	get := handoffSnapshotGetter(func(_ context.Context, id string) (client.SessionSnapshot, error) {
		if id != "final" {
			t.Fatalf("requested session %q, want final", id)
		}
		return client.SessionSnapshot{Title: "Server display title", State: "completed", Turns: 7,
			Usage:            client.Usage{InputTokens: 1_234_567, OutputTokens: 13_400, CacheReadTokens: 9, CacheWriteTokens: 3},
			AuxiliaryUsage:   client.Usage{InputTokens: 2_500, OutputTokens: 40, CacheReadTokens: 1_000},
			ContextOccupancy: &client.ContextOccupancy{InputTokens: 999}}, nil
	})
	got := handoffOutput(t, "final", true, get, nil, false)
	want := "Session ID:    final\n" +
		"Title:         Server display title\n" +
		"Model calls:   7\n" +
		"Tokens (main): 1.2M input, 13.4K output, 9 cache read, 3 cache write\n" +
		"Tokens (aux):  2.5K input, 40 output, 1K cache read\n" +
		"Resume:        mecatui --resume 'final'\n" +
		"               mecatui --resume-latest (may select a different chat)\n"
	if got != "\n"+want {
		t.Fatalf("aligned summary = %q, want suffix %q", got, want)
	}
	if strings.Contains(got, "999") {
		t.Fatalf("context meter leaked into lifetime totals: %q", got)
	}
	got = handoffOutput(t, "final", true, handoffSnapshotGetter(func(context.Context, string) (client.SessionSnapshot, error) {
		return client.SessionSnapshot{State: "completed", Turns: 0}, nil
	}), nil, false)
	if strings.Contains(got, "Title:") || strings.Contains(got, "cache read") || strings.Contains(got, "Tokens (aux)") || !strings.Contains(got, "Model calls:   0\n") {
		t.Fatalf("empty title/zero cache = %q", got)
	}
}

func TestMecatuiExitHandoff_Scenario1_SafePresentation(t *testing.T) {
	id := "雪 space;$() ' \\ \" `printf injected`"
	got := handoffOutput(t, id, true, nil, nil, false)
	line := ""
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "Resume:") {
			line = strings.TrimSpace(strings.TrimPrefix(l, "Resume:"))
		}
	}
	if line == "" {
		t.Fatalf("missing command: %q", got)
	}
	cmd := exec.Command("sh", "-c", "set -- "+line+"; printf '%s\\n' \"$#\" \"$1\" \"$2\" \"$3\"")
	parsed, err := cmd.Output()
	if err != nil || string(parsed) != "3\nmecatui\n--resume\n"+id+"\n" {
		t.Fatalf("sh parsed %q: output=%q, err=%v", line, parsed, err)
	}
	for _, unsafeID := range []string{"two\nlines", "escape\x1b[31m", "bidi\u202eright"} {
		got := handoffOutput(t, unsafeID, true, nil, nil, false)
		quoted, _ := json.Marshal(unsafeID)
		if !strings.Contains(got, finalSessionHandoffPrefix+string(quoted)+"\n") || strings.Contains(got, "Resume:") || strings.Contains(got, "--resume-latest") || strings.Contains(got, "Session ID:") {
			t.Fatalf("unsafe ID %q: %q", unsafeID, got)
		}
	}
	got = handoffOutput(t, "final", true, handoffSnapshotGetter(func(context.Context, string) (client.SessionSnapshot, error) {
		return client.SessionSnapshot{State: "completed", Title: "ok\nFAKE LINE\x1b[31m\u202eevil", Turns: 1}, nil
	}), nil, false)
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\u202e") || strings.Contains(got, "\nFAKE LINE") || strings.Count(got, "Title:") != 1 || !strings.Contains(got, "Title:         okFAKE LINE[31mevil\n") {
		t.Fatalf("untrusted title controls/lines: %q", got)
	}
}

func TestMecatuiExitHandoff_Scenario2_SnapshotUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		getter handoffSnapshotGetter
	}{
		{"blocked", func(ctx context.Context, _ string) (client.SessionSnapshot, error) {
			<-ctx.Done()
			return client.SessionSnapshot{Title: "stale"}, ctx.Err()
		}},
		{"failed", func(context.Context, string) (client.SessionSnapshot, error) {
			return client.SessionSnapshot{Title: "stale"}, errors.New("unavailable")
		}},
		{"missing", func(context.Context, string) (client.SessionSnapshot, error) { return client.SessionSnapshot{}, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			got := handoffOutput(t, "final", true, tc.getter, nil, false)
			if time.Since(start) > 2*time.Second {
				t.Fatalf("snapshot blocked cleanup beyond deadline: %s", time.Since(start))
			}
			if got != "\nSession ID:    final\nResume:        mecatui --resume 'final'\n               mecatui --resume-latest (may select a different chat)\n" || strings.Contains(got, "Title:") || strings.Contains(got, "Model calls:") || strings.Contains(got, "Tokens (main):") {
				t.Fatalf("failed snapshot must retain only safe guidance: %q", got)
			}
		})
	}
	t.Run("embedded GetSession missing", func(t *testing.T) {
		checkEmbeddedCompositionChild(t, "missing")
	})
}

func TestMecatuiExitHandoff_Scenario2_ExitMatrix(t *testing.T) {
	for _, tc := range []struct {
		name        string
		id          string
		embedded    bool
		err         error
		interrupted bool
		want        string
	}{
		{"no session", "", true, nil, false, ""},
		{"failed", "final", true, errors.New("failed"), false, ""},
		{"interrupted", "final", true, nil, true, ""},
		{"connected", "remote", false, nil, false, "\n" + finalSessionHandoffPrefix + `"remote"` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			got := handoffOutput(t, tc.id, tc.embedded, handoffSnapshotGetter(func(context.Context, string) (client.SessionSnapshot, error) {
				called = true
				return client.SessionSnapshot{Title: "must not display", Turns: 99}, nil
			}), tc.err, tc.interrupted)
			if got != tc.want || called {
				t.Fatalf("got %q, lookup=%v; want %q and no lookup", got, called, tc.want)
			}
		})
	}
	// Exercise a real UI connect intent through the same exit and restart seams.
	m := ui.New(ui.Deps{Connect: staticConnectController{target: "remote.example:443"}, ConnectOpen: true, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), NoAltScreen: true})
	model, _ := m.Update(m.Init()())
	m = model.(ui.Model)
	for range 2 {
		model, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = model.(ui.Model)
	}
	intent, ok := m.ConnectRestartIntent()
	if !ok {
		t.Fatal("connect intent not produced")
	}
	model, _ = m.Update(client.SessionReadyMsg{SessionID: "pre-restart"})
	m = model.(ui.Model)
	if m.ActiveSessionID() != "pre-restart" {
		t.Fatalf("pre-restart ID = %q", m.ActiveSessionID())
	}
	var out bytes.Buffer
	lookups := 0
	finishFinalSessionHandoff(&out, m, nil, false, true, handoffSnapshotGetter(func(context.Context, string) (client.SessionSnapshot, error) {
		lookups++
		return client.SessionSnapshot{State: "completed", Title: "pre-restart title", Turns: 9}, nil
	}), func() { out.WriteString("cleanup-complete\n") })
	if out.String() != "cleanup-complete\n" || lookups != 0 {
		t.Fatalf("restart handoff=%q lookups=%d", out.String(), lookups)
	}
	if err := restartFromConnectIntentWith([]string{"mecatui"}, intent, restartTransport{}, connectRestartOps{run: func([]string, runOptions) error {
		out.WriteString("successor started\n")
		return nil
	}}); err != nil || out.String() != "cleanup-complete\nsuccessor started\n" {
		t.Fatalf("restart err=%v output=%q", err, out.String())
	}
}
