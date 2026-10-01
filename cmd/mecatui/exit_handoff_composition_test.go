package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/internal/app"
)

// The child owns a real embedded server; the parent observes the process stderr
// independently of the terminal and the server's shutdown.
func TestMecatuiExitHandoff_Scenario1_EmbeddedComposition(t *testing.T) {
	if os.Getenv("MECATUI_TEST_HANDOFF_COMPOSITION") == "" {
		checkEmbeddedCompositionChild(t, "available")
		return
	}
	writeIsolatedExecutionSettings(t, app.PlacementHostLocal)
	tempRoot, err := os.MkdirTemp("/tmp", "mh-") // sockaddr_un requires a short runtime socket path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempRoot) })
	t.Setenv("XDG_RUNTIME_DIR", tempRoot)
	before, err := filepath.Glob(filepath.Join(tempRoot, "mecatui-*", "mecated.sock"))
	if err != nil {
		t.Fatal(err)
	}
	known := make(map[string]bool, len(before))
	for _, socket := range before {
		known[socket] = true
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	err = runWithOptions([]string{"mecatui", "--mock", "--quiet", "--no-store", "--no-memory", "--no-user-model", "--user-model-dir=" + t.TempDir(), "--no-soul", "--no-skills", "--no-commands", "--workspace=" + t.TempDir()}, runOptions{runProgram: func(_ context.Context, m ui.Model) (tea.Model, error) {
		sockets, err := filepath.Glob(filepath.Join(tempRoot, "mecatui-*", "mecated.sock"))
		var fresh []string
		for _, socket := range sockets {
			if !known[socket] {
				fresh = append(fresh, socket)
			}
		}
		if err != nil || len(fresh) != 1 {
			t.Fatalf("embedded sockets=%v err=%v", fresh, err)
		}
		target := "unix://" + fresh[0]
		cl, err := client.Dial(client.DialConfig{Server: target})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cl.Close() }()
		first, _, _, err := cl.CreateSession(ctx, client.ModeFromString("default"), client.ModelSelection{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cl.RenameSession(ctx, first, "first title"); err != nil {
			t.Fatal(err)
		}
		m0, _ := m.Update(client.SessionReadyMsg{SessionID: first})
		m = m0.(ui.Model)
		final := seedStartupResumeSession(ctx, t, target, "")
		if final == first {
			t.Fatal("seed must switch sessions")
		}
		if _, err := cl.RenameSession(ctx, final, "final title"); err != nil {
			t.Fatal(err)
		}
		m0, _ = m.Update(client.SessionReadyMsg{SessionID: final})
		m = m0.(ui.Model)
		if m.ActiveSessionID() != final {
			t.Fatalf("active=%q final=%q", m.ActiveSessionID(), final)
		}
		if os.Getenv("MECATUI_TEST_HANDOFF_COMPOSITION") == "missing" {
			m0, _ = m.Update(client.SessionReadyMsg{SessionID: "missing-final"})
			m = m0.(ui.Model)
		}
		// Run an alternate-screen program before returning the actual UI model.
		if _, err := tea.NewProgram(handoffTestModel{}, tea.WithInput(nil), tea.WithOutput(os.Stderr)).Run(); err != nil {
			t.Fatal(err)
		}
		return m, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	sockets, err := filepath.Glob(filepath.Join(tempRoot, "mecatui-*", "mecated.sock"))
	for _, socket := range sockets {
		if !known[socket] {
			t.Fatalf("embedded server not cleaned: %v %v", sockets, err)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stderr.WriteString("handoff-child-cleaned\n")
}

func checkEmbeddedCompositionChild(t *testing.T, scenario string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestMecatuiExitHandoff_Scenario1_EmbeddedComposition$")
	cmd.Env = append(os.Environ(), "MECATUI_TEST_HANDOFF_COMPOSITION="+scenario)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("embedded child: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.String() != "PASS\n" {
		t.Fatalf("stdout changed by program: %q", stdout.String())
	}
	got := stderr.String()
	line := strings.Index(got, finalSessionHandoffPrefix)
	if line < 0 || strings.Count(got, finalSessionHandoffPrefix) != 1 || strings.LastIndex(got[:line], "\x1b[?1049l") < 0 || !strings.Contains(got[line:], "handoff-child-cleaned\n") {
		t.Fatalf("handoff must follow teardown and precede server shutdown: %q", got)
	}
	fields := strings.SplitN(got[line:], "\n", 2)
	var id string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(fields[0], finalSessionHandoffPrefix)), &id); err != nil || id == "" {
		t.Fatalf("invalid ID: %q: %v", fields[0], err)
	}
	if !strings.Contains(got[line:], "Resume: mecatui --resume '"+id+"'\n") || !strings.Contains(got[line:], "Or: mecatui --resume-latest (may select a different chat)\n") {
		t.Fatalf("final ID command missing: %q", got[line:])
	}
	if scenario == "missing" {
		if id != "missing-final" || strings.Contains(got[line:], "Session:") || strings.Contains(got[line:], "Model calls:") || strings.Contains(got[line:], "Tokens (main):") {
			t.Fatalf("failed lookup retained stale summary: %q", got[line:])
		}
	} else if !strings.Contains(got[line:], "Session: final title\nModel calls: 1\nTokens (main): 0 input, 0 output\n") || strings.Contains(got[line:], "first title") {
		t.Fatalf("wrong final session snapshot: %q", got[line:])
	}
}
